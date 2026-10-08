package install

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/mod"
	"saahil.local/brain/templates"
)

// ShareDir holds what the binary lays out for hosts; its mod/ is what the Claude Code marketplace links to.
func ShareDir() string { return filepath.Join(config.Home(), ".local", "share", "brain") }

// ExtractMod writes the embedded plugin into <dir>/mod, replacing files whose content changed (atomic per file).
func ExtractMod(dir string) (changed int, err error) {
	root := filepath.Join(dir, "mod")
	err = fs.WalkDir(mod.Files, ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		data, err := mod.Files.ReadFile(path)
		if err != nil {
			return err
		}
		dst := filepath.Join(root, path)
		if old, err := os.ReadFile(dst); err == nil && string(old) == string(data) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		tmp := dst + ".brain-tmp"
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			return err
		}
		changed++
		return os.Rename(tmp, dst)
	})
	return changed, err
}

// WriteBrainDoc keeps <brain>/BRAIN.md equal to the binary's template (code-owned, like the mod).
func WriteBrainDoc(brainRoot string) (bool, error) {
	data, err := templates.FS.ReadFile("BRAIN.md")
	if err != nil {
		return false, err
	}
	dst := filepath.Join(brainRoot, "BRAIN.md")
	if old, err := os.ReadFile(dst); err == nil && string(old) == string(data) {
		return false, nil
	}
	tmp := dst + ".brain-tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, dst)
}

// modFileSteps writes each embedded mod file into the marketplace folder. A file counts as ours when it starts the
// way our copy starts (JSON cannot carry a marker comment).
func modFileSteps() []Step {
	var steps []Step
	_ = fs.WalkDir(mod.Files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := mod.Files.ReadFile(path)
		if err != nil {
			return err
		}
		steps = append(steps, Step{Kind: "file", Path: filepath.Join(ModsDir(), ModName, path), Label: "mod",
			Content: data, Owned: data[:min(len(data), 32)]})
		return nil
	})
	return steps
}

// dropOldModLink removes the symlink earlier installs put in the marketplace (Claude Code would not load through it),
// only when it still points at our own laid-out copy.
func dropOldModLink() error {
	link := filepath.Join(ModsDir(), ModName)
	target, err := os.Readlink(link)
	if err != nil {
		return nil
	}
	if config.Canonical(target) != config.Canonical(filepath.Join(ShareDir(), "mod")) {
		return nil
	}
	return os.Remove(link)
}

// EnsureClaudePlugin installs the mod as a Claude Code plugin when Claude does not list it yet: a plugin in a
// directory marketplace is only listed in the marketplace file until `claude plugin install` registers it.
func EnsureClaudePlugin() (string, error) {
	bin, err := exec.LookPath("claude")
	if err != nil {
		return "", nil
	}
	if _, err := os.Stat(filepath.Join(ModsDir(), ModName, ".claude-plugin", "plugin.json")); err != nil {
		return "", nil
	}
	ref := ModName + "@" + ModMarketplace
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, _ := exec.CommandContext(ctx, bin, "plugin", "list").CombinedOutput()
	if strings.Contains(string(out), ref) {
		return "", nil
	}
	if out, err := exec.CommandContext(ctx, bin, "plugin", "install", ref, "--scope", "user").CombinedOutput(); err != nil {
		return "", fmt.Errorf("claude plugin install %s: %v %s", ref, err, strings.TrimSpace(string(out)))
	}
	return "claude plugin: installed " + ref, nil
}
