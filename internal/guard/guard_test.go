package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func brain(t *testing.T) string {
	root := t.TempDir()
	for rel, body := range map[string]string{
		"hubs/index.md":    "---\nid: index\ntype: hub\nstatus: current\nsummary: home\n---\n",
		"memory/MEMORY.md": strings.Repeat("- line\n", 190),
	} {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// Catches the anti-ledger rule leaking: a STATE.md written past 8 KB must be refused before it lands, whether it is
// written whole or grown by an edit.
func TestBlocksOversizeState(t *testing.T) {
	root := brain(t)
	state := filepath.Join(root, "work", "box", "STATE.md")
	big := strings.Repeat("x", 9*1024)
	if d := Decide(root, Request{Tool: "Write", Path: state, Content: []byte(big), HasContent: true}); d.Decision != Block {
		t.Fatalf("Write 9 KB STATE.md: %+v", d)
	}
	os.MkdirAll(filepath.Dir(state), 0o755)
	os.WriteFile(state, []byte("# State\n\nsmall\n"), 0o644)
	d := Decide(root, Request{Tool: "Edit", Path: state, Edits: []Edit{{Old: "small", New: big}}})
	if d.Decision != Block || !strings.Contains(d.Reason, "STATE.md") {
		t.Fatalf("Edit growing STATE.md to 9 KB: %+v", d)
	}
}

// Catches ledger-style growth of a baseline other than STATE.md (MEMORY.md appended past 200 lines), while an
// edit that shrinks an oversized baseline is let through so it can be fixed.
func TestBlocksBaselineGrowth(t *testing.T) {
	root := brain(t)
	mem := filepath.Join(root, "memory", "MEMORY.md")
	grown := strings.Repeat("- line\n", 230)
	if d := Decide(root, Request{Tool: "Write", Path: mem, Content: []byte(grown), HasContent: true}); d.Decision != Block {
		t.Fatalf("MEMORY.md to 230 lines: %+v", d)
	}
	os.WriteFile(mem, []byte(strings.Repeat("- line\n", 260)), 0o644)
	if d := Decide(root, Request{Tool: "Write", Path: mem, Content: []byte(grown), HasContent: true}); d.Decision == Block {
		t.Fatalf("shrinking an oversized MEMORY.md was blocked: %+v", d)
	}
}

// Catches a big doc read whole: a Read of a > 50 KB doc under the brain must say how many sections it has and how
// to read one.
func TestReadOfBigDocAppendsSections(t *testing.T) {
	root := brain(t)
	doc := filepath.Join(root, "work", "box", "plans", "PLAN.md")
	os.MkdirAll(filepath.Dir(doc), 0o755)
	var b strings.Builder
	for i := range 6 {
		b.WriteString("## Part " + string(rune('A'+i)) + "\n\n" + strings.Repeat("words ", 2000) + "\n")
	}
	os.WriteFile(doc, []byte(b.String()), 0o644)
	d := Decide(root, Request{Tool: "Read", Path: doc})
	if d.Decision != Allow || d.Append != "6 sections — use brain_sections work/box/plans/PLAN.md to read one" {
		t.Fatalf("%+v", d)
	}
}

// Catches the guard reaching outside the brain: writes anywhere else are none of its business.
func TestOutsideBrainIsAllowed(t *testing.T) {
	root := brain(t)
	other := filepath.Join(t.TempDir(), "STATE.md")
	if d := Decide(root, Request{Tool: "Write", Path: other, Content: []byte(strings.Repeat("x", 9*1024)), HasContent: true}); d.Decision != Allow {
		t.Fatalf("%+v", d)
	}
}
