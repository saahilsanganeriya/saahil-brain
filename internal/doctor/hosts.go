package doctor

import (
	"context"
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
	rep := install.Verify(filepath.Join(config.LaptopHome, "Documents", "saahil-brain"), []string{name})[0]
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
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]) + ":"
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
