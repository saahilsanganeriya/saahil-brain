// Package install wires the brain into each agent host, idempotently, with a backup of every file it changes and a
// manifest of everything it added, so `--off <host>` can take exactly that back out.
package install

import (
	"path/filepath"

	"saahil.local/brain/internal/config"
)

// HookSpec is one command for one hook event (Claude Code, Codex and Cursor share the shape).
type HookSpec struct {
	Event   string `json:"event"`
	Matcher string `json:"matcher,omitempty"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"` // seconds
}

func BrainBinary() string { return filepath.Join(config.LaptopHome, ".local", "bin", "brain") }

func BrainCommand(args string) string { return BrainBinary() + " " + args }

// MeshHooks deliver notes keyed by session id: at every tool call, prompt and turn end. They are the only brain
// commands in Claude Code's settings.json; the brain's own hooks run from the mod, so each event has one owner.
func MeshHooks() []HookSpec {
	inbox := BrainCommand("hook inbox")
	return []HookSpec{
		{Event: "PostToolUse", Matcher: ".*", Command: inbox, Timeout: 5},
		{Event: "UserPromptSubmit", Command: inbox, Timeout: 5},
		{Event: "Stop", Command: inbox, Timeout: 5},
	}
}

// ModOwnedHooks are the `brain hook` subcommands the Claude Code mod runs; one of them in settings.json would run twice.
var ModOwnedHooks = []string{"hook start", "hook prompt", "hook post", "hook end", "hook compact", "guard"}
