package rpc

import (
	"strings"
	"testing"
)

// TestSyncAgentStateNamesWhyNoSyncRan catches a stale laptop sync that names no cause: on 2026-10-15 the hourly sync
// had not run for three days, and no record said whether its LaunchAgent was unloaded, stuck in one run (launchd
// starts no new run while one lives) or failing every hour. A nested block's own "state" must not pass for the
// agent's.
func TestSyncAgentStateNamesWhyNoSyncRan(t *testing.T) {
	running := "gui/501/com.saahil.brain-sync = {\n\tactive count = 1\n\tpath = /Users/test-user/Library/LaunchAgents/com.saahil.brain-sync.plist\n" +
		"\tstate = running\n\n\tprogram = /Users/test-user/.local/bin/brain\n" +
		"\truns = 81\n\tpid = 4242\n\tlast exit code = 0\n\trun interval = 3600 seconds\n}\n"
	waiting := "gui/501/com.saahil.brain-sync = {\n\tactive count = 0\n\tstate = not running\n\tevent triggers = {\n\t\tstate = 0\n\t}\n" +
		"\truns = 12\n\tlast exit code = 1\n}\n"
	missing := "Bad request.\nCould not find service \"com.saahil.brain-sync\" in domain for user gui: 501\n"
	cases := []struct {
		name, print, elapsed, children, logTail string
		want                                    []string
	}{
		{"hung", running, "2-03:04:05", "4250 unison\n", "",
			[]string{"running pid 4242 for 2-03:04:05", "unison", "runs 81", "sync-enable"}},
		{"failing", waiting, "", "", "brain: unison not installed on the laptop",
			[]string{"waiting (not running)", "runs 12", "last exit 1", "unison not installed"}},
		{"unloaded", missing, "", "", "", []string{"not loaded", "sync-enable"}},
	}
	for _, c := range cases {
		got := describeSyncAgent(parseLaunchctlPrint(c.print), c.elapsed, c.children, c.logTail)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q lacks %q", c.name, got, w)
			}
		}
	}
}
