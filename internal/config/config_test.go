package config

import "testing"

// Catches the two machines indexing one file under two paths (devbox /home/test-user vs the laptop spelling).
func TestCanonicalPath(t *testing.T) {
	cases := map[string]string{
		"/home/test-user/brain/notes/x.md":             "/Users/test-user/brain/notes/x.md",
		"/Users/test-user/brain/notes/x.md": "/Users/test-user/brain/notes/x.md",
		"/home/test-user":         "/Users/test-user",
		"/home/test-userx/y":      "/home/test-userx/y",
		"/home/test-user/a/../b/": "/Users/test-user/b",
	}
	for in, want := range cases {
		if got := Canonical(in); got != want {
			t.Errorf("Canonical(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSyncCarries: doctor must agree with the sync about what reaches the other machine, or the laptop reports bulk
// left on the devbox (a work/ .json, an artifacts/ file) as dangling links.
func TestSyncCarries(t *testing.T) {
	for rel, want := range map[string]bool{
		"notes/lesson-x.md": true, "work/box/plans/SPEC.md#goal": true, "work/box/tools/Makefile": true,
		"work/box/evidence/run.json": false, "work/box/artifacts/report.md": false, "work/box/tools/target/x.d": false,
	} {
		if got := SyncCarries(rel); got != want {
			t.Errorf("SyncCarries(%q) = %v, want %v", rel, got, want)
		}
	}
}
