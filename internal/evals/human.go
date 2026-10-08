package evals

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/index"
	"saahil.local/brain/internal/llm"
	"saahil.local/brain/internal/rank"
	"saahil.local/brain/internal/session"
)

// The human set is the headline retrieval eval (DESIGN §9; asked for by the owner on 2026-10-08): prompts the owner
// typed, each with the brain file that answered it. Each machine builds its own set from its own transcripts, since
// only that machine can re-read the prompts, and the report merges both machines' rows.

var DefaultHumanFrom = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

const (
	answerWindow    = 10 // tool calls after a prompt whose reads may hold its answer
	maxCandidates   = 6
	minTypedWords   = 6
	judgeBatchSize  = 10
	judgeParallel   = 4
	judgeCallBudget = 0.75 // USD, one judge call
	judgeStep       = "human-judge"
	promptChars     = 2000 // of a prompt, for the judge
	replyChars      = 600
	excerptChars    = 800
)

//go:embed judge-answer.md
var judgeSkill string

const judgeSchema = `{"type":"object","properties":{"answers":{"type":"array","items":{"type":"object",` +
	`"properties":{"id":{"type":"string"},"files":{"type":"array","items":{"type":"string"}}},"required":["id","files"]}}},` +
	`"required":["answers"]}`

type HumanItem struct {
	ID         string    `json:"id"`
	Machine    string    `json:"machine"`
	Transcript string    `json:"transcript"` // the prompt is re-read from here when the eval runs, never stored
	Turn       int       `json:"turn"`       // the prompt's index among the transcript's owner prompts
	At         time.Time `json:"at"`
	CWD        string    `json:"cwd"`
	Session    string    `json:"session,omitempty"` // its session note: notes learned from it are hidden
	Targets    []string  `json:"targets"`
}

type HumanManifest struct {
	Built          time.Time      `json:"built"`
	Machine        string         `json:"machine"`
	From           string         `json:"from"`
	Model          string         `json:"model"`
	Transcripts    int            `json:"transcripts"`
	OwnerPrompts   int            `json:"owner_prompts"`
	Typed          int            `json:"typed"`
	WithCandidates int            `json:"with_candidates"`
	Judged         int            `json:"judged"`
	None           int            `json:"judged_none"`
	Kept           int            `json:"kept"`
	Calls          int            `json:"calls"`
	Cached         int            `json:"cached"`
	CostUSD        float64        `json:"cost_usd"`
	Skipped        map[string]int `json:"skipped"`
}

type HumanSet struct {
	Manifest HumanManifest
	Items    []HumanItem
}

type HumanBuildOptions struct {
	From     time.Time
	Now      time.Time
	Max      int // items kept
	JudgeMax int // prompts sent to the judge, at most
	DryRun   bool
	Runner   llm.Runner
	Ledger   *llm.Ledger
}

var ErrNoHumanSet = errors.New("no human eval set on this machine: run `brain eval build --human` first")

func humanSetPath(root string, m config.Machine) string {
	return filepath.Join(setsDir(root), "human-"+string(m)+".jsonl")
}

func humanManifestPath(root string, m config.Machine) string {
	return filepath.Join(setsDir(root), "human-"+string(m)+".manifest.json")
}

// ---- which prompts the owner typed ----

var (
	pastedBlock        = regexp.MustCompile(`(?s)<pasted_content[^>]*>.*?</pasted_content[^>]*>`)
	automationPrefixes = []string{"self-check (owner automation", "advisor note", "[owner's advisor"}
)

// ownerTyped reports whether an owner prompt is one the owner typed: not an Orca dispatch brief, a slash command or
// harness tag, or a note an automation sent through the owner's terminal, with minTypedWords words of the owner's own
// outside pastes.
func ownerTyped(prompt string) bool {
	if isDispatchBrief(prompt) {
		return false
	}
	own := strings.TrimSpace(pastedBlock.ReplaceAllString(prompt, " "))
	if strings.HasPrefix(own, "<") || strings.HasPrefix(own, "/") {
		return false
	}
	lower := strings.ToLower(own)
	for _, p := range automationPrefixes {
		if strings.HasPrefix(lower, p) {
			return false
		}
	}
	return len(strings.Fields(own)) >= minTypedWords
}

// ---- transcripts ----

type turn struct {
	index int
	at    time.Time
	cwd   string
	text  string // redacted, held in memory only
	reply string // the agent's last words before the next owner prompt, redacted, memory only
	refs  []string
	tools int
}

type toolBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name"`
	Text  string          `json:"text"`
	Input json.RawMessage `json:"input"`
}

var assistantType = []byte(`"type":"assistant"`)

// typedTurns reads a transcript's owner-typed prompts from `from` on, each with what the next answerWindow tool calls
// read and the agent's last words before the next owner prompt. A turn's index counts every owner prompt the way
// ownerPrompts does, so the eval re-reads the same prompt. owners counts the owner prompts from `from` on.
func typedTurns(transcript string, from time.Time) (turns []turn, owners int, err error) {
	f, err := os.Open(transcript)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	var cur *turn
	index, cwd := -1, ""
	sc := newTranscriptScanner(f)
	for sc.Scan() {
		line := sc.Bytes()
		human := bytes.Contains(line, humanOrigin)
		if !human && (cur == nil || !bytes.Contains(line, assistantType)) {
			continue
		}
		var l transcriptLine
		if json.Unmarshal(line, &l) != nil {
			continue
		}
		if l.CWD != "" {
			cwd = config.Canonical(l.CWD)
		}
		if text := l.ownerPrompt(); human && text != "" {
			index++
			cur = nil
			at, _ := time.Parse(time.RFC3339Nano, l.Timestamp)
			if at.Before(from) {
				continue
			}
			owners++
			if text = session.Redact(text); ownerTyped(text) {
				turns = append(turns, turn{index: index, at: at, cwd: cwd, text: text})
				cur = &turns[len(turns)-1]
			}
			continue
		}
		if cur == nil || l.Type != "assistant" || l.IsSidechain || l.Message == nil {
			continue
		}
		var blocks []toolBlock
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			switch {
			case b.Type == "text" && strings.TrimSpace(b.Text) != "":
				cur.reply = b.Text
			case b.Type == "tool_use" && cur.tools < answerWindow:
				cur.tools++
				cur.refs = append(cur.refs, readRefs(b.Name, b.Input, cwd)...)
			}
		}
	}
	for i := range turns {
		turns[i].reply = truncate(session.Redact(turns[i].reply), replyChars)
	}
	return turns, owners, sc.Err()
}

var brainCLIRef = regexp.MustCompile(`\bbrain (?:show|sections) +([^\s'";|&]+)`)

// readRefs are the files and brain nodes one tool call read: its path inputs, the absolute paths in a shell command,
// and the node a brain_show or brain_sections opened. A write reads nothing: the agent already had what it wrote.
func readRefs(name string, input json.RawMessage, cwd string) []string {
	var in struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		Path         string `json:"path"`
		Command      string `json:"command"`
		ID           string `json:"id"`
	}
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	switch {
	case name == "Write" || name == "Edit" || name == "MultiEdit" || name == "NotebookEdit":
		return nil
	case strings.Contains(name, "brain_show") || strings.Contains(name, "brain_sections"):
		return []string{in.ID}
	}
	var refs []string
	for _, p := range []string{in.FilePath, in.NotebookPath, in.Path} {
		switch {
		case p == "":
		case filepath.IsAbs(p) || strings.HasPrefix(p, "~/"):
			refs = append(refs, p)
		case cwd != "":
			refs = append(refs, filepath.Join(cwd, p))
		}
	}
	if in.Command != "" {
		for _, tok := range strings.FieldsFunc(in.Command, func(r rune) bool { return strings.ContainsRune(" \t\n'\"`;|&()<>=", r) }) {
			tok = strings.TrimRight(tok, ".,:)]}")
			if (strings.HasPrefix(tok, "/") || strings.HasPrefix(tok, "~/")) && !strings.ContainsAny(tok, "$*?[{") {
				refs = append(refs, tok)
			}
		}
		for _, m := range brainCLIRef.FindAllStringSubmatch(in.Command, -1) {
			refs = append(refs, m[1])
		}
	}
	return refs
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// ---- candidates ----

var answerRoots = []string{"work/", "notes/", "hubs/", "memory-import/"}

// answerCandidates resolves a turn's reads to the brain files that may hold its answer: indexed docs and data files
// under work/, notes/, hubs/ and memory-import/ that existed when the prompt was typed. A byte copy counts as its
// canonical and a memory as the notes filed from it (raw memory copies never rank); generic files and files whose
// text the prompt already holds are dropped.
func (b *builder) answerCandidates(t turn, filed map[string][]string) []string {
	prompt := rank.Shingles(t.text)
	seen := map[string]bool{}
	var out []string
	for _, ref := range t.refs {
		p, ok := b.answerPath(ref)
		if !ok || seen[p] {
			continue
		}
		seen[p] = true
		if !b.exist.before(p, t.at) {
			b.skipped["human: file born after the prompt"]++
			continue
		}
		var id string
		_ = b.ix.DB().QueryRow(`SELECT id FROM node WHERE path = ? AND kind != 'section' LIMIT 1`, p).Scan(&id)
		if body, err := rank.Body(b.ix.DB(), id); err == nil && rank.Echo(body, prompt) >= rank.EchoShare {
			b.skipped["human: file text already in the prompt"]++
			continue
		}
		paths := []string{p}
		if k, isMemory := strings.CutPrefix(p, "memory-import/"); isMemory {
			paths = nil
			for _, note := range filed[k] {
				var np string
				if b.ix.DB().QueryRow(`SELECT path FROM node WHERE id = ?`, note).Scan(&np) == nil {
					paths = append(paths, np)
				}
			}
			if len(paths) == 0 {
				b.skipped["human: memory not filed as a note"]++
			}
		}
		for _, cp := range paths {
			if !slices.Contains(out, cp) {
				out = append(out, cp)
			}
		}
	}
	return out[:min(len(out), maxCandidates)]
}

// answerPath maps a read to the path of an indexed brain file under answerRoots, if it is one.
func (b *builder) answerPath(ref string) (string, bool) {
	if strings.HasPrefix(ref, "~/") {
		ref = filepath.Join(config.Home(), ref[2:])
	}
	if filepath.IsAbs(ref) {
		ref = config.Canonical(ref)
		// a saahil-docs spelling (the old box root, a compat or worktree link to it) is the box under work/
		if _, rest, ok := strings.Cut(ref, "/saahil-docs/"); ok {
			ref = "work/" + rest
		}
	}
	id, err := b.ix.Resolve(ref)
	if err != nil {
		return "", false
	}
	var kind, typ, p, canonical string
	if b.ix.DB().QueryRow(`SELECT kind, type, path, canonical_of FROM node WHERE id = ?`, id).Scan(&kind, &typ, &p, &canonical) != nil {
		return "", false
	}
	if canonical != "" && b.ix.DB().QueryRow(`SELECT kind, type, path FROM node WHERE id = ?`, canonical).Scan(&kind, &typ, &p) != nil {
		return "", false
	}
	if kind == "section" {
		kind = "doc" // a section opened by id: its document
	}
	if (kind != "doc" && kind != "artifact") || typ == "session" || typ == "bulk" || typ == "repo" || genericFiles[path.Base(p)] ||
		!slices.ContainsFunc(answerRoots, func(r string) bool { return strings.HasPrefix(p, r) }) {
		return "", false
	}
	return p, true
}

// ---- build ----

type pendingItem struct {
	item       HumanItem
	turn       turn
	candidates []string
}

// BuildHuman builds this machine's human set: owner-typed prompts from its transcripts since opt.From whose next tool
// calls read a brain file, each judged by one Opus call per batch for the file that answered it ('none' dropped).
// Prompts go to the judge sessions-in-turn until opt.Max items are kept or opt.JudgeMax prompts were judged. A dry run
// counts the candidates and neither calls the judge nor writes.
func BuildHuman(ctx context.Context, ix *index.Index, opt HumanBuildOptions) (HumanSet, error) {
	machine := config.ThisMachine()
	exist, err := newExistence(ix.Root, ix.DB())
	if err != nil {
		return HumanSet{}, err
	}
	b := &builder{ix: ix, root: ix.Root, exist: exist, skipped: map[string]int{}}
	m := HumanManifest{Built: opt.Now.UTC(), Machine: string(machine), From: opt.From.Format(time.DateOnly), Model: config.ModelJudgment,
		Skipped: b.skipped}
	transcripts, err := filepath.Glob(filepath.Join(config.ClaudeDir(), "projects", "*", "*.jsonl"))
	if err != nil {
		return HumanSet{}, err
	}
	sort.Strings(transcripts)
	filed := filedNotes(ix.DB())
	var pending []pendingItem
	for _, tr := range transcripts {
		if fi, err := os.Stat(tr); err != nil || fi.ModTime().Before(opt.From) {
			continue // last written before the window: no prompt in it is
		}
		m.Transcripts++
		turns, owners, err := typedTurns(tr, opt.From)
		if err != nil {
			b.skipped["human: transcript not fully readable"]++
		}
		m.OwnerPrompts += owners
		m.Typed += len(turns)
		canon := config.Canonical(tr)
		sid := strings.TrimSuffix(filepath.Base(tr), ".jsonl")
		note := sessionNoteOf(ix, sid)
		for _, t := range turns {
			cands := b.answerCandidates(t, filed)
			if len(cands) == 0 {
				b.skipped["human: no brain file read in the next 10 tool calls"]++
				continue
			}
			pending = append(pending, pendingItem{turn: t, candidates: cands, item: HumanItem{
				ID: fmt.Sprintf("human-%s-%s-%d", machine, sid[:min(8, len(sid))], t.index), Machine: string(machine),
				Transcript: canon, Turn: t.index, At: t.at.UTC(), CWD: t.cwd, Session: note}})
		}
	}
	m.WithCandidates = len(pending)
	if opt.DryRun {
		return HumanSet{Manifest: m}, nil
	}
	items, err := b.judge(ctx, judgeOrder(pending), opt, &m)
	if err != nil {
		return HumanSet{}, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	m.Kept = len(items)
	set := HumanSet{Manifest: m, Items: items}
	if err := os.MkdirAll(setsDir(ix.Root), 0o755); err != nil {
		return set, err
	}
	if err := writeAtomic(humanSetPath(ix.Root, machine), jsonLines(items)); err != nil {
		return set, err
	}
	raw, _ := json.MarshalIndent(m, "", "  ")
	return set, writeAtomic(humanManifestPath(ix.Root, machine), append(raw, '\n'))
}

// sessionNoteOf is the session note of a transcript's session (sessions/…/<date>-<session8>-<slug>.md), "" when the
// brain has none.
func sessionNoteOf(ix *index.Index, sessionID string) string {
	if len(sessionID) < 8 {
		return ""
	}
	var p string
	_ = ix.DB().QueryRow(`SELECT path FROM node WHERE type = 'session' AND path LIKE ? ORDER BY path LIMIT 1`,
		"sessions/%-"+sessionID[:8]+"-%").Scan(&p)
	return p
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return string(sum[:])
}

// judgeOrder spreads the judged prompts over sessions and over each session's life: sessions take turns, and inside a
// session prompts go in hash order, so one long session cannot fill the set.
func judgeOrder(items []pendingItem) []pendingItem {
	bySession := map[string][]pendingItem{}
	var sessions []string
	for _, it := range items {
		if _, ok := bySession[it.item.Transcript]; !ok {
			sessions = append(sessions, it.item.Transcript)
		}
		bySession[it.item.Transcript] = append(bySession[it.item.Transcript], it)
	}
	sort.Slice(sessions, func(i, j int) bool { return hashOf(sessions[i]) < hashOf(sessions[j]) })
	for _, s := range sessions {
		sort.Slice(bySession[s], func(i, j int) bool { return hashOf(bySession[s][i].item.ID) < hashOf(bySession[s][j].item.ID) })
	}
	out := make([]pendingItem, 0, len(items))
	for round := 0; len(out) < len(items); round++ {
		for _, s := range sessions {
			if round < len(bySession[s]) {
				out = append(out, bySession[s][round])
			}
		}
	}
	return out
}

type judgeCandidate struct {
	Path    string `json:"path"`
	Title   string `json:"title,omitempty"`
	Summary string `json:"summary,omitempty"`
	Excerpt string `json:"excerpt"`
}

type judgeItem struct {
	ID         string           `json:"id"`
	CWD        string           `json:"cwd"`
	Prompt     string           `json:"prompt"`
	Reply      string           `json:"reply,omitempty"`
	Candidates []judgeCandidate `json:"candidates"`
}

// judge sends the prompts in order, judgeParallel batches at a time, until opt.Max are kept or opt.JudgeMax judged,
// and returns the kept items with the files the judge picked as their targets.
func (b *builder) judge(ctx context.Context, order []pendingItem, opt HumanBuildOptions, m *HumanManifest) ([]HumanItem, error) {
	var kept []HumanItem
	var mu sync.Mutex
	for next := 0; next < len(order) && len(kept) < opt.Max && m.Judged < opt.JudgeMax; {
		var batches [][]pendingItem
		for len(batches) < judgeParallel && next < len(order) && m.Judged < opt.JudgeMax {
			n := min(judgeBatchSize, len(order)-next, opt.JudgeMax-m.Judged)
			batches = append(batches, order[next:next+n])
			next += n
			m.Judged += n
		}
		answers := make([]map[string][]string, len(batches))
		errs := make([]error, len(batches))
		var wg sync.WaitGroup
		for i, batch := range batches {
			wg.Add(1)
			go func() {
				defer wg.Done()
				answers[i], errs[i] = b.judgeBatch(ctx, batch, opt, m, &mu)
			}()
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			return nil, err
		}
		for i, batch := range batches {
			for _, p := range batch {
				files := answers[i][p.item.ID]
				if len(files) == 0 {
					m.None++
					continue
				}
				if len(kept) < opt.Max {
					p.item.Targets = files
					kept = append(kept, p.item)
				}
			}
		}
	}
	return kept, nil
}

// judgeBatch asks the judge about one batch, from the ledger when this exact batch was answered before. Each item's
// answer keeps only its own candidates.
func (b *builder) judgeBatch(ctx context.Context, batch []pendingItem, opt HumanBuildOptions, m *HumanManifest, mu *sync.Mutex) (map[string][]string, error) {
	items := make([]judgeItem, len(batch))
	for i, p := range batch {
		items[i] = judgeItem{ID: p.item.ID, CWD: p.item.CWD, Prompt: truncate(p.turn.text, promptChars), Reply: p.turn.reply}
		for _, c := range p.candidates {
			items[i].Candidates = append(items[i].Candidates, b.describe(c))
		}
	}
	raw, err := json.Marshal(map[string]any{"step": "answer", "items": items})
	if err != nil {
		return nil, err
	}
	input := string(raw)
	key := llm.Key("eval", judgeStep, config.ModelJudgment, judgeSkill, input)
	mu.Lock()
	res, done, err := opt.Ledger.Done(key)
	if err == nil && !done {
		err = opt.Ledger.Begin(key, "eval", judgeStep, config.ModelJudgment, time.Now())
	}
	if done {
		m.Cached++
	}
	mu.Unlock()
	if err != nil {
		return nil, err
	}
	if !done {
		var runErr error
		res, runErr = opt.Runner.Run(ctx, llm.Call{Skill: judgeSkill, Input: input, Model: config.ModelJudgment,
			MaxBudgetUSD: judgeCallBudget, JSONSchema: judgeSchema})
		status := "done"
		if runErr != nil {
			status = "failed"
		}
		mu.Lock()
		m.Calls++
		m.CostUSD += res.CostUSD
		err := opt.Ledger.Finish(key, res, status, time.Now())
		mu.Unlock()
		if err := errors.Join(runErr, err); err != nil {
			return nil, fmt.Errorf("judge: %w", err)
		}
	}
	answer := res.JSON
	if len(answer) == 0 {
		answer = []byte(res.Text)
	}
	var out struct {
		Answers []struct {
			ID    string   `json:"id"`
			Files []string `json:"files"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(answer, &out); err != nil {
		return nil, fmt.Errorf("judge answer is not the schema's JSON: %w", err)
	}
	picked := map[string][]string{}
	mu.Lock()
	defer mu.Unlock()
	for _, a := range out.Answers {
		i := slices.IndexFunc(batch, func(p pendingItem) bool { return p.item.ID == a.ID })
		if i < 0 {
			b.skipped["human: judge answered an id it was not asked"]++
			continue
		}
		for _, f := range a.Files {
			if slices.Contains(batch[i].candidates, f) && !slices.Contains(picked[a.ID], f) {
				picked[a.ID] = append(picked[a.ID], f)
			} else if !slices.Contains(batch[i].candidates, f) {
				b.skipped["human: judge picked a file it was not offered"]++
			}
		}
	}
	return picked, nil
}

func (b *builder) describe(p string) judgeCandidate {
	c := judgeCandidate{Path: p}
	var id string
	_ = b.ix.DB().QueryRow(`SELECT id, title, summary FROM node WHERE path = ? AND kind != 'section' LIMIT 1`, p).Scan(&id, &c.Title, &c.Summary)
	if body, err := rank.Body(b.ix.DB(), id); err == nil {
		c.Excerpt = truncate(session.Redact(body), excerptChars)
	}
	return c
}

func LoadHuman(root string, m config.Machine) (HumanSet, error) {
	var set HumanSet
	raw, err := os.ReadFile(humanManifestPath(root, m))
	if errors.Is(err, fs.ErrNotExist) {
		return set, ErrNoHumanSet
	}
	if err != nil {
		return set, err
	}
	if err := json.Unmarshal(raw, &set.Manifest); err != nil {
		return set, err
	}
	return set, readLines(humanSetPath(root, m), &set.Items)
}

// ---- the eval ----

type HumanRow struct {
	ID       string   `json:"id"`
	Machine  string   `json:"machine"`
	Split    string   `json:"split"`
	Targets  []string `json:"targets"`
	Top3     bool     `json:"top3"`
	Calls    int      `json:"calls"`    // the L2 navigator's tool calls to the first target; 0 = not within 3
	L0Calls  int      `json:"l0_calls"` // the same, counting what L0 and L1 hand the session first
	Level    string   `json:"level"`
	Pointers []string `json:"pointers"`
	Error    string   `json:"error,omitempty"`
}

func (HumanRow) header() []string {
	return []string{"id", "machine", "split", "targets", "top-3", "calls", "L0-aware calls", "level", "pointers"}
}

func (r HumanRow) cells() []string {
	top3 := yes(r.Top3)
	if r.Error != "" {
		top3 = "error: " + r.Error
	}
	return []string{r.ID, r.Machine, r.Split, list(r.Targets), top3, callsCell(r.Calls), callsCell(r.L0Calls), r.Level, list(r.Pointers)}
}

type HumanRun struct {
	Own      Report         // this machine's rows: what the other machine merges
	Merged   Report         // both machines'
	Machines map[string]int // rows per machine in Merged
	Other    string         // the other machine's report merged in, "" when it has none
}

// humanSplit puts an item in DEV or TEST by its answer file (its first target by name), so prompts answered by one file
// never sit in both halves; a session key left one half nearly empty, two long sessions holding most items.
func humanSplit(it HumanItem) string { return SplitOf(slices.Min(it.Targets)) }

// Human runs this machine's items, re-reading each prompt from its transcript, and merges the other machine's rows
// from its latest report. With write, both reports land under ReportsDir: human-<machine> and the merged human.
func (e *Env) Human(set HumanSet, write bool) (HumanRun, error) {
	machine := config.ThisMachine()
	items := set.Items
	if e.only != "" {
		items = slices.DeleteFunc(slices.Clone(items), func(it HumanItem) bool { return humanSplit(it) != e.only })
	}
	rows, err := e.humanRows(items)
	if err != nil {
		return HumanRun{}, err
	}
	run := HumanRun{Own: e.humanReport("human-"+string(machine), rows, nil)}
	other, file, err := latestHumanRows(e.Index.Root, machine.Other())
	if err != nil {
		return run, err
	}
	if e.only != "" {
		other = slices.DeleteFunc(other, func(r HumanRow) bool { return r.Split != e.only })
	}
	run.Other = file
	note := fmt.Sprintf("%s rows: from %s", machine.Other(), file)
	if file == "" {
		note = fmt.Sprintf("%s rows: none yet (run `brain eval human` there; its report reaches this machine with the sync)", machine.Other())
	}
	all := append(slices.Clone(rows), other...)
	run.Merged = e.humanReport("human", all, []string{note})
	run.Own.SetsBuilt, run.Merged.SetsBuilt = set.Manifest.Built, set.Manifest.Built
	run.Machines = map[string]int{string(machine): len(rows), string(machine.Other()): len(other)}
	if write {
		for _, r := range []Report{run.Own, run.Merged} {
			if _, err := r.Write(e.Index.Root); err != nil {
				return run, err
			}
		}
	}
	return run, nil
}

func (e *Env) humanRows(items []HumanItem) ([]HumanRow, error) {
	last := map[string]int{}
	for _, it := range items {
		last[it.Transcript] = max(last[it.Transcript], it.Turn)
	}
	prompts := map[string][]string{} // transcript → its owner prompts, read once
	for tr, n := range last {
		prompts[tr] = ownerPrompts(tr, n+1)
	}
	rows := make([]HumanRow, 0, len(items))
	for _, it := range items {
		r := HumanRow{ID: it.ID, Machine: it.Machine, Split: humanSplit(it), Targets: it.Targets}
		ps := prompts[it.Transcript]
		switch {
		case it.Turn >= len(ps):
			r.Error = "owner prompt " + strconv.Itoa(it.Turn) + " is not in the transcript on this machine"
		case !ownerTyped(ps[it.Turn]):
			r.Error = "turn " + strconv.Itoa(it.Turn) + " is no longer an owner-typed prompt"
		}
		if r.Error != "" {
			rows = append(rows, r)
			continue
		}
		q := ps[it.Turn]
		t := e.targetsByPath(it.Targets)
		g := e.guardFor(it.Session, it.At)
		g.vetted = map[string]bool{}
		for _, target := range it.Targets {
			g.vetted[target] = true
		}
		r.Pointers = e.Ranker.Pointers(q, it.CWD, g.hides)
		r.Top3 = t.any(r.Pointers)
		r.Calls = e.brainCalls(q, t, g)
		l0, err := e.levelZero(it.CWD, g)
		if err != nil {
			return nil, fmt.Errorf("%s: L0: %w", it.ID, err)
		}
		r.L0Calls, r.Level = reach(t, l0, r.Pointers, r.Calls)
		rows = append(rows, r)
	}
	return rows, nil
}

func (e *Env) humanReport(eval string, rows []HumanRow, notes []string) Report {
	ok := func(r HumanRow) bool { return r.Error == "" }
	metrics, splits := perSplit(rows, func(r HumanRow) string { return r.Split }, func(rows []HumanRow) []Metric {
		return []Metric{
			rate("human", "top-3 hit rate (pointer ranker)", rows, ok, func(r HumanRow) bool { return r.Top3 }, 0.70),
			rate("human", "reachable within 3 tool calls, counting L0 + L1", rows, ok, func(r HumanRow) bool { return r.L0Calls > 0 }, 0.80),
		}
	})
	worst := slices.DeleteFunc(slices.Clone(rows), func(r HumanRow) bool { return r.Top3 && r.L0Calls > 0 })
	sort.SliceStable(worst, func(i, j int) bool {
		if worst[i].Top3 != worst[j].Top3 {
			return !worst[i].Top3
		}
		return worst[i].ID < worst[j].ID
	})
	failed := len(slices.DeleteFunc(slices.Clone(rows), ok))
	return newReport(e, eval, metrics, splits, rows, worst[:min(20, len(worst))], append([]string{
		"set: prompts the owner typed (human origin; no Orca brief, slash command or automation note; 6+ words of the owner's own outside pastes), from each machine's transcripts; targets: the brain files (work/, notes/, hubs/, a memory as its filed note) the session read or opened in the next 10 tool calls, existing before the prompt, that a judge (one Opus call per batch) picked as the answer; prompts it judged 'none' are not in the set",
		"query: the prompt, re-read from the transcript when the eval runs (never stored); each machine runs its own rows and merges the other machine's from its latest report",
		"index: the live index as of the prompt (the navigation leak guard); counting L0 + L1 as in navigation",
		fmt.Sprintf("%d items had no readable prompt and are left out of every rate", failed),
	}, notes...))
}

// latestHumanRows reads the rows of machine m's latest human report, and names its file ("" when there is none).
func latestHumanRows(root string, m config.Machine) ([]HumanRow, string, error) {
	files, err := filepath.Glob(filepath.Join(Dir(root), "reports", "*", "human-"+string(m)+".json"))
	if err != nil || len(files) == 0 {
		return nil, "", err
	}
	sort.Strings(files) // date folders: the last is the latest
	file := files[len(files)-1]
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, "", err
	}
	var r struct {
		Rows []HumanRow `json:"rows"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, "", fmt.Errorf("%s: %w", file, err)
	}
	rel, _ := filepath.Rel(root, file)
	return r.Rows, filepath.ToSlash(rel), nil
}

// Headline is the merged result in one line: both rates and how many rows each machine gave.
func (r HumanRun) Headline() string {
	cell := func(i int) string {
		if i >= len(r.Merged.Metrics) || r.Merged.Metrics[i].All.N == 0 {
			return "n/a"
		}
		return fmt.Sprintf("%.1f%%", r.Merged.Metrics[i].All.Value*100)
	}
	n := 0
	if len(r.Merged.Metrics) > 0 {
		n = r.Merged.Metrics[0].All.N
	}
	machines := make([]string, 0, len(r.Machines))
	for _, m := range []config.Machine{config.Devbox, config.Laptop} {
		machines = append(machines, fmt.Sprintf("%s %d", m, r.Machines[string(m)]))
	}
	return fmt.Sprintf("%-15s owner-typed prompts → the file that answered: top-3 %s · within 3 calls (L0 + L1) %s  (n=%d: %s)",
		"human", cell(0), cell(1), n, strings.Join(machines, ", "))
}
