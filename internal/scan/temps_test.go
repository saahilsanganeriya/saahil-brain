package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"saahil.local/brain/internal/config"
)

// TestArchiveStaleTempsOnTheRemote catches the devbox doctor's sync.conflicts going red for days (2026-11-03: 8
// half-copies from 10-29 in a campaign box, 36 owner-ruled copies beside them): archiveStaleTemps ran only inside
// brain sync, which never runs on unison's remote, so the remote's abandoned half-copies never moved. The remote's
// own hourly step must move half-copies past the grace — files and folders — into the same archive sync uses,
// leaving the owner-ruled conflict copies live.
func TestArchiveStaleTempsOnTheRemote(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	stale := now.Add(-config.UnisonTempGrace - time.Hour)
	for _, p := range []string{
		"work/box/.unison.GOAL.md.0123.unison.tmp",
		"work/box/.unison.STATE.md.0123.unison.tmp",
		"work/box/sub/.unison.f.0123.unison.tmp",
		"work/box/.unison.plans.0123.unison.tmp/BRIEF.md",
		"work/box/STATE (conflict on 2026-10-23).md",
		"work/box/artifacts/.unison.leftover.0123.unison.tmp",
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(filepath.Join(root, "work/box/artifacts/.unison.leftover.0123.unison.tmp"), stale, stale); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(root, "work/box/.unison.fresh.0123.unison.tmp")
	if err := os.WriteFile(fresh, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(root, "work/box/.unison.plans.0123.unison.tmp"), stale, stale); err != nil {
		t.Fatal(err)
	}
	// the file half-copies and the folder's contents carry the stale stamp
	for _, p := range []string{
		"work/box/.unison.GOAL.md.0123.unison.tmp",
		"work/box/.unison.STATE.md.0123.unison.tmp",
		"work/box/sub/.unison.f.0123.unison.tmp",
		"work/box/.unison.plans.0123.unison.tmp/BRIEF.md",
	} {
		if err := os.Chtimes(filepath.Join(root, p), stale, stale); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Setenv("BRAIN_HOME", t.TempDir())
	dest := filepath.Join(root, ".archive", "conflicts", string(config.ThisMachine()))
	moved, err := ArchiveStaleTemps(root, now)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 4 {
		t.Fatalf("moved %d; want 4 (3 files + 1 folder, moving whole, past the grace)", moved)
	}
	for _, gone := range []string{
		"work/box/.unison.GOAL.md.0123.unison.tmp",
		"work/box/.unison.STATE.md.0123.unison.tmp",
		"work/box/sub/.unison.f.0123.unison.tmp",
	} {
		if _, err := os.Stat(filepath.Join(root, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s must move into the archive", gone)
		}
		if _, err := os.Stat(filepath.Join(dest, gone)); err != nil {
			t.Fatalf("%s must be in the archive: %v", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "work/box/.unison.plans.0123.unison.tmp")); !os.IsNotExist(err) {
		t.Fatal("an abandoned half-copy folder must move whole into the archive")
	}
	if _, err := os.Stat(filepath.Join(dest, "work/box/.unison.plans.0123.unison.tmp/BRIEF.md")); err != nil {
		t.Fatalf("the folder's contents must be in the archive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "work/box/STATE (conflict on 2026-10-23).md")); err != nil {
		t.Fatal("an owner-ruled conflict copy must stay live for the owner")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("a half-copy within the grace may still be in flight: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "work/box/artifacts/.unison.leftover.0123.unison.tmp")); err != nil {
		t.Fatal("bulk folders are never synced, so a half-copy there is never stale sync debris")
	}
	log, err := os.ReadFile(filepath.Join(dest, "log.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(log), "\n"); n != 4 {
		t.Fatalf("log.tsv holds %d lines; want 4", n)
	}
}
