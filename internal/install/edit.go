package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Step is one piece of desired state in one file. Every kind is an idempotent ensure, a check (the ensure run on a
// copy) and, from the manifest entry it produced, an exact removal.
type Step struct {
	Kind    string
	Path    string
	Label   string     // what doctor reports it as: hooks | mcp | mod | rules | extension | ignore
	Hooks   []HookSpec // hook, cursor-hook
	Key     []string   // json-key: the path to the leaf; json-item: Key[0] is the array, matched by the item's "name"
	Value   any
	Table   string // toml-table, dotted
	Body    string
	Content []byte // file
	Owned   []byte // file: a prefix that marks the file as ours, so a newer version may replace it
	Target  string // symlink
	Lines   []string
	Drop    []string // ignore-line: earlier wrong forms of the same line
}

func family(kind string) string {
	switch kind {
	case "hook", "cursor-hook", "json-key", "json-item":
		return "json"
	}
	return kind
}

// ---- JSON documents ----

func parseJSONDoc(path string, raw []byte) (map[string]any, error) {
	doc := map[string]any{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return doc, nil
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON, refusing to edit: %w", path, err)
	}
	return doc, nil
}

func childMap(parent map[string]any, key string) map[string]any {
	m, _ := parent[key].(map[string]any)
	if m == nil {
		m = map[string]any{}
		parent[key] = m
	}
	return m
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func rawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// ensureJSON applies one step to doc; it returns what it added and anything in the way that it left alone.
func ensureJSON(doc map[string]any, s Step) (added []Entry, conflicts []string) {
	switch s.Kind {
	case "hook", "cursor-hook":
		hooks := childMap(doc, "hooks")
		if s.Kind == "cursor-hook" && doc["version"] == nil {
			doc["version"] = 1
		}
		for _, h := range s.Hooks {
			list, _ := hooks[h.Event].([]any)
			if hookIndex(list, s.Kind, h) >= 0 {
				continue
			}
			hooks[h.Event] = append(list, hookItem(s.Kind, h))
			added = append(added, Entry{Kind: s.Kind, Path: s.Path, Event: h.Event, Matcher: h.Matcher, Command: h.Command})
		}
	case "json-key":
		parent := doc
		for _, k := range s.Key[:len(s.Key)-1] {
			parent = childMap(parent, k)
		}
		leaf := s.Key[len(s.Key)-1]
		if have, ok := parent[leaf]; ok {
			if !jsonEqual(have, s.Value) {
				conflicts = append(conflicts, fmt.Sprintf("%s: %s is already set to something else", s.Path, strings.Join(s.Key, ".")))
			}
			return nil, conflicts
		}
		parent[leaf] = s.Value
		added = append(added, Entry{Kind: s.Kind, Path: s.Path, Key: s.Key, Value: rawJSON(s.Value)})
	case "json-item":
		list, _ := doc[s.Key[0]].([]any)
		item := s.Value.(map[string]any)
		for _, have := range list {
			if m, _ := have.(map[string]any); m != nil && m["name"] == item["name"] {
				if !jsonEqual(m, item) {
					conflicts = append(conflicts, fmt.Sprintf("%s: %s %v differs from ours", s.Path, s.Key[0], item["name"]))
				}
				return nil, conflicts
			}
		}
		doc[s.Key[0]] = append(list, item)
		added = append(added, Entry{Kind: s.Kind, Path: s.Path, Key: s.Key, Value: rawJSON(item)})
	}
	return added, conflicts
}

// Claude Code and Codex nest commands in matcher groups; Cursor lists them flat.
func hookItem(kind string, h HookSpec) map[string]any {
	cmd := map[string]any{"command": h.Command}
	if h.Timeout > 0 {
		cmd["timeout"] = h.Timeout
	}
	if kind == "cursor-hook" {
		if h.Matcher != "" {
			cmd["matcher"] = h.Matcher
		}
		return cmd
	}
	cmd["type"] = "command"
	group := map[string]any{"hooks": []any{cmd}}
	if h.Matcher != "" {
		group["matcher"] = h.Matcher
	}
	return group
}

// hookIndex finds the list item that runs h's command under h's matcher, or -1.
func hookIndex(list []any, kind string, h HookSpec) int {
	for i, item := range list {
		m, _ := item.(map[string]any)
		if matcher, _ := m["matcher"].(string); matcher != h.Matcher {
			continue
		}
		if kind == "cursor-hook" {
			if m["command"] == h.Command {
				return i
			}
			continue
		}
		entries, _ := m["hooks"].([]any)
		for _, e := range entries {
			if cmd, _ := e.(map[string]any); cmd["command"] == h.Command {
				return i
			}
		}
	}
	return -1
}

// removeJSON takes one entry back out of doc; false when it is no longer there as we added it.
func removeJSON(doc map[string]any, e Entry) bool {
	switch e.Kind {
	case "hook", "cursor-hook":
		hooks, _ := doc["hooks"].(map[string]any)
		list, _ := hooks[e.Event].([]any)
		h := HookSpec{Event: e.Event, Matcher: e.Matcher, Command: e.Command}
		i := hookIndex(list, e.Kind, h)
		if i < 0 {
			return false
		}
		if e.Kind == "hook" {
			group := list[i].(map[string]any)
			var keep []any
			for _, c := range group["hooks"].([]any) {
				if cmd, _ := c.(map[string]any); cmd["command"] != e.Command {
					keep = append(keep, c)
				}
			}
			if len(keep) > 0 {
				group["hooks"] = keep
				return true
			}
		}
		list = append(list[:i:i], list[i+1:]...)
		if len(list) == 0 {
			delete(hooks, e.Event)
		} else {
			hooks[e.Event] = list
		}
		return true
	case "json-key":
		parent := doc
		for _, k := range e.Key[:len(e.Key)-1] {
			if parent, _ = parent[k].(map[string]any); parent == nil {
				return false
			}
		}
		leaf := e.Key[len(e.Key)-1]
		if have, ok := parent[leaf]; !ok || !jsonEqual(have, e.Value) {
			return false
		}
		delete(parent, leaf)
		return true
	case "json-item":
		list, _ := doc[e.Key[0]].([]any)
		var want any
		_ = json.Unmarshal(e.Value, &want)
		for i, have := range list {
			if jsonEqual(have, want) {
				doc[e.Key[0]] = append(list[:i:i], list[i+1:]...)
				return true
			}
		}
	}
	return false
}

// ---- TOML tables (Codex config.toml): appended as text, so the owner's comments and order survive ----

var tomlHeader = regexp.MustCompile(`^\s*\[\[?\s*([^\[\]]+?)\s*\]\]?\s*(#.*)?$`)

// tomlTableName normalises `["mcp_servers"."brain"]` and `[mcp_servers.brain]` to mcp_servers.brain.
func tomlTableName(line string) (string, bool) {
	m := tomlHeader.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	parts := strings.Split(m[1], ".")
	for i, p := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(p), `"'`)
	}
	return strings.Join(parts, "."), true
}

func tomlHasTable(text, table string) bool {
	for _, line := range strings.Split(text, "\n") {
		if name, ok := tomlTableName(line); ok && name == table {
			return true
		}
	}
	return false
}

func tomlAppendTable(text, table, body string) string {
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if text != "" {
		text += "\n"
	}
	return text + "[" + table + "]\n" + body
}

// tomlRemoveTable drops the table and its sub-tables in whichever spelling Codex rewrote them to.
func tomlRemoveTable(text, table string) (string, bool) {
	var out []string
	skipping, removed := false, false
	for _, line := range strings.Split(text, "\n") {
		if name, ok := tomlTableName(line); ok {
			skipping = name == table || strings.HasPrefix(name, table+".")
			if skipping {
				removed = true
				for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
					out = out[:len(out)-1]
				}
			}
		}
		if !skipping {
			out = append(out, line)
		}
	}
	result := strings.Join(out, "\n")
	if removed && result != "" && !strings.HasSuffix(result, "\n") {
		result += "\n"
	}
	return result, removed
}

// ---- global git ignore lines ----

// mergeIgnore drops the exact lines in drop and appends missing patterns; it reports each change as +line / -line.
func mergeIgnore(text string, drop, patterns []string) (string, []string) {
	dropSet := map[string]bool{}
	for _, d := range drop {
		dropSet[d] = true
	}
	have := map[string]bool{}
	var kept, changes []string
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if dropSet[strings.TrimSpace(line)] {
			changes = append(changes, "-"+strings.TrimSpace(line))
			continue
		}
		kept = append(kept, line)
		have[strings.TrimSpace(line)] = true
	}
	out := strings.Join(kept, "\n")
	if out != "" {
		out += "\n"
	}
	for _, p := range patterns {
		if !have[p] {
			out += p + "\n"
			changes = append(changes, "+"+p)
		}
	}
	return out, changes
}

func removeIgnoreLine(text, line string) (string, bool) {
	var kept []string
	removed := false
	for _, l := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if strings.TrimSpace(l) == line && !removed {
			removed = true
			continue
		}
		kept = append(kept, l)
	}
	out := strings.Join(kept, "\n")
	if out != "" {
		out += "\n"
	}
	return out, removed
}

func readOptional(path string) ([]byte, bool, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return raw, err == nil, err
}
