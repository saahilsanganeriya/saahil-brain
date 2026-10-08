// Package rank holds the brain's graph scores (global PageRank, local-push personalized PageRank) and search.
package rank

import (
	"context"
	"database/sql"
	"math"
	"strconv"
)

const (
	Damping   = 0.85
	prTol     = 1e-9
	prMaxIter = 100
	pushEps   = 1e-4 // residual per unit degree left unpushed; bounds work to ~1/(eps·(1-Damping)) pushes
)

// UpdatePageRank recomputes global PageRank over every edge (directed) and stores it on node.pagerank, rounded to
// four significant digits so an update that barely moves the graph rewrites few rows.
func UpdatePageRank(ctx context.Context, db *sql.DB) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, pagerank FROM node ORDER BY id`)
	if err != nil {
		return 0, err
	}
	var ids []string
	var old []float64
	index := map[string]int32{}
	for rows.Next() {
		var id string
		var pr float64
		if err := rows.Scan(&id, &pr); err != nil {
			rows.Close()
			return 0, err
		}
		index[id] = int32(len(ids))
		ids, old = append(ids, id), append(old, pr)
	}
	rows.Close()
	out := make([][]int32, len(ids))
	rows, err = db.QueryContext(ctx, `SELECT src, dst FROM edge`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var src, dst string
		if err := rows.Scan(&src, &dst); err != nil {
			rows.Close()
			return 0, err
		}
		s, ok1 := index[src]
		d, ok2 := index[dst]
		if ok1 && ok2 && s != d {
			out[s] = append(out[s], d)
		}
	}
	rows.Close()
	pr, iters := PageRank(out)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return iters, err
	}
	defer tx.Rollback()
	upd, err := tx.Prepare(`UPDATE node SET pagerank = ? WHERE id = ?`)
	if err != nil {
		return iters, err
	}
	defer upd.Close()
	for i, v := range pr {
		if v = round4(v); v != old[i] {
			if _, err := upd.Exec(v, ids[i]); err != nil {
				return iters, err
			}
		}
	}
	return iters, tx.Commit()
}

func round4(v float64) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'g', 4, 64), 64)
	return f
}

// PageRank runs power iteration with uniform teleport; dangling nodes spread their mass uniformly. Nodes are
// visited in index order, so the result is bit-identical for the same graph.
func PageRank(out [][]int32) ([]float64, int) {
	n := len(out)
	if n == 0 {
		return nil, 0
	}
	p, next := make([]float64, n), make([]float64, n)
	for i := range p {
		p[i] = 1 / float64(n)
	}
	iter := 0
	for iter < prMaxIter {
		iter++
		dangling := 0.0
		for i := range next {
			next[i] = 0
		}
		for u, vs := range out {
			if len(vs) == 0 {
				dangling += p[u]
				continue
			}
			share := Damping * p[u] / float64(len(vs))
			for _, v := range vs {
				next[v] += share
			}
		}
		base := (1-Damping)/float64(n) + Damping*dangling/float64(n)
		delta := 0.0
		for i := range next {
			next[i] += base
			delta += math.Abs(next[i] - p[i])
		}
		p, next = next, p
		if delta < prTol {
			break
		}
	}
	return p, iter
}

// Push is Andersen–Chung–Lang local push for personalized PageRank on an undirected graph given by neighbors
// (called once per node it expands). Teleport is 1-Damping to the normalised seeds; a node with no neighbors keeps
// the mass that reaches it. Error in L1 is at most eps times the total degree of the nodes left with residual.
func Push(neighbors func(string) []string, seeds map[string]float64, eps float64) map[string]float64 {
	alpha := 1 - Damping
	total := 0.0
	for _, w := range seeds {
		total += w
	}
	p, r := map[string]float64{}, map[string]float64{}
	adj := map[string][]string{}
	var queue []string
	queued := map[string]bool{}
	for s, w := range seeds {
		if w > 0 {
			r[s] = w / total
			queue, queued[s] = append(queue, s), true
		}
	}
	for len(queue) > 0 {
		u := queue[0]
		queue, queued[u] = queue[1:], false
		vs, ok := adj[u]
		if !ok {
			vs = neighbors(u)
			adj[u] = vs
		}
		ru := r[u]
		if ru < eps*float64(max(1, len(vs))) {
			continue
		}
		r[u] = 0
		if len(vs) == 0 {
			p[u] += ru
			continue
		}
		p[u] += alpha * ru
		share := (1 - alpha) * ru / float64(len(vs))
		for _, v := range vs {
			r[v] += share
			deg := 1
			if known, ok := adj[v]; ok {
				deg = max(1, len(known))
			}
			if !queued[v] && r[v] >= eps*float64(deg) {
				queue, queued[v] = append(queue, v), true
			}
		}
	}
	return p
}
