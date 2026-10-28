package delta

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const seedHub = "---\nid: git\ntype: hub\nstatus: current\nsummary: git practice\nbroader: [index]\n---\nscope\n"

func newRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range map[string]string{
		"hubs/index.md": "---\nid: index\ntype: hub\nstatus: current\nsummary: home\n---\nhome\n",
		"hubs/git.md":   seedHub,
		"notes/lesson-old-rebase.md": "---\nid: lesson-old-rebase\ntype: lesson\nstatus: current\nsummary: rebase " +
			"before push\nscope: global\nhubs: [git]\nevidence: [.import/laptop/x/rebase.md]\n---\nwhen behind → rebase.\n",
		".import/laptop/x/rebase.md": "synthetic memory\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func parse(t *testing.T, raw string) Delta {
	t.Helper()
	var d Delta
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, dir := range []string{"notes", "hubs"} {
		entries, _ := os.ReadDir(filepath.Join(root, dir))
		for _, e := range entries {
			b, err := os.ReadFile(filepath.Join(root, dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			out[dir+"/"+e.Name()] = string(b)
		}
	}
	return out
}

const mixedDelta = `{"ops":[
 {"op":"add_note","note":{"id":"lesson-new-stack","type":"lesson","status":"current","summary":"stack PRs with gh stack",
  "scope":"global","hubs":["git"],"evidence":[".import/laptop/x/rebase.md"],"sources":[".import/laptop/x/rebase.md"],
  "aliases":["gh stack"],"as_of":"2026-10-01","body":"when PRs depend on each other → gh stack link, because bases cascade."}},
 {"op":"update_note","id":"lesson-old-rebase","set":{"aliases":["rebase first"]},"append_body":"Seen again 2026-10-02."},
 {"op":"link","src":"lesson-new-stack","rel":"related","dst":"lesson-old-rebase","why":"both about branch order"},
 {"op":"counter","id":"lesson-old-rebase","field":"helpful","delta":1}
]}`

// TestApplyDeltaIsIdempotent catches a delta applier that appends a body twice, re-adds a link, bumps a counter again
// or rewrites a file's timestamp when the same delta is replayed (a dream retry or a rerun of a filing batch).
func TestApplyDeltaIsIdempotent(t *testing.T) {
	root := newRoot(t)
	stats, err := OpenStats(filepath.Join(root, ".index", "brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer stats.Close()
	d := parse(t, mixedDelta)
	opts := Options{Stats: stats, Now: time.Date(2026, 10, 8, 7, 0, 0, 0, time.UTC)}

	first, err := Apply(context.Background(), root, d, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Rejected) != 0 || first.Counters != 1 || len(first.Files) != 2 {
		t.Fatalf("first apply: %+v", first)
	}
	after := snapshot(t, root)
	if !strings.Contains(after["notes/lesson-new-stack.md"], "{id: lesson-old-rebase, why: both about branch order}") {
		t.Fatalf("link not written in flow style:\n%s", after["notes/lesson-new-stack.md"])
	}

	opts.Now = opts.Now.Add(time.Hour)
	second, err := Apply(context.Background(), root, d, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Files) != 0 || second.Counters != 0 || len(second.Rejected) != 0 {
		t.Fatalf("second apply changed something: %+v", second)
	}
	for path, content := range snapshot(t, root) {
		if content != after[path] {
			t.Errorf("%s changed on replay:\n%s\n---\n%s", path, after[path], content)
		}
	}
	if n, _ := stats.Get("lesson-old-rebase", "helpful"); n != 1 {
		t.Errorf("helpful = %d after two applies, want 1", n)
	}
}

// TestDeltaNeverDeletes catches a delta that removes knowledge: a delete op, a supersede that drops the old note, or
// an update whose list "set" silently shrinks a list — all must leave every file and fact in place.
func TestDeltaNeverDeletes(t *testing.T) {
	root := newRoot(t)
	d := parse(t, `{"ops":[
 {"op":"delete","id":"lesson-old-rebase","why":"stale"},
 {"op":"add_note","note":{"id":"lesson-rebase-v2","type":"lesson","status":"current","summary":"rebase onto origin/main",
  "scope":"global","hubs":["git"],"evidence":[".import/laptop/x/rebase.md"],"body":"when behind → rebase onto origin/main."}},
 {"op":"supersede","old":"lesson-old-rebase","new":"lesson-rebase-v2"},
 {"op":"update_note","id":"lesson-old-rebase","set":{"hubs":[]}}
]}`)
	rep, err := Apply(context.Background(), root, d, Options{Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if rep.DeletesRejected != 1 {
		t.Errorf("deletes rejected = %d, want 1", rep.DeletesRejected)
	}
	old, err := os.ReadFile(filepath.Join(root, "notes/lesson-old-rebase.md"))
	if err != nil {
		t.Fatal("superseded note is gone:", err)
	}
	for _, want := range []string{"status: superseded", "hubs: [git]", "when behind → rebase."} {
		if !strings.Contains(string(old), want) {
			t.Errorf("old note lost %q:\n%s", want, old)
		}
	}
	nw, _ := os.ReadFile(filepath.Join(root, "notes/lesson-rebase-v2.md"))
	if !strings.Contains(string(nw), "supersedes: [lesson-old-rebase]") {
		t.Errorf("new note does not record what it supersedes:\n%s", nw)
	}
}

// TestOneOpsFilesShareAGroup catches files of one supersede op reported as unrelated: the dream's edit cap then landed
// the new note (supersedes: [old]) and held the old one, which stayed current.
func TestOneOpsFilesShareAGroup(t *testing.T) {
	root := newRoot(t)
	d := parse(t, `{"ops":[
 {"op":"add_note","note":{"id":"lesson-rebase-v2","type":"lesson","status":"current","summary":"rebase onto origin/main",
  "scope":"global","hubs":["git"],"evidence":[".import/laptop/x/rebase.md"],"body":"when behind → rebase onto origin/main."}},
 {"op":"supersede","old":"lesson-old-rebase","new":"lesson-rebase-v2"},
 {"op":"add_note","note":{"id":"idea-alone","type":"idea","status":"current","summary":"fine","scope":"hub","hubs":["git"],
  "body":"a valid idea"}}
]}`)
	rep, err := Apply(context.Background(), root, d, Options{DryRun: true, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	groups := map[string]string{}
	for _, f := range rep.Files {
		groups[f.Path] = f.Group
	}
	want := map[string]string{"notes/idea-alone.md": "", "notes/lesson-old-rebase.md": "notes/lesson-old-rebase.md",
		"notes/lesson-rebase-v2.md": "notes/lesson-old-rebase.md"}
	if len(groups) != len(want) {
		t.Fatalf("files %v, want %v", groups, want)
	}
	for path, g := range want {
		if groups[path] != g {
			t.Errorf("%s in group %q, want %q (all: %v)", path, groups[path], g, groups)
		}
	}
}

// TestApplyRejectsInvalidOpsOnly catches an applier that writes a note breaking FORMAT.md (over the 4 KB cap, no hub,
// a lesson over 300 chars, a dangling link) or that throws away the valid ops of the same delta with it.
func TestApplyRejectsInvalidOpsOnly(t *testing.T) {
	root := newRoot(t)
	big := strings.Repeat("fact. ", 800)
	d := parse(t, `{"ops":[
 {"op":"add_note","note":{"id":"finding-too-big","type":"finding","status":"current","summary":"s","scope":"hub",
  "hubs":["git"],"evidence":[".import/laptop/x/rebase.md"],"body":"`+big+`"}},
 {"op":"add_note","note":{"id":"finding-no-hub","type":"finding","status":"current","summary":"s","scope":"hub",
  "evidence":[".import/laptop/x/rebase.md"],"body":"b"}},
 {"op":"link","src":"lesson-old-rebase","rel":"about","dst":"system-does-not-exist"},
 {"op":"add_note","note":{"id":"idea-ok","type":"idea","status":"current","summary":"fine","scope":"hub","hubs":["git"],
  "body":"a valid idea"}}
]}`)
	rep, err := Apply(context.Background(), root, d, Options{Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rejected) != 3 || len(rep.Files) != 1 || rep.Files[0].Path != "notes/idea-ok.md" {
		t.Fatalf("want 3 rejected and only idea-ok written: %+v", rep)
	}
	for _, id := range []string{"finding-too-big", "finding-no-hub"} {
		if _, err := os.Stat(filepath.Join(root, "notes", id+".md")); err == nil {
			t.Errorf("%s was written", id)
		}
	}
}

// TestDeltaNeverWritesUnderWork catches a delta that reaches outside notes/ and hubs/ — above all into work/, which
// must stay empty in the shadow phase so the cut-over can rename saahil-docs into it — while work/… evidence still
// resolves through the read-only shadow root.
func TestDeltaNeverWritesUnderWork(t *testing.T) {
	root := newRoot(t)
	shadow := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(shadow, "box"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shadow, "box", "CARD.md"), []byte("card\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := parse(t, `{"ops":[
 {"op":"add_note","note":{"id":"finding-x/../../work/box/CARD","type":"finding","status":"current","summary":"s","scope":"work",
  "hubs":["git"],"evidence":["work/box/CARD.md"],"body":"b"}},
 {"op":"add_note","note":{"id":"card-box","type":"card","status":"current","summary":"s","scope":"work","hubs":["git"],"body":"b"}},
 {"op":"add_note","note":{"id":"finding-box-state","type":"finding","status":"current","summary":"s","scope":"work",
  "hubs":["git"],"evidence":["work/box/CARD.md#status"],"body":"b"}}
]}`)
	rep, err := Apply(context.Background(), root, d, Options{Now: time.Now(), WorkRoot: shadow})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rejected) != 2 || len(rep.Files) != 1 || rep.Files[0].Path != "notes/finding-box-state.md" {
		t.Fatalf("want the two escapes rejected and the work/ evidence note written: %+v", rep)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "work")); len(entries) != 0 {
		t.Fatalf("work/ is not empty: %v", entries)
	}
}

// TestHelpfulCounterStampsItsTime catches helpful counters that never reach the L2 ranker's recency term (it reads
// last_helpful_at): a helpful increment stamps the time; a harmful one on a new row leaves it empty.
func TestHelpfulCounterStampsItsTime(t *testing.T) {
	stats, err := OpenStats(filepath.Join(t.TempDir(), "brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer stats.Close()
	if _, err := stats.AddCounters("k1", []Counter{{ID: "lesson-a", Field: "helpful", Delta: 1}, {ID: "lesson-b", Field: "harmful", Delta: 1}}); err != nil {
		t.Fatal(err)
	}
	at := map[string]string{}
	for _, id := range []string{"lesson-a", "lesson-b"} {
		var s sql.NullString
		stats.db.QueryRow(`SELECT last_helpful_at FROM node_stats WHERE id = ?`, id).Scan(&s)
		at[id] = s.String
	}
	if _, err := time.Parse(time.RFC3339, at["lesson-a"]); err != nil || at["lesson-b"] != "" {
		t.Fatalf("last_helpful_at: helpful %q, harmful %q", at["lesson-a"], at["lesson-b"])
	}
}

// Catches a note that cites a session note by an old slug staying frozen: the session note writer used to move a note
// on /rename, and every later edit of a citing note (a merge into it included) was refused for the dangling path.
func TestRenamedSessionCitationIsRepointed(t *testing.T) {
	root := newRoot(t)
	const dir = "sessions/devbox/2026/10/"
	files := map[string]string{
		dir + "2026-10-08-11111111-new-name.md": "---\nid: session-2026-10-08-11111111\ntype: session\n---\n",
		"notes/lesson-cites-session.md": "---\nid: lesson-cites-session\ntype: lesson\nstatus: current\nsummary: cites a " +
			"session\nscope: global\nhubs: [git]\nevidence: ['" + dir + "2026-10-08-11111111-old-name.md#Owner prompts']\n" +
			"sources: [" + dir + "2026-10-08-11111111-old-name.md, " + dir + "2026-10-08-11111111-new-name.md]\n---\nbody.\n",
	}
	for path, content := range files {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755)
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := Apply(context.Background(), root, parse(t, `{"ops":[{"op":"update_note","id":"lesson-cites-session"},
		{"op":"update_note","id":"lesson-cites-session","set":{"aliases":["session cite"]}}]}`),
		Options{Now: time.Now()})
	if err != nil || len(rep.Rejected) != 0 {
		t.Fatalf("rejected=%+v err=%v", rep.Rejected, err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "notes", "lesson-cites-session.md"))
	got, _ := Parse(b)
	want := dir + "2026-10-08-11111111-new-name.md"
	if len(got.Evidence) != 1 || got.Evidence[0] != want+"#Owner prompts" || len(got.Sources) != 1 || got.Sources[0] != want {
		t.Fatalf("evidence %v, sources %v; want both on %s", got.Evidence, got.Sources, want)
	}
}
