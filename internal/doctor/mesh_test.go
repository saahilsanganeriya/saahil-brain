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
