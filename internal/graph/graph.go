// Package graph turns scanned files into index rows: nodes, sections, full-text rows and the derived edges
// (part_of, section_of, copy_of, version_of, links_to, mentions) plus the authored ones from frontmatter.
// Updates are incremental by (mtime, size), then sha256; an incremental run must equal a full rebuild.
package graph

import (
	"context"
	"database/sql"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/parse"
	"saahil.local/brain/internal/scan"
	"saahil.local/brain/internal/store"
)

const ftsBodyMax = 32 << 10

// fileKinds are the node kinds that come from one walked path; the rest are derived by the global passes.
const fileKinds = `('doc', 'artifact', 'link', 'skip')`

type Stats struct {
	Walked, Changed, Deleted, Ignored int
	GraphChanged                      bool // a node, edge or mtime was written; false means derived data still holds
	Took                              time.Duration
}

type nodeRow struct {
	id, kind, typ, path, title, summary, status, hubs, scope, asOf, updated, sha string
	size, mtime                                                                  int64
}

type edgeRow struct{ src, dst, rel, why string }

type ftsRow struct{ id, title, aliases, summary, body string }

type result struct {
	node      nodeRow
	edges     []edgeRow
	sections  []parse.Section
	fts       []ftsRow
	touchOnly bool // same bytes, new mtime
	known     bool // the path was indexed before, so its old rows must go
}

type prevFile struct {
	mtime, size int64
	sha         string
}

// ID is a node's id: a note or hub is known by its file name, everything else by its path.
func ID(p string) string {
	if (strings.HasPrefix(p, "notes/") || strings.HasPrefix(p, "hubs/")) && strings.Count(p, "/") == 1 &&
		strings.HasSuffix(p, ".md") {
		return strings.TrimSuffix(path.Base(p), ".md")
	}
	return p
}

// Update brings the index at db up to date with roots. full ignores what is already indexed.
func Update(ctx context.Context, db *sql.DB, roots []scan.Root, full bool) (Stats, error) {
	start := time.Now()
	var st Stats
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return st, err
	}
	defer tx.Rollback()
	if full {
		for _, q := range []string{`DELETE FROM node`, `DELETE FROM edge`, `DELETE FROM section`, `DROP TABLE fts`, store.FTSSchema} {
			if _, err := tx.Exec(q); err != nil {
				return st, err
			}
		}
	}
	prev, err := loadPrev(tx)
	if err != nil {
		return st, err
	}
	w, err := newWriter(tx)
	if err != nil {
		return st, err
	}
	defer w.close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs, results := make(chan scan.Entry, 256), make(chan result, 256)
	seen := map[string]bool{}
	var walkErr error
	var walkStats scan.Stats
	go func() {
		defer close(jobs)
		walkStats, walkErr = scan.Walk(ctx, roots, func(e scan.Entry) error {
			seen[e.Path] = true
			if p, ok := prev[e.Path]; ok && p.mtime == e.MTime && p.size == e.Size {
				return nil
			}
			select {
			case jobs <- e:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	var wg sync.WaitGroup
	for range min(8, runtime.NumCPU()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range jobs {
				p, known := prev[e.Path]
				r := process(e, p, roots)
				r.known = known
				results <- r
			}
		}()
	}
	go func() { wg.Wait(); close(results) }()

	var writeErr error
	for r := range results {
		if writeErr == nil {
			if writeErr = w.write(r); writeErr != nil {
				cancel()
			}
			st.Changed++
		}
	}
	if writeErr != nil {
		return st, writeErr
	}
	if walkErr != nil {
		return st, walkErr
	}
	st.Walked, st.Ignored = len(seen), walkStats.Ignored
	for p := range prev {
		if !seen[p] {
			if err := w.remove(ID(p), p); err != nil {
				return st, err
			}
			st.Deleted++
		}
	}
	st.GraphChanged = full || st.Changed > 0 || st.Deleted > 0
	if st.GraphChanged {
		if err := derive(tx); err != nil {
			return st, err
		}
	}
	st.Took = time.Since(start)
	if _, err := tx.Exec(`INSERT OR REPLACE INTO meta(key, value) VALUES ('run.updated_at', ?)`,
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		return st, err
	}
	return st, tx.Commit()
}

func loadPrev(tx *sql.Tx) (map[string]prevFile, error) {
	rows, err := tx.Query(`SELECT path, mtime, size, sha256 FROM node WHERE kind IN ` + fileKinds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	prev := map[string]prevFile{}
	for rows.Next() {
		var p string
		var f prevFile
		if err := rows.Scan(&p, &f.mtime, &f.size, &f.sha); err != nil {
			return nil, err
		}
		prev[p] = f
	}
	return prev, rows.Err()
}

// ---- one file → rows ----

func process(e scan.Entry, prev prevFile, roots []scan.Root) result {
	id := ID(e.Path)
	n := nodeRow{id: id, path: e.Path, title: path.Base(e.Path), size: e.Size, mtime: e.MTime}
	switch e.Kind {
	case scan.Link:
		n.kind, n.typ, n.summary = "link", "link", "→ "+e.Target
		return result{node: n, edges: []edgeRow{{src: id, dst: ID(e.Target), rel: "links_to"}}}
	case scan.Skip:
		n.kind, n.typ, n.summary = "skip", e.Reason, e.Reason
		return result{node: n}
	case scan.Bulk:
		n.kind, n.typ, n.summary = "artifact", "bulk", scan.BulkShape(e)
		if e.Class == "repo" {
			n.typ = "repo"
		}
		return result{node: n, edges: partOf(id, e.Path), fts: []ftsRow{{id: id, title: n.title, summary: n.summary}}}
	}
	c := scan.Load(e)
	if c.Skip != "" {
		n.kind, n.typ, n.summary = "skip", c.Skip, c.Skip
		return result{node: n}
	}
	n.sha = c.SHA
	if prev.sha != "" && prev.sha == c.SHA {
		return result{node: n, touchOnly: true}
	}
	if e.Class == "data" {
		n.kind, n.typ, n.summary = "artifact", "artifact", parse.Shape(e.Path, c.Data)
		return result{node: n, edges: partOf(id, e.Path), fts: []ftsRow{{id: id, title: n.title, summary: n.summary}}}
	}
	var d parse.Doc
	if e.Class == "html" {
		d = parse.HTML(c.Data)
	} else {
		d = parse.Markdown(c.Data)
	}
	n.kind, n.typ = "doc", docType(e.Path, d.Type)
	if d.Title != "" {
		n.title = d.Title
	}
	n.summary, n.status, n.scope, n.asOf, n.updated = d.Summary, d.Status, d.Scope, d.AsOf, d.Updated
	n.hubs = strings.Join(d.Hubs, ",")
	r := result{node: n, edges: partOf(id, e.Path)}
	for _, a := range d.Authored {
		if dst := refID(a.Dst); dst != "" {
			r.edges = append(r.edges, edgeRow{src: id, dst: dst, rel: a.Rel, why: a.Why})
		}
	}
	for _, l := range d.Links {
		if dst := linkID(e.Path, l, roots); dst != "" && dst != id {
			r.edges = append(r.edges, edgeRow{src: id, dst: dst, rel: "links_to"})
		}
	}
	for _, m := range d.Mentions {
		r.edges = append(r.edges, edgeRow{src: id, dst: m, rel: "mentions"})
	}
	aliases := strings.Join(d.Aliases, " ")
	body := d.Body
	if e.Class == "md" && e.Size > config.BigDocBytes {
		r.sections = d.Sections()
		var heads []string
		for i, s := range r.sections {
			sid := sectionID(id, i)
			r.edges = append(r.edges, edgeRow{src: sid, dst: id, rel: "section_of"})
			r.fts = append(r.fts, ftsRow{id: sid, title: sectionTitle(n.title, s.Heading), summary: s.Summary, body: capText(s.Text)})
			heads = append(heads, s.Heading)
		}
		body = strings.Join(heads, "\n")
	}
	r.fts = append([]ftsRow{{id: id, title: n.title, aliases: aliases, summary: n.summary, body: capText(body)}}, r.fts...)
	return r
}

func sectionTitle(doc, heading string) string {
	if heading == doc {
		return doc
	}
	return doc + " › " + heading
}

func sectionID(doc string, idx int) string { return doc + "#" + strconv.Itoa(idx) }

func capText(s string) string {
	if len(s) <= ftsBodyMax {
		return s
	}
	cut := ftsBodyMax
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}

func partOf(id, p string) []edgeRow {
	if dir := path.Dir(p); dir != "." && dir != "/" {
		return []edgeRow{{src: id, dst: dir, rel: "part_of"}}
	}
	return nil
}

// refID resolves an authored target: a path (has a slash) → its node id, anything else is an id already.
func refID(ref string) string {
	ref, _, _ = strings.Cut(strings.TrimSpace(ref), "#")
	if strings.Contains(ref, "/") {
		return ID(path.Clean(ref))
	}
	return ref
}

// linkID resolves a link written in the file at src to a node id; unresolvable targets keep their text so the
// dangling count can see them.
func linkID(src string, l parse.Link, roots []scan.Root) string {
	t := l.Target
	if l.Wiki {
		if strings.Contains(t, "/") {
			return refID(t)
		}
		// Claude memory links name a file in the same memory folder
		if strings.HasPrefix(src, "memory-import/") || strings.HasPrefix(src, "memory/") {
			return path.Join(path.Dir(src), t+".md")
		}
		return t
	}
	if strings.HasPrefix(t, "/") {
		if p, ok := scan.Presented(roots, t); ok {
			return ID(p)
		}
		return config.Canonical(t)
	}
	return ID(path.Join(path.Dir(src), t))
}

func docType(p, declared string) string {
	if declared != "" {
		return declared
	}
	base := strings.ToUpper(path.Base(p))
	switch {
	case strings.HasPrefix(p, "memory/") || strings.HasPrefix(p, "memory-import/"):
		return "memory"
	case strings.HasPrefix(p, "hubs/"):
		return "hub"
	case strings.HasPrefix(p, "sessions/"):
		return "session"
	case strings.HasPrefix(p, "journal/"):
		return "day"
	case base == "CARD.MD":
		return "card"
	case base == "STATE.MD":
		return "state"
	}
	for _, k := range []string{"SPEC", "PLAN", "REPORT", "REVIEW", "HANDOFF", "README", "DESIGN"} {
		if strings.HasPrefix(base, k) {
			if k == "DESIGN" {
				return "spec"
			}
			return strings.ToLower(k)
		}
	}
	return "doc"
}

// ---- writer: the single goroutine that touches the transaction ----

type writer struct {
	tx                                                     *sql.Tx
	node, touch, edge, section, fts, delFTS, delNodes, rid *sql.Stmt
	delEdges, delSections                                  *sql.Stmt
}

func newWriter(tx *sql.Tx) (*writer, error) {
	w := &writer{tx: tx}
	stmts := []struct {
		dst **sql.Stmt
		sql string
	}{
		{&w.node, `INSERT INTO node(id, kind, type, path, title, summary, status, hubs, scope, as_of, updated, size, sha256, mtime)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET kind=excluded.kind, type=excluded.type,
			path=excluded.path, title=excluded.title, summary=excluded.summary, status=excluded.status, hubs=excluded.hubs,
			scope=excluded.scope, as_of=excluded.as_of, updated=excluded.updated, size=excluded.size,
			sha256=excluded.sha256, mtime=excluded.mtime`},
		{&w.touch, `UPDATE node SET mtime = ?, size = ? WHERE id = ?`},
		{&w.edge, `INSERT OR IGNORE INTO edge(src, dst, rel, why, source) VALUES (?,?,?,?,?)`},
		{&w.section, `INSERT INTO section(doc, idx, heading, line_start, line_end, bytes, summary) VALUES (?,?,?,?,?,?,?)`},
		{&w.fts, `INSERT INTO fts(rowid, id, title, aliases, summary, body) VALUES (?,?,?,?,?,?)`},
		{&w.rid, `SELECT rowid FROM node WHERE id = ?`},
		{&w.delFTS, `DELETE FROM fts WHERE rowid IN (SELECT rowid FROM node WHERE id = ? OR (kind = 'section' AND path = ?))`},
		{&w.delNodes, `DELETE FROM node WHERE kind = 'section' AND path = ?`},
		{&w.delEdges, `DELETE FROM edge WHERE source = ?`},
		{&w.delSections, `DELETE FROM section WHERE doc = ?`},
	}
	for _, s := range stmts {
		st, err := tx.Prepare(s.sql)
		if err != nil {
			return nil, err
		}
		*s.dst = st
	}
	return w, nil
}

func (w *writer) close() {
	for _, s := range []*sql.Stmt{w.node, w.touch, w.edge, w.section, w.fts, w.rid, w.delFTS, w.delNodes, w.delEdges, w.delSections} {
		if s != nil {
			s.Close()
		}
	}
}

// clear drops what a file contributed (its sections, their fts rows, its edges) but keeps its node row.
func (w *writer) clear(id, p string) error {
	for _, step := range []struct {
		st   *sql.Stmt
		args []any
	}{{w.delFTS, []any{id, p}}, {w.delNodes, []any{p}}, {w.delEdges, []any{id}}, {w.delSections, []any{id}}} {
		if _, err := step.st.Exec(step.args...); err != nil {
			return err
		}
	}
	return nil
}

func (w *writer) remove(id, p string) error {
	if err := w.clear(id, p); err != nil {
		return err
	}
	_, err := w.tx.Exec(`DELETE FROM node WHERE id = ?`, id)
	return err
}

func (w *writer) write(r result) error {
	n := r.node
	if r.touchOnly {
		_, err := w.touch.Exec(n.mtime, n.size, n.id)
		return err
	}
	if r.known {
		if err := w.clear(n.id, n.path); err != nil {
			return err
		}
	}
	if err := w.upsert(n); err != nil {
		return err
	}
	for i, s := range r.sections {
		sid := sectionID(n.id, i)
		sec := nodeRow{id: sid, kind: "section", typ: "section", path: n.path, title: sectionTitle(n.title, s.Heading),
			summary: s.Summary, size: int64(s.Bytes)}
		if err := w.upsert(sec); err != nil {
			return err
		}
		if _, err := w.section.Exec(n.id, i, s.Heading, s.LineStart, s.LineEnd, s.Bytes, s.Summary); err != nil {
			return err
		}
	}
	for _, e := range r.edges {
		if _, err := w.edge.Exec(e.src, e.dst, e.rel, e.why, n.id); err != nil {
			return err
		}
	}
	for _, f := range r.fts {
		var rowid int64
		if err := w.rid.QueryRow(f.id).Scan(&rowid); err != nil {
			return err
		}
		if _, err := w.fts.Exec(rowid, f.id, f.title, f.aliases, f.summary, f.body); err != nil {
			return err
		}
	}
	return nil
}

func (w *writer) upsert(n nodeRow) error {
	_, err := w.node.Exec(n.id, n.kind, n.typ, n.path, n.title, n.summary, n.status, n.hubs, n.scope, n.asOf,
		n.updated, n.size, n.sha, n.mtime)
	return err
}

// ---- global passes: recomputed from the node table every run, so their result never depends on history ----

func derive(tx *sql.Tx) error {
	steps := []func(*sql.Tx) error{deriveDirs, deriveCopies, deriveVersions, deriveMentions, deriveMeta}
	for _, step := range steps {
		if err := step(tx); err != nil {
			return err
		}
	}
	return nil
}

func deriveDirs(tx *sql.Tx) error {
	want := map[string]bool{}
	rows, err := tx.Query(`SELECT path FROM node WHERE kind IN ('doc', 'artifact', 'link')`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		for d := path.Dir(p); d != "." && d != "/" && !want[d]; d = path.Dir(d) {
			want[d] = true
		}
	}
	rows.Close()
	have := map[string]bool{}
	rows, err = tx.Query(`SELECT id FROM node WHERE kind = 'dir'`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		have[id] = true
	}
	rows.Close()
	for d := range have {
		if !want[d] {
			if _, err := tx.Exec(`DELETE FROM node WHERE id = ?`, d); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM edge WHERE source = 'derived:dir' AND src = ?`, d); err != nil {
				return err
			}
		}
	}
	for d := range want {
		if have[d] {
			continue
		}
		typ := "dir"
		if strings.Count(d, "/") == 1 && strings.HasPrefix(d, "work/") {
			typ = "work"
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO node(id, kind, type, path, title) VALUES (?, 'dir', ?, ?, ?)`,
			d, typ, d, path.Base(d)); err != nil {
			return err
		}
		if parent := path.Dir(d); parent != "." {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO edge(src, dst, rel, source) VALUES (?, ?, 'part_of', 'derived:dir')`, d, parent); err != nil {
				return err
			}
		}
	}
	return nil
}

// deriveCopies keeps one canonical node per content hash: the oldest file, then the smallest id. Only rows whose
// canonical changed are written.
func deriveCopies(tx *sql.Tx) error {
	for _, q := range []string{
		`WITH g AS (SELECT id, CASE WHEN first_value(id) OVER w = id THEN '' ELSE first_value(id) OVER w END AS canon
			FROM node WHERE sha256 != '' AND size > 0 AND kind IN ('doc', 'artifact')
			WINDOW w AS (PARTITION BY kind, sha256 ORDER BY mtime, id))
		 UPDATE node SET canonical_of = g.canon FROM g WHERE node.id = g.id AND node.canonical_of != g.canon`,
		`DELETE FROM edge WHERE source = 'derived:copy'
			AND NOT EXISTS (SELECT 1 FROM node n WHERE n.id = edge.src AND n.canonical_of = edge.dst)`,
		`INSERT OR IGNORE INTO edge(src, dst, rel, source)
			SELECT id, canonical_of, 'copy_of', 'derived:copy' FROM node WHERE canonical_of != ''`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

var versionRE = regexp.MustCompile(`(?i)([-_. ]v\d+(\.\d+)*|[-_. ]?\d{4}-\d{2}-\d{2}(t\d{2,6}z?)?|[-_ ]?\(\d+\)|[-_. ](copy|old|bak|orig|prev))`)

// versionStem strips version marks from a file name: DESIGN.v2.md, DESIGN-2026-10-07.md and DESIGN (1).md all
// become design.md.
func versionStem(name string) (string, bool) {
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	stem := versionRE.ReplaceAllString(base, "")
	return strings.ToLower(stem + ext), stem != base && len(stem) >= 3
}

// deriveVersions links each older version to the newest file of its group (same folder, same stem) as version_of.
func deriveVersions(tx *sql.Tx) error {
	if _, err := tx.Exec(`DELETE FROM edge WHERE source = 'derived:version'`); err != nil {
		return err
	}
	type member struct {
		id    string
		mtime int64
	}
	groups, versioned := map[string][]member{}, map[string]bool{}
	rows, err := tx.Query(`SELECT id, path, mtime FROM node WHERE kind = 'doc' AND canonical_of = ''`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var m member
		var p string
		if err := rows.Scan(&m.id, &p, &m.mtime); err != nil {
			rows.Close()
			return err
		}
		stem, isVersioned := versionStem(path.Base(p))
		key := path.Dir(p) + "/" + stem
		groups[key] = append(groups[key], m)
		versioned[key] = versioned[key] || isVersioned
	}
	rows.Close()
	for key, ms := range groups {
		if len(ms) < 2 || !versioned[key] {
			continue
		}
		sort.Slice(ms, func(i, j int) bool {
			return ms[i].mtime > ms[j].mtime || (ms[i].mtime == ms[j].mtime && ms[i].id < ms[j].id)
		})
		for _, m := range ms[1:] {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO edge(src, dst, rel, source) VALUES (?, ?, 'version_of', 'derived:version')`,
				m.id, ms[0].id); err != nil {
				return err
			}
		}
	}
	return nil
}

func deriveMentions(tx *sql.Tx) error {
	for _, q := range []string{
		`DELETE FROM node WHERE kind IN ('pr', 'ticket', 'code') AND id NOT IN (SELECT dst FROM edge WHERE rel = 'mentions')`,
		`INSERT OR IGNORE INTO node(id, kind, type, title)
			SELECT DISTINCT dst, substr(dst, 1, instr(dst, ':') - 1), substr(dst, 1, instr(dst, ':') - 1), substr(dst, instr(dst, ':') + 1)
			FROM edge WHERE rel = 'mentions'`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// deriveMeta records link health: dangling targets the index would hold (docs, data, folders), links to files it
// never indexes (code, config), and absolute paths outside every root.
func deriveMeta(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT e.dst FROM edge e WHERE e.rel = 'links_to' AND NOT EXISTS (SELECT 1 FROM node n WHERE n.id = e.dst)`)
	if err != nil {
		return err
	}
	counts := map[string]int{"links.dangling": 0, "links.unindexed": 0, "links.external": 0}
	for rows.Next() {
		var dst string
		if err := rows.Scan(&dst); err != nil {
			rows.Close()
			return err
		}
		switch {
		case strings.HasPrefix(dst, "/"):
			counts["links.external"]++
		case path.Ext(dst) != "" && scan.ClassOf(dst) == "":
			counts["links.unindexed"]++
		default:
			counts["links.dangling"]++
		}
	}
	rows.Close()
	for k, v := range counts {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO meta(key, value) VALUES (?, ?)`, k, strconv.Itoa(v)); err != nil {
			return err
		}
	}
	return nil
}
