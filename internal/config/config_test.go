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
