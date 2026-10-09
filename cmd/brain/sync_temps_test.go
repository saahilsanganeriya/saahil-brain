package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"saahil.local/brain/internal/config"
)

// TestArchiveStaleTemps: an aborted transfer of a file a live agent writes leaves unison's half-copy beside it, which
// the doctor counted as an unhandled conflict; only half-copies older than this run, next to a failed file, move.
func TestArchiveStaleTemps(t *testing.T) {
	t.Setenv("BRAIN_HOME", t.TempDir())
	root := t.TempDir()
	dir := filepath.Join(root, "work", "box", "logs")
	_ = os.MkdirAll(dir, 0o755)
	started := time.Now()
	for name, age := range map[string]time.Duration{".unison.stdout.ab.unison.tmp": time.Hour, ".unison.stderr.cd.unison.tmp": -time.Second, "stdout": time.Hour} {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte("x"), 0o644)
		_ = os.Chtimes(p, started.Add(-age), started.Add(-age))
	}
	n, err := archiveStaleTemps(root, []byte("Synchronization complete\n  failed: work/box/logs/stdout\n"), started)
	if err != nil || n != 1 {
		t.Fatalf("moved %d, err %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".unison.stdout.ab.unison.tmp")); !os.IsNotExist(err) {
		t.Fatal("the stale half-copy must move into the archive")
	}
	for _, keep := range []string{".unison.stderr.cd.unison.tmp", "stdout"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Fatalf("%s must stay: %v", keep, err)
		}
	}
}

// TestArchiveAbandonedTemps catches the 2026-10-09 laptop doctor going red: half-copies left by an earlier run, in a
// folder with no failed file this run, stayed live past the doctor's grace. Past it they move; younger ones stay.
func TestArchiveAbandonedTemps(t *testing.T) {
	t.Setenv("BRAIN_HOME", t.TempDir())
	root := t.TempDir()
	dir := filepath.Join(root, "inbox")
	_ = os.MkdirAll(dir, 0o755)
	started := time.Now()
	for name, age := range map[string]time.Duration{".unison.old.ab.unison.tmp": config.UnisonTempGrace + time.Minute,
		".unison.new.cd.unison.tmp": 10 * time.Minute} {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte("x"), 0o644)
		_ = os.Chtimes(p, started.Add(-age), started.Add(-age))
	}
	n, err := archiveStaleTemps(root, []byte("Synchronization complete\n"), started)
	if err != nil || n != 1 {
		t.Fatalf("moved %d, err %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".unison.old.ab.unison.tmp")); !os.IsNotExist(err) {
		t.Fatal("a half-copy past the grace must move into the archive")
	}
	if _, err := os.Stat(filepath.Join(dir, ".unison.new.cd.unison.tmp")); err != nil {
		t.Fatalf("a half-copy within the grace may still be in flight and must stay: %v", err)
	}
}
