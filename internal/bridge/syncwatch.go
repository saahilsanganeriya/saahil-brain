package bridge

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"saahil.local/brain/internal/rpc"
)

const (
	syncWatchEvery     = 10 * time.Minute
	syncReloadGap      = time.Hour
	syncReloadLogLimit = 1 << 20
	launchdLogHalf     = 8 << 10
)

// WatchSyncAgent runs on the bridge, which launchd keeps alive: when launchd stops spawning the hourly sync agent
// (2026-10-12 and 10-21, each time unseen for days), it records launchd's view and reloads the agent.
func WatchSyncAgent(ctx context.Context) {
	w := &syncAgentWatch{print: rpc.SyncAgentPrint, launchdLog: launchdLogLines, reload: EnableSync}
	for {
		w.check(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-time.After(syncWatchEvery):
		}
	}
}

type syncAgentWatch struct {
	print      func(context.Context) string
	launchdLog func(context.Context) string
	reload     func() error
	last       time.Time
}

func (w *syncAgentWatch) check(ctx context.Context, now time.Time) {
	view := w.print(ctx)
	if !rpc.SyncAgentSpawnFailed(view) || (!w.last.IsZero() && now.Sub(w.last) < syncReloadGap) {
		return
	}
	w.last = now
	record := fmt.Sprintf("%s%s reloaded %s: launchd stopped spawning it\n-- launchctl print\n%s\n-- launchd log, last 2 h\n%s\n",
		rpc.SyncReloadMark, now.UTC().Format(time.RFC3339), SyncLabel, view, w.launchdLog(ctx))
	if err := w.reload(); err != nil {
		record += "-- reload failed: " + err.Error() + "\n"
	}
	appendSyncReloadRecord(record)
}

// appendSyncReloadRecord keeps the newest records: past 1 MB the file moves to .old, replacing the one before.
func appendSyncReloadRecord(record string) {
	path := rpc.SyncAgentReloadLog()
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	if info, err := os.Stat(path); err == nil && info.Size() > syncReloadLogLimit {
		_ = os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(record)
}

// launchdLogLines is what the unified log says about the agent over two hourly ticks, its first and last 8 KB
// (26 h took over 90 s on the laptop).
func launchdLogLines(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/log", "show", "--last", "2h", "--style", "compact",
		"--predicate", fmt.Sprintf("eventMessage CONTAINS %q", SyncLabel)).Output()
	s := string(out)
	if len(s) > 2*launchdLogHalf {
		s = s[:launchdLogHalf] + "\n…\n" + s[len(s)-launchdLogHalf:]
	}
	if err != nil {
		s += fmt.Sprintf("(log show: %v)\n", err)
	}
	return s
}
