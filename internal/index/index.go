// Package index is the facade the rest of brain calls: open the index, update it, search, show, walk the graph.
package index

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/graph"
	"saahil.local/brain/internal/rank"
	"saahil.local/brain/internal/scan"
	"saahil.local/brain/internal/store"
)

type Index struct {
	Root  string
	Roots []scan.Root
	st    *store.Store
}

type (
	Hit        = rank.Hit
	SearchOpts = rank.Opts
)

type Stats struct {
	graph.Stats
	PageRankIters int
}

// Open opens (creating if needed) the index of the brain at root, reading the default roots.
func Open(root string) (*Index, error) {
	st, err := store.Open(store.Path(root))
	if err != nil {
		return nil, err
	}
	return &Index{Root: root, Roots: scan.DefaultRoots(root), st: st}, nil
}

func (ix *Index) Close() error { return ix.st.Close() }

func (ix *Index) DB() *sql.DB { return ix.st.DB }

// Update scans the roots (only changed files unless full) and recomputes global PageRank.
func (ix *Index) Update(ctx context.Context, full bool) (Stats, error) {
	gs, err := graph.Update(ctx, ix.st.DB, ix.Roots, full)
	if err != nil {
		return Stats{Stats: gs}, err
	}
	if !gs.GraphChanged {
		return Stats{Stats: gs}, nil
	}
	iters, err := rank.UpdatePageRank(ctx, ix.st.DB)
	if err == nil && full {
		err = ix.st.Compact()
	}
	return Stats{Stats: gs, PageRankIters: iters}, err
}

func (ix *Index) Search(q string, opts SearchOpts) ([]Hit, error) {
	return rank.Search(ix.st.DB, q, opts)
}

type Edge struct{ Src, Dst, Rel, Why string }

type Node struct {
	ID, Kind, Type, Path, Title, Summary, Status, Hubs, Scope, AsOf, Updated, SHA256, CanonicalOf string
	Size, MTime                                                                                   int64
	PageRank                                                                                      float64
	Out, In                                                                                       []Edge
}

type Section struct {
	Idx                       int
	Heading                   string
	LineStart, LineEnd, Bytes int
	Summary                   string
}

var ErrNotFound = errors.New("not in the index")

// Resolve turns an id, a node path or an absolute file path (either home spelling) into a node id.
func (ix *Index) Resolve(ref string) (string, error) {
	cands := []string{ref}
	if filepath.IsAbs(ref) {
		if p, ok := scan.Presented(ix.Roots, ref); ok {
			cands = []string{graph.ID(p), p}
		}
	} else {
		cands = append(cands, graph.ID(strings.TrimPrefix(ref, "./")))
	}
	for _, c := range cands {
		var id string
		if err := ix.st.DB.QueryRow(`SELECT id FROM node WHERE id = ? OR (path = ? AND kind != 'section') LIMIT 1`, c, c).Scan(&id); err == nil {
			return id, nil
		}
	}
	return "", ErrNotFound
}

func (ix *Index) Show(ref string) (Node, error) {
	id, err := ix.Resolve(ref)
	if err != nil {
		return Node{}, err
	}
	var n Node
	err = ix.st.DB.QueryRow(`SELECT id, kind, type, path, title, summary, status, hubs, scope, as_of, updated, sha256,
		canonical_of, size, mtime, pagerank FROM node WHERE id = ?`, id).Scan(&n.ID, &n.Kind, &n.Type, &n.Path, &n.Title,
		&n.Summary, &n.Status, &n.Hubs, &n.Scope, &n.AsOf, &n.Updated, &n.SHA256, &n.CanonicalOf, &n.Size, &n.MTime, &n.PageRank)
	if err != nil {
		return n, err
	}
	if n.Out, err = ix.edges(`SELECT src, dst, rel, why FROM edge WHERE src = ? AND rel != 'section_of' ORDER BY rel, dst LIMIT 500`, id); err != nil {
		return n, err
	}
	n.In, err = ix.edges(`SELECT src, dst, rel, why FROM edge WHERE dst = ? AND rel NOT IN ('part_of', 'section_of') ORDER BY rel, src LIMIT 500`, id)
	return n, err
}

func (ix *Index) edges(q, id string) ([]Edge, error) {
	rows, err := ix.st.DB.Query(q, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Edge
	for rows.Next() {
		var e Edge
		if err := rows.Scan(&e.Src, &e.Dst, &e.Rel, &e.Why); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (ix *Index) Sections(ref string) ([]Section, error) {
	id, err := ix.Resolve(ref)
	if err != nil {
		return nil, err
	}
	rows, err := ix.st.DB.Query(`SELECT idx, heading, line_start, line_end, bytes, summary FROM section WHERE doc = ? ORDER BY idx`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Section
	for rows.Next() {
		var s Section
		if err := rows.Scan(&s.Idx, &s.Heading, &s.LineStart, &s.LineEnd, &s.Bytes, &s.Summary); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

type Neighbor struct {
	ID, Rel, Via, Title string
	Depth               int
	Outgoing            bool
}

const maxNeighbors = 200

// Neighbors walks edges both ways breadth-first up to depth, returning at most 200 nodes.
func (ix *Index) Neighbors(ref string, depth int) ([]Neighbor, error) {
	id, err := ix.Resolve(ref)
	if err != nil {
		return nil, err
	}
	seen, frontier, out := map[string]bool{id: true}, []string{id}, []Neighbor{}
	for d := 1; d <= depth && len(frontier) > 0 && len(out) < maxNeighbors; d++ {
		var next []string
		for _, cur := range frontier {
			rows, err := ix.st.DB.Query(`SELECT e.dst, e.rel, 1, coalesce(n.title, '') FROM edge e LEFT JOIN node n ON n.id = e.dst WHERE e.src = ?
				UNION ALL SELECT e.src, e.rel, 0, coalesce(n.title, '') FROM edge e LEFT JOIN node n ON n.id = e.src WHERE e.dst = ? LIMIT 1000`, cur, cur)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var nb Neighbor
				if err := rows.Scan(&nb.ID, &nb.Rel, &nb.Outgoing, &nb.Title); err != nil {
					rows.Close()
					return nil, err
				}
				if seen[nb.ID] || len(out) >= maxNeighbors {
					continue
				}
				seen[nb.ID] = true
				nb.Depth, nb.Via = d, cur
				out = append(out, nb)
				next = append(next, nb.ID)
			}
			rows.Close()
		}
		frontier = next
	}
	return out, nil
}

// RecomputePageRank refreshes global PageRank without rescanning.
func (ix *Index) RecomputePageRank(ctx context.Context) (int, error) {
	return rank.UpdatePageRank(ctx, ix.st.DB)
}

// DefaultRoot is the brain this machine indexes.
func DefaultRoot() string { return config.BrainRoot() }
