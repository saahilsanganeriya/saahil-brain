package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
