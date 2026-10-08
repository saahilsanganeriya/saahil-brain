package filing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// Catches the global MEMORY.md losing the owner's standing rules (Pacific time, /goal full text, …): hundreds of
// anti-patterns filled the 25 KB before any lesson. Notes filed from memories the owner's own index listed first.
func TestRenderPrioritizesOwnerIndex(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"notes", "hubs", "memory", ".brain", ".import/laptop/p"} {
		_ = os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	note := func(id, typ, asOf string) {
		_ = os.WriteFile(filepath.Join(root, "notes", id+".md"), []byte("---\nid: "+id+"\ntype: "+typ+
			"\nstatus: current\nsummary: fact "+id+"\nscope: global\nhubs: [git]\nas_of: "+asOf+"\n---\nbody\n"), 0o644)
	}
	note("anti-pattern-newer", "anti-pattern", "2026-10-07")
	note("lesson-owner-rule", "lesson", "2026-09-01")
	_ = os.WriteFile(filepath.Join(root, ".import/laptop/p/MEMORY.md"), []byte("# Memory index\n\nStanding rules — read first:\n- [Times in Pacific](times-in-pacific.md) — owner\n"), 0o644)
	st := state{Filed: map[string]filed{"s1": {Path: ".import/laptop/p/times-in-pacific.md", Notes: []string{"lesson-owner-rule"}, At: time.Now()}}, Attempts: map[string]int{}}
	b, _ := json.Marshal(st)
	_ = os.WriteFile(statePath(root), b, 0o644)
	if err := RenderMemoryIndex(root); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "memory", "MEMORY.md"))
	text := string(raw)
	if i, j := strings.Index(text, "lesson-owner-rule"), strings.Index(text, "anti-pattern-newer"); i < 0 || j < 0 || i > j {
		t.Fatalf("owner-index note not first:\n%s", text)
	}
}
