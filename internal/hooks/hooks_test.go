package hooks

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// Catches a start digest over the 8,000-char cap when hot.md, STATE.md, CARD.md and the start-here list are all
// large, and the current
// work being missed when the folder name says nothing (it must come from the latest session note for this cwd).
func TestDigestUnderCap(t *testing.T) {
	long := strings.Repeat("- finding-ocr-recall "+strings.Repeat("long start-here line ", 45)+"\n", 7)
	f := newFixture(t, true, map[string]string{
		"hot.md":                   strings.Repeat("hot words here ", 700),
		"work/ocr-census/STATE.md": "# State\n\n" + strings.Repeat("state line of the census run\n", 900),
		"hubs/image-lane.md":       "---\nid: image-lane\ntype: hub\nstatus: current\nsummary: image lane\nbroader: [index]\n---\n## Start here\n\n" + long,
	})
	out, err := Start(f.env, Input{SessionID: "s1", Cwd: f.cwd, Event: "SessionStart"})
	if err != nil {
		t.Fatal(err)
	}
	got := contextOf(t, out)
	if n := utf8.RuneCountInString(got); n == 0 || n > 8000 {
		t.Fatalf("digest is %d chars", n)
	}
	for _, want := range []string{"This week (hot.md)", "Work ocr-census: STATE.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("digest lacks %q", want)
		}
	}
	// with a small hot.md everything fits, the hub's start-here list included and nothing past it
	f = newFixture(t, true, nil)
	out, _ = Start(f.env, Input{SessionID: "s2", Cwd: f.cwd})
	got = contextOf(t, out)
	if !strings.Contains(got, "Work ocr-census: CARD.md") || !strings.Contains(got, "- decision-paddle-first") || strings.Contains(got, "not this") {
		t.Fatalf("digest:\n%s", got)
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
	err := f.db.QueryRow(`SELECT count(*), group_concat(time || session || hook || candidates || chosen, '|') FROM lookups`).Scan(&rows, &dump)
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
	if lookups != 2 || stats != 0 {
		t.Fatalf("lookups %d (want 2), node_stats %d (want 0)", lookups, stats)
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
