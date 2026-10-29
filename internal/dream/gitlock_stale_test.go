package dream

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestADeadProcesssGitLockIsMovedAsideNotWaitedOn catches dreams failing hour after hour on a lock no process holds:
// a git run under the brain was killed and left .git/index.lock, git never removes it, and every snapshot waited out
// its minute and failed (14 dreams in a row).
func TestADeadProcesssGitLockIsMovedAsideNotWaitedOn(t *testing.T) {
	brain := t.TempDir()
	acts := &Activities{Cfg: Config{BrainRoot: brain}}
	if err := os.WriteFile(filepath.Join(brain, "note.md"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := acts.Snapshot(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(brain, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	dead := time.Now().Add(-15 * time.Hour)
	if err := os.Chtimes(lock, dead, dead); err != nil {
		t.Fatal(err)
	}
	waits := 0
	defer func(prev func(context.Context, time.Duration) error) { gitLockWait = prev }(gitLockWait)
	gitLockWait = func(context.Context, time.Duration) error { waits++; return nil }
	if err := os.WriteFile(filepath.Join(brain, "note.md"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := acts.Snapshot(context.Background(), "run-2"); err != nil {
		t.Fatalf("snapshot failed on a lock its dead process left: %v", err)
	}
	if waits != 0 {
		t.Fatalf("waited %d times on a lock 15 h old, want 0", waits)
	}
	moved, _ := filepath.Glob(filepath.Join(brain, ".archive", "git-locks", "*-index.lock"))
	if len(moved) != 1 {
		t.Fatalf("the stale lock was not kept in .archive/git-locks: %v", moved)
	}
}

// TestALiveProcesssGitLockIsWaitedOnNeverMoved catches the stale-lock repair taking a lock a running git still holds.
func TestALiveProcesssGitLockIsWaitedOnNeverMoved(t *testing.T) {
	brain := t.TempDir()
	acts := &Activities{Cfg: Config{BrainRoot: brain}}
	if err := os.WriteFile(filepath.Join(brain, "note.md"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := acts.Snapshot(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(brain, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	waits := 0
	defer func(prev func(context.Context, time.Duration) error) { gitLockWait = prev }(gitLockWait)
	gitLockWait = func(context.Context, time.Duration) error { waits++; return nil }
	if err := os.WriteFile(filepath.Join(brain, "note.md"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := acts.Snapshot(context.Background(), "run-2")
	if err == nil || !strings.Contains(err.Error(), "index.lock': File exists") {
		t.Fatalf("snapshot err = %v, want the held lock named", err)
	}
	if waits != 5 {
		t.Fatalf("waited %d times on a fresh lock, want 5", waits)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("a fresh lock was moved: %v", err)
	}
}
