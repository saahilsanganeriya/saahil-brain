package bridge

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"saahil.local/brain/internal/rpc"
)

// TestBridgeReloadsTheSyncAgentLaunchdStoppedSpawning catches the laptop's hourly sync dying for days: on 2026-10-12
// and again on 10-21 launchd stopped spawning the agent (last exit 78: EX_CONFIG) and nothing ran it again until a
// care run reloaded it one to three days later, with no record of why. The bridge must keep launchd's view and
// reload it, at most once an hour, and leave an agent that runs, or fails inside brain sync, alone.
func TestBridgeReloadsTheSyncAgentLaunchdStoppedSpawning(t *testing.T) {
	t.Setenv("BRAIN_HOME", t.TempDir())
	t.Setenv("BRAIN_MACHINE", "laptop")
	dead := "gui/501/com.saahil.brain-sync = {\n\tactive count = 0\n\tstate = not running\n\truns = 36\n" +
		"\tlast exit code = 78: EX_CONFIG\n}\n"
	failing := "gui/501/com.saahil.brain-sync = {\n\tstate = not running\n\truns = 12\n\tlast exit code = 1\n}\n"
	running := "gui/501/com.saahil.brain-sync = {\n\tstate = running\n\truns = 37\n\tpid = 4242\n" +
		"\tlast exit code = 78: EX_CONFIG\n}\n"
	view, reloads := failing, 0
	w := syncAgentWatch{
		print:      func(context.Context) string { return view },
		launchdLog: func(context.Context) string { return "launchd: test spawn failure line" },
		reload:     func() error { reloads++; return nil },
	}
	t0 := time.Date(2026, 10, 22, 22, 0, 0, 0, time.UTC)
	ctx := context.Background()

	w.check(ctx, t0)
	view = running
	w.check(ctx, t0.Add(10*time.Minute))
	if reloads != 0 {
		t.Fatalf("reloaded an agent that runs or fails inside brain sync: %d reloads", reloads)
	}
	view = dead
	w.check(ctx, t0.Add(20*time.Minute))
	w.check(ctx, t0.Add(50*time.Minute))
	if reloads != 1 {
		t.Fatalf("launchd stopped spawning it: %d reloads in 30 min, want 1", reloads)
	}
	raw, _ := os.ReadFile(rpc.SyncAgentReloadLog())
	for _, want := range []string{"2026-10-22T22:20:00Z", "last exit code = 78: EX_CONFIG", "test spawn failure line"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("reload record lacks %q:\n%s", want, raw)
		}
	}
	w.check(ctx, t0.Add(81*time.Minute))
	if reloads != 2 {
		t.Errorf("still dead an hour later: %d reloads, want 2", reloads)
	}
	if got := rpc.LastSyncAgentReload(t0.Add(2 * time.Hour)); !strings.Contains(got, "23:21:00Z") {
		t.Errorf("last reload = %q, want the 23:21 one", got)
	}
	if got := rpc.SyncAgentReloadRecord(t0.Add(2 * time.Hour)); !strings.HasPrefix(got, rpc.SyncReloadMark+"2026-10-22T23:21:00Z") ||
		!strings.Contains(got, "test spawn failure line") || strings.Contains(got, "22:20:00Z") {
		t.Errorf("the devbox reads %q, want only the newest record, whole", got)
	}
	if got := rpc.LastSyncAgentReload(t0.Add(26 * time.Hour)); got != "" {
		t.Errorf("a reload over a day old still reported: %q", got)
	}
}
