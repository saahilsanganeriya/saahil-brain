package lint

import (
	"fmt"
	"sort"
	"strings"

	"saahil.local/brain/internal/delta"
)

const maxHubDepth = 3

type fact struct {
	path, typ, status string
	broader, supers   []string
}

// graphFacts gathers notes and hubs during the walk for the rules that span files.
type graphFacts map[string]fact

func (g graphFacts) collect(rel string, data []byte) {
	if k := Kind(rel); k != "note" && k != "hub" {
		return
	}
	n, err := delta.Parse(data)
	if err != nil || n.ID == "" {
		return
	}
	g[n.ID] = fact{path: rel, typ: n.Type, status: n.Status, broader: n.Broader, supers: n.Supersedes}
}

func (g graphFacts) check() []Issue {
	var out []Issue
	add := func(id, rule, format string, args ...any) {
		out = append(out, Issue{Path: g[id].path, Rule: rule, Sev: Error, Msg: fmt.Sprintf(format, args...)})
	}
	ids := make([]string, 0, len(g))
	for id := range g {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	hubEdges := func(id string) []string {
		if g[id].typ != "hub" {
			return nil
		}
		return g[id].broader
	}
	for _, cyc := range cycles(ids, hubEdges) {
		add(cyc[0], "broader-cycle", "broader loops: %s", strings.Join(cyc, " → "))
	}
	for _, id := range ids {
		if g[id].typ != "hub" || id == "index" {
			continue
		}
		if d := g.hubDepth(id, map[string]bool{}); d > maxHubDepth && d < 1<<20 {
			add(id, "broader-depth", "hub sits %d levels below index (≤ %d)", d, maxHubDepth)
		}
	}

	noteEdges := func(id string) []string { return g[id].supers }
	for _, cyc := range cycles(ids, noteEdges) {
		add(cyc[0], "supersedes-cycle", "supersedes loops: %s", strings.Join(cyc, " → "))
	}
	// one question = one connected group of the supersedes graph; it has one current answer
	for _, group := range components(ids, noteEdges) {
		var current []string
		for _, id := range group {
			if g[id].status == "current" {
				current = append(current, id)
			}
		}
		if len(current) > 1 {
			add(current[0], "one-current", "%d current notes answer one question: %s", len(current), strings.Join(current, ", "))
		}
	}
	return out
}

// hubDepth is the shortest broader path to index; unreachable hubs report a huge depth (the cycle or missing
// broader is reported on its own).
func (g graphFacts) hubDepth(id string, seen map[string]bool) int {
	if id == "index" {
		return 0
	}
	if seen[id] {
		return 1 << 20
	}
	seen[id] = true
	best := 1 << 20
	for _, b := range g[id].broader {
		if _, ok := g[b]; ok {
			best = min(best, 1+g.hubDepth(b, seen))
		}
	}
	delete(seen, id)
	return best
}

// cycles finds each loop once (by DFS colouring), listed from its smallest id.
func cycles(ids []string, next func(string) []string) [][]string {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var stack []string
	var out [][]string
	var visit func(string)
	visit = func(u string) {
		color[u] = grey
		stack = append(stack, u)
		for _, v := range next(u) {
			switch color[v] {
			case white:
				if contains(ids, v) {
					visit(v)
				}
			case grey:
				i := len(stack) - 1
				for stack[i] != v {
					i--
				}
				cyc := append([]string(nil), stack[i:]...)
				out = append(out, append(cyc, v))
			}
		}
		stack = stack[:len(stack)-1]
		color[u] = black
	}
	for _, id := range ids {
		if color[id] == white {
			visit(id)
		}
	}
	return out
}

// components groups ids joined by next in either direction; singletons are left out.
func components(ids []string, next func(string) []string) [][]string {
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if p, ok := parent[x]; ok && p != x {
			parent[x] = find(p)
			return parent[x]
		}
		parent[x] = x
		return x
	}
	for _, id := range ids {
		for _, v := range next(id) {
			if contains(ids, v) {
				parent[find(id)] = find(v)
			}
		}
	}
	groups := map[string][]string{}
	for id := range parent {
		r := find(id)
		groups[r] = append(groups[r], id)
	}
	var out [][]string
	for _, grp := range groups {
		if len(grp) > 1 {
			sort.Strings(grp)
			out = append(out, grp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

func contains(sorted []string, v string) bool {
	i := sort.SearchStrings(sorted, v)
	return i < len(sorted) && sorted[i] == v
}
