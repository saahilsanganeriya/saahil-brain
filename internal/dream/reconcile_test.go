package dream

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestReconcileSkipsWhatTheSyncNeverCarries catches the reconcile step timing out (2026-10-26 02Z: three 15-minute
// attempts, 45 min lost; 11–29 min in the other dreams): it walked every folder of the brain on one goroutine, bulk
// folders included, where the sync never carries a file and so no copy can be a sync conflict. It must count what the
// doctor counts: no bulk folder, no handled loser under .archive, and a fresh unison half-copy as in flight.
func TestReconcileSkipsWhatTheSyncNeverCarries(t *testing.T) {
	brain := t.TempDir()
	for _, p := range []string{
		"notes/a (conflict on 2026-10-23).md", "notes/a.md",
		"work/box-a/STATE (conflict on 2026-10-22).md",
		"work/box-a/artifacts/run-1/out (conflict on 2026-10-22).md",
		"work/box-a/tools/node_modules/pkg/x (conflict on 2026-10-22).md",
		".archive/conflicts/devbox/old (conflict on 2026-10-01).md",
		"work/box-b/.unison.GOAL.md.0123.unison.tmp",
	} {
		write(t, filepath.Join(brain, p), "")
	}
	acts := &Activities{Cfg: Config{BrainRoot: brain}}
	out, err := acts.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := "2 conflict copies left for the owner; 1 unison half-copy in flight"
	if out.Status != "ok" || out.Reason != want {
		t.Fatalf("reconcile: %+v; want ok %q", out, want)
	}

	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(filepath.Join(brain, "work/box-b/.unison.GOAL.md.0123.unison.tmp"), old, old); err != nil {
		t.Fatal(err)
	}
	if out, _ = acts.Reconcile(context.Background()); out.Reason != "3 conflict copies left for the owner" {
		t.Fatalf("an abandoned half-copy is the owner's too: %+v", out)
	}
}
