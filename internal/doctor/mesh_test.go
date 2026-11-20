package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// TestSyncLineToleratesOnlyFilesWrittenDuringTheRun catches sync.last red on both machines on 2026-11-20 for a file a
// live agent was writing while unison copied it (unison exit 2, "has been modified during synchronization"): the next
// run carries such a file, as it does a skipped one. A failure for any other reason, a file that failed in the
// previous run too, or an exit 2 with no failures recorded stays red.
func TestSyncLineToleratesOnlyFilesWrittenDuringTheRun(t *testing.T) {
	t.Setenv("BRAIN_HOME", t.TempDir())
	t.Setenv("BRAIN_MACHINE", "laptop")
	_ = os.MkdirAll(config.StateDir(), 0o700)
	const midRun = "The source file /Users/test-user/brain/work/box/log.txt has been modified during synchronization.  Transfer aborted."
	ended := time.Now().Add(-5 * time.Minute).Format(time.RFC3339)
	for _, tc := range []struct {
		name, fields string
		want         Status
	}{
		{"written during the run", `"failed":[{"path":"work/box/log.txt","reason":"` + midRun + `"}]`, OK},
		{"failed in the previous run too", `"failed":[{"path":"work/box/log.txt","reason":"` + midRun + `"}],"repeated":["work/box/log.txt"]`, Fail},
		{"another reason", `"failed":[{"path":"work/box/log.txt","reason":"` + midRun + `"},{"path":"work/box/a.md","reason":"Error in writing: Permission denied"}]`, Fail},
		{"no failures recorded", `"failed":[]`, Fail},
	} {
		rec := fmt.Sprintf(`{"ended":%q,"exitCode":2,"summary":"  failed: work/box/log.txt",%s}`, ended, tc.fields)
		_ = os.WriteFile(filepath.Join(config.StateDir(), "sync-last.json"), []byte(rec), 0o600)
		status, detail := checkSync(context.Background())
		if status != tc.want {
			t.Errorf("%s: %s %q, want %s", tc.name, status, detail, tc.want)
		}
		if tc.want == OK && !strings.Contains(detail, "written during the run") {
			t.Errorf("%s: %q does not say the files were written during the run", tc.name, detail)
		}
	}
}
