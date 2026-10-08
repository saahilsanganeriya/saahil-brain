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

// AddGlobalGitIgnore appends the given patterns to the global ignore file when missing, after dropping the exact
// lines in drop (an earlier, wrong form of the same entry); it reports what changed.
func AddGlobalGitIgnore(path string, drop []string, patterns ...string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	dropSet := map[string]bool{}
	for _, d := range drop {
		dropSet[d] = true
	}
	have := map[string]bool{}
	var kept []string
	var added []string
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if dropSet[strings.TrimSpace(line)] {
			added = append(added, "-"+strings.TrimSpace(line))
			continue
		}
		kept = append(kept, line)
		have[strings.TrimSpace(line)] = true
	}
	text := strings.Join(kept, "\n")
	if text != "" {
		text += "\n"
	}
	for _, p := range patterns {
		if have[p] {
			continue
		}
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += p + "\n"
		added = append(added, "+"+p)
	}
	if len(added) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return added, os.WriteFile(path, []byte(text), 0o644)
}
