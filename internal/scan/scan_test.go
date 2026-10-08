package scan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
