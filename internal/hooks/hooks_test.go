package hooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"saahil.local/brain/internal/delta"
	"saahil.local/brain/internal/index"
)

type fixture struct {
	env Env
	cwd string
	db  *sql.DB
}

func put(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func note(id, typ, status, summary string, hubs []string, aliases ...string) string {
	return string(delta.Render(delta.Note{ID: id, Type: typ, Status: status, Summary: summary, Scope: "hub", Hubs: hubs,
		Evidence: []string{"work/ocr-census/STATE.md"}, Aliases: aliases}))
}

// newFixture builds a small brain and its index in a temp home, so no real memory folder or saahil-docs is read.
func newFixture(t *testing.T, live bool, extra map[string]string) fixture {
	home := t.TempDir()
	t.Setenv("BRAIN_HOME", home)
	root := filepath.Join(home, "brain")
	cwd := filepath.Join(home, "orca", "workspaces", "repo", "unrelated-folder")
	files := map[string]string{
		"hubs/index.md": note("index", "hub", "current", "home", nil),
		"hubs/image-lane.md": "---\nid: image-lane\ntype: hub\nstatus: current\nsummary: image lane\nbroader: [index]\n---\n# Image lane\n\n" +
			"## Start here\n\n- finding-ocr-recall — recall on the census\n- decision-paddle-first\n\n## Members\n\n- not this\n",
		"notes/finding-ocr-recall.md":    note("finding-ocr-recall", "finding", "current", "PaddleOCR recall on the image census is 0.91", []string{"image-lane"}, "ocr recall"),
		"notes/decision-paddle-first.md": note("decision-paddle-first", "decision", "current", "Run PaddleOCR before the vision model on scans", []string{"image-lane"}),
		"notes/finding-old-recall.md":    note("finding-old-recall", "finding", "superseded", "PaddleOCR recall on the image census was 0.80", []string{"image-lane"}),
		"notes/finding-tabular-nulls.md": note("finding-tabular-nulls", "finding", "current", "Tabular lane drops null columns before detection", []string{"image-lane"}),
		"work/ocr-census/STATE.md":       "# State\n\nOCR census half done.\n",
		"work/ocr-census/CARD.md":        "---\ntype: card\nstatus: live\nsummary: OCR census\nhubs: [image-lane]\n---\n# OCR census\n",
		"sessions/devbox/2026/10/2026-10-07-aaaa1111-ocr.md": "---\nid: session-2026-10-07-aaaa1111\ntype: session\ncwd: " + cwd +
			"\nworks: [ocr-census, gone-box]\n---\n## Owner prompts\n",
	}
	// unrelated docs give BM25 a realistic document frequency, so scores sit on the real corpus's scale
	words := strings.Fields("ledger queue retry budget tunnel lambda vector shard cache merge render socket parser cluster token")
	for i := range 150 {
		files[fmt.Sprintf("work/filler/doc-%03d.md", i)] = fmt.Sprintf("# %s %s notes\n\n%s %s %s and %s.\n",
			words[i%15], words[(i/15)%15], words[(i*7)%15], words[(i*3)%15], words[(i*11)%15], words[(i+5)%15])
	}
	for rel, body := range extra {
		files[rel] = body
	}
	for rel, body := range files {
		put(t, root, rel, body)
	}
	if live {
		put(t, root, ".brain/live", "")
	}
	ix, err := index.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Update(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ix.Close() })
	clock := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	env := Env{Root: root, WorkRoot: filepath.Join(home, "no-shadow"), StateDir: filepath.Join(home, "state"),
		Now: func() time.Time { clock = clock.Add(time.Millisecond); return clock }}
	if err := RebuildNames(env); err != nil {
		t.Fatal(err)
	}
	return fixture{env: env, cwd: cwd, db: ix.DB()}
}

func contextOf(t *testing.T, out []byte) string {
	t.Helper()
	if len(out) == 0 {
		return ""
	}
	var v struct {
		HookSpecificOutput struct{ AdditionalContext string } `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("hook output is not JSON: %s", out)
	}
	return v.HookSpecificOutput.AdditionalContext
}

func (f fixture) prompt(t *testing.T, session, text string) string {
	t.Helper()
	out, err := Prompt(f.env, Input{SessionID: session, Cwd: f.cwd, Prompt: text})
	if err != nil {
		t.Fatal(err)
	}
	return contextOf(t, out)
}

func (f fixture) names(t *testing.T) *Names {
	t.Helper()
	nm, err := BuildNames(f.db, func(p string) []byte { b, _ := os.ReadFile(filepath.Join(f.env.Root, p)); return b })
	if err != nil {
		t.Fatal(err)
	}
	return nm
}

func (f fixture) start(t *testing.T, in Input) string {
	t.Helper()
	if in.Cwd == "" {
		in.Cwd = f.cwd
	}
	out, err := Start(f.env, in)
	if err != nil {
		t.Fatal(err)
	}
	return contextOf(t, out)
}

func ids(lk Lookup) []string {
	var out []string
	for _, p := range lk.Chosen {
		out = append(out, p.ID)
	}
	return out
}

// digestPart drops the pointer list from a prompt's context, leaving the digest the first prompt carries.
func digestPart(context string) string {
	if i := strings.Index(context, "Brain pointers ("); i >= 0 {
		return strings.TrimSpace(context[:i])
	}
	return context
}

// TestDigestWorkArrivesWithTheFirstPrompt catches the start digest choosing work by the cwd alone (the laptop's
// digest on 2026-10-08 carried boxes its last session there had used) and the split digest passing 8,000 chars:
// SessionStart gives hot.md only; the first prompt names another box and gets its STATE, CARD and hub list within
// what start left; later prompts get none; a resumed session and a subagent get the whole digest of the picked box;
// a first prompt that names nothing falls back to the cwd's box, and a host with no prompt hook gets it at start.
func TestDigestWorkArrivesWithTheFirstPrompt(t *testing.T) {
	long := strings.Repeat("- finding-ocr-recall "+strings.Repeat("long start-here line ", 45)+"\n", 7)
	f := newFixture(t, true, map[string]string{
		"hot.md":                strings.Repeat("hot words here ", 700),
		"work/slice-y/CARD.md":  "---\ntype: card\nstatus: live\nsummary: slice y\nhubs: [image-lane]\n---\n# Slice Y\n",
		"work/slice-y/STATE.md": "# State\n\n" + strings.Repeat("state line of the slice run\n", 900),
		"work/lane-a/CARD.md":   "---\ntype: card\nstatus: live\nsummary: lane a\n---\n# Lane A\n",
		"work/lane-b/CARD.md":   "---\ntype: card\nstatus: live\nsummary: lane b\n---\n# Lane B\n",
		"hubs/image-lane.md":    "---\nid: image-lane\ntype: hub\nstatus: current\nsummary: image lane\nbroader: [index]\n---\n## Start here\n\n" + long,
	})
	start := f.start(t, Input{SessionID: "s1", Event: "SessionStart"})
	if !strings.Contains(start, "This week (hot.md)") || strings.Contains(start, "Work ") {
		t.Fatalf("start digest must be hot.md only:\n%s", start)
	}
	first := digestPart(f.prompt(t, "s1", "Pick up work/slice-y/STATE.md and finish the gate."))
	for _, want := range []string{"Work slice-y: STATE.md", "Hub image-lane: start here"} {
		if !strings.Contains(first, want) {
			t.Errorf("first prompt's digest lacks %q:\n%s", want, first)
		}
	}
	if strings.Contains(first, "ocr-census") || strings.Contains(first, "hot.md") {
		t.Errorf("first prompt's digest repeats hot.md or picks the cwd's box over the named one:\n%s", first)
	}
	if n := utf8.RuneCountInString(start) + utf8.RuneCountInString(first); n > 8000 {
		t.Errorf("start + first prompt digest is %d chars", n)
	}
	if later := f.prompt(t, "s1", "Pick up work/slice-y/STATE.md again."); strings.Contains(later, "Work ") {
		t.Errorf("a later prompt carried the digest again:\n%s", later)
	}
	for _, in := range []Input{{SessionID: "s1", Event: "SessionStart", Source: "resume"}, {SessionID: "s1", Event: "SubagentStart", AgentID: "a1"}} {
		if got := f.start(t, in); !strings.Contains(got, "Work slice-y: STATE.md") || !strings.Contains(got, "This week (hot.md)") {
			t.Errorf("%s/%s lost the picked box's digest:\n%s", in.Event, in.Source, got)
		}
	}
	if st, _ := f.env.loadState("s1"); !slices.Equal(st.Work, []string{"slice-y"}) {
		t.Errorf("state after resume and subagent: work %v, want [slice-y]", st.Work)
	}
	// the image-lane coordinator's first prompt on 2026-10-08 named eight boxes once each as reading, not its own
	for session, prompt := range map[string]string{"s2": "continue", "s4": "Read slice-y, lane-a and lane-b first."} {
		f.start(t, Input{SessionID: session, Event: "SessionStart"})
		if got := f.prompt(t, session, prompt); !strings.Contains(got, "Work ocr-census: STATE.md") {
			t.Errorf("%q names nothing or only a reading list: the cwd's box must be the work:\n%s", prompt, got)
		}
	}
	if got := f.start(t, Input{ConversationID: "c3", Event: "sessionStart"}); !strings.Contains(got, "Work ocr-census: STATE.md") {
		t.Errorf("a host with no prompt hook must get the work digest at start:\n%s", got)
	}
}

// TestPointersNeverNameTheSessionsOwnBox catches pointers spent on what the session already holds (on 2026-10-08 the
// image-lane coordinator was pointed at its own stale CARD): a prompt naming its box's STATE gets the census note,
// never a doc of the box.
func TestPointersNeverNameTheSessionsOwnBox(t *testing.T) {
	f := newFixture(t, true, nil)
	lk, err := Pointers(f.db, "Check work/ocr-census/STATE.md and the ocr recall on the image census.",
		Seeds{Work: []string{"ocr-census"}, Hubs: []string{"image-lane"}}, f.names(t))
	if err != nil {
		t.Fatal(err)
	}
	got := ids(lk)
	if !slices.Contains(got, "finding-ocr-recall") || slices.ContainsFunc(got, func(id string) bool { return strings.HasPrefix(id, "work/ocr-census") }) {
		t.Fatalf("pointers %v: want the census note and nothing from the session's own box", got)
	}
}

// TestFactsBeforeAnotherBoxsWorkDocs catches raw work docs of another box taking slots from facts: the lesson ranks
// first, the plan the prompt names by path may follow, and the runs the plan links to (named by nobody, not the best
// match) stay out.
func TestFactsBeforeAnotherBoxsWorkDocs(t *testing.T) {
	f := newFixture(t, true, map[string]string{
		"work/ledger-sweep/CARD.md":          "---\ntype: card\nstatus: live\nsummary: ledger sweep\n---\n# Ledger sweep\n",
		"work/ledger-sweep/plans/PLAN.md":    "# Ledger sweep plan\n\nRuns: [run a](../reports/run-a.md), [run b](../reports/run-b.md).\n",
		"work/ledger-sweep/reports/run-a.md": "# Ledger sweep run a\n\nThe retry budget held.\n",
		"work/ledger-sweep/reports/run-b.md": "# Ledger sweep run b\n\nThe retry budget broke.\n",
		"notes/lesson-ledger-retry-budget.md": note("lesson-ledger-retry-budget", "lesson", "current",
			"Cap the ledger sweep retry budget at three attempts", []string{"image-lane"}, "retry budget"),
	})
	lk, err := Pointers(f.db, "Compare work/ledger-sweep/plans/PLAN.md with what the retry budget runs showed.",
		Seeds{Work: []string{"ocr-census"}, Hubs: []string{"image-lane"}}, f.names(t))
	if err != nil {
		t.Fatal(err)
	}
	got := ids(lk)
	if len(got) == 0 || got[0] != "lesson-ledger-retry-budget" || !slices.Contains(got, "work/ledger-sweep/plans/PLAN.md") ||
		slices.ContainsFunc(got, func(id string) bool { return strings.Contains(id, "/reports/run-") }) {
		t.Fatalf("pointers %v: want the lesson first, the named plan, and no unnamed run report", got)
	}
}

// TestPausedBoxStaysOutUnlessNamed catches a paused box surfacing on its own (identity-resolution boxes the owner
// left on 2026-09-28 reached the laptop's digest on 2026-10-08): neither the digest nor the pointers show the paused
// box the cwd's last session used until a prompt names it.
func TestPausedBoxStaysOutUnlessNamed(t *testing.T) {
	f := newFixture(t, true, map[string]string{
		"work/ocr-census/CARD.md":           "---\ntype: card\nstatus: paused\nsummary: OCR census\nhubs: [image-lane]\n---\n# OCR census\n",
		"work/ocr-census/reports/recall.md": "---\ntype: report\nstatus: live\nsummary: OCR recall on the image census\naliases: [ocr recall]\n---\n# OCR recall\n\nRecall 0.91, as [the finding](../../../notes/finding-ocr-recall.md) says.\n",
	})
	nm := f.names(t)
	const ask = "What was the ocr recall on the image census?"
	for _, prompt := range []string{ask, ask + " See ocr-census."} {
		lk, err := Pointers(f.db, prompt, Seeds{}, nm)
		if err != nil {
			t.Fatal(err)
		}
		named := prompt != ask
		if got := ids(lk); slices.Contains(got, "work/ocr-census/reports/recall.md") != named {
			t.Errorf("%q: pointers %v (box named: %v)", prompt, got, named)
		}
	}
	f.start(t, Input{SessionID: "s1"})
	if got := f.prompt(t, "s1", ask); strings.Contains(got, "Work ocr-census") {
		t.Errorf("digest picked the paused box from the cwd:\n%s", got)
	}
	f.start(t, Input{SessionID: "s2"})
	if got := f.prompt(t, "s2", "Status of ocr-census please."); !strings.Contains(got, "Work ocr-census: STATE.md") {
		t.Errorf("a first prompt naming the paused box must get its digest:\n%s", got)
	}
}

// Catches pointers on prompts that match nothing well (chatter, one stray common word) and a missing pointer when
// the prompt names a note; a superseded note must never be pointed at.
func TestAbstainsOnWeakMatch(t *testing.T) {
	f := newFixture(t, true, nil)
	// the last one names a hub, so only the score threshold keeps its member notes out
	for _, weak := range []string{"try now", "give full status update please", "rename the variable in the image lane loader"} {
		if got := f.prompt(t, "s1", weak); got != "" {
			t.Errorf("%q got pointers:\n%s", weak, got)
		}
	}
	got := f.prompt(t, "s1", "what was the ocr recall on the image census?")
	if !strings.Contains(got, "finding-ocr-recall") || strings.Contains(got, "finding-old-recall") {
		t.Fatalf("pointers:\n%s", got)
	}
}

// Catches the prompt's text reaching storage: the lookups table and the session state hold ids, never words of the
// prompt (here a word no node id contains).
func TestLookupLogHasNoPromptText(t *testing.T) {
	f := newFixture(t, false, nil)
	text := "what was the ocr recall on the image census for zanzibar"
	if got := f.prompt(t, "s1", text); got != "" {
		t.Fatalf("shadow mode injected:\n%s", got)
	}
	var rows int
	var dump string
	err := f.db.QueryRow(`SELECT count(*) FILTER (WHERE hook = 'prompt'), group_concat(time || session || hook || candidates || chosen, '|') FROM lookups`).Scan(&rows, &dump)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := os.ReadFile(f.env.statePath("s1"))
	if rows != 1 || !strings.Contains(dump, "finding-ocr-recall") {
		t.Fatalf("want one prompt row naming the pointer, got %d: %s", rows, dump)
	}
	if strings.Contains(dump+string(state), "zanzibar") || strings.Contains(dump+string(state), text) {
		t.Fatalf("prompt text stored:\n%s\n%s", dump, state)
	}
}

// Catches shadow mode leaking: before the cut-over neither start nor prompt may print anything, and node_stats
// must not move, while each still logs its lookup.
func TestShadowInjectsNothing(t *testing.T) {
	f := newFixture(t, false, nil)
	out, err := Start(f.env, Input{SessionID: "s1", Cwd: f.cwd})
	if err != nil || len(out) != 0 {
		t.Fatalf("start printed %q (%v)", out, err)
	}
	if got := f.prompt(t, "s1", "what was the ocr recall on the image census?"); got != "" {
		t.Fatalf("prompt printed %q", got)
	}
	var lookups, stats int
	f.db.QueryRow(`SELECT (SELECT count(*) FROM lookups), (SELECT count(*) FROM node_stats)`).Scan(&lookups, &stats)
	if lookups != 3 || stats != 0 {
		t.Fatalf("lookups %d (want start, digest, prompt), node_stats %d (want 0)", lookups, stats)
	}
}

// Catches the usage funnel miscounting: a Read of a pointed-at note counts opened once per session however often
// it is read; an Edit counts followed; a file never shown counts nothing.
func TestPostCountsOncePerSession(t *testing.T) {
	f := newFixture(t, true, nil)
	f.prompt(t, "s1", "what was the ocr recall on the image census?")
	notePath := filepath.Join(f.env.Root, "notes", "finding-ocr-recall.md")
	post := func(tool, path string) {
		in, _ := json.Marshal(map[string]string{"file_path": path})
		if err := Post(f.env, Input{SessionID: "s1", ToolName: tool, ToolInput: in}); err != nil {
			t.Fatal(err)
		}
	}
	post("Read", notePath)
	post("Read", notePath)
	post("Edit", notePath)
	post("Read", filepath.Join(f.env.Root, "notes", "finding-tabular-nulls.md"))
	var shown, opened, followed int
	f.db.QueryRow(`SELECT shown, opened, followed FROM node_stats WHERE id = 'finding-ocr-recall'`).Scan(&shown, &opened, &followed)
	var others int
	f.db.QueryRow(`SELECT count(*) FROM node_stats WHERE id = 'finding-tabular-nulls' AND opened > 0`).Scan(&others)
	if shown != 1 || opened != 1 || followed != 1 || others != 0 {
		t.Fatalf("shown %d opened %d followed %d, unshown opened %d", shown, opened, followed, others)
	}
}

// Catches the explore slot never opening: with three strong, well-shown matches, a fresh note that also matches
// takes the third slot.
func TestExploreSlotGoesToFreshNote(t *testing.T) {
	f := newFixture(t, true, map[string]string{
		"notes/finding-ocr-census-a.md":     note("finding-ocr-census-a", "finding", "current", "OCR recall census on scans a", []string{"image-lane"}, "ocr recall census"),
		"notes/finding-ocr-census-b.md":     note("finding-ocr-census-b", "finding", "current", "OCR recall census on scans b", []string{"image-lane"}, "ocr recall census"),
		"notes/finding-ocr-census-fresh.md": note("finding-ocr-census-fresh", "finding", "current", "OCR recall census on photos", []string{"image-lane"}, "photos"),
	})
	for _, id := range []string{"finding-ocr-recall", "finding-ocr-census-a", "finding-ocr-census-b"} {
		if _, err := f.db.Exec(`INSERT INTO node_stats(id, shown) VALUES (?, 9)`, id); err != nil {
			t.Fatal(err)
		}
	}
	read := func(p string) []byte { b, _ := os.ReadFile(filepath.Join(f.env.Root, p)); return b }
	nm, err := BuildNames(f.db, read)
	if err != nil {
		t.Fatal(err)
	}
	lk, err := Pointers(f.db, "ocr recall census on scans and photos", Seeds{}, nm)
	if err != nil {
		t.Fatal(err)
	}
	if len(lk.Chosen) != 3 || lk.Chosen[2].ID != "finding-ocr-census-fresh" {
		t.Fatalf("chosen %+v", lk.Chosen)
	}
}

// TestChatterInABoxWorktreeAbstains catches the session's own work box opening the gate by itself: "try now" typed
// in the box's worktree gets no pointers (its first prompt carries the box's digest, nothing more).
func TestChatterInABoxWorktreeAbstains(t *testing.T) {
	f := newFixture(t, true, nil)
	f.cwd = filepath.Join(filepath.Dir(f.cwd), "ocr-census")
	for _, chatter := range []string{"try now", "continue please"} {
		if got := f.prompt(t, "s1", chatter); strings.Contains(got, "Brain pointers") {
			t.Errorf("%q in the box's worktree got pointers:\n%s", chatter, got)
		}
	}
}

// TestNamingAFolderDoesNotDoubleItsNamedFiles catches a brief's subject losing to files the brief names once that
// also anchor a folder it names: the mention and the folder credit were added, one piece of evidence counted twice
// (image-lane briefs on 2026-10-08 put plans/SPEC.md fourth behind three recon files, on 20 DEV items).
func TestNamingAFolderDoesNotDoubleItsNamedFiles(t *testing.T) {
	f := newFixture(t, true, map[string]string{
		"work/lane-z/CARD.md":       "---\ntype: card\nstatus: live\nsummary: lane z\n---\n# Lane Z\n",
		"work/lane-z/STATE.md":      "# State\n\nRunning.\n",
		"work/lane-z/GOAL.md":       "# Goal\n\nShip it.\n",
		"work/lane-z/plans/SPEC.md": "---\ntype: spec\nstatus: live\n---\n# Lane Z spec\n\nThe design.\n",
		"work/lane-z/recon/a.md":    "# Recon a\n\nFirst probe.\n",
		"work/lane-z/recon/b.md":    "# Recon b\n\nSecond probe.\n",
		"work/lane-z/recon/c.md":    "# Recon c\n\nThird probe.\n",
	})
	brief := "=== TASK === Attack the design in work/lane-z/plans/SPEC.md. Inputs in work/lane-z/recon/: " +
		"work/lane-z/recon/a.md, work/lane-z/recon/b.md and work/lane-z/recon/c.md."
	lk, err := Pointers(f.db, brief, Seeds{}, f.names(t))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(lk.Chosen, func(p Pointer) bool { return p.ID == "work/lane-z/plans/SPEC.md" }) {
		t.Fatalf("pointers %+v: the spec the brief names must be one", lk.Chosen)
	}
}

// TestNoPointerToTheBriefThePromptHolds catches a slot spent on the brief file a dispatch was pasted from: the
// session already holds its text (33 of 121 DEV items had one among their pointers on 2026-10-08).
func TestNoPointerToTheBriefThePromptHolds(t *testing.T) {
	const text = "Measure the OCR recall on the scanned census pages and write the numbers into the census report."
	f := newFixture(t, true, map[string]string{
		"work/slice-x/briefs/w9.md":  "# W9\n\n" + text + "\n",
		"work/slice-x/plans/PLAN.md": "# Plan\n\nThe census plan.\n",
	})
	brief := "=== TASK === W9 " + text + " Brief: work/slice-x/briefs/w9.md; plan: work/slice-x/plans/PLAN.md."
	lk, err := Pointers(f.db, brief, Seeds{}, f.names(t))
	if err != nil {
		t.Fatal(err)
	}
	got := ids(lk)
	if slices.Contains(got, "work/slice-x/briefs/w9.md") || !slices.Contains(got, "work/slice-x/plans/PLAN.md") {
		t.Fatalf("pointers %v: want the plan, never the brief the prompt holds", got)
	}
}
