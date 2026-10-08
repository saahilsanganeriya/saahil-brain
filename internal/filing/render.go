package filing

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/delta"
)

type HubEntry struct {
	ID      string   `json:"id"`
	Summary string   `json:"summary"`
	Aliases []string `json:"aliases,omitempty"`
}

// Hubs lists the live hubs, the only ids a skill may put in a note's hubs field.
func Hubs(root string) ([]HubEntry, error) {
	notes, err := readNotes(filepath.Join(root, "hubs"))
	var out []HubEntry
	for _, n := range notes {
		if n.Type == "hub" && n.Status == "current" {
			out = append(out, HubEntry{n.ID, n.Summary, n.Aliases})
		}
	}
	return out, err
}

func readNotes(dir string) ([]delta.Note, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var out []delta.Note
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		if n, err := delta.Parse(b); err == nil { // a broken file is lint's to report, not the renderer's
			out = append(out, n)
		}
	}
	return out, nil
}

const (
	maxFactLines = 120
	maxHubLines  = 30
)

// procedural rules first: they change what an agent does next
var typeRank = map[string]int{"anti-pattern": 0, "lesson": 1, "playbook": 2, "decision": 3, "finding": 4}

// RenderMemoryIndex overwrites memory/MEMORY.md, the baseline Claude Code loads in every repo: the current global
// notes as one-line facts (the summary itself, then its link) and the hubs as entry points, within 200 lines / 25 KB.
func RenderMemoryIndex(root string) error {
	hubs, err := Hubs(root)
	if err != nil {
		return err
	}
	notes, err := readNotes(filepath.Join(root, "notes"))
	if err != nil {
		return err
	}
	var facts []delta.Note
	for _, n := range notes {
		if n.Scope == "global" && n.Status == "current" {
			facts = append(facts, n)
		}
	}
	sort.SliceStable(facts, func(i, j int) bool {
		ri, rj := rank(facts[i].Type), rank(facts[j].Type)
		if ri != rj {
			return ri < rj
		}
		if facts[i].AsOf != facts[j].AsOf {
			return facts[i].AsOf > facts[j].AsOf
		}
		return facts[i].ID < facts[j].ID
	})

	header := "# Memory — the brain's global baseline\n\nRendered by the dream from notes/ (scope: global) and hubs/; " +
		"edit those, never this file. Each line is the fact; the link holds the detail.\n"
	var hubLines, factLines []string
	for _, h := range hubs[:min(len(hubs), maxHubLines)] {
		hubLines = append(hubLines, "- "+h.Summary+" — hub ["+h.ID+"](../hubs/"+h.ID+".md)")
	}
	used := len(header) + len("\n## Hubs\n") + len("\n## Facts and rules\n") + len(strings.Join(hubLines, "\n")) + 1
	lines := strings.Count(header, "\n") + 4 + len(hubLines)
	for _, n := range facts {
		line := "- " + n.Summary + " — [" + n.ID + "](../notes/" + n.ID + ".md)"
		if len(factLines) == maxFactLines || lines+1 > config.MemoryIndexMaxLines ||
			used+len(line)+1 > config.MemoryIndexMaxBytes {
			break
		}
		factLines, used, lines = append(factLines, line), used+len(line)+1, lines+1
	}
	out := header + "\n## Facts and rules\n" + strings.Join(factLines, "\n") + "\n\n## Hubs\n" + strings.Join(hubLines, "\n") + "\n"
	return writeAtomic(filepath.Join(root, "memory", "MEMORY.md"), []byte(out))
}

func rank(t string) int {
	if r, ok := typeRank[t]; ok {
		return r
	}
	return len(typeRank)
}
