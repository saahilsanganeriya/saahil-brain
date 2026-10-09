package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"saahil.local/brain/adapters"
	"saahil.local/brain/internal/config"
)

// Host is one agent host and the state the brain needs in it.
type Host struct {
	Name  string
	Dir   string // present when this exists; "" = always
	Steps []Step
}

func (h Host) Present() bool {
	if h.Dir == "" {
		return true
	}
	_, err := os.Stat(h.Dir)
	return err == nil
}

const (
	ModName        = "saahil-brain"
	ModMarketplace = "saahil-mods"
	fileMarker     = "@saahil-brain-managed"
)

func ModsDir() string { return filepath.Join(config.Home(), "mods", ModMarketplace) }

// HostNames lists the hosts `--off` accepts.
func HostNames() []string {
	return []string{"claude", "mesh", "codex", "cursor", "prime", "omp", "git"}
}

// Hosts is the whole wiring. repo is the saahil-brain checkout whose mod/ the Claude Code marketplace links to.
func Hosts(repo string) []Host {
	home := config.Home()
	claude := filepath.Join(home, ".claude")
	rules := "<!-- " + fileMarker + ": brain install writes this file; brain install --off claude removes it -->\n" +
		"@" + filepath.Join(config.LaptopHome(), "brain", "BRAIN.md") + "\n"
	claudeSteps := []Step{
		{Kind: "file", Path: filepath.Join(claude, "rules", "brain.md"), Label: "rules", Content: []byte(rules), Owned: []byte("<!-- " + fileMarker)},
	}
	// after the cut-over (the brain is live) every repo's Claude Code memory is the brain's one global folder
	if _, err := os.Stat(filepath.Join(config.BrainRoot(), ".brain", "live")); err == nil {
		claudeSteps = append(claudeSteps, Step{Kind: "json-key", Path: filepath.Join(claude, "settings.json"), Label: "memory",
			Key: []string{"autoMemoryDirectory"}, Value: filepath.Join(config.LaptopHome(), "brain", "memory")})
	}
	// The mod carries every brain hook. Claude Code refuses plugin files that resolve outside its marketplace folder,
	// so the embedded files are written into the marketplace as ordinary managed files (no symlink).
	if _, err := os.Stat(filepath.Join(ModsDir(), ".claude-plugin", "marketplace.json")); err == nil {
		claudeSteps = append(claudeSteps, modFileSteps()...)
		claudeSteps = append(claudeSteps,
			Step{Kind: "json-item", Path: filepath.Join(ModsDir(), ".claude-plugin", "marketplace.json"), Label: "mod",
				Key: []string{"plugins"}, Value: map[string]any{"name": ModName, "source": "./" + ModName,
					"description": "The brain in Claude Code: brain_* tools, a pane, a band, the write guard and the brain hooks"}},
			Step{Kind: "json-key", Path: filepath.Join(claude, "settings.json"), Label: "mod",
				Key: []string{"enabledPlugins", ModName + "@" + ModMarketplace}, Value: true})
	}

	codex := filepath.Join(home, ".codex")
	cursor := filepath.Join(home, ".cursor")
	table, body := splitTable(embedded("codex/mcp.toml"))
	var cursorMCP any
	_ = json.Unmarshal(embedded("cursor/mcp.json"), &cursorMCP)
	return []Host{
		{Name: "claude", Dir: claude, Steps: claudeSteps},
		// the agent mesh's inbox hooks, apart from the brain so `--off claude` leaves messaging working
		{Name: "mesh", Dir: claude, Steps: []Step{{Kind: "hook", Path: filepath.Join(claude, "settings.json"), Label: "hooks", Hooks: MeshHooks()}}},
		{Name: "codex", Dir: codex, Steps: []Step{
			{Kind: "hook", Path: filepath.Join(codex, "hooks.json"), Label: "hooks", Hooks: hookList("codex/hooks.json")},
			{Kind: "toml-table", Path: filepath.Join(codex, "config.toml"), Label: "mcp", Table: table, Body: body},
		}},
		{Name: "cursor", Dir: cursor, Steps: []Step{
			{Kind: "cursor-hook", Path: filepath.Join(cursor, "hooks.json"), Label: "hooks", Hooks: hookList("cursor/hooks.json")},
			{Kind: "json-key", Path: filepath.Join(cursor, "mcp.json"), Label: "mcp", Key: []string{"mcpServers", "brain"}, Value: cursorMCP},
		}},
		extensionHost("prime", filepath.Join(home, ".prime", "agent"), "prime/brain.ts"),
		extensionHost("omp", filepath.Join(home, ".omp", "agent"), "omp/brain.ts"),
		// the brain is linked at the root of every repo at the cut-over; the link must never be committed. Anchored:
		// an unanchored `brain` would also hide every folder named brain (cmd/brain in this repo).
		{Name: "git", Steps: []Step{{Kind: "ignore-line", Path: GlobalGitIgnorePath(), Label: "ignore",
			Lines: []string{"/brain"}, Drop: []string{"brain", "brain/"}}}},
	}
}

func extensionHost(name, agentDir, file string) Host {
	return Host{Name: name, Dir: agentDir, Steps: []Step{{Kind: "file", Label: "extension",
		Path: filepath.Join(agentDir, "extensions", ModName+".ts"), Content: embedded(file), Owned: []byte("// " + fileMarker)}}}
}

func embedded(name string) []byte {
	b, err := adapters.Files.ReadFile(name)
	if err != nil {
		panic(err) // the embed list and these names are one change
	}
	return render(b)
}

func hookList(name string) []HookSpec {
	var hooks []HookSpec
	if err := json.Unmarshal(embedded(name), &hooks); err != nil {
		panic(name + ": " + err.Error())
	}
	return hooks
}

func splitTable(toml []byte) (table, body string) {
	head, rest, _ := strings.Cut(string(toml), "\n")
	table, _ = tomlTableName(head)
	return table, rest
}

// claudeDuplicateHooks finds brain commands in settings.json that would run twice per event: a mod-owned hook (the
// mod already runs it) or one command listed twice under one event.
func claudeDuplicateHooks() []string {
	raw, err := os.ReadFile(filepath.Join(config.ClaudeDir(), "settings.json"))
	if err != nil {
		return nil
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(raw, &settings) != nil {
		return nil
	}
	var problems []string
	for event, groups := range settings.Hooks {
		seen := map[string]bool{}
		for _, g := range groups {
			for _, h := range g.Hooks {
				if !strings.Contains(h.Command, BrainBinary()) {
					continue
				}
				for _, owned := range ModOwnedHooks {
					if strings.Contains(h.Command, BrainBinary()+" "+owned) {
						problems = append(problems, "settings.json "+event+" runs `brain "+owned+"`, which the mod already runs")
					}
				}
				if seen[h.Command] {
					problems = append(problems, "settings.json "+event+" lists `"+h.Command+"` twice")
				}
				seen[h.Command] = true
			}
		}
	}
	return problems
}
