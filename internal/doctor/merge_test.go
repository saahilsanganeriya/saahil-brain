package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"saahil.local/brain/internal/delta"
)

// Catches the owner-questions line counting pairs the owner already settled: an answered question, or one whose note
// was superseded since, no longer waits; an open pair does.
func TestOwnerQuestionsCountsOnlyOpenOnes(t *testing.T) {
	root := t.TempDir()
	for id, status := range map[string]string{"decision-p": "current", "decision-q": "current", "decision-r": "superseded"} {
		p := filepath.Join(root, "notes", id+".md")
		os.MkdirAll(filepath.Dir(p), 0o755)
		n := delta.Note{ID: id, Type: "decision", Status: status, Summary: id, Scope: "global", Hubs: []string{"brain"}}
		if err := os.WriteFile(p, delta.Render(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	queue := `{"ids":["decision-p","decision-q"],"as_of":["2026-10-03","2026-10-03"],"question":"which holds?"}
{"ids":["decision-p","decision-r"],"as_of":["2026-10-03","2026-09-01"],"question":"which holds?"}
{"ids":["decision-q","decision-p"],"as_of":["2026-10-03","2026-10-03"],"question":"again?","answered":"2026-10-09"}
`
	os.MkdirAll(filepath.Join(root, ".brain"), 0o755)
	if err := os.WriteFile(filepath.Join(root, ".brain", "owner-questions.jsonl"), []byte(queue), 0o644); err != nil {
		t.Fatal(err)
	}
	if st, detail := ownerQuestions(root); st != OK || !strings.HasPrefix(detail, "1 wait") {
		t.Fatalf("got %s %q, want ok with 1 waiting", st, detail)
	}
}
