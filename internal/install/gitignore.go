package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"saahil.local/brain/internal/config"
)

// GlobalGitIgnorePath is core.excludesFile when set, else git's default ~/.config/git/ignore.
func GlobalGitIgnorePath() string {
	out, err := exec.Command("git", "config", "--global", "--get", "core.excludesFile").Output()
	if p := strings.TrimSpace(string(out)); err == nil && p != "" {
		if strings.HasPrefix(p, "~/") {
			p = filepath.Join(config.Home(), p[2:])
		}
		return p
	}
	return filepath.Join(config.Home(), ".config", "git", "ignore")
}

// AddGlobalGitIgnore appends the given patterns to the global ignore file when missing; it reports what it added.
func AddGlobalGitIgnore(path string, patterns ...string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	have := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		have[strings.TrimSpace(line)] = true
	}
	var added []string
	text := string(raw)
	for _, p := range patterns {
		if have[p] {
			continue
		}
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += p + "\n"
		added = append(added, p)
	}
	if len(added) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return added, os.WriteFile(path, []byte(text), 0o644)
}
