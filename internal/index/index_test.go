package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestShowFolderListsItsAnchorDocs catches brain_show of a folder that hides what is in it (its part_of edges were
// filtered out, so the navigator's next hop from a folder went nowhere), and anchors out of order: the box's STATE
// must come before an ordinary doc.
func TestShowFolderListsItsAnchorDocs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BRAIN_HOME", home)
	root := filepath.Join(home, "brain")
	for rel, body := range map[string]string{
		"work/box/aaa-notes.md": "# Notes\n\nScratch.\n",
		"work/box/STATE.md":     "# State\n\nHalf done.\n",
		"work/box/runs/r1.md":   "# Run one\n",
	} {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ix, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	if _, err := ix.Update(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	n, err := ix.Show("work/box")
	if err != nil {
		t.Fatal(err)
	}
	var contents []string
	for _, e := range n.In {
		if e.Rel == "part_of" {
			contents = append(contents, e.Src)
		}
	}
	want := []string{"work/box/STATE.md", "work/box/aaa-notes.md", "work/box/runs"}
	if len(contents) < 3 || contents[0] != want[0] || contents[1] != want[1] || contents[len(contents)-1] != want[2] {
		t.Fatalf("contents %v, want %v (anchors first, then sub-folders)", contents, want)
	}
}
