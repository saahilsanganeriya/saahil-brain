package main

import (
	"slices"
	"testing"
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
