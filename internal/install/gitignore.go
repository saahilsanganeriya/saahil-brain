package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"saahil.local/brain/internal/config"
)

// GlobalGitIgnorePath is core.excludesFile when set, else git's default ~/.config/git/ignore. Under BRAIN_HOME git reads
// that home's config, so a test install never finds (and edits) the real ignore file.
func GlobalGitIgnorePath() string {
	cmd := exec.Command("git", "config", "--global", "--get", "core.excludesFile")
	if os.Getenv("BRAIN_HOME") != "" {
		cmd.Env = append(os.Environ(), "HOME="+config.Home(), "XDG_CONFIG_HOME="+filepath.Join(config.Home(), ".config"))
	}
	out, err := cmd.Output()
	if p := strings.TrimSpace(string(out)); err == nil && p != "" {
		if strings.HasPrefix(p, "~/") {
			p = filepath.Join(config.Home(), p[2:])
		}
		return p
	}
	return filepath.Join(config.Home(), ".config", "git", "ignore")
}
