package install

import "testing"

// Catches the unanchored `brain` entry (it hid cmd/brain in every repo) surviving a re-install, and a second /brain.
func TestGlobalGitIgnoreAnchorsBrain(t *testing.T) {
	text := "out/\nsaahil-docs\nbrain\nbrain/\n.claude/\n"
	for range 2 {
		text, _ = mergeIgnore(text, []string{"brain", "brain/"}, []string{"/brain"})
	}
	if want := "out/\nsaahil-docs\n.claude/\n/brain\n"; text != want {
		t.Fatalf("ignore file =\n%q\nwant\n%q", text, want)
	}
}
