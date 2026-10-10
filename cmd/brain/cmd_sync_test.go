package main

import (
	"slices"
	"strings"
	"testing"

	"saahil.local/brain/internal/config"
)

// TestSyncIgnoresDreamOwnState catches the 2026-10-09 doctor failure on both machines: .brain/dream-last-skip.json,
// which every hourly dream skip rewrites on the devbox, still synced; after an interrupted sync both replicas read as
// changed and unison left a conflict copy. The dream's own state stays on the machine that dreams.
func TestSyncIgnoresDreamOwnState(t *testing.T) {
	args := unisonArgs("/brain")
	for _, path := range []string{".brain/dream-state.json", ".brain/dream.lock", ".brain/dream-last-skip.json"} {
		i := slices.Index(args, "Path "+path)
		if i < 1 || args[i-1] != "-ignore" {
			t.Errorf("unison must ignore %s", path)
		}
	}
}

// TestSyncNamesThisMachineStably catches the 2026-10-10 conflict copies: unison names its archive after the local
// hostname, which macOS changes with the network; each change moved the sync onto the other name's stale archive, and
// files both replicas already agreed on came back as conflict copies. Unison runs under the machine's fixed name.
func TestSyncNamesThisMachineStably(t *testing.T) {
	t.Setenv("UNISONLOCALHOSTNAME", "Some-MacBook.local")
	name := ""
	for _, kv := range unisonCommand("/usr/bin/unison", unisonArgs("/brain")).Env {
		if v, ok := strings.CutPrefix(kv, "UNISONLOCALHOSTNAME="); ok {
			name = v
		}
	}
	if want := string(config.ThisMachine()); name != want {
		t.Errorf("unison runs as host %q, want the fixed %q", name, want)
	}
}
