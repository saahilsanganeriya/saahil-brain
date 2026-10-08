package delta

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func note(id, hub string, related ...Related) *Note {
	return &Note{ID: id, Type: "finding", Status: "current", Summary: "s " + id, Scope: "hub", Hubs: []string{hub},
		Evidence: []string{"src.md"}, Related: related, Body: "fact " + id}
}

// Catches the filing defect seen on the first real batch (15 of 21 notes rejected): notes in one batch that cite each
// other, or cite a note from a later batch, must be filed; the missing link waits and is added when its target lands.
func TestBatchCrossReferencesSurvive(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"notes", "hubs"} {
		_ = os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	_ = os.WriteFile(filepath.Join(root, "src.md"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "hubs", "git.md"), Render(Note{ID: "git", Type: "hub", Status: "current",
		Summary: "git", Broader: []string{"index"}}), 0o644)
	now := time.Date(2026, 10, 8, 8, 0, 0, 0, time.UTC)
	first := Delta{Ops: []Op{
		{Op: "add_note", Note: note("finding-a", "git", Related{ID: "finding-b", Why: "same bug"})},
		{Op: "add_note", Note: note("finding-b", "git", Related{ID: "finding-a", Why: "same bug"}, Related{ID: "finding-later", Why: "next batch"})},
	}}
	rep, err := Apply(context.Background(), root, first, Options{Now: now})
	if err != nil || len(rep.Rejected) != 0 || len(rep.Files) != 2 {
		t.Fatalf("batch: files=%d rejected=%+v err=%v", len(rep.Files), rep.Rejected, err)
	}
	if len(rep.DroppedRefs) != 1 || rep.DroppedRefs[0].Ref != "finding-later" {
		t.Fatalf("dropped = %+v", rep.DroppedRefs)
	}
	if _, err := Apply(context.Background(), root, Delta{Ops: []Op{{Op: "add_note", Note: note("finding-later", "git")}}}, Options{Now: now}); err != nil {
		t.Fatal(err)
	}
	added, waiting, err := ResolvePending(root, now)
	if err != nil || added != 1 || waiting != 0 {
		t.Fatalf("resolve: added=%d waiting=%d err=%v", added, waiting, err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "notes", "finding-b.md"))
	n, err := Parse(b)
	if err != nil || len(n.Related) != 2 {
		t.Fatalf("finding-b related = %+v err=%v", n.Related, err)
	}
}
