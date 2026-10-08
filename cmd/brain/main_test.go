package main

import (
	"os"
	"path/filepath"
	"testing"
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
