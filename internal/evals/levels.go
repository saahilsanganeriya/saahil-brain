package evals

import (
	"crypto/sha256"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"saahil.local/brain/internal/filing"
	"saahil.local/brain/internal/graph"
	"saahil.local/brain/internal/hooks"
	"saahil.local/brain/internal/index"
	"saahil.local/brain/internal/parse"
)

// The levels a session reaches a file through (DESIGN §5): L0 is loaded before the first prompt (MEMORY.md and the
// start digest), L1 is the prompt's pointers, L2 is what it pulls with search, show and open.
const (
	levelL0 = "L0"
	levelL1 = "L1"
	levelL2 = "L2"
)

// reach is the L0-aware navigator: a target linked from L0 or pointed at by L1 is one call away (open it); anything
// else costs what the scripted L2 navigator needs. Ties at one call go to the level the session held first.
func reach(t targetSet, l0, pointers []string, l2Calls int) (calls int, level string) {
	switch {
	case t.any(l0):
		return 1, levelL0
	case t.any(pointers):
		return 1, levelL1
	case l2Calls > 0:
		return l2Calls, levelL2
	}
	return 0, ""
}

// levelZero lists what a session that started at g.start held at L0: the ids linked from MEMORY.md as it would have
// rendered then, and from the start digest the start hook would have built for its cwd.
func (e *Env) levelZero(cwd string, g leakGuard) ([]string, error) {
	memory, err := e.memoryIndexAsOf(g)
	if err != nil {
		return nil, err
	}
	links := linkedIDs(e.Index, "memory/MEMORY.md", memory, memory)
	d := hooks.DigestAsOf(e.hooks, cwd, e.digestVisible(g))
	for i, source := range d.Sources {
		if hub, ok := strings.CutPrefix(source, "hub:"); ok {
			source = "hubs/" + hub + ".md"
		}
		links = append(links, linkedIDs(e.Index, source, d.Blocks[i], d.Text)...)
	}
	return links, nil
}

// digestVisible is the start digest's view of the brain at g.start: session notes of sessions that had ended, work
// boxes that existed, and quoted files (hot.md, STATE.md, CARD.md, hubs) only when today's text is the text then. A
// CARD or hub the dream wrote after the session started never shows.
func (e *Env) digestVisible(g leakGuard) func(string) bool {
	return func(p string) bool {
		switch {
		case strings.HasPrefix(p, "sessions/"):
			return g.exist.sessionEndedBefore(p, g.start)
		case strings.HasPrefix(p, "work/") && strings.Count(p, "/") == 1:
			return !g.hides(p)
		}
		return g.exist.unchangedBefore(p, g.start)
	}
}

// memoryIndexAsOf renders MEMORY.md with the production code over the notes and hubs that existed at g.start. Sessions
// with the same set of such notes share one rendering.
func (e *Env) memoryIndexAsOf(g leakGuard) (string, error) {
	if e.memoryFiles == nil {
		for _, dir := range []string{"notes", "hubs"} {
			paths, err := filepath.Glob(filepath.Join(e.Index.Root, dir, "*.md"))
			if err != nil {
				return "", err
			}
			for _, p := range paths {
				e.memoryFiles = append(e.memoryFiles, dir+"/"+filepath.Base(p))
			}
		}
		sort.Strings(e.memoryFiles)
	}
	kept := map[string]bool{}
	key := sha256.New()
	for _, p := range e.memoryFiles {
		if e.noteExisted(p, g) {
			kept[p] = true
			key.Write([]byte(p + "\n"))
		}
	}
	k := string(key.Sum(nil))
	if text, ok := e.memoryTexts[k]; ok {
		return text, nil
	}
	text, err := filing.MemoryIndexText(e.Index.Root, func(p string) bool { return kept[p] })
	if err == nil {
		e.memoryTexts[k] = text
	}
	return text, err
}

// noteExisted dates a note or hub (filed today, mostly) by its sources: it existed at g.start when its own file did,
// or when it has sources and every one did (a memory by its .import copy, which keeps the memory's mtime; a session
// note once its session ended). A note learned from the replayed session never existed before it.
func (e *Env) noteExisted(p string, g leakGuard) bool {
	id := graph.ID(p)
	if g.sourced[id] {
		return false
	}
	if g.exist.before(p, g.start) {
		return true
	}
	sources := e.sourcesOf()[id]
	for _, s := range sources {
		if strings.HasPrefix(s, "sessions/") {
			if !g.exist.sessionEndedBefore(s, g.start) {
				return false
			}
		} else if !g.exist.before(s, g.start) {
			return false
		}
	}
	return len(sources) > 0
}

func (e *Env) sourcesOf() map[string][]string {
	if e.sources != nil {
		return e.sources
	}
	e.sources = map[string][]string{}
	rows, err := e.Index.DB().Query(`SELECT src, dst FROM edge WHERE rel = 'sources'`)
	if err != nil {
		return e.sources
	}
	defer rows.Close()
	for rows.Next() {
		var src, dst string
		if rows.Scan(&src, &dst) == nil {
			e.sources[src] = append(e.sources[src], dst)
		}
	}
	return e.sources
}

// linkedIDs resolves the links in text, a block of the file at src, the way the index resolves a file's links. A link
// whose target the final cut of shown dropped was never seen.
func linkedIDs(ix *index.Index, src, text, shown string) []string {
	var out []string
	for _, l := range parse.Markdown([]byte(text)).Links {
		if !strings.Contains(shown, l.Target) {
			continue
		}
		var id string
		switch t := l.Target; {
		case l.Wiki && !strings.Contains(t, "/"):
			id = t
		case l.Wiki:
			id = graph.ID(path.Clean(t))
		case strings.HasPrefix(t, "/"):
			id, _ = ix.Resolve(t)
		default:
			id = graph.ID(path.Join(path.Dir(src), t))
		}
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}
