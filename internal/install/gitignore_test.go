package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Catches the unanchored `brain` entry (it hid cmd/brain in every repo) surviving a re-install, and a second /brain.
func TestGlobalGitIgnoreAnchorsBrain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ignore")
	_ = os.WriteFile(path, []byte("out/\nsaahil-docs\nbrain\nbrain/\n.claude/\n"), 0o644)
	for range 2 {
		if _, err := AddGlobalGitIgnore(path, []string{"brain", "brain/"}, "/brain"); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(path)
	if got, want := string(raw), "out/\nsaahil-docs\n.claude/\n/brain\n"; got != want {
		t.Fatalf("ignore file =\n%q\nwant\n%q", got, want)
	}
	if strings.Count(string(raw), "/brain") != 1 {
		t.Fatal("duplicate /brain")
	}
}
