package dream

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"go.temporal.io/sdk/workflow"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/delta"
	"saahil.local/brain/internal/llm"
)

// ---- curate.merge: one rule filed as several notes, and global rules that disagree ----

const (
	mergeStepName = "curate.merge"
	// with no change the step still runs once a day; the same input is then a $0 ledger answer
	mergeEvery     = 24 * time.Hour
	maxMergeGroups = 20 // per run (the 2026-10-08 sweep merged 15); the rest are found again next run
	questionChars  = 400
)

// MergeEntry is one current global note as the find stage sees it.
type MergeEntry struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	AsOf    string `json:"as_of"`
	Project string `json:"project"` // the repo its source memory came from; "" when it is not a project memory
	Summary string `json:"summary"`
}

type DuplicateGroup struct {
	Keep      string   `json:"keep"`
	Supersede []string `json:"supersede"`
	Why       string   `json:"why"`
}

type Conflict struct {
	Newer      string `json:"newer"`
	Older      string `json:"older"`
	Resolution string `json:"resolution"` // supersede | narrow | owner
	Why        string `json:"why"`
}

type StaleNote struct {
	ID         string `json:"id"`
	ReplacedBy string `json:"replaced_by"`
	Why        string `json:"why"`
}

type MergePlan struct {
	Duplicates []DuplicateGroup `json:"duplicates"`
	Conflicts  []Conflict       `json:"conflicts"`
	Stale      []StaleNote      `json:"stale"`
}

type MergeBody struct {
	Keep string `json:"keep"`
	Body string `json:"body"`
}

// OwnerQuestion is one line of .brain/owner-questions.jsonl; the coordinator relays it and sets answered once the
// owner's answer is applied.
type OwnerQuestion struct {
	IDs      [2]string `json:"ids"`
	AsOf     [2]string `json:"as_of"`
	Question string    `json:"question"`
	Created  time.Time `json:"created"`
	Answered string    `json:"answered,omitempty"`
}

const mergeFindSchema = `{"type":"object","additionalProperties":false,"required":["duplicates","conflicts","stale"],"properties":{
"duplicates":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["keep","supersede","why"],"properties":{"keep":{"type":"string"},"supersede":{"type":"array","items":{"type":"string"}},"why":{"type":"string"}}}},
"conflicts":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["newer","older","resolution","why"],"properties":{"newer":{"type":"string"},"older":{"type":"string"},"resolution":{"type":"string","enum":["supersede","narrow","owner"]},"why":{"type":"string"}}}},
"stale":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","replaced_by","why"],"properties":{"id":{"type":"string"},"replaced_by":{"type":"string"},"why":{"type":"string"}}}}}}`

const mergeBodiesSchema = `{"type":"object","additionalProperties":false,"required":["bodies"],"properties":{
"bodies":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["keep","body"],"properties":{"keep":{"type":"string"},"body":{"type":"string"}}}}}}`

// globalCatalog reads the current global notes, sorted by id.
func globalCatalog(root string) ([]MergeEntry, map[string]delta.Note, error) {
	paths, err := filepath.Glob(filepath.Join(root, "notes", "*.md"))
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(paths)
	var entries []MergeEntry
	notes := map[string]delta.Note{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, err
		}
		n, err := delta.Parse(b)
		if err != nil || n.Status != "current" || n.Scope != "global" {
			continue
		}
		entries = append(entries, MergeEntry{n.ID, n.Type, n.AsOf, sourceProject(n.Sources), n.Summary})
		notes[n.ID] = n
	}
	return entries, notes, nil
}

// sourceProject names the repo of a memory filed from .import/<machine>/<project slug>/: the slug is the repo's path
// with "/" turned into "-", so the home's Documents folder is cut off it.
func sourceProject(sources []string) string {
	for _, s := range sources {
		parts := strings.Split(s, "/")
		if len(parts) < 3 || parts[0] != ".import" {
			continue
		}
		for _, home := range []string{config.LaptopHome(), config.DevboxHome()} {
			prefix := strings.ReplaceAll(filepath.Join(home, "Documents"), "/", "-") + "-"
			if rest, ok := strings.CutPrefix(parts[2], prefix); ok {
				return strings.TrimPrefix(rest, "worktrees-")
			}
		}
		return ""
	}
	return ""
}

func fingerprint(entries []MergeEntry) string {
	b, _ := json.Marshal(entries)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ---- gate and state ----

type mergeState struct {
	LastRun     time.Time `json:"last_run"`
	Fingerprint string    `json:"fingerprint"`
	RunID       string    `json:"run_id,omitempty"`
}

type MergeGateResult struct {
	Skip  string `json:"skip,omitempty"`
	Notes int    `json:"notes"`
}

// MergeGate runs curate.merge when the current global notes differ from what the last merge left, or a day after it.
func (a *Activities) MergeGate(_ context.Context, now time.Time) (MergeGateResult, error) {
	entries, _, err := globalCatalog(a.Cfg.BrainRoot)
	if err != nil {
		return MergeGateResult{}, err
	}
	res := MergeGateResult{Notes: len(entries)}
	var st mergeState
	if b, err := os.ReadFile(a.path(".brain", "merge-state.json")); err == nil {
		if err := json.Unmarshal(b, &st); err != nil {
			return res, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return res, err
	}
	switch {
	case len(entries) < 2:
		res.Skip = fmt.Sprintf("%d current global notes: nothing to compare", len(entries))
	case st.Fingerprint == fingerprint(entries) && now.Sub(st.LastRun) < mergeEvery:
		res.Skip = fmt.Sprintf("no global note changed since the last merge %s ago (runs again after %s)",
			now.Sub(st.LastRun).Round(time.Minute), mergeEvery)
	}
	return res, nil
}

type MergeDoneInput struct {
	RunID string
	Now   time.Time
	Owner []OwnerQuestion
}

type MergeDoneResult struct {
	Queued  int
	Waiting int
}

// MergeDone queues the owner questions and records the global set as the merge left it, so the merge's own writes do
// not wake the next merge. Groups that wait (a failed or skipped bodies call) are found again within a day, so a group
// that keeps failing costs at most one try a day.
func (a *Activities) MergeDone(_ context.Context, in MergeDoneInput) (MergeDoneResult, error) {
	var out MergeDoneResult
	var err error
	if out.Queued, err = queueOwnerQuestions(a.Cfg.BrainRoot, in.Owner, in.Now); err != nil {
		return out, err
	}
	entries, _, err := globalCatalog(a.Cfg.BrainRoot)
	if err != nil {
		return out, err
	}
	b, _ := json.MarshalIndent(mergeState{LastRun: in.Now, Fingerprint: fingerprint(entries), RunID: in.RunID}, "", "  ")
	if err := writeFile(a.path(".brain", "merge-state.json"), b); err != nil {
		return out, err
	}
	out.Waiting, err = WaitingOwnerQuestions(a.Cfg.BrainRoot)
	return out, err
}

func ownerQuestionsPath(root string) string {
	return filepath.Join(root, ".brain", "owner-questions.jsonl")
}

func readOwnerQuestions(root string) ([]OwnerQuestion, error) {
	raw, err := os.ReadFile(ownerQuestionsPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []OwnerQuestion
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var q OwnerQuestion
		if json.Unmarshal(sc.Bytes(), &q) == nil && q.IDs[0] != "" {
			out = append(out, q)
		}
	}
	return out, sc.Err()
}

func pairKey(x, y string) string {
	if x > y {
		x, y = y, x
	}
	return x + " " + y
}

// queueOwnerQuestions appends the pairs not asked before (in either order), so a retried or repeated run asks once.
func queueOwnerQuestions(root string, qs []OwnerQuestion, now time.Time) (int, error) {
	have, err := readOwnerQuestions(root)
	if err != nil || len(qs) == 0 {
		return 0, err
	}
	asked := map[string]bool{}
	for _, q := range have {
		asked[pairKey(q.IDs[0], q.IDs[1])] = true
	}
	var buf bytes.Buffer
	n := 0
	for _, q := range qs {
		if k := pairKey(q.IDs[0], q.IDs[1]); !asked[k] {
			asked[k] = true
			q.Created = now.UTC()
			b, _ := json.Marshal(q)
			buf.Write(append(b, '\n'))
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	if err := os.MkdirAll(filepath.Dir(ownerQuestionsPath(root)), 0o755); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(ownerQuestionsPath(root), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return 0, err
	}
	return n, f.Close()
}

// WaitingOwnerQuestions counts the questions still open: not answered, and both notes still current.
func WaitingOwnerQuestions(root string) (int, error) {
	qs, err := readOwnerQuestions(root)
	if err != nil {
		return 0, err
	}
	current := func(id string) bool {
		b, err := os.ReadFile(filepath.Join(root, "notes", id+".md"))
		if err != nil {
			return false
		}
		n, err := delta.Parse(b)
		return err == nil && n.Status == "current"
	}
	waiting := 0
	for _, q := range qs {
		if q.Answered == "" && current(q.IDs[0]) && current(q.IDs[1]) {
			waiting++
		}
	}
	return waiting, nil
}

// ---- the step: find, settle, write bodies, build the delta ----

type MergeStepInput struct {
	RunSpentUSD float64
	BudgetUSD   float64 // per model call
	CapUSD      float64 // spent + budget must stay within it before each call
	Today       string  // YYYY-MM-DD: the date on a narrowing line
}

type MergeStepResult struct {
	Status  string // ok | skipped | failed: whether the find stage answered
	Reason  string
	Waiting []string // duplicate groups and too-full narrowed notes with no body this run: "<id>: <why>"
	CostUSD float64
	Calls   int
	Cached  int
	StopLLM bool
	Notes   int
	Plan    MergePlan // as settled by code, not as the model answered
	Delta   delta.Delta
	Edits   []Edit
	Owner   []OwnerQuestion
	Refused []string // units the applier refused, each with its first reason
}

func (r *MergeStepResult) add(s LLMStepResult) {
	r.CostUSD += s.CostUSD
	if s.Called {
		r.Calls++
	}
	if s.Cached {
		r.Cached++
	}
	r.StopLLM = r.StopLLM || s.StopLLM
}

// MergeStep asks the merge skill which current global notes are duplicates, conflicts or stale, settles the answer by
// code, asks for one merged body per duplicate group, and returns the delta as whole-file edits for ApplyEdits. It
// writes no note and no question (ApplyEdits and MergeDone do); only the ledger records the calls.
func (a *Activities) MergeStep(ctx context.Context, in MergeStepInput) (MergeStepResult, error) {
	var out MergeStepResult
	skill, err := a.readSkill("merge")
	if errors.Is(err, os.ErrNotExist) {
		out.Status, out.Reason = "skipped", "skill missing: merge.md"
		return out, nil
	}
	if err != nil {
		return out, err
	}
	entries, notes, err := globalCatalog(a.Cfg.BrainRoot)
	if err != nil {
		return out, err
	}
	out.Notes = len(entries)
	input, _ := json.Marshal(map[string]any{"step": mergeStepName, "stage": "find", "notes": entries})
	var plan MergePlan
	step, err := a.mergeCall(ctx, in, string(skill), mergeStepName, string(input), mergeFindSchema, &plan, out.CostUSD)
	out.add(step)
	out.Status, out.Reason = step.Status, step.Reason
	if err != nil || step.Status != "ok" {
		return out, err
	}
	out.Plan = settlePlan(plan, notes)

	// one call per group: a failed or over-budget answer costs only its group, and each group's answer is cached
	// on its own notes, so a change elsewhere does not pay for it again
	bodies := map[string]string{}
	for _, g := range out.Plan.Duplicates {
		if out.StopLLM {
			out.Waiting = append(out.Waiting, g.Keep+": no more model calls this run")
			continue
		}
		input, err := a.bodiesInput(g, notes)
		if err != nil {
			return out, err
		}
		var ans struct {
			Bodies []MergeBody `json:"bodies"`
		}
		step, err := a.mergeCall(ctx, in, string(skill), mergeStepName+".bodies", input, mergeBodiesSchema, &ans, out.CostUSD)
		out.add(step)
		if err != nil {
			return out, err
		}
		if step.Status != "ok" {
			out.Waiting = append(out.Waiting, g.Keep+": "+step.Reason)
		}
		for _, b := range ans.Bodies {
			if b.Keep == g.Keep {
				bodies[g.Keep] = b.Body
			}
		}
	}
	shorter, err := a.shorterBodies(ctx, in, string(skill), &out, notes)
	if err != nil {
		return out, err
	}
	units, owner := mergeUnits(out.Plan, bodies, shorter, notes, in.Today)
	out.Owner = owner
	out.Delta, out.Edits, out.Refused, err = a.dryRunUnits(ctx, units)
	return out, err
}

// shorterBodies asks the bodies stage, as a group of one, to write each note too full for its narrowing shorter, so
// the line and its link fit; a note with no usable body waits for the next merge.
func (a *Activities) shorterBodies(ctx context.Context, in MergeStepInput, skill string, out *MergeStepResult,
	notes map[string]delta.Note) (map[string]string, error) {
	shorter := map[string]string{}
	for _, c := range out.Plan.Conflicts {
		older := notes[c.Older]
		if c.Resolution != "narrow" || narrowed(older, c.Newer) {
			continue
		}
		line, link, linkOnly := narrowing(c, in.Today)
		if bodyHolds(older, line, link) || !tooFull(older, linkOnly) {
			continue
		}
		if out.StopLLM {
			out.Waiting = append(out.Waiting, c.Older+": no more model calls this run")
			continue
		}
		content, err := os.ReadFile(a.path("notes", c.Older+".md"))
		if err != nil {
			return nil, err
		}
		gr := MergeGroupInput{Keep: c.Older, Supersede: []string{}, Type: older.Type,
			CapChars: shorterCap(older, line, link), Notes: []MergeNoteText{{c.Older, string(content)}}}
		input, err := json.Marshal(map[string]any{"step": mergeStepName, "stage": "bodies", "groups": []MergeGroupInput{gr}})
		if err != nil {
			return nil, err
		}
		var ans struct {
			Bodies []MergeBody `json:"bodies"`
		}
		step, err := a.mergeCall(ctx, in, skill, mergeStepName+".bodies", string(input), mergeBodiesSchema, &ans, out.CostUSD)
		out.add(step)
		if err != nil {
			return nil, err
		}
		if step.Status != "ok" {
			out.Waiting = append(out.Waiting, c.Older+": "+step.Reason)
		}
		for _, b := range ans.Bodies {
			if b.Keep == c.Older && strings.TrimSpace(b.Body) != "" {
				shorter[c.Older] = b.Body
			}
		}
	}
	return shorter, nil
}

// mergeCall pays for (or reads from the ledger) one stage's answer and decodes it into dst; an answer that does not
// decode is unmarked, since a cached broken answer would fail every later merge on the same notes.
func (a *Activities) mergeCall(ctx context.Context, in MergeStepInput, skill, step, input, schema string, dst any,
	spentHere float64) (LLMStepResult, error) {
	p := paid{workflow: "dream", step: step, model: config.ModelJudgment, skill: skill, input: input, schema: schema,
		spentUSD: in.RunSpentUSD + spentHere, capUSD: in.CapUSD, budgetUSD: in.BudgetUSD}
	res, out, err := a.paidCall(ctx, p)
	if err != nil || out.Status != "ok" {
		return out, err
	}
	if err := json.Unmarshal(answerJSON(res), dst); err != nil {
		out.Status, out.Reason = "failed", "answer to "+step+" is not its JSON"
		return out, a.Ledger.Finish(llm.Key(p.workflow, p.step, p.model, p.skill, p.input), llm.Result{}, "failed", time.Now())
	}
	return out, nil
}

// settlePlan keeps what the applier and the owner's rules allow: ids that are current global notes, each in one group
// at most; the newest note kept; a supersede or narrow only when the newer note is dated after the older (equal or
// missing dates go to the owner); a stale note replaced only by one dated no earlier. A note this plan supersedes takes
// no further part; a group's kept note stays current, so it may still be narrowed or asked about.
func settlePlan(p MergePlan, notes map[string]delta.Note) MergePlan {
	var out MergePlan
	grouped, gone := map[string]bool{}, map[string]bool{}
	known := func(id string) bool { _, ok := notes[id]; return ok }
	for _, g := range p.Duplicates {
		if len(out.Duplicates) == maxMergeGroups {
			break
		}
		var members []string
		for _, id := range append([]string{g.Keep}, g.Supersede...) {
			if known(id) && !grouped[id] && !slices.Contains(members, id) {
				members = append(members, id)
			}
		}
		if len(members) < 2 {
			continue
		}
		keep := members[0]
		for _, id := range members {
			if notes[id].AsOf > notes[keep].AsOf {
				keep = id
			}
		}
		group := DuplicateGroup{Keep: keep, Why: g.Why}
		for _, id := range members {
			grouped[id] = true
			if id != keep {
				gone[id] = true
				group.Supersede = append(group.Supersede, id)
			}
		}
		out.Duplicates = append(out.Duplicates, group)
	}
	asked := map[string]bool{}
	for _, c := range p.Conflicts {
		k := pairKey(c.Newer, c.Older)
		if c.Newer == c.Older || !known(c.Newer) || !known(c.Older) || gone[c.Newer] || gone[c.Older] || asked[k] {
			continue
		}
		switch c.Resolution {
		case "supersede", "narrow":
			if !dated(notes[c.Newer]) || !dated(notes[c.Older]) || notes[c.Newer].AsOf <= notes[c.Older].AsOf {
				c.Resolution = "owner"
			}
		case "owner":
		default:
			continue
		}
		asked[k] = true
		if c.Resolution == "supersede" {
			gone[c.Older] = true
		}
		out.Conflicts = append(out.Conflicts, c)
	}
	for _, s := range p.Stale {
		if s.ID == s.ReplacedBy || !known(s.ID) || !known(s.ReplacedBy) || gone[s.ID] || gone[s.ReplacedBy] ||
			notes[s.ReplacedBy].AsOf < notes[s.ID].AsOf {
			continue
		}
		gone[s.ID] = true
		out.Stale = append(out.Stale, s)
	}
	return out
}

func dated(n delta.Note) bool { return len(n.AsOf) == len("2006-01-02") }

// MergeGroupInput is one group as the bodies stage reads it: its notes' whole text and the cap the kept body must fit.
type MergeGroupInput struct {
	Keep      string          `json:"keep"`
	Supersede []string        `json:"supersede"`
	Type      string          `json:"type"`
	CapChars  int             `json:"cap_chars"`
	Notes     []MergeNoteText `json:"notes"`
}

type MergeNoteText struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

// bodiesInput gives the bodies stage one group's whole notes and the cap the kept body must fit.
func (a *Activities) bodiesInput(g DuplicateGroup, notes map[string]delta.Note) (string, error) {
	keep := notes[g.Keep]
	gr := MergeGroupInput{Keep: g.Keep, Supersede: g.Supersede, Type: keep.Type, CapChars: bodyCap(keep, g.Supersede)}
	for _, id := range append([]string{g.Keep}, g.Supersede...) {
		b, err := os.ReadFile(a.path("notes", id+".md"))
		if err != nil {
			return "", err
		}
		gr.Notes = append(gr.Notes, MergeNoteText{id, string(b)})
	}
	b, err := json.Marshal(map[string]any{"step": mergeStepName, "stage": "bodies", "groups": []MergeGroupInput{gr}})
	return string(b), err
}

// bodyCap is what a kept body may hold: a lesson's 300 chars, else the note cap less the frontmatter as the merge
// leaves it (its supersedes ids and updated stamp added).
func bodyCap(n delta.Note, supersede []string) int {
	if n.Type == "lesson" {
		return config.LessonMaxChars
	}
	n.Body, n.Updated = "", time.Time{}.Format(time.RFC3339)
	n.Supersedes = append(slices.Clone(n.Supersedes), supersede...)
	return config.NoteMaxBytes - len(delta.Render(n)) - 1
}

// unit is a set of ops that applies whole or not at all: a merged body without its supersedes, or the reverse, would
// hide facts or repeat them.
type unit struct {
	label string
	ops   []delta.Op
}

// mergeUnits turns a settled plan into units, and its owner pairs into questions. A group whose merged body is
// missing waits for the next merge, as does a narrow whose note is too full for it and has no shorter body; a narrow
// already written (its line names the newer note) is not written again.
func mergeUnits(p MergePlan, bodies, shorter map[string]string, notes map[string]delta.Note, today string) ([]unit, []OwnerQuestion) {
	var units []unit
	var owner []OwnerQuestion
	for _, g := range p.Duplicates {
		body := strings.TrimSpace(bodies[g.Keep])
		if body == "" {
			continue
		}
		body += "\n"
		u := unit{label: "merge into " + g.Keep, ops: []delta.Op{{Op: "update_note", ID: g.Keep, ReplaceBody: &body}}}
		for _, old := range g.Supersede {
			u.ops = append(u.ops, delta.Op{Op: "supersede", Old: old, New: g.Keep})
		}
		units = append(units, u)
	}
	for _, c := range p.Conflicts {
		newer, older := notes[c.Newer], notes[c.Older]
		switch c.Resolution {
		case "supersede":
			units = append(units, unit{label: c.Newer + " supersedes " + c.Older,
				ops: []delta.Op{{Op: "supersede", Old: c.Older, New: c.Newer}}})
		case "narrow":
			if narrowed(older, c.Newer) {
				continue
			}
			line, link, linkOnly := narrowing(c, today)
			var ops []delta.Op
			switch body, ok := shorter[c.Older]; {
			case bodyHolds(older, line, link):
				ops = []delta.Op{{Op: "update_note", ID: c.Older, AppendBody: line}, link}
			case !tooFull(older, linkOnly):
				ops = []delta.Op{linkOnly}
			case ok:
				body = strings.TrimSpace(body) + "\n\n" + line + "\n"
				ops = []delta.Op{{Op: "update_note", ID: c.Older, ReplaceBody: &body}, link}
			default:
				continue
			}
			units = append(units, unit{label: c.Newer + " narrows " + c.Older, ops: ops})
		case "owner":
			owner = append(owner, OwnerQuestion{
				IDs:  [2]string{c.Newer, c.Older},
				AsOf: [2]string{newer.AsOf, older.AsOf},
				Question: oneLine(fmt.Sprintf("%s (as of %s) and %s (as of %s) disagree: %s. Which holds, or does each "+
					"hold in its own situation?", c.Newer, newer.AsOf, c.Older, older.AsOf, c.Why), questionChars),
			})
		}
	}
	for _, s := range p.Stale {
		units = append(units, unit{label: s.ReplacedBy + " replaces stale " + s.ID,
			ops: []delta.Op{{Op: "supersede", Old: s.ID, New: s.ReplacedBy}}})
	}
	return units, owner
}

// narrowed: the older note already names the newer as its narrower, in a body line or in the related link's why.
func narrowed(older delta.Note, newer string) bool {
	if strings.Contains(older.Body, "by "+newer+":") {
		return true
	}
	return slices.ContainsFunc(older.Related, func(r delta.Related) bool {
		return r.ID == newer && strings.HasPrefix(r.Why, "Narrowed ")
	})
}

// narrowing is what a narrow writes into the older note: the line and its link, or, when the body cannot hold the
// line, the link alone with the line as its why.
func narrowing(c Conflict, today string) (string, delta.Op, delta.Op) {
	why := oneLine(c.Why, 300)
	link := delta.Op{Op: "link", Src: c.Older, Rel: "related", Dst: c.Newer, Why: "the newer owner rule that narrows this one"}
	linkOnly := link
	linkOnly.Why = "Narrowed " + today + ": " + why
	return "Narrowed " + today + " by " + c.Newer + ": " + why, link, linkOnly
}

// tooFull: a note other than a lesson that cannot take even the link alone within the 4 KB cap; its body is written
// shorter before the narrow (a lesson's 300-char body leaves its link room).
func tooFull(n delta.Note, linkOnly delta.Op) bool {
	if n.Type == "lesson" {
		return false
	}
	n.Related = append(slices.Clone(n.Related), delta.Related{ID: linkOnly.Dst, Why: linkOnly.Why})
	n.Updated = time.Time{}.Format(time.RFC3339)
	return len(delta.Render(n)) > config.NoteMaxBytes
}

// shorterCap is what a too-full note's shorter body may hold so that it, the narrowing line and the link fit the cap.
func shorterCap(n delta.Note, line string, link delta.Op) int {
	n.Body, n.Updated = "", time.Time{}.Format(time.RFC3339)
	n.Related = append(slices.Clone(n.Related), delta.Related{ID: link.Dst, Why: link.Why})
	return config.NoteMaxBytes - len(delta.Render(n)) - len("\n\n"+line+"\n")
}

// bodyHolds: the note stays within the applier's caps (a lesson's 300 chars, a note's 4 KB) with the line appended
// and the link added; a lesson body usually sits near its cap, so its narrowing goes in the link's why instead.
func bodyHolds(n delta.Note, line string, link delta.Op) bool {
	n.Body = strings.TrimRight(n.Body, "\n") + "\n\n" + line + "\n"
	if n.Type == "lesson" && utf8.RuneCountInString(strings.TrimSpace(n.Body)) > config.LessonMaxChars {
		return false
	}
	n.Related = append(slices.Clone(n.Related), delta.Related{ID: link.Dst, Why: link.Why})
	n.Updated = time.Time{}.Format(time.RFC3339)
	return len(delta.Render(n)) <= config.NoteMaxBytes
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max-1]) + "…"
	}
	return s
}

// dryRunUnits applies the units in dry run and drops every unit the applier refuses any op of, until what is left
// applies cleanly; the result comes back as whole-file edits so ApplyEdits does the writing, backups and caps.
func (a *Activities) dryRunUnits(ctx context.Context, units []unit) (delta.Delta, []Edit, []string, error) {
	var refused []string
	for {
		var d delta.Delta
		owner := map[int]int{} // op index → unit index
		for ui, u := range units {
			for _, op := range u.ops {
				owner[len(d.Ops)] = ui
				d.Ops = append(d.Ops, op)
			}
		}
		rep, err := delta.Apply(ctx, a.Cfg.BrainRoot, d, delta.Options{DryRun: true, Now: time.Now(),
			WorkRoot: a.shadowRoot()})
		if err != nil {
			return d, nil, refused, err
		}
		drop := map[int]bool{}
		for _, r := range rep.Rejected {
			if ui := owner[r.Index]; !drop[ui] {
				drop[ui] = true
				refused = append(refused, units[ui].label+": "+r.Reason)
			}
		}
		if len(drop) == 0 {
			var edits []Edit
			for _, f := range rep.Files {
				edits = append(edits, Edit{Path: f.Path, Action: "write", Content: f.Content, Why: mergeStepName})
			}
			return d, edits, refused, nil
		}
		var kept []unit
		for ui, u := range units {
			if !drop[ui] {
				kept = append(kept, u)
			}
		}
		units = kept
	}
}

// ---- workflow ----

// merge is the dream's curate.merge step: gate, one Opus activity (find, then a bodies call per group), ApplyEdits,
// then MergeDone.
func (r *run) merge(start time.Time) {
	began := workflow.Now(r.ctx)
	rec := StepRecord{Name: mergeStepName}
	finish := func(status, reason string) {
		rec.Status, rec.Reason = status, reason
		r.record(rec, began)
	}
	var gate MergeGateResult
	if err := workflow.ExecuteActivity(r.det, a.MergeGate, start).Get(r.ctx, &gate); err != nil {
		finish("failed", err.Error())
		return
	}
	if gate.Skip != "" {
		finish("skipped", gate.Skip)
		return
	}
	if r.capStopped() {
		finish("skipped", r.stopLLM)
		return
	}
	var out MergeStepResult
	err := workflow.ExecuteActivity(llmContext(r.ctx, config.ModelJudgment), a.MergeStep, MergeStepInput{
		RunSpentUSD: r.rep.CostUSD, BudgetUSD: stepBudgetUSD[config.ModelJudgment], CapUSD: RunCapUSD,
		Today: start.Format("2006-01-02")}).Get(r.ctx, &out)
	r.rep.CostUSD, r.rep.LLMCalls, r.rep.LLMCached = r.rep.CostUSD+out.CostUSD, r.rep.LLMCalls+out.Calls, r.rep.LLMCached+out.Cached
	rec.CostUSD, rec.Called, rec.Cached, rec.Refused = out.CostUSD, out.Calls > 0, out.Cached > 0, len(out.Refused)
	if out.StopLLM {
		r.stopLLM = out.Reason
	}
	if err != nil {
		finish("failed", err.Error())
		return
	}
	if out.Status != "ok" {
		finish(out.Status, out.Reason)
		return
	}
	if len(out.Edits) > 0 {
		r.proposed = append(r.proposed, out.Edits...)
		rec.Edits = r.apply(mergeStepName, out.Edits)
	}
	var done MergeDoneResult
	if err := workflow.ExecuteActivity(r.det, a.MergeDone, MergeDoneInput{RunID: r.rep.RunID, Now: start,
		Owner: out.Owner}).Get(r.ctx, &done); err != nil {
		finish("failed", "merge done: "+err.Error())
		return
	}
	finish("ok", out.summary(done))
}

func (r MergeStepResult) summary(done MergeDoneResult) string {
	narrow, superseded := 0, 0
	for _, c := range r.Plan.Conflicts {
		switch c.Resolution {
		case "narrow":
			narrow++
		case "supersede":
			superseded++
		}
	}
	s := fmt.Sprintf("%d global notes: %d duplicate groups, %d conflicts (%d superseded, %d narrowed, %d to the owner), "+
		"%d stale; %d files; %d owner questions queued, %d wait", r.Notes, len(r.Plan.Duplicates), len(r.Plan.Conflicts),
		superseded, narrow, len(r.Owner), len(r.Plan.Stale), len(r.Edits), done.Queued, done.Waiting)
	if len(r.Waiting) > 0 {
		s += fmt.Sprintf("; %d bodies wait, first: %s", len(r.Waiting), r.Waiting[0])
	}
	if len(r.Refused) > 0 {
		s += fmt.Sprintf("; %d refused, first: %s", len(r.Refused), r.Refused[0])
	}
	if r.Reason != "" && r.Reason != "cached" {
		s += "; " + r.Reason
	}
	return s
}
