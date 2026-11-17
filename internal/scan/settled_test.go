package scan

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"saahil.local/brain/internal/config"
)

// TestArchiveSettledConflicts catches the doctor's sync.conflicts growing from 36 to 123 copies in one sync
// (2026-11-17): the other machine held only an older version of files this machine wrote again, the sync kept that
// older version as a conflict copy, and every copy waited for the owner. A copy that holds nothing its live file lacks
// (the same bytes, lines the live file still has in the same order, or an earlier committed version of that file)
// needs no decision and must move into the archive. A copy with a line of its own, or with no live file, must stay.
func TestArchiveSettledConflicts(t *testing.T) {
	root := t.TempDir()
	put := func(rel, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	put("notes/rewritten.md", "first wording\n")
	git("add", "-A")
	git("commit", "-q", "-m", "one")
	put("notes/rewritten.md", "second wording, nothing kept\n")
	git("add", "-A")
	git("commit", "-q", "-m", "two")

	put("notes/rewritten (conflict_on_2026-11-17).md", "first wording\n")
	put("work/box/LEDGER.md", "row 1\nrow 2\nrow 3\n")
	put("work/box/LEDGER (conflict_on_2026-11-17).md", "row 1\nrow 3\n")
	put("work/box/same.md", "a\n")
	put("work/box/same (conflict #1_on_2026-11-17).md", "a\n")
	put("work/box/STATE.md", "the newer state\n")
	put("work/box/STATE (conflict_on_2026-11-17).md", "an edit only this copy holds\nthe newer state\n")
	put("work/box/reordered.md", "b\na\n")
	put("work/box/reordered (conflict_on_2026-11-17).md", "a\nb\n")
	put("work/box/gone (conflict_on_2026-11-17).md", "the live file was removed\n")
	put("notes/never-committed.md", "third\n")
	put("notes/never-committed (conflict_on_2026-11-17).md", "an uncommitted earlier wording\n")

	t.Setenv("BRAIN_HOME", t.TempDir())
	now := time.Now()
	moved, err := ArchiveSettledConflicts(root, now)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 3 {
		t.Fatalf("moved %d; want 3 (an earlier committed version, an ordered subset of lines, the same bytes)", moved)
	}
	dest := filepath.Join(root, ".archive", "conflicts", string(config.ThisMachine()))
	for _, rel := range []string{
		"notes/rewritten (conflict_on_2026-11-17).md",
		"work/box/LEDGER (conflict_on_2026-11-17).md",
		"work/box/same (conflict #1_on_2026-11-17).md",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(err) {
			t.Fatalf("%s holds nothing its live file lacks and must leave the live tree", rel)
		}
		if _, err := os.Stat(filepath.Join(dest, rel)); err != nil {
			t.Fatalf("%s must be kept in the archive: %v", rel, err)
		}
	}
	for _, rel := range []string{
		"work/box/STATE (conflict_on_2026-11-17).md",
		"work/box/reordered (conflict_on_2026-11-17).md",
		"work/box/gone (conflict_on_2026-11-17).md",
		"notes/never-committed (conflict_on_2026-11-17).md",
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Fatalf("%s may hold an edit the live file lacks and must stay for the owner: %v", rel, err)
		}
	}
	log, err := os.ReadFile(filepath.Join(dest, "log.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(log), "\n"); n != 3 {
		t.Fatalf("log.tsv holds %d lines; want 3", n)
	}
	if again, err := ArchiveSettledConflicts(root, now); err != nil || again != 0 {
		t.Fatalf("a second pass moved %d (err %v); want 0", again, err)
	}
}
