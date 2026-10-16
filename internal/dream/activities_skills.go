package dream

import (
	"io/fs"

	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"saahil.local/brain/skills"
	"sort"
	"strings"
	"time"

	"saahil.local/brain/internal/delta"
	"saahil.local/brain/internal/filing"
	"saahil.local/brain/internal/llm"
)

// SkillStep is LLMStep for skills that answer with a delta (file, distil, link, hubs, summarise, verify): same input
// and result, so brain-worker registers it under the activity name "LLMStep" and the workflow and its history stay
// as they are. The delta is applied in dry run and comes back as whole-file edits, so the workflow's ApplyEdits keeps
// doing the backups, caps and reverts; a refused delete comes back as a delete edit, which the workflow counts.
func (a *Activities) SkillStep(ctx context.Context, in LLMStepInput) (LLMStepResult, error) {
	skill, err := a.readSkill(in.Skill)
	if errors.Is(err, os.ErrNotExist) {
		return LLMStepResult{Status: "skipped", Reason: "skill missing: " + in.Skill + ".md"}, nil
	}
	if err != nil {
		return LLMStepResult{}, err
	}
	input, err := a.skillInput(in)
	if err != nil {
		return LLMStepResult{}, err
	}
	key := llm.Key(in.Workflow, in.Step, in.Model, string(skill), input)
	res, out, err := a.callOnce(ctx, in, key, string(skill), input)
	if err != nil || out.Status != "ok" {
		return out, err
	}
	raw := res.JSON
	if len(raw) == 0 {
		raw = json.RawMessage(res.Text)
	}
	var d delta.Delta
	if err := json.Unmarshal(raw, &d); err != nil {
		// unmark the key at no cost: a cached broken answer would fail every later dream on the same material
		out.Status, out.Reason = "failed", "answer is not a delta"
		return out, a.Ledger.Finish(key, llm.Result{}, "failed", time.Now())
	}
	work := ""
	if len(a.Cfg.ShadowDocs) > 0 {
		work = a.Cfg.ShadowDocs[0]
	}
	rep, err := delta.Apply(ctx, a.Cfg.BrainRoot, d, delta.Options{DryRun: true, Now: time.Now(), WorkRoot: work})
	if err != nil {
		return out, err
	}
	for _, f := range rep.Files {
		out.Edits = append(out.Edits, Edit{Path: f.Path, Action: "write", Content: f.Content, Why: in.Step})
	}
	var reasons []string
	for _, r := range rep.Rejected {
		if r.Op == "delete" {
			out.Edits = append(out.Edits, Edit{Path: "notes/" + r.Target + ".md", Action: "delete", Why: r.Reason})
		} else {
			reasons = append(reasons, fmt.Sprintf("op %d %s %s: %s", r.Index, r.Op, r.Target, r.Reason))
		}
	}
	if len(rep.PendingCounters) > 0 {
		stats, err := delta.OpenStats(filepath.Join(a.Cfg.BrainRoot, ".index", "brain.db"))
		if err != nil {
			return out, err
		}
		_, err = stats.AddCounters(key, rep.PendingCounters) // once per answer, however often it is replayed
		stats.Close()
		if err != nil {
			return out, err
		}
	}
	out.Reason = strings.TrimPrefix(fmt.Sprintf("%s; %d ops, %d files, %d rejected, %d summaries, %d verdicts (answer in "+
		".data/llm/%s.json)", out.Reason, len(d.Ops), len(rep.Files), len(rep.Rejected), len(d.Summaries),
		len(d.Verdicts), key), "; ")
	if len(reasons) > 0 {
		out.Reason += "; first rejection: " + reasons[0]
	}
	out.Refused = len(reasons)
	return out, nil
}

// callOnce answers a key from the ledger for $0, or pays for one call when the run and daily caps allow it.
func (a *Activities) callOnce(ctx context.Context, in LLMStepInput, key, skill, input string) (llm.Result, LLMStepResult, error) {
	if res, ok, err := a.Ledger.Done(key); err != nil || ok {
		return res, LLMStepResult{Status: "ok", Reason: "cached", Cached: true}, err
	}
	if in.RunSpentUSD+in.BudgetUSD > RunCapUSD {
		return llm.Result{}, LLMStepResult{Status: "skipped", StopLLM: true, Reason: fmt.Sprintf(
			"run cap: $%.2f spent + $%.2f step budget > $%.2f", in.RunSpentUSD, in.BudgetUSD, RunCapUSD)}, nil
	}
	now := time.Now().UTC()
	today, err := a.Ledger.SpentSince(now.Truncate(24 * time.Hour))
	if err != nil {
		return llm.Result{}, LLMStepResult{}, err
	}
	if today+in.BudgetUSD > DailyCapUSD {
		return llm.Result{}, LLMStepResult{Status: "skipped", StopLLM: true, Reason: fmt.Sprintf(
			"daily cap: $%.2f spent today + $%.2f step budget > $%.2f", today, in.BudgetUSD, DailyCapUSD)}, nil
	}
	if err := a.Ledger.Begin(key, in.Workflow, in.Step, in.Model, now); err != nil {
		return llm.Result{}, LLMStepResult{}, err
	}
	res, runErr := a.LLM.Run(ctx, llm.Call{Skill: skill, Input: input, Model: in.Model, MaxBudgetUSD: in.BudgetUSD,
		JSONSchema: delta.Schema})
	out := LLMStepResult{Status: "ok", CostUSD: res.CostUSD, Called: true}
	var modelErr *llm.ModelError
	switch {
	case errors.As(runErr, &modelErr): // it cost money; a retry would pay again
		return res, LLMStepResult{Status: "failed", Reason: modelErr.Error(), CostUSD: res.CostUSD, Called: true},
			a.Ledger.Finish(key, res, "failed", time.Now())
	case runErr != nil:
		_ = a.Ledger.Finish(key, res, "failed", time.Now())
		return res, out, runErr
	}
	return res, out, a.Ledger.Finish(key, res, "done", time.Now())
}

// ---- what each skill reads besides the step's material ----

type skillPayload struct {
	stepPayload
	Today      string             `json:"today,omitempty"`
	Hubs       []filing.HubEntry  `json:"hubs,omitempty"`
	Candidates []entry            `json:"candidates,omitempty"` // link: notes that share a hub with the new ones
	Pages      []payloadFile      `json:"pages,omitempty"`      // hubs: the pages of the hubs this run touched
	Members    map[string][]entry `json:"members,omitempty"`    // hubs: their live notes, newest first
	Evidence   []payloadFile      `json:"evidence,omitempty"`   // verify: the files new claims cite
}

type entry struct {
	ID      string   `json:"id"`
	Type    string   `json:"type"`
	Status  string   `json:"status"`
	Summary string   `json:"summary"`
	Hubs    []string `json:"hubs,omitempty"`
	AsOf    string   `json:"as_of,omitempty"`
}

const (
	maxCandidates = 60
	maxMembers    = 40
)

var claimTypes = map[string]bool{"finding": true, "decision": true, "lesson": true, "anti-pattern": true, "playbook": true}

// skillInput adds to the dream's payload what a delta skill needs: the hub ids it may use, and per step the notes or
// files it judges against. today is the newest material's date, so a rerun of the same material asks the same thing.
func (a *Activities) skillInput(in LLMStepInput) (string, error) {
	base, err := buildInput(in)
	if err != nil {
		return "", err
	}
	var p skillPayload
	if err := json.Unmarshal([]byte(base), &p.stepPayload); err != nil {
		return "", err
	}
	for _, m := range in.Material {
		if d := m.MTime.UTC().Format("2006-01-02"); d > p.Today {
			p.Today = d
		}
	}
	if p.Hubs, err = filing.Hubs(a.Cfg.BrainRoot); err != nil {
		return "", err
	}
	if in.Skill == "link" || in.Skill == "hubs" || in.Skill == "verify" {
		var proposed []delta.Note
		for _, e := range in.Proposed {
			if n, err := delta.Parse([]byte(e.Content)); err == nil {
				proposed = append(proposed, n)
			}
		}
		switch in.Skill {
		case "link", "hubs":
			err = a.addCatalog(&p, in.Skill, proposed)
		case "verify":
			a.addEvidence(&p, proposed)
		}
	}
	b, _ := json.Marshal(p)
	return string(b), err
}

func (a *Activities) addCatalog(p *skillPayload, skill string, proposed []delta.Note) error {
	touched, isNew := map[string]bool{}, map[string]bool{}
	for _, n := range proposed {
		isNew[n.ID] = true
		for _, h := range n.Hubs {
			touched[h] = true
		}
	}
	paths, err := filepath.Glob(filepath.Join(a.Cfg.BrainRoot, "notes", "*.md"))
	if err != nil {
		return err
	}
	var all []entry
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		n, err := delta.Parse(b)
		if err != nil || isNew[n.ID] || (n.Status != "current" && n.Status != "proposed") {
			continue
		}
		all = append(all, entry{n.ID, n.Type, n.Status, n.Summary, n.Hubs, n.AsOf})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].AsOf > all[j].AsOf })
	if skill == "link" {
		for _, e := range all {
			if len(p.Candidates) < maxCandidates && anyIn(e.Hubs, touched) {
				p.Candidates = append(p.Candidates, e)
			}
		}
		return nil
	}
	p.Members = map[string][]entry{}
	for h := range touched {
		if b, err := os.ReadFile(filepath.Join(a.Cfg.BrainRoot, "hubs", h+".md")); err == nil {
			p.Pages = append(p.Pages, payloadFile{Path: "hubs/" + h + ".md", Content: string(b)})
		}
		for _, e := range all {
			if len(p.Members[h]) < maxMembers && anyIn(e.Hubs, map[string]bool{h: true}) {
				p.Members[h] = append(p.Members[h], e)
			}
		}
	}
	sort.Slice(p.Pages, func(i, j int) bool { return p.Pages[i].Path < p.Pages[j].Path })
	return nil
}

// addEvidence reads the files that new claims cite, within the dream's per-file and total input caps.
func (a *Activities) addEvidence(p *skillPayload, proposed []delta.Note) {
	seen, total := map[string]bool{}, 0
	for _, n := range proposed {
		if !claimTypes[n.Type] {
			continue
		}
		for _, ref := range append(append([]string{}, n.Evidence...), n.Sources...) {
			path, _, _ := strings.Cut(ref, "#")
			if seen[path] {
				continue
			}
			seen[path] = true
			b, err := a.readBrainDoc(path)
			if err != nil || total >= inputContentCap {
				continue
			}
			f := payloadFile{Path: path, Truncated: len(b) > fileContentCap}
			f.Content = string(b[:min(len(b), fileContentCap)])
			total += len(f.Content)
			p.Evidence = append(p.Evidence, f)
		}
	}
}

// readBrainDoc reads a brain-relative path, a work/ doc from the shadow's docs when the shadow brain lacks it; an
// absolute path or one that climbs out of the brain is refused.
func (a *Activities) readBrainDoc(path string) ([]byte, error) {
	if filepath.IsAbs(path) || strings.HasPrefix(filepath.Clean(path), "..") {
		return nil, fs.ErrPermission
	}
	b, err := os.ReadFile(filepath.Join(a.Cfg.BrainRoot, path))
	if rest, ok := strings.CutPrefix(path, "work/"); err != nil && ok && len(a.Cfg.ShadowDocs) > 0 {
		b, err = os.ReadFile(filepath.Join(a.Cfg.ShadowDocs[0], rest))
	}
	return b, err
}

func anyIn(list []string, set map[string]bool) bool {
	for _, v := range list {
		if set[v] {
			return true
		}
	}
	return false
}

// readSkill reads <SkillsDir>/<name>.md when a skills folder is configured (tests, trying a skill out), else the skill
// embedded in this binary, so the worker's skills are always the version its code expects.
func (a *Activities) readSkill(name string) ([]byte, error) {
	if a.Cfg.SkillsDir != "" {
		return os.ReadFile(filepath.Join(a.Cfg.SkillsDir, name+".md"))
	}
	b, err := skills.FS.ReadFile(name + ".md")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	return b, err
}
