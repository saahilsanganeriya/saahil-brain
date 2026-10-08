package view

import (
	"os"
	"path/filepath"
	"strings"
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
	for rel, want := range map[string]bool{"notes/n.md": true, "work/box/plans/SPEC.md": true, "work/box/STATE.md": true,
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

// TestNotesRenderedWithWikilinks catches a view whose graph shows ~1,700 orphans: the brain links notes by bare ids in
// the frontmatter, which a viewer does not draw, so the view's read-only copies carry them as [[wikilinks]].
func TestNotesRenderedWithWikilinks(t *testing.T) {
	root, dir := filepath.Join(t.TempDir(), "brain"), filepath.Join(t.TempDir(), "view")
	note := "---\nid: lesson-x\ntype: lesson\nstatus: current\nsummary: when x do y\nscope: global\nhubs: [git]\n" +
		"evidence: [work/box/STATE.md#plan]\nrelated: [{id: finding-y, why: same incident}]\nas_of: 2026-10-08\n---\nWhen x → y.\n"
	for rel, body := range map[string]string{"notes/lesson-x.md": note, "work/box/STATE.md": "# s\n"} {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(body), 0o644)
	}
	if _, err := Build(root, dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "notes", "lesson-x.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[[git]]", "[[finding-y]]", "[[work/box/STATE.md#plan]]", "- related: [[finding-y]] — same incident"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("rendered note lacks %q:\n%s", want, got)
		}
	}
	if fi, _ := os.Stat(filepath.Join(dir, "notes", "lesson-x.md")); fi.Mode().Perm() != 0o444 {
		t.Error("a rendered copy must be read-only, so an edit in the viewer cannot land anywhere")
	}
	_ = os.WriteFile(filepath.Join(dir, "notes", "mine.md"), []byte("mine"), 0o644)
	_ = os.Remove(filepath.Join(root, "notes", "lesson-x.md"))
	if _, err := Build(root, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes", "lesson-x.md")); !os.IsNotExist(err) {
		t.Error("the copy of a note that left the brain must go")
	}
	if _, err := os.Stat(filepath.Join(dir, "notes", "mine.md")); err != nil {
		t.Error("a file the owner made in the view must stay")
	}
}
