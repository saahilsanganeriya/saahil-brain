package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSeparateFrom: after the cut-over the old docs path links into <brain>/work; treating it as a read-only root
// refused every CARD the dream wrote into work/, so only a tree outside the brain stays separate.
func TestSeparateFrom(t *testing.T) {
	dir := t.TempDir()
	brain, docs := filepath.Join(dir, "brain"), filepath.Join(dir, "docs")
	for _, d := range []string{filepath.Join(brain, "work"), docs} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "saahil-docs")
	if err := os.Symlink(filepath.Join(brain, "work"), link); err != nil {
		t.Fatal(err)
	}
	if !separateFrom(docs, brain) || separateFrom(link, brain) || separateFrom(filepath.Join(dir, "gone"), brain) {
		t.Fatal("want: own tree separate; a link into the brain or a missing path not separate")
	}
}
