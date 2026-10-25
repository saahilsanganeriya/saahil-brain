package dream

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCommitWaitsOutAForeignGitLock catches a dream lost to another process's git: a foreign index.lock outlived the
// Commit activity's three attempts in 15 s, so the dream failed after paying for every step.
func TestCommitWaitsOutAForeignGitLock(t *testing.T) {
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
	gitLockWait = func(context.Context, time.Duration) error {
		waits++
		return os.Remove(lock) // the other process finishes while the dream waits
	}
	if err := os.WriteFile(filepath.Join(brain, "note.md"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := acts.gitCommit(context.Background(), "dream"); err != nil {
		t.Fatalf("commit failed on a lock another process releases: %v", err)
	}
	if waits != 1 {
		t.Fatalf("waited %d times for the lock, want 1", waits)
	}
}
