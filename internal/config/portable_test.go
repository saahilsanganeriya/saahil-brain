package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ownerLiterals are values only the owner's identity.json may hold; shipped code reads them from the settings.
var ownerLiterals = []string{"/Users/test-user", "/home/test-user", "devbox-host", "0000a1a1", "0000e1e1"}

// TestNoOwnerLiteralsInShippedCode catches a per-person value (the owner's homes, devbox host, Orca ids) coming back
// into code or rendered host files: on anyone else's machines it points at paths and servers that are not theirs.
func TestNoOwnerLiteralsInShippedCode(t *testing.T) {
	root := repoRoot(t)
	shipped := map[string]bool{".go": true, ".js": true, ".ts": true, ".json": true, ".toml": true}
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			hidden := strings.HasPrefix(name, ".") && name != ".claude-plugin"
			if path != root && (name == "testdata" || name == "node_modules" || name == "bin" || hidden) {
				return filepath.SkipDir
			}
			return nil
		}
		if !shipped[filepath.Ext(name)] || strings.HasSuffix(name, "_test.go") || strings.Contains(name, ".test.") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(raw), "\n") {
			for _, lit := range ownerLiterals {
				if strings.Contains(line, lit) {
					rel, _ := filepath.Rel(root, path)
					found = append(found, rel+":"+strconv.Itoa(i+1)+": "+lit)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Errorf("%d owner literals in shipped code (read them from the identity settings):\n%s", len(found), strings.Join(found, "\n"))
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's directory")
		}
		dir = parent
	}
}
