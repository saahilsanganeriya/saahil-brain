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
	for _, path := range []string{".brain/dream-state.json", ".brain/dream.lock", ".brain/dream-last-skip.json", ".brain/held-carry.json"} {
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

// TestSyncRecordsEachFailedFileAndWhy catches the 2026-11-20 record that kept only unison's last line ("failed:
// <path>"): neither machine could tell a file a live agent wrote during the run from a real failure, and the
// devbox could not see the second failed file at all. The output below is unison 2.53.3's, with placeholder paths.
func TestSyncRecordsEachFailedFileAndWhy(t *testing.T) {
	out := "Starting...\n" +
		" 99%  1/2  (98.6 MiB of 98.6 MiB)   239 MiB/s    00:00:00 ETA\r" +
		"Failed [work/box/log.txt]: The source file /Users/test-user/brain/work/box/log.txt\n" +
		"has been modified during synchronization.  Transfer aborted.\n" +
		"[BGN] Updating file work/box/ok.md from /Users/test-user/brain to //devbox//Users/test-user/brain\n" +
		"[END] Updating file work/box/ok.md\n" +
		"Failed [work/box/a.md]: Error in writing: Permission denied\n" +
		"Synchronization incomplete at 23:07:58  (1 item transferred, 0 skipped, 2 failed)\n" +
		"  failed: work/box/log.txt\n  failed: work/box/a.md\n"
	got := syncFailures(out)
	want := []syncFailure{
		{Path: "work/box/log.txt", Reason: "The source file /Users/test-user/brain/work/box/log.txt has been modified during synchronization.  Transfer aborted."},
		{Path: "work/box/a.md", Reason: "Error in writing: Permission denied"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("syncFailures = %q\nwant %q", got, want)
	}
	prev := []syncFailure{{Path: "work/box/a.md"}, {Path: "work/other.md"}}
	if rep := repeatedFailures(prev, got); !slices.Equal(rep, []string{"work/box/a.md"}) {
		t.Errorf("repeatedFailures = %q, want only the path that failed in both runs", rep)
	}
}
