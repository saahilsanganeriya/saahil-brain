package delta

import (
	"context"
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
