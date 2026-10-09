// Package identitytest gives tests a home whose ~/.config/brain/identity.json they choose, so no test reads the
// identity of the machine it runs on.
package identitytest

import (
	"os"
	"path/filepath"
	"testing"
)

// Write points BRAIN_HOME at a fresh home whose identity.json holds identityJSON, and returns that home.
func Write(t testing.TB, identityJSON string) string {
	t.Helper()
	return WriteIn(t, t.TempDir(), identityJSON)
}

// WriteIn is Write into a home the test already made (its identity names paths inside it).
func WriteIn(t testing.TB, home, identityJSON string) string {
	t.Helper()
	path := filepath.Join(home, ".config", "brain", "identity.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(identityJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BRAIN_HOME", home)
	return home
}

// Owner is Write with testdata/identity/owner.json: the owner's own file, under which every behaviour is what it was
// before the settings existed.
func Owner(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if raw, err := os.ReadFile(filepath.Join(dir, "testdata", "identity", "owner.json")); err == nil {
			return Write(t, string(raw))
		}
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			t.Fatal("testdata/identity/owner.json is missing at the module root")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no module root above the test's directory")
		}
		dir = parent
	}
}
