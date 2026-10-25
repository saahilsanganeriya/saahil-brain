package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/rpc"
)

// TestStaleSyncLineSaysWhatTheAgentIsDoing catches the laptop's sync.last failing for three days (2026-10-12 to
// 10-15) with only the record's age: the line must carry what launchd holds for the hourly agent once the record is
// stale, and stay short while it is fresh.
func TestStaleSyncLineSaysWhatTheAgentIsDoing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BRAIN_HOME", home)
	t.Setenv("BRAIN_MACHINE", "laptop")
	saved := rpc.SyncAgentState
	defer func() { rpc.SyncAgentState = saved }()
	rpc.SyncAgentState = func(context.Context) string { return "hourly agent com.saahil.brain-sync running pid 4242" }
	_ = os.MkdirAll(config.StateDir(), 0o700)
	write := func(ended time.Time) {
		rec := fmt.Sprintf(`{"ended":%q,"exitCode":1,"summary":"skipped: one file"}`, ended.Format(time.RFC3339))
		if err := os.WriteFile(filepath.Join(config.StateDir(), "sync-last.json"), []byte(rec), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(time.Now().Add(-72 * time.Hour))
	if status, detail := checkSync(context.Background()); status != Fail || !strings.Contains(detail, "running pid 4242") {
		t.Errorf("stale: %s %q, want fail naming the agent's state", status, detail)
	}
	write(time.Now().Add(-10 * time.Minute))
	if status, detail := checkSync(context.Background()); status != OK || strings.Contains(detail, "hourly agent") {
		t.Errorf("fresh: %s %q, want ok without the agent", status, detail)
	}
}

// TestSyncLineWarnsAfterTheBridgeReloadedTheAgent catches a reload that hides the failure it healed: once the bridge
// reloads the hourly agent the next sync is fresh, and without a warning no care run would see launchd stop spawning
// it again, nor read the record of why.
func TestSyncLineWarnsAfterTheBridgeReloadedTheAgent(t *testing.T) {
	t.Setenv("BRAIN_HOME", t.TempDir())
	t.Setenv("BRAIN_MACHINE", "laptop")
	_ = os.MkdirAll(config.StateDir(), 0o700)
	rec := fmt.Sprintf(`{"ended":%q,"exitCode":1,"summary":"skipped: one file"}`, time.Now().Add(-5*time.Minute).Format(time.RFC3339))
	_ = os.WriteFile(filepath.Join(config.StateDir(), "sync-last.json"), []byte(rec), 0o600)
	reload := rpc.SyncReloadMark + time.Now().Add(-time.Hour).UTC().Format(time.RFC3339) + " reloaded com.saahil.brain-sync\n"
	_ = os.WriteFile(rpc.SyncAgentReloadLog(), []byte(reload), 0o600)
	if status, detail := checkSync(context.Background()); status != Warn || !strings.Contains(detail, "reloaded the hourly agent") {
		t.Errorf("fresh sync an hour after a reload: %s %q, want warn naming the reload", status, detail)
	}
}

// TestConflictWalkFansOutWithTheSerialResult catches the devbox's sync.conflicts timing out at 2 min (2026-10-25):
// one serial walk of work/'s 2.24 M entries outran the check. The walk now reads folders on goroutines; at any width
// it must list the same copies in the same order as the serial walk, skip what the sync never carries, and count
// unison's fresh half-copies as in flight.
func TestConflictWalkFansOutWithTheSerialResult(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{
		"notes/a (conflict on 2026-10-23).md", "notes/a.md",
		"work/box-a/plans/b (conflict #1 on 2026-10-23).md", "work/box-a/plans/b.md",
		"work/box-b/STATE (conflict on 2026-10-22).md", "work/box-b/deep/x/y/z (conflict on 2026-10-22).md",
		"work/box-b/artifacts/run (conflict on 2026-10-22).md", ".archive/conflicts/old (conflict on 2026-10-01).md",
		"work/box-c/.unison.tmp-1",
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"notes/a (conflict on 2026-10-23).md", "work/box-a/plans/b (conflict #1 on 2026-10-23).md",
		"work/box-b/STATE (conflict on 2026-10-22).md", "work/box-b/deep/x/y/z (conflict on 2026-10-22).md"}
	defer func(prev func()) { conflictWalkForked = prev }(conflictWalkForked)
	for _, width := range []int{0, 1, 3, 64} {
		var forked atomic.Int64
		conflictWalkForked = func() { forked.Add(1) }
		found, inFlight := conflictCopies(root, width)
		if strings.Join(found, "|") != strings.Join(want, "|") || inFlight != 1 {
			t.Fatalf("width %d: found %q, %d in flight; want %q, 1", width, found, inFlight, want)
		}
		if (width == 0) != (forked.Load() == 0) {
			t.Fatalf("width %d: %d folders read on a goroutine", width, forked.Load())
		}
	}
}
