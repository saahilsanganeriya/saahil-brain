package install

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 8, 7, 0, 0, 0, time.UTC)

// fakeHome mirrors the devbox's host set with synthetic config: Claude Code (and the mods marketplace), Codex, Cursor
// and prime-agent; no oh-my-pi. Each file already holds an entry of the owner's that the installer must keep.
func fakeHome(t *testing.T) (home, repo string) {
	t.Helper()
	home, repo = t.TempDir(), t.TempDir()
	t.Setenv("BRAIN_HOME", home)
	files := map[string]string{
		".claude/settings.json": `{"model":"opus","hooks":{"Stop":[{"hooks":[{"type":"command","command":"/x/notify.sh"}]}],` +
			`"UserPromptSubmit":[{"hooks":[{"type":"command","command":"/Users/test-user/.local/bin/brain hook inbox","timeout":5}]}]}}`,
		"mods/saahil-mods/.claude-plugin/marketplace.json": `{"name":"saahil-mods","plugins":[{"name":"other-mod","source":"./other-mod"}]}`,
		".codex/hooks.json":                `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/x/codex-start.sh"}]}]}}`,
		".codex/config.toml":               "model = \"gpt-5\"\n\n[\"mcp_servers\".\"docs\"]\n\"url\" = \"https://docs.example.com/mcp\"\n",
		".cursor/hooks.json":               `{"version":1,"hooks":{"sessionStart":[{"command":"/x/cursor-start.sh","timeout":10}]}}`,
		".prime/agent/extensions/other.ts": "export default function () {}\n",
		".config/git/ignore":               "out/\nbrain\n",
	}
	for rel, body := range files {
		path := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.MkdirAll(filepath.Join(repo, "mod", ".claude-plugin"), 0o755)
	_ = os.WriteFile(filepath.Join(repo, "mod", ".claude-plugin", "plugin.json"), []byte(`{"name":"saahil-brain"}`), 0o600)
	return home, repo
}

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && !strings.Contains(path, ".bak-brain-") {
			raw, _ := os.ReadFile(path)
			files[path] = string(raw)
		}
		return nil
	})
	return files
}

// Catches a re-install that adds a hook, MCP table or marketplace entry twice, rewrites a file that was already
// right, loses the owner's own entries, runs a brain hook twice per event in Claude Code, or writes a manifest
// without the backup and before/after hashes of each file it changed.
func TestInstallIsIdempotent(t *testing.T) {
	home, repo := fakeHome(t)
	m, _ := LoadManifest(ManifestPath())
	first, err := Install(m, repo, nil, t0, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Save(ManifestPath()); err != nil {
		t.Fatal(err)
	}
	after := snapshot(t, home)

	second, err := Install(m, repo, nil, t0.Add(time.Hour), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, rep := range second {
		if len(rep.Added) > 0 || len(rep.Changes) > 0 || len(rep.Conflicts) > 0 {
			t.Errorf("second install of %s changed something: %+v", rep.Host, rep)
		}
	}
	if again := snapshot(t, home); len(again) != len(after) {
		t.Fatalf("second install wrote files: %d → %d", len(after), len(again))
	}
	for _, rep := range Verify(repo, nil) {
		if rep.Present && (len(rep.Added) > 0 || len(rep.Conflicts) > 0) {
			t.Errorf("verify %s: missing %v, conflicts %v", rep.Host, rep.Missing, rep.Conflicts)
		}
	}

	present := map[string]bool{}
	for _, rep := range first {
		present[rep.Host] = rep.Present
		for _, c := range rep.Changes {
			if c.SHA256After == "" || (c.Backup != "" && c.SHA256Before == "") {
				t.Errorf("%s: incomplete change record %+v", rep.Host, c)
			}
		}
	}
	if !present["claude"] || !present["codex"] || !present["cursor"] || !present["prime"] || present["omp"] {
		t.Fatalf("hosts detected = %v", present)
	}
	settings := after[filepath.Join(home, ".claude/settings.json")]
	for _, want := range []string{`"model": "opus"`, "/x/notify.sh", `"saahil-brain@saahil-mods": true`} {
		if !strings.Contains(settings, want) {
			t.Errorf("settings.json lost or lacks %s:\n%s", want, settings)
		}
	}
	if n := strings.Count(settings, "brain hook inbox"); n != 3 {
		t.Errorf("settings.json runs hook inbox %d times, want once on each of 3 events", n)
	}
	if dup := claudeDuplicateHooks(); len(dup) > 0 {
		t.Errorf("duplicate brain hooks: %v", dup)
	}
	toml := after[filepath.Join(home, ".codex/config.toml")]
	if strings.Count(toml, "[mcp_servers.brain]") != 1 || !strings.Contains(toml, `"url" = "https://docs.example.com/mcp"`) {
		t.Errorf("config.toml:\n%s", toml)
	}
	if link, _ := os.Readlink(filepath.Join(home, "mods/saahil-mods/saahil-brain")); link != filepath.Join(repo, "mod") {
		t.Errorf("mod link → %q", link)
	}
	backups, _ := filepath.Glob(filepath.Join(home, ".codex", "*.bak-brain-*"))
	if len(backups) != 2 {
		t.Errorf("codex backups = %v", backups)
	}
}

// Catches `--off` restoring a backup over later edits, leaving any of our entries behind, removing an entry the
// installer did not add (the inbox hook the owner had before), or deleting a file the owner has changed since.
func TestOffRemovesOnlyOurEntries(t *testing.T) {
	home, repo := fakeHome(t)
	before := snapshot(t, home)
	m, _ := LoadManifest(ManifestPath())
	if _, err := Install(m, repo, nil, t0, false); err != nil {
		t.Fatal(err)
	}
	// the owner edits after the install: a new Codex hook, and their own line in the rules file
	codexHooks := filepath.Join(home, ".codex/hooks.json")
	raw, _ := os.ReadFile(codexHooks)
	edited := strings.Replace(string(raw), `"hooks": {`, `"hooks": {"Notify":[{"hooks":[{"type":"command","command":"/x/later.sh"}]}],`, 1)
	_ = os.WriteFile(codexHooks, []byte(edited), 0o600)
	rules := filepath.Join(home, ".claude/rules/brain.md")
	_ = os.WriteFile(rules, []byte("my own rule\n"), 0o600)

	for _, host := range []string{"codex", "claude", "mesh", "cursor", "prime", "git"} {
		if _, err := Off(m, host, t0.Add(time.Hour), false); err != nil {
			t.Fatal(host, err)
		}
	}
	now := snapshot(t, home)
	if got := now[filepath.Join(home, ".codex/config.toml")]; got != before[filepath.Join(home, ".codex/config.toml")] {
		t.Errorf("config.toml not back to the owner's:\n%s", got)
	}
	codex := now[codexHooks]
	if strings.Contains(codex, "brain hook") || !strings.Contains(codex, "/x/later.sh") || !strings.Contains(codex, "/x/codex-start.sh") {
		t.Errorf("codex hooks.json after off:\n%s", codex)
	}
	settings := now[filepath.Join(home, ".claude/settings.json")]
	if strings.Count(settings, "brain hook inbox") != 1 || strings.Contains(settings, "saahil-brain@") || !strings.Contains(settings, "/x/notify.sh") {
		t.Errorf("settings.json after off:\n%s", settings)
	}
	if now[rules] != "my own rule\n" {
		t.Error("deleted the rules file the owner had changed")
	}
	if _, err := os.Lstat(filepath.Join(home, "mods/saahil-mods/saahil-brain")); err == nil {
		t.Error("mod link left behind")
	}
	market := now[filepath.Join(home, "mods/saahil-mods/.claude-plugin/marketplace.json")]
	if strings.Contains(market, "saahil-brain") || !strings.Contains(market, "other-mod") {
		t.Errorf("marketplace after off:\n%s", market)
	}
	for _, gone := range []string{".prime/agent/extensions/saahil-brain.ts"} {
		if _, ok := now[filepath.Join(home, gone)]; ok {
			t.Errorf("%s left behind", gone)
		}
	}
	if !bytes.Contains([]byte(now[filepath.Join(home, ".cursor/hooks.json")]), []byte("/x/cursor-start.sh")) {
		t.Error("cursor lost the owner's hook")
	}
}

// Catches the installer putting a file into a repo checkout (a company worktree above all): a host folder that is
// inside a git work tree is refused, not written.
func TestInstallRefusesToWriteInsideARepo(t *testing.T) {
	home, repo := fakeHome(t)
	_ = os.MkdirAll(filepath.Join(home, ".cursor", ".git"), 0o755)
	m, _ := LoadManifest(ManifestPath())
	if _, err := Install(m, repo, []string{"cursor"}, t0, false); err == nil || !strings.Contains(err.Error(), "git repository") {
		t.Fatalf("err = %v", err)
	}
}
