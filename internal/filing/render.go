package filing

import (
	"os"
	"path/filepath"
	"slices"
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
	out, err := MemoryIndexText(root, nil)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(root, "memory", "MEMORY.md"), []byte(out))
}

// MemoryIndexText is MEMORY.md's text over the notes and hubs keep accepts by brain path (notes/<id>.md,
// hubs/<id>.md); nil keeps all. Evals render it as of a past session's start with the same order and caps.
func MemoryIndexText(root string, keep func(brainPath string) bool) (string, error) {
	kept := func(dir, id string) bool { return keep == nil || keep(dir+"/"+id+".md") }
	hubs, err := Hubs(root)
	if err != nil {
		return "", err
	}
	hubs = slices.DeleteFunc(hubs, func(h HubEntry) bool { return !kept("hubs", h.ID) })
	notes, err := readNotes(filepath.Join(root, "notes"))
	if err != nil {
		return "", err
	}
	var facts []delta.Note
	for _, n := range notes {
		if n.Scope == "global" && n.Status == "current" && kept("notes", n.ID) {
			facts = append(facts, n)
		}
	}
	prio := indexPriority(root)
	if prio == nil {
		prio = map[string]int{}
	}
	// a note that supersedes one the owner's index ranked takes that place (the old one is no longer current)
	for _, n := range facts {
		for _, old := range n.Supersedes {
			if p, ok := prio[old]; ok {
				if have, set := prio[n.ID]; !set || p < have {
					prio[n.ID] = p
				}
			}
		}
	}
	sort.SliceStable(facts, func(i, j int) bool {
		pi, iok := prio[facts[i].ID]
		pj, jok := prio[facts[j].ID]
		if iok != jok {
			return iok // a note filed from a memory the owner's own index listed comes first
		}
		if iok && pi != pj {
			return pi < pj
		}
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
	return header + "\n## Facts and rules\n" + strings.Join(factLines, "\n") + "\n\n## Hubs\n" + strings.Join(hubLines, "\n") + "\n", nil
}

func rank(t string) int {
	if r, ok := typeRank[t]; ok {
		return r
	}
	return len(typeRank)
}

// indexPriority ranks notes by where the owner's old MEMORY.md indexes (.import/<machine>/<project>/MEMORY.md) listed
// the memory they were filed from: the "Standing rules" section first, then everything else, each in index order. It
// is the owner's own ordering of what matters, so the baseline keeps it.
func indexPriority(root string) map[string]int {
	byFile := map[string]int{}
	indexes, _ := filepath.Glob(filepath.Join(root, ".import", "*", "*", "MEMORY.md"))
	sort.Strings(indexes)
	for _, idx := range indexes {
		raw, err := os.ReadFile(idx)
		if err != nil {
			continue
		}
		section, pos := 1, 0
		for _, line := range strings.Split(string(raw), "\n") {
			lower := strings.ToLower(strings.TrimSpace(line))
			if !strings.HasPrefix(lower, "- ") && lower != "" {
				section = 1
				if strings.Contains(lower, "standing rule") || strings.Contains(lower, "read first") {
					section = 0
				}
				continue
			}
			for _, m := range indexLinkRE.FindAllStringSubmatch(line, -1) {
				file := filepath.Base(m[1])
				score := section*100000 + pos
				if old, ok := byFile[file]; !ok || score < old {
					byFile[file] = score
				}
				pos++
			}
		}
	}
	st, err := loadState(root)
	if err != nil {
		return nil
	}
	out := map[string]int{}
	for _, f := range st.Filed {
		score, ok := byFile[filepath.Base(f.Path)]
		if !ok {
			continue
		}
		for _, id := range f.Notes {
			if old, seen := out[id]; !seen || score < old {
				out[id] = score
			}
		}
	}
	return out
}
