package scan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
)

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func walkAll(t *testing.T, roots ...Root) map[string]Entry {
	t.Helper()
	got := map[string]Entry{}
	_, err := Walk(context.Background(), roots, func(e Entry) error {
		if _, dup := got[e.Path]; dup {
			t.Fatalf("visited %s twice", e.Path)
		}
		got[e.Path] = e
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestScanDoesNotFollowDirLinks catches a walker that follows directory symlinks: the saahil-docs/saahil-docs →
// saahil-docs loop found in worktrees would recurse forever or index every file twice.
func TestScanDoesNotFollowDirLinks(t *testing.T) {
	docs := t.TempDir()
	write(t, filepath.Join(docs, "box", "plans", "SPEC.md"), []byte("# Spec\n"))
	if err := os.Symlink(docs, filepath.Join(docs, "box", "saahil-docs")); err != nil {
		t.Fatal(err)
	}
	got := walkAll(t, Root{Abs: docs, Prefix: "work"})

	if _, ok := got["work/box/plans/SPEC.md"]; !ok {
		t.Fatalf("SPEC.md not visited: %v", got)
	}
	link, ok := got["work/box/saahil-docs"]
	if !ok || link.Kind != Link || link.Target != "work" {
		t.Fatalf("loop link = %+v, want a Link entry targeting work", link)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2 (the link was followed?): %v", len(got), got)
	}
}

// TestScanSkipsSyncConflictCopies catches a sync's losing copy ("<file> (conflict_on_<date>).md", an older version)
// being indexed as live content: it ranked beside the winner and the prompt hook showed it as a pointer.
func TestScanSkipsSyncConflictCopies(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "notes", "decision-x.md"), []byte("# X\n"))
	write(t, filepath.Join(root, "notes", "decision-x (conflict_on_2026-10-23).md"), []byte("# X, older\n"))

	got := walkAll(t, Root{Abs: root})
	if _, ok := got["notes/decision-x.md"]; !ok {
		t.Fatalf("the winner was not visited: %v", got)
	}
	if _, ok := got["notes/decision-x (conflict_on_2026-10-23).md"]; ok {
		t.Fatalf("a sync conflict copy was emitted: %v", got)
	}
}

// TestScanSurvivesOddFiles catches a crash, hang or unbounded read on the odd files a real corpus holds: a binary
// file named .md, invalid UTF-8, a 214 MB jsonl, an over-cap markdown, a FIFO named .md, and a .env that must never
// be opened.
func TestScanSurvivesOddFiles(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "binary.md"), []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"))
	write(t, filepath.Join(root, "latin1.md"), []byte("# Caf\xe9\n"))
	write(t, filepath.Join(root, "huge.md"), []byte("# Huge\n"+strings.Repeat("word ", MaxDocBytes/5+1000)))
	write(t, filepath.Join(root, ".env"), []byte("SECRET=x"))
	if err := os.Chmod(filepath.Join(root, ".env"), 0); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "pipe.md"), 0o644); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(root, "run", "rows.jsonl")
	write(t, big, []byte(`{"doc_id": 1, "label": "x"}`+"\n"))
	if err := os.Truncate(big, 214<<20); err != nil { // sparse: 214 MB on paper, no disk
		t.Fatal(err)
	}

	got := walkAll(t, Root{Abs: root})
	if _, ok := got[".env"]; ok {
		t.Fatal(".env was emitted")
	}
	if e, ok := got["pipe.md"]; ok && e.Kind != Skip {
		t.Fatalf("FIFO emitted as %v, want skipped", e.Kind)
	}
	want := map[string]string{"binary.md": "binary", "latin1.md": "invalid-utf8"}
	for path, reason := range want {
		c := Load(got[path])
		if c.Skip != reason {
			t.Errorf("%s: skip = %q, want %q", path, c.Skip, reason)
		}
	}
	huge := Load(got["huge.md"])
	if huge.Skip != "" || !huge.Truncated || len(huge.Data) > MaxDocBytes || huge.SHA == "" {
		t.Errorf("huge.md: skip=%q truncated=%v len=%d sha=%q", huge.Skip, huge.Truncated, len(huge.Data), huge.SHA)
	}
	rows := Load(got["run/rows.jsonl"])
	if rows.Skip != "" || len(rows.Data) > MaxShapeBytes || !strings.HasPrefix(string(rows.Data), `{"doc_id"`) {
		t.Errorf("rows.jsonl: skip=%q len=%d", rows.Skip, len(rows.Data))
	}
}

// TestConflictWalkFansOutWithTheSerialResult catches the devbox's sync.conflicts timing out at 2 min (2026-10-25):
// one serial walk of work/'s 2.24 M entries outran the check. The walk now reads folders on goroutines; at any width
// it must list the same copies in the same order as the serial walk, skip what the sync never carries, and count
// unison's fresh half-copies as in flight.
func TestConflictWalkFansOutWithTheSerialResult(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{
		"notes/a (conflict on 2026-10-23).md", "notes/a.md",
		"work/box-a/plans/b (conflict #1 on 2026-10-23).md", "work/box-a/plans/b.md",
		"work/box-b/STATE (conflict on 2026-10-22).md", "work/box-b/deep/x/y/z (conflict on 2026-10-22).md",
		"work/box-b/artifacts/run (conflict on 2026-10-22).md", ".archive/conflicts/old (conflict on 2026-10-01).md",
		"work/box-c/.unison.tmp-1",
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"notes/a (conflict on 2026-10-23).md", "work/box-a/plans/b (conflict #1 on 2026-10-23).md",
		"work/box-b/STATE (conflict on 2026-10-22).md", "work/box-b/deep/x/y/z (conflict on 2026-10-22).md"}
	defer func(prev func()) { conflictWalkForked = prev }(conflictWalkForked)
	for _, width := range []int{0, 1, 3, 64} {
		var forked atomic.Int64
		conflictWalkForked = func() { forked.Add(1) }
		found, inFlight := ConflictCopies(root, width)
		if strings.Join(found, "|") != strings.Join(want, "|") || inFlight != 1 {
			t.Fatalf("width %d: found %q, %d in flight; want %q, 1", width, found, inFlight, want)
		}
		if (width == 0) != (forked.Load() == 0) {
			t.Fatalf("width %d: %d folders read on a goroutine", width, forked.Load())
		}
	}
}
