package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Catches a command that parses no flags running (and writing) when asked for --help.
func TestHelpNeverRunsACommand(t *testing.T) {
	ran := false
	commands["zz-test-writer"] = command{path: "zz-test-writer", summary: "test", run: func([]string) int { ran = true; return 0 }}
	defer delete(commands, "zz-test-writer")
	for _, h := range []string{"-h", "--help"} {
		if code := dispatch([]string{"zz-test-writer", h}); code != 0 || ran {
			t.Fatalf("%s: code %d ran=%v", h, code, ran)
		}
	}
}

// Catches the dream reverting every edit of a run (2026-10-08 09:20Z): its check passed brain-relative paths
// (notes/x.md) to lint from another working directory, and lint failed them as missing files (exit 2).
func TestLintResolvesBrainRelativePaths(t *testing.T) {
	root := t.TempDir()
	t.Setenv("BRAIN_ROOT", root)
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	note := "---\nid: idea-x\ntype: idea\nstatus: current\nsummary: s\nscope: global\nhubs: [git]\n---\nbody\n"
	_ = os.WriteFile(filepath.Join(root, "notes", "idea-x.md"), []byte(note), 0o644)
	_ = os.MkdirAll(filepath.Join(root, "hubs"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "hubs", "git.md"), []byte("---\nid: git\ntype: hub\nstatus: current\nsummary: git\nbroader: [index]\n---\n"), 0o644)
	t.Chdir(t.TempDir())
	if code := cmdLint([]string{"notes/idea-x.md"}); code == 2 {
		t.Fatal("lint could not find a brain-relative path from another directory")
	}
}

// Catches sync conflict copies left in the live tree (doctor's "0 unhandled conflicts" fails, agents read stale
// copies): the newer file stays, the loser moves under .archive/conflicts/<machine>/ with the same relative path.
func TestArchiveConflictsMovesLosersOnly(t *testing.T) {
	root := t.TempDir()
	t.Setenv("BRAIN_MACHINE", "devbox")
	box := filepath.Join(root, "work", "box")
	_ = os.MkdirAll(box, 0o755)
	_ = os.WriteFile(filepath.Join(box, "a.md"), []byte("newer"), 0o644)
	loser := "a.md (conflict_on_2026-10-08)"
	_ = os.WriteFile(filepath.Join(box, loser), []byte("older"), 0o644)
	n, err := archiveConflicts(root, time.Now())
	if err != nil || n != 1 {
		t.Fatalf("archived %d, %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(box, "a.md")); err != nil {
		t.Fatal("the live file moved")
	}
	if b, err := os.ReadFile(filepath.Join(root, ".archive", "conflicts", "devbox", "work", "box", loser)); err != nil || string(b) != "older" {
		t.Fatalf("loser not preserved: %v", err)
	}
}
