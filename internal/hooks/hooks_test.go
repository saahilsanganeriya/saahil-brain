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

// TestDigestListsWhatTheWorksSessionsReadMost catches a digest silent on the files earlier sessions on its work kept
// reading (on 2026-10-04 slice-a gate workers each spent calls finding the driver campaign's STATE.md and ledger,
// read by most slice-a sessions before them): most-read first, a file one session read, a folder summary (sessions
// list a folder they ls) and what the digest quotes stay out, and a replay counts only the sessions that had ended
// by its start.
func TestDigestListsWhatTheWorksSessionsReadMost(t *testing.T) {
	session := func(id string, works, touched []string) string {
		return "---\nid: session-" + id + "\ntype: session\nended: 2026-10-07T10:00:00Z\nworks: [" + strings.Join(works, ", ") +
			"]\ntouched: [" + strings.Join(touched, ", ") + "]\n---\n## Owner prompts\n"
	}
	slice, lane := []string{"slice-y"}, []string{"lane-a"}
	read := []string{"work/driver/STATE.md", "work/driver/LEDGER.md", "work/slice-y/STATE.md", "work/driver/artifacts"}
	f := newFixture(t, true, map[string]string{
		"work/slice-y/CARD.md":                          "---\ntype: card\nstatus: live\nsummary: slice y\n---\n# Slice Y\n",
		"work/slice-y/STATE.md":                         "# State\n\nGate two is next.\n",
		"work/lane-a/CARD.md":                           "---\ntype: card\nstatus: live\nsummary: lane a\n---\n# Lane A\n",
		"work/driver/STATE.md":                          "# Driver state\n\nThe stack and its gates.\n",
		"work/driver/LEDGER.md":                         "# Failure ledger\n\nRows still open.\n",
		"work/driver/plans/old-plan.md":                 "# Old plan\n\nSuperseded thinking.\n",
		"work/driver/artifacts/run.json":                "{}",
		"sessions/devbox/2026/10/2026-10-05-a1-gate.md": session("a1", slice, read),
		"sessions/devbox/2026/10/2026-10-05-a2-gate.md": session("a2", slice, read),
		"sessions/devbox/2026/10/2026-10-06-a3-gate.md": session("a3", slice, []string{"work/driver/STATE.md", "work/driver/plans/old-plan.md", "work/driver/artifacts"}),
		"sessions/devbox/2026/10/2026-10-06-b1-lane.md": session("b1", lane, []string{"work/driver/plans/old-plan.md"}),
	})
	digest := digestPart(f.prompt(t, "s1", "Pick up work/slice-y/STATE.md and run gate two."))
	state, ledger := strings.Index(digest, "[[work/driver/STATE.md]] (3)"), strings.Index(digest, "[[work/driver/LEDGER.md]] (2)")
	if state < 0 || ledger < state || strings.Contains(digest, "old-plan") || strings.Contains(digest, "artifacts") ||
		strings.Contains(digest, "[[work/slice-y/STATE.md]]") {
		t.Fatalf("first prompt's digest must list the driver's STATE, then its ledger, and no file read once, folder or quoted file:\n%s", digest)
	}
	visible := func(p string) bool { return p != "sessions/devbox/2026/10/2026-10-05-a2-gate.md" }
	replay := DigestAsOf(f.env, f.db, f.cwd, "Pick up work/slice-y/STATE.md and run gate two.", visible).Text
	if !strings.Contains(replay, "[[work/driver/STATE.md]] (2)") || strings.Contains(replay, "LEDGER") {
		t.Fatalf("a replay must not count a session that had not ended by its start:\n%s", replay)
	}
}

// TestPointersSkipWhatTheDigestQuotes catches pointers spent on what L0 already holds (on 2026-10-08 the image-lane
// coordinator was pointed at its own stale CARD) and the first pass's hiding of the session's whole box (it lost the
// session's own AUDIT.md, the answer to the prompt): the box's CARD stays out, its report on the asked question
// stays in.
func TestPointersSkipWhatTheDigestQuotes(t *testing.T) {
	f := newFixture(t, true, map[string]string{
		"work/ocr-census/reports/recall.md": "---\ntype: report\nstatus: live\nsummary: OCR recall on the image census\naliases: [census recall report]\n---\n# OCR recall\n\nRecall 0.91.\n",
	})
	lk, err := Pointers(f.db, "Check work/ocr-census/STATE.md and work/ocr-census/CARD.md, then the census recall report.",
		Seeds{Work: []string{"ocr-census"}, Hubs: []string{"image-lane"}}, f.names(t))
	if err != nil {
		t.Fatal(err)
	}
	got := ids(lk)
	if slices.Contains(got, "work/ocr-census/CARD.md") || !slices.Contains(got, "work/ocr-census/reports/recall.md") {
		t.Fatalf("pointers %v: want the box's report and not its CARD", got)
	}
}

// TestRawWorkDocsOfOtherBoxesYieldToFacts catches another box's work docs that match by text alone taking slots
// from a fact, and the first pass's dropping of what the prompt points at: the lesson and the plan the prompt names
// are in, the run reports nobody names stay out, and with no fact or named doc the single best report may answer.
func TestRawWorkDocsOfOtherBoxesYieldToFacts(t *testing.T) {
	report := func(run string) string {
		return "---\ntype: report\nstatus: live\nsummary: Ledger sweep retry budget run " + run +
			"\nhubs: [image-lane]\n---\n# Ledger sweep retry budget run " + run + "\n\nThe retry budget held.\n"
	}
	f := newFixture(t, true, map[string]string{
		"work/ledger-sweep/CARD.md":          "---\ntype: card\nstatus: live\nsummary: ledger sweep\n---\n# Ledger sweep\n",
		"work/ledger-sweep/plans/PLAN.md":    "# Ledger sweep plan\n\nThree runs.\n",
		"work/ledger-sweep/reports/run-b.md": report("b"),
		"work/ledger-sweep/reports/run-c.md": report("c"),
		"notes/lesson-ledger-retry-budget.md": note("lesson-ledger-retry-budget", "lesson", "current",
			"Cap the ledger sweep retry budget at three attempts", []string{"image-lane"}, "retry budget"),
	})
	nm, seeds := f.names(t), Seeds{Work: []string{"ocr-census"}, Hubs: []string{"image-lane"}}
	lk, err := Pointers(f.db, "Compare work/ledger-sweep/plans/PLAN.md with what the ledger sweep retry budget runs showed.", seeds, nm)
	if err != nil {
		t.Fatal(err)
	}
	got := ids(lk)
	if !slices.Contains(got, "lesson-ledger-retry-budget") || !slices.Contains(got, "work/ledger-sweep/plans/PLAN.md") ||
		slices.ContainsFunc(got, func(id string) bool { return strings.Contains(id, "/reports/run-") }) {
		t.Fatalf("pointers %v: want the lesson and the named plan, and no unnamed run report", got)
	}
	seeds.Hide = func(id string) bool { return id == "lesson-ledger-retry-budget" }
	if lk, err = Pointers(f.db, "What did the ledger sweep retry budget runs show in the image lane?", seeds, nm); err != nil {
		t.Fatal(err)
	}
	if got := ids(lk); len(got) != 1 || !strings.Contains(got[0], "/reports/run-") {
		t.Fatalf("pointers %v: want only the single best report", got)
	}
}

// TestPausedBoxIsDemotedNotHidden catches a paused box hidden outright (the first pass lost pseudo-gen's DESIGN.md,
// the only answer to a prompt about it) and one kept where a live box answers as well (identity-resolution boxes the
// owner left on 2026-09-28 reached the laptop's digest on 2026-10-08): the paused box's sheet answers until a live
// box's sheet exists; the digest takes the cwd's paused box only when its session named no live one.
func TestPausedBoxIsDemotedNotHidden(t *testing.T) {
	sheet := func(run string) string {
		return "---\ntype: report\nstatus: live\nsummary: Census recall sheet of the " + run + "\n---\n# Census recall sheet, " + run + "\n\nRecall 0.91.\n"
	}
	f := newFixture(t, true, map[string]string{
		"work/ledger/CARD.md":              "---\ntype: card\nstatus: live\nsummary: ledger\n---\n# Ledger\n",
		"work/ledger/plans/PLAN.md":        "# Plan\n\nSee [the first sheet](../../ocr-census/reports/sheet.md) and [the rerun sheet](../../ocr-rerun/reports/sheet.md).\n",
		"work/ocr-census/CARD.md":          "---\ntype: card\nstatus: paused\nsummary: OCR census\nhubs: [image-lane]\n---\n# OCR census\n",
		"work/ocr-census/reports/sheet.md": sheet("first run"),
		"work/ocr-rerun/CARD.md":           "---\ntype: card\nstatus: live\nsummary: OCR rerun\n---\n# OCR rerun\n",
		"work/ocr-rerun/STATE.md":          "# State\n\nOCR rerun going.\n",
		"work/ocr-rerun/reports/sheet.md":  sheet("rerun"),
	})
	nm := f.names(t)
	const ask = "Follow work/ledger/plans/PLAN.md: where is the census recall sheet?"
	noRerun := func(id string) bool { return strings.HasPrefix(id, "work/ocr-rerun") }
	for _, tc := range []struct {
		hide       func(string) bool
		want, drop string
	}{
		{noRerun, "work/ocr-census/reports/sheet.md", ""},
		{nil, "work/ocr-rerun/reports/sheet.md", "work/ocr-census/reports/sheet.md"},
	} {
		lk, err := Pointers(f.db, ask, Seeds{Hide: tc.hide}, nm)
		if err != nil {
			t.Fatal(err)
		}
		if got := ids(lk); !slices.Contains(got, tc.want) || slices.Contains(got, tc.drop) {
			t.Errorf("pointers %v: want %s and not %q", got, tc.want, tc.drop)
		}
	}
	const chat = "What was the ocr recall on the image census?"
	f.start(t, Input{SessionID: "s1"})
	if got := f.prompt(t, "s1", chat); !strings.Contains(got, "Work ocr-census: STATE.md") {
		t.Errorf("the cwd's only box is paused: the digest must still carry it:\n%s", got)
	}
	put(t, f.env.Root, "sessions/devbox/2026/10/2026-10-08-bbbb2222-ocr.md", "---\nid: session-2026-10-08-bbbb2222\ntype: session\ncwd: "+
		f.cwd+"\nworks: [ocr-census, ocr-rerun]\n---\n## Owner prompts\n")
	f.start(t, Input{SessionID: "s2"})
	if got := f.prompt(t, "s2", chat); strings.Contains(got, "Work ocr-census") || !strings.Contains(got, "Work ocr-rerun: STATE.md") {
		t.Errorf("the cwd's session named a live box: the digest must carry it, not the paused one:\n%s", got)
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

// TestPostLogsEveryBrainPullWithItsChannel catches MCP use being invisible (2026-10-09): only a session's first pull
// was logged, so how much the brain's tools are used could not be measured, nor MCP told from the CLI.
func TestPostLogsEveryBrainPullWithItsChannel(t *testing.T) {
	f := newFixture(t, true, nil)
	f.prompt(t, "s1", "what was the ocr recall on the image census?")
	for _, c := range []struct{ tool, input string }{
		{"mcp__saahil-brain__brain_search", `{"query":"ocr recall"}`},
		{"mcp__saahil-brain__brain_search", `{"query":"census"}`},
		{"Bash", `{"command":"brain show finding-ocr-recall"}`},
	} {
		if err := Post(f.env, Input{SessionID: "s1", ToolName: c.tool, ToolInput: json.RawMessage(c.input)}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := f.db.Query(`SELECT candidates FROM lookups WHERE hook = 'pull' AND session = 's1' ORDER BY time, rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var c string
		_ = rows.Scan(&c)
		got = append(got, c)
	}
	want := []string{`["brain_search","mcp"]`, `["brain_search","mcp"]`, `["brain_show","cli"]`}
	if !slices.Equal(got, want) {
		t.Fatalf("pull rows %v, want %v", got, want)
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
