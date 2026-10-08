package doctor

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/index"
	"saahil.local/brain/internal/lint"
	"saahil.local/brain/internal/store"
)

// Graph health (DESIGN §9): measured on what the brain writes itself — notes/ and hubs/ — from the index, plus the
// caps of every baseline on disk. Quality gaps warn; broken supersession or a breached cap fails.

const authored = `('hubs', 'about', 'evidence', 'supersedes', 'sources', 'related', 'broader')`

// brainWritten selects note and hub docs; n is the node alias.
const brainWritten = `n.kind = 'doc' AND (n.path LIKE 'notes/%' OR n.path LIKE 'hubs/%')`

func init() {
	for _, c := range []struct {
		name string
		run  func(*sql.DB) (Status, string, error)
	}{
		{"graph orphans", orphans},
		{"graph dangling links", dangling},
		{"graph hub coverage", hubCoverage},
		{"graph duplicates", duplicates},
		{"graph supersession", supersession},
	} {
		Register(Check{Name: c.name, Run: func(ctx context.Context) (Status, string) {
			return withGraph(config.BrainRoot(), c.run)
		}})
	}
	Register(Check{Name: "graph caps", Run: func(context.Context) (Status, string) { return caps(config.BrainRoot()) }})
}

func withGraph(root string, run func(*sql.DB) (Status, string, error)) (Status, string) {
	if _, err := os.Stat(store.Path(root)); err != nil {
		return Skip, "no index yet (brain index builds it)"
	}
	ix, err := index.Open(root)
	if err != nil {
		return Fail, err.Error()
	}
	defer ix.Close()
	st, detail, err := run(ix.DB())
	if err != nil {
		return Fail, err.Error()
	}
	return st, detail
}

func count(db *sql.DB, q string, args ...any) (int, error) {
	var n int
	err := db.QueryRow(q, args...).Scan(&n)
	return n, err
}

func orphans(db *sql.DB) (Status, string, error) {
	total, err := count(db, `SELECT count(*) FROM node n WHERE `+brainWritten)
	if err != nil || total == 0 {
		return Skip, "no notes or hubs indexed yet", err
	}
	n, err := count(db, `SELECT count(*) FROM node n WHERE `+brainWritten+`
		AND NOT EXISTS (SELECT 1 FROM edge e WHERE e.src = n.id AND e.rel IN `+authored+`)
		AND NOT EXISTS (SELECT 1 FROM edge e WHERE e.dst = n.id AND e.rel IN `+authored+`)`)
	if err != nil {
		return Fail, "", err
	}
	return verdict(n == 0, Warn), fmt.Sprintf("%d of %d notes and hubs have no authored edge (target 0)", n, total), nil
}

// dangling counts refs from notes and hubs whose target is neither indexed nor on disk (.import/ is never indexed).
func dangling(db *sql.DB) (Status, string, error) {
	rows, err := db.Query(`SELECT e.dst FROM edge e JOIN node n ON n.id = e.src WHERE ` + brainWritten + `
		AND (e.rel = 'links_to' OR e.rel IN ` + authored + `) AND NOT EXISTS (SELECT 1 FROM node d WHERE d.id = e.dst)`)
	if err != nil {
		return Fail, "", err
	}
	defer rows.Close()
	l, n, elsewhere := lint.New(config.BrainRoot()), 0, 0
	var examples []string
	for rows.Next() {
		var dst string
		if err := rows.Scan(&dst); err != nil {
			return Fail, "", err
		}
		// an absolute path outside the brain is a mention of this machine's disk (a laptop folder, a build output),
		// not a link the brain owns; the index counts those as external
		if strings.HasPrefix(dst, "/") && !strings.HasPrefix(config.Canonical(dst), config.Canonical(config.BrainRoot())+"/") {
			continue
		}
		switch {
		case l.Resolves(dst):
		case !config.SyncCarries(dst):
			elsewhere++ // bulk or a non-text file under work/: it lives on the machine that made it, by design
		default:
			n++
			if len(examples) < 3 {
				examples = append(examples, dst)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return Fail, "", err
	}
	detail := fmt.Sprintf("%d links and refs from notes and hubs resolve to nothing (target 0)", n)
	if len(examples) > 0 {
		detail += ": " + strings.Join(examples, ", ")
	}
	if elsewhere > 0 {
		detail += fmt.Sprintf("; %d point at bulk the sync leaves on the other machine", elsewhere)
	}
	return verdict(n == 0, Warn), detail, nil
}

func hubCoverage(db *sql.DB) (Status, string, error) {
	live, err := count(db, `SELECT count(*) FROM node n WHERE n.kind = 'doc' AND n.path LIKE 'notes/%' AND n.status IN ('current', 'proposed')`)
	if err != nil || live == 0 {
		return Skip, "no live notes indexed yet", err
	}
	covered, err := count(db, `SELECT count(*) FROM node n WHERE n.kind = 'doc' AND n.path LIKE 'notes/%' AND n.status IN ('current', 'proposed')
		AND EXISTS (SELECT 1 FROM edge e JOIN node h ON h.id = e.dst AND h.type = 'hub' WHERE e.src = n.id AND e.rel = 'hubs')`)
	if err != nil {
		return Fail, "", err
	}
	pct := 100 * float64(covered) / float64(live)
	return verdict(pct >= 90, Warn), fmt.Sprintf("%.1f%% of %d live notes sit in a resolving hub (target ≥ 90%%)", pct, live), nil
}

func duplicates(db *sql.DB) (Status, string, error) {
	n, err := count(db, `SELECT count(*) FROM node n WHERE `+brainWritten+` AND n.canonical_of != ''`)
	if err != nil {
		return Fail, "", err
	}
	return verdict(n == 0, Warn), fmt.Sprintf("%d notes or hubs are byte copies of another (target 0)", n), nil
}

// supersession: every superseded target exists and is marked superseded or retired, and no chain loops.
func supersession(db *sql.DB) (Status, string, error) {
	rows, err := db.Query(`SELECT e.src, e.dst, coalesce(d.status, '') FROM edge e JOIN node n ON n.id = e.src
		LEFT JOIN node d ON d.id = e.dst WHERE ` + brainWritten + ` AND e.rel = 'supersedes'`)
	if err != nil {
		return Fail, "", err
	}
	defer rows.Close()
	next := map[string][]string{}
	total, bad := 0, 0
	for rows.Next() {
		var src, dst, status string
		if err := rows.Scan(&src, &dst, &status); err != nil {
			return Fail, "", err
		}
		total++
		if status != "superseded" && status != "retired" {
			bad++
		}
		next[src] = append(next[src], dst)
	}
	if err := rows.Err(); err != nil {
		return Fail, "", err
	}
	if total == 0 {
		return OK, "no supersedes edges yet", nil
	}
	loops := loopCount(next)
	detail := fmt.Sprintf("%d of %d supersedes edges point at a note still live or missing; %d loops (target 0, 0)", bad, total, loops)
	return verdict(bad == 0 && loops == 0, Fail), detail, nil
}

func loopCount(next map[string][]string) int {
	const (
		white = iota
		grey
		black
	)
	color, loops := map[string]int{}, 0
	var visit func(string)
	visit = func(u string) {
		color[u] = grey
		for _, v := range next[u] {
			switch color[v] {
			case white:
				visit(v)
			case grey:
				loops++
			}
		}
		color[u] = black
	}
	for u := range next {
		if color[u] == white {
			visit(u)
		}
	}
	return loops
}

// caps reads every baseline and note on disk: they are few, and the index does not keep line or word counts.
func caps(root string) (Status, string) {
	var files []string
	for _, pattern := range []string{"notes/*.md", "hubs/*.md", "memory/MEMORY.md", "hot.md", "work/*/STATE.md", "work/*/CARD.md"} {
		m, _ := filepath.Glob(filepath.Join(root, pattern))
		files = append(files, m...)
	}
	if len(files) == 0 {
		return Skip, "no notes or baselines yet"
	}
	var over []string
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, f)
		if msg := lint.Cap(filepath.ToSlash(rel), data); msg != "" {
			over = append(over, filepath.ToSlash(rel))
		}
	}
	detail := fmt.Sprintf("%d of %d baselines and notes within their caps (target 100%%)", len(files)-len(over), len(files))
	if len(over) > 0 {
		detail += ": over — " + strings.Join(over[:min(len(over), 5)], ", ")
	}
	return verdict(len(over) == 0, Fail), detail
}

func verdict(ok bool, breach Status) Status {
	if ok {
		return OK
	}
	return breach
}
