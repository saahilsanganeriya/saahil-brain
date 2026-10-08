package view

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBuild catches a view that lets bulk (artifacts, dot-folders) into the viewer, keeps links to files that left
// the brain, or overwrites a real file someone put in the view.
func TestBuild(t *testing.T) {
	root, dir := filepath.Join(t.TempDir(), "brain"), filepath.Join(t.TempDir(), "view")
	for _, rel := range []string{"notes/n.md", "work/box/plans/SPEC.md", "work/box/STATE.md", "work/box/artifacts/out.md",
		"work/box/.cache/x.md", "work/box/run.json"} {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("x"), 0o644)
	}
	if _, err := Build(root, dir); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]bool{"notes": true, "work/box/plans/SPEC.md": true, "work/box/STATE.md": true,
		"work/box/artifacts/out.md": false, "work/box/.cache/x.md": false, "work/box/run.json": false} {
		if _, err := os.Lstat(filepath.Join(dir, rel)); (err == nil) != want {
			t.Errorf("%s in view = %v, want %v", rel, err == nil, want)
		}
	}

	_ = os.Remove(filepath.Join(root, "work/box/plans/SPEC.md"))
	st, err := Build(root, dir)
	if err != nil || st.Removed != 1 {
		t.Fatalf("refresh: %+v %v", st, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "work/box/plans")); !os.IsNotExist(err) {
		t.Error("a folder the stale link left empty must go")
	}

	_ = os.WriteFile(filepath.Join(dir, "work/box/STATE.md.real"), []byte("mine"), 0o644)
	_ = os.Remove(filepath.Join(dir, "work/box/STATE.md"))
	_ = os.WriteFile(filepath.Join(dir, "work/box/STATE.md"), []byte("mine"), 0o644)
	if _, err := Build(root, dir); err == nil {
		t.Fatal("a real file in the view must stop the build, never be overwritten")
	}
	if _, err := Build(root, filepath.Join(root, "view")); err == nil {
		t.Fatal("a view inside the brain must be refused")
	}
}
