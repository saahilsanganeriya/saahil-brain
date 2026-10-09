package install

import (
	"context"
	"encoding/json"
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

// render fills the placeholders the embedded mod and adapters carry with this person's binary and homes.
func render(data []byte) []byte {
	return []byte(strings.NewReplacer("@BRAIN_BIN@", BrainBinary(), "@LAPTOP_HOME@", config.LaptopHome(),
		"@DEVBOX_HOME@", config.DevboxHome()).Replace(string(data)))
}

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
		data = render(data)
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
		data = render(data)
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

// EnsureClaudeMarketplace registers the mods marketplace with Claude Code when Claude does not know it yet (Claude
// reads a directory marketplace only once added). Its registry is checked first; the CLI runs only when needed.
func EnsureClaudeMarketplace() (string, error) {
	if _, err := os.Stat(filepath.Join(ModsDir(), ".claude-plugin", "marketplace.json")); err != nil {
		return "", nil
	}
	var known map[string]json.RawMessage
	if raw, err := os.ReadFile(filepath.Join(config.ClaudeDir(), "plugins", "known_marketplaces.json")); err == nil &&
		json.Unmarshal(raw, &known) == nil && known[ModMarketplace] != nil {
		return "", nil
	}
	bin, err := exec.LookPath("claude")
	if err != nil {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, bin, "plugin", "marketplace", "add", ModsDir()).CombinedOutput(); err != nil {
		return "", fmt.Errorf("claude plugin marketplace add %s: %v %s", ModsDir(), err, strings.TrimSpace(string(out)))
	}
	return "claude plugin: added marketplace " + ModsDir(), nil
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
	if ok, _ := ClaudeLoadsMod(); ok {
		return "", nil
	}
	ref := ModName + "@" + ModMarketplace
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, bin, "plugin", "install", ref, "--scope", "user").CombinedOutput(); err != nil {
		return "", fmt.Errorf("claude plugin install %s: %v %s", ref, err, strings.TrimSpace(string(out)))
	}
	return "claude plugin: installed " + ref, nil
}

// ClaudeLoadsMod checks what Claude Code itself records, without running the claude CLI (under launchd on the
// laptop it hangs): the plugin is in its installed registry, enabled in settings, and the mod is real files inside the
// marketplace folder (Claude refuses plugin files that resolve outside it, as a symlinked mod did).
func ClaudeLoadsMod() (bool, string) {
	ref := ModName + "@" + ModMarketplace
	var reg struct {
		Plugins map[string][]json.RawMessage `json:"plugins"` // a directory marketplace is read in place, not from installPath
	}
	raw, err := os.ReadFile(filepath.Join(config.ClaudeDir(), "plugins", "installed_plugins.json"))
	if err != nil || json.Unmarshal(raw, &reg) != nil || len(reg.Plugins[ref]) == 0 {
		return false, ref + " is not in Claude's installed plugins (brain install runs claude plugin install)"
	}
	var settings struct {
		Enabled map[string]bool `json:"enabledPlugins"`
	}
	raw, err = os.ReadFile(filepath.Join(config.ClaudeDir(), "settings.json"))
	if err != nil || json.Unmarshal(raw, &settings) != nil || !settings.Enabled[ref] {
		return false, ref + " is not enabled in settings.json"
	}
	dir := filepath.Join(ModsDir(), ModName)
	if fi, err := os.Lstat(dir); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		return false, dir + " must be a real folder inside the marketplace"
	}
	return true, ""
}
