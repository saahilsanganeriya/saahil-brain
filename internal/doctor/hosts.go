package doctor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/install"
)

// hostBinaries name the CLI whose version doctor shows beside each host's wiring.
var hostBinaries = map[string]string{"claude": "claude", "codex": "codex", "prime": "prime-agent", "omp": "omp"}

func init() {
	for _, name := range []string{"claude", "codex", "cursor", "prime", "omp"} {
		c := Check{Name: "host." + name, Run: func(ctx context.Context) (Status, string) { return checkHost(ctx, name) }}
		if name == "claude" {
			c.Timeout = 90 * time.Second // `claude plugin list` can refresh marketplaces first
		}
		Register(c)
	}
}

// checkHost: is the host installed, and are our hooks, MCP entry, extension or mod (whichever it takes) in place?
func checkHost(ctx context.Context, name string) (Status, string) {
	rep := install.Verify(linkedRepo(), []string{name})[0]
	if !rep.Present {
		return Skip, "not installed on this machine"
	}
	detail := hostVersion(ctx, name)
	var parts []string
	for _, label := range rep.Labels {
		mark := "✓"
		if contains(rep.Missing, label) {
			mark = "missing"
		}
		parts = append(parts, label+" "+mark)
	}
	detail = strings.TrimSpace(detail + " " + strings.Join(parts, " · "))
	if name == "claude" {
		// files in place are not enough: Claude Code itself must load the mod (it refused a symlinked one)
		if loaded, why := claudeLoadsMod(ctx); !loaded {
			return Fail, detail + "; Claude Code does not load the mod: " + why
		}
		detail += " · loaded ✓"
	}
	switch {
	case len(rep.Missing) > 0:
		return Fail, detail + " (brain install)"
	case len(rep.Conflicts) > 0:
		return Warn, detail + "; " + strings.Join(rep.Conflicts, "; ")
	}
	return OK, detail
}

// claudeLoadsMod asks Claude Code whether the saahil-brain plugin is enabled and loaded.
func claudeLoadsMod(ctx context.Context) (bool, string) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		return true, ""
	}
	out, err := exec.CommandContext(ctx, bin, "plugin", "list").CombinedOutput()
	if err != nil {
		return false, "claude plugin list failed: " + err.Error()
	}
	lines := strings.Split(string(out), "\n")
	for i, line := range lines {
		if !strings.Contains(line, install.ModName+"@"+install.ModMarketplace) {
			continue
		}
		for _, next := range lines[i+1 : min(i+8, len(lines))] {
			next = strings.TrimSpace(next)
			if strings.HasPrefix(next, "Status:") {
				if strings.Contains(next, "enabled") && !strings.Contains(next, "failed") {
					return true, ""
				}
				return false, next
			}
			if strings.HasPrefix(next, "Error:") {
				return false, next
			}
		}
		return false, "no status line"
	}
	return false, "not listed by claude plugin list"
}

func hostVersion(ctx context.Context, name string) string {
	bin, ok := hostBinaries[name]
	if !ok {
		return ""
	}
	out, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if line[0] >= '0' && line[0] <= '9' {
				line = bin + " " + line
			}
			return line + ":"
		}
	}
	return ""
}

// linkedRepo is the checkout the installed mod link points into, so doctor checks what is installed.
func linkedRepo() string {
	if target, err := os.Readlink(filepath.Join(install.ModsDir(), install.ModName)); err == nil {
		return filepath.Dir(target)
	}
	return filepath.Join(config.LaptopHome, "Documents", "saahil-brain")
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
