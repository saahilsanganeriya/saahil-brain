package doctor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/install"
)

// hostBinaries name the CLI whose version doctor shows beside each host's wiring.
var hostBinaries = map[string]string{"claude": "claude", "codex": "codex", "prime": "prime-agent", "omp": "omp"}

func init() {
	for _, name := range []string{"claude", "codex", "cursor", "prime", "omp"} {
		Register(Check{Name: "host." + name, Run: func(ctx context.Context) (Status, string) { return checkHost(ctx, name) }})
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
	switch {
	case len(rep.Missing) > 0:
		return Fail, detail + " (brain install)"
	case len(rep.Conflicts) > 0:
		return Warn, detail + "; " + strings.Join(rep.Conflicts, "; ")
	}
	return OK, detail
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
