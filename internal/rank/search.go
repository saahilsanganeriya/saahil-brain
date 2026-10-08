package rank

import (
	"database/sql"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

type Opts struct {
	Limit int
	Deep  bool     // add personalized PageRank from the best matches and Seeds
	Seeds []string // node ids of the active work, hubs or notes
	Types []string // keep only these node types
}

type Hit struct {
	ID, Kind, Type, Path, Title, Summary string
	Score, Relevance, Proximity          float64
}

const (
	candidates   = 100
	ftsTop       = 300 // FTS matches considered before exclusions; copies and old versions take a share
	pprSeeds     = 10
	pprExtra     = 200 // nodes reached only through the graph that may join the candidates
	maxExpandDeg = 300 // a big folder or a much-cited PR is not expanded by the push; mass reaching it stays there
)

// Score weights; relevance and proximity are normalised to [0, 1] within one query.
const (
	wRelevance  = 1.0
	wProximity  = 0.5
	wUsefulness = 0.3
	wRecency    = 0.15
	wScope      = 0.2
	wPageRank   = 0.05
)

var typePrior = map[string]float64{
	"decision": 0.15, "finding": 0.12, "lesson": 0.12, "playbook": 0.12, "anti-pattern": 0.1, "hub": 0.1,
	"card": 0.1, "spec": 0.08, "plan": 0.06, "state": 0.08, "report": 0.05, "memory": 0.05, "section": 0.04,
	"artifact": -0.05, "bulk": -0.1, "repo": -0.1,
}

// artifactRelevance damps BM25 for data files: their few short fields (name and shape) score above whole docs.
const artifactRelevance = 0.6

// rankable joins the node (or, for a section, its document) and drops what FORMAT.md says is never ranked:
// superseded or retired, byte copies, older versions, sessions and days, logs and the archive.
const rankable = `
	LEFT JOIN edge p ON p.src = n.id AND p.rel = 'section_of'
	JOIN node x ON x.id = coalesce(p.dst, n.id)
	LEFT JOIN node_stats s ON s.id = n.id
	WHERE x.status NOT IN ('superseded', 'retired') AND x.canonical_of = '' AND x.type NOT IN ('session', 'day')
	  AND +n.kind IN ('doc', 'section', 'artifact') -- unary + keeps the planner off node_kind (a 58k-row scan)
	  AND x.path NOT LIKE 'log/%' AND x.path NOT LIKE '%/log/%' AND x.path NOT LIKE '.archive/%' AND x.path NOT LIKE '%/.archive/%'
	  AND NOT EXISTS (SELECT 1 FROM edge v WHERE v.src = x.id AND v.rel = 'version_of')
	  AND NOT EXISTS (SELECT 1 FROM edge v WHERE v.dst = x.id AND v.rel = 'supersedes')`

const hitCols = `n.id, n.kind, n.type, n.path, n.title, n.summary, n.hubs, n.pagerank,
	coalesce(s.helpful, 0), coalesce(s.harmful, 0), coalesce(s.last_helpful_at, '')`

type cand struct {
	Hit
	hubs             string
	pagerank         float64
	helpful, harmful int
	lastHelpful      string
	bm25             float64
}

func scanCand(rows *sql.Rows, withBM25 bool) (cand, error) {
	var c cand
	dst := []any{&c.ID, &c.Kind, &c.Type, &c.Path, &c.Title, &c.Summary, &c.hubs, &c.pagerank, &c.helpful, &c.harmful, &c.lastHelpful}
	if withBM25 {
		dst = append(dst, &c.bm25)
	}
	err := rows.Scan(dst...)
	return c, err
}

const (
	maxTerms      = 12    // a long query (a dispatch brief) is cut to its rarest words
	minTerms      = 3     // kept whatever their cost
	termBudget    = 12000 // postings one query may score
	wMention      = 0.5   // a document the query names by path
	anchorsPerDir = 3
	anchorRel     = 0.8  // relevance of a named folder's anchor documents
	wOwn          = 0.25 // above full relevance for a folder or doc named like a named file, times the match strength
)

// matchQuery is an FTS5 OR of the rare terms in any column and the common ones in the short columns, each quoted
// (no operator injection).
func matchQuery(rare, common []string) string {
	quote := func(ts []string) string {
		quoted := make([]string, len(ts))
		for i, t := range ts {
			quoted[i] = `"` + t + `"`
		}
		return strings.Join(quoted, " OR ")
	}
	switch {
	case len(common) == 0:
		return quote(rare)
	case len(rare) == 0:
		return "{title aliases summary} : (" + quote(common) + ")"
	}
	return quote(rare) + " OR {title aliases summary} : (" + quote(common) + ")"
}

// Search ranks nodes for q: score = relevance (BM25 over title, aliases, summary, body for the query's salient
// words, OR-ed; a document named by path ranks first) + proximity (personalized PageRank from the best matches and
// Seeds, Deep only) + usefulness + recency of helpful use + scope fit + type prior + a little global PageRank.
func Search(db *sql.DB, q string, opts Opts) ([]Hit, error) {
	if opts.Limit <= 0 {
		opts.Limit = 10
	}
	words := Words(q)
	df, err := DocFreqs(db, words)
	if err != nil {
		return nil, err
	}
	cands, err := bm25Candidates(db, matchQuery(Salient(words, df, maxTerms, termBudget)))
	if err != nil {
		return nil, err
	}
	best := 0.0
	for _, c := range cands {
		best = math.Max(best, -c.bm25)
	}
	for i := range cands {
		if best > 0 {
			cands[i].Relevance = -cands[i].bm25 / best
		}
		if cands[i].Kind == "artifact" {
			cands[i].Relevance *= artifactRelevance
		}
	}
	if cands, err = addMentions(db, cands, q, opts.Seeds); err != nil {
		return nil, err
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].Relevance > cands[j].Relevance })
	if opts.Deep {
		if cands, err = addProximity(db, cands, opts.Seeds); err != nil {
			return nil, err
		}
	}
	return score(cands, opts), nil
}

// addMentions puts the documents q names by path at full relevance plus wMention, a named folder's anchor
// documents at anchorRel, and the folders and documents named like a named file (briefs/fix-7266.md →
// evidence/fix-7266/) above every text match, by the match's strength.
func addMentions(db *sql.DB, cands []cand, q string, seeds []string) ([]cand, error) {
	var boxes []string
	for _, s := range seeds {
		if b := boxOf(s); b != "" {
			boxes = append(boxes, b)
		}
	}
	named, err := Mentions(db, q, boxes)
	if err != nil || len(named) == 0 {
		return cands, err
	}
	rel := map[string]float64{}
	var stems []string
	for _, id := range named {
		if b := boxOf(id); b != "" && !slices.Contains(boxes, b) {
			boxes = append(boxes, b)
		}
		anchors, err := Anchors(db, id, anchorsPerDir)
		if err != nil {
			return nil, err
		}
		if len(anchors) == 0 {
			rel[id] = max(rel[id], 1+wMention)
			stems = append(stems, stem(id))
		}
		for _, a := range anchors {
			rel[a] = max(rel[a], anchorRel)
		}
	}
	own, err := MatchFolders(db, boxes, stems)
	if err != nil {
		return nil, err
	}
	for id, strength := range own {
		anchors, err := Anchors(db, id, anchorsPerDir)
		if err != nil {
			return nil, err
		}
		if len(anchors) == 0 {
			rel[id] = max(rel[id], 1+wOwn*strength)
		}
		for i, a := range anchors {
			rel[a] = max(rel[a], 1+wOwn*strength*(1-0.15*float64(i)))
		}
	}
	at := map[string]int{}
	for i, c := range cands {
		at[c.ID] = i
	}
	var missing []any
	for id, r := range rel {
		if i, ok := at[id]; ok {
			cands[i].Relevance = max(cands[i].Relevance, r)
		} else {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return cands, nil
	}
	rows, err := db.Query(`SELECT `+hitCols+` FROM node n `+rankable+` AND n.id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(missing)), ",")+`)`, missing...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanCand(rows, false)
		if err != nil {
			return nil, err
		}
		c.Relevance = rel[c.ID]
		cands = append(cands, c)
	}
	return cands, rows.Err()
}

func bm25Candidates(db *sql.DB, match string) ([]cand, error) {
	if match == "" {
		return nil, nil
	}
	// rank inside FTS first: joining every match through the exclusions before the sort costs 100+ ms on a common word
	rows, err := db.Query(`SELECT `+hitCols+`, m.b FROM (SELECT rowid AS r, bm25(fts, 0.0, 10.0, 8.0, 4.0, 1.0) AS b
		FROM fts WHERE fts MATCH ? ORDER BY b LIMIT ?) m JOIN node n ON n.rowid = m.r `+rankable+` ORDER BY m.b LIMIT ?`,
		match, ftsTop, candidates)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []cand
	for rows.Next() {
		c, err := scanCand(rows, true)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func addProximity(db *sql.DB, cands []cand, extraSeeds []string) ([]cand, error) {
	seeds := map[string]float64{}
	for i := 0; i < len(cands) && i < pprSeeds; i++ {
		seeds[cands[i].ID] += cands[i].Relevance
	}
	for _, s := range extraSeeds {
		seeds[s] += 1
	}
	if len(seeds) == 0 {
		return cands, nil
	}
	nb, err := db.Prepare(`SELECT dst FROM edge WHERE src = ?1 UNION ALL SELECT src FROM edge WHERE dst = ?1 LIMIT ?2`)
	if err != nil {
		return nil, err
	}
	defer nb.Close()
	var queryErr error
	neighbors := func(u string) []string {
		rows, err := nb.Query(u, maxExpandDeg+1)
		if err != nil {
			queryErr = err
			return nil
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var v string
			if rows.Scan(&v) == nil {
				out = append(out, v)
			}
		}
		if len(out) > maxExpandDeg {
			return nil
		}
		return out
	}
	ppr := Push(neighbors, seeds, pushEps)
	if queryErr != nil {
		return nil, queryErr
	}

	have := map[string]bool{}
	for _, c := range cands {
		have[c.ID] = true
	}
	type scored struct {
		id string
		p  float64
	}
	var extra []scored
	for id, p := range ppr {
		if !have[id] {
			extra = append(extra, scored{id, p})
		}
	}
	sort.Slice(extra, func(i, j int) bool {
		return extra[i].p > extra[j].p || (extra[i].p == extra[j].p && extra[i].id < extra[j].id)
	})
	if len(extra) > pprExtra {
		extra = extra[:pprExtra] // many are folders or mention nodes and fall out at the rankable filter
	}
	if len(extra) > 0 {
		args := make([]any, len(extra))
		marks := make([]string, len(extra))
		for i, e := range extra {
			args[i], marks[i] = e.id, "?"
		}
		rows, err := db.Query(`SELECT `+hitCols+` FROM node n `+rankable+` AND n.id IN (`+strings.Join(marks, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			c, err := scanCand(rows, false)
			if err != nil {
				rows.Close()
				return nil, err
			}
			cands = append(cands, c)
		}
		rows.Close()
	}
	best := 0.0
	for _, c := range cands {
		best = math.Max(best, ppr[c.ID])
	}
	for i := range cands {
		if best > 0 {
			cands[i].Proximity = ppr[cands[i].ID] / best
		}
	}
	return cands, nil
}

func score(cands []cand, opts Opts) []Hit {
	keep := map[string]bool{}
	for _, t := range opts.Types {
		keep[t] = true
	}
	bestPR := 0.0
	for _, c := range cands {
		bestPR = math.Max(bestPR, c.pagerank)
	}
	now := time.Now()
	hits := make([]Hit, 0, len(cands))
	for _, c := range cands {
		if len(keep) > 0 && !keep[c.Type] && !keep[c.Kind] {
			continue
		}
		s := wRelevance*c.Relevance + wProximity*c.Proximity + typePrior[c.Type]
		s += wUsefulness * float64(c.helpful-c.harmful) / float64(c.helpful+c.harmful+2)
		if t, err := time.Parse(time.RFC3339, c.lastHelpful); err == nil {
			s += wRecency * math.Exp(-now.Sub(t).Hours()/(24*30))
		}
		if scopeFits(c, opts.Seeds) {
			s += wScope
		}
		if bestPR > 0 {
			s += wPageRank * c.pagerank / bestPR
		}
		c.Score = s
		hits = append(hits, c.Hit)
	}
	sort.Slice(hits, func(i, j int) bool {
		return hits[i].Score > hits[j].Score || (hits[i].Score == hits[j].Score && hits[i].ID < hits[j].ID)
	})
	// one hit per document (the doc or its best section), and one per title+summary (snapshots of one plan)
	seenDoc, seenText, out := map[string]bool{}, map[string]bool{}, hits[:0]
	for _, h := range hits {
		doc, _, _ := strings.Cut(h.ID, "#")
		text := h.Title + "\x00" + h.Summary
		if seenDoc[doc] || seenText[text] {
			continue
		}
		seenDoc[doc], seenText[text] = true, true
		if out = append(out, h); len(out) == opts.Limit {
			break
		}
	}
	return out
}

// scopeFits: the node sits under a seed folder (a work box) or names a seed among its hubs.
func scopeFits(c cand, seeds []string) bool {
	for _, s := range seeds {
		if strings.HasPrefix(c.Path, s+"/") || strings.Contains(","+c.hubs+",", ","+s+",") {
			return true
		}
	}
	return false
}
