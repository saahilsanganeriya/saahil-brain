// Package install wires the brain into each agent host, idempotently, with a backup of every file it changes.
package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"saahil.local/brain/internal/config"
)

// HookSpec is one command for one Claude Code hook event.
type HookSpec struct {
	Event   string
	Matcher string
	Command string
	Timeout int // seconds
}

func BrainCommand(args string) string {
	return filepath.Join(config.LaptopHome, ".local", "bin", "brain") + " " + args
}

// MeshHooks deliver notes keyed by session id: at every tool call, prompt and turn end.
func MeshHooks() []HookSpec {
	inbox := BrainCommand("hook inbox")
	return []HookSpec{
		{Event: "PostToolUse", Matcher: ".*", Command: inbox, Timeout: 5},
		{Event: "UserPromptSubmit", Command: inbox, Timeout: 5},
		{Event: "Stop", Command: inbox, Timeout: 5},
	}
}

// AddClaudeHooks merges hooks into a Claude Code settings.json. It keeps every other key and hook untouched, adds a
// command only where the same event+matcher does not already run it, writes atomically, and keeps a timestamped
// backup of the previous file. It reports what it added.
func AddClaudeHooks(settingsPath string, hooks []HookSpec, now time.Time) ([]string, error) {
	raw, err := os.ReadFile(settingsPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	settings := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return nil, fmt.Errorf("%s is not valid JSON, refusing to edit: %w", settingsPath, err)
		}
	}
	hookMap, _ := settings["hooks"].(map[string]any)
	if hookMap == nil {
		hookMap = map[string]any{}
	}
	var added []string
	for _, h := range hooks {
		groups, _ := hookMap[h.Event].([]any)
		if hasCommand(groups, h.Matcher, h.Command) {
			continue
		}
		entry := map[string]any{"type": "command", "command": h.Command}
		if h.Timeout > 0 {
			entry["timeout"] = h.Timeout
		}
		group := map[string]any{"hooks": []any{entry}}
		if h.Matcher != "" {
			group["matcher"] = h.Matcher
		}
		hookMap[h.Event] = append(groups, group)
		added = append(added, h.Event+" "+h.Matcher+" → "+h.Command)
	}
	if len(added) == 0 {
		return nil, nil
	}
	settings["hooks"] = hookMap
	return added, writeJSONWithBackup(settingsPath, raw, settings, now)
}

func hasCommand(groups []any, matcher, command string) bool {
	for _, g := range groups {
		group, _ := g.(map[string]any)
		if m, _ := group["matcher"].(string); m != matcher {
			continue
		}
		entries, _ := group["hooks"].([]any)
		for _, e := range entries {
			if entry, _ := e.(map[string]any); entry["command"] == command {
				return true
			}
		}
	}
	return false
}

func writeJSONWithBackup(path string, previous []byte, value any, now time.Time) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		return err
	}
	out := buf.Bytes()
	if err := json.Unmarshal(out, new(any)); err != nil {
		return err
	}
	if len(previous) > 0 {
		backup := path + ".bak-brain-" + now.UTC().Format("20060102T150405Z")
		if err := os.WriteFile(backup, previous, 0o600); err != nil {
			return err
		}
	}
	tmp := path + ".brain-tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
