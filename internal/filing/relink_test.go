package filing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Catches filed notes keeping [[old-memory-name]] links (114 dangling links on the real brain): a name whose memory
// was filed points at its note, an existing id is kept, anything else loses only its brackets.
func TestRelinkRewritesMemoryNamesToNotes(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"notes", "hubs", ".brain"} {
		_ = os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	st := state{Filed: map[string]filed{"sha1": {Path: ".import/devbox/p/depot-cli-show-and-logs.md",
		Notes: []string{"playbook-depot-logs"}, At: time.Now()}}, Attempts: map[string]int{}}
	b, _ := json.Marshal(st)
	_ = os.WriteFile(statePath(root), b, 0o644)
	_ = os.WriteFile(filepath.Join(root, "notes", "playbook-depot-logs.md"), []byte("---\nid: playbook-depot-logs\n---\nx\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "hubs", "git.md"), []byte("---\nid: git\n---\n"), 0o644)
	src := "---\nid: finding-a\nsummary: see [[depot-cli-show-and-logs]]\n---\nSee [[depot-cli-show-and-logs]], [[git]], [[gone-memory|the old one]] and [[gone-too]].\n"
	_ = os.WriteFile(filepath.Join(root, "notes", "finding-a.md"), []byte(src), 0o644)
	rep, err := Relink(root)
	if err != nil || rep.Rewritten != 1 || rep.Unwrapped != 2 || rep.Files != 1 {
		t.Fatalf("report %+v err %v", rep, err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "notes", "finding-a.md"))
	want := "---\nid: finding-a\nsummary: see [[depot-cli-show-and-logs]]\n---\nSee [[playbook-depot-logs]], [[git]], the old one and gone-too.\n"
	if string(got) != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if again, _ := Relink(root); again.Files != 0 {
		t.Fatalf("second run changed %d files", again.Files)
	}
}
