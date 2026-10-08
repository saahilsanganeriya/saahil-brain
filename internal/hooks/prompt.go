package hooks

import (
	"database/sql"
	"fmt"
	"maps"
	"math"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/rank"
)

// Ranking for L1 (DESIGN §5). The alias gate comes first: a prompt that names nothing the brain knows by alias,
// hub id, title or path gets no pointers. Then BM25 over title, aliases and summary, one graph hop from the named
// nodes, the best matches, the active work and its hubs, scope fit and a type prior. What the session already holds
// never takes a slot (its own work box, the brief it was pasted from), facts rank above raw work docs, and another
// box's work doc needs the prompt to name it or to be the single best match.
const (
	ftsTop        = 300
	textCands     = 40
	hopSeeds      = 10
	hopCands      = 50
	maxSeedDegree = 300
	maxLineChars  = 150
	exploreShown  = 3 // a note shown fewer times than this may take the explore slot

	// relevance saturates as bm25/(bm25+relHalf): a match worth relHalf is half way to certain
	relHalf     = 12.0
	wRelevance  = 0.3
	wAlias      = 0.4 // times the name's weight (Names): 1 multi-word alias, 0.8 note id or title, 0.5 one word
	wProximity  = 0.35
	wScope      = 0.1
	wMention    = 0.6  // a document the prompt names by path
	wFolder     = 0.45 // times the folder's strength: an anchor document of a folder the prompt or session names
	linkShare   = 0.5  // a document a named document links to gets this share of wMention
	maxLinks    = 30   // links read per named document
	workFolder  = 0.6  // the strength of the session's own work box as a folder
	anchorStep  = 0.15 // each later anchor of a folder counts this much less
	anchorsPer  = 3
	exploreFrac = 0.85 // the explore slot takes a note scoring ≥ this share of the threshold
	nameHit     = 0.3  // a name hit this strong names its node: a multi-word name, or one rare word
	boxNameHit  = 0.8  // a CARD alias hit this strong names its box: a multi-word alias with a rare word
)

// Threshold is the score the best pointer must reach or the hook abstains. Calibrated on the 209 real prompts
// (brain-redesign scratch/real-prompts.txt): 0.40 abstains on 195; of the 14 that get pointers, 13 point at the
// note the prompt is about. 0.35 adds as many wrong pointers as right ones.
var Threshold = 0.40

// typePrior: knowledge notes first; hubs and work baselines already reach the session through the start digest.
var typePrior = map[string]float64{
	"decision": 0.06, "finding": 0.05, "lesson": 0.06, "playbook": 0.05, "anti-pattern": 0.05, "system": 0.03,
	"concept": 0.03, "feedback": 0.02, "reference": 0.02, "project": 0.02, "user": 0.02,
	"spec": 0.02, "plan": 0.02, "hub": -0.1, "card": -0.05, "state": -0.05, "readme": -0.05,
	"artifact": -0.15, "bulk": -0.2, "repo": -0.2, "section": -0.02,
}

var tokenRE = regexp.MustCompile(`[\p{L}\p{N}_]+`)

// stopwords: function words and the chatter of prompts ("please", "now", "can you")
var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`the and for with what how why does this that are was were from into you your
		yours can could would should will shall may might must have has had having been being but not nor any all some
		just also then than them they their there these those here when where which who whom whose about after again
		against before below between both down during each few more most other over own same such too very only out
		off once under until while above because its it's let lets get got make made use used using want wanted need
		needs please thanks thank okay yes now still well like really actually maybe sure think know see look done
		do did doing our ours ourselves him his her hers she he me my mine myself we us it is be am an as at by if in
		of on or so to up no go via per etc one two new way thing things something anything everything give take keep
		run try tell said says say show showing check again full status update right wait yeah sorry able else
		going gonna lot much many even ever every whatever users saahilsanganeriya saahil documents home https http
		www com github`) {
		stopwords[w] = true
	}
}

func hasDigit(s string) bool { return strings.ContainsAny(s, "0123456789") }

type Pointer struct {
	ID, Path, Line, Edge string
	Score                float64
}

type Lookup struct {
	Candidates []string
	Chosen     []Pointer
	Best       float64
	Abstained  bool
}

// Seeds are the session's context: its work boxes (picked by its first prompt) and their hubs.
type Seeds struct {
	Work, Hubs []string
	// Hide drops nodes a pre-session index would not hold (offline evals replay a past session); nil keeps all.
	Hide func(id string) bool
}

type cand struct {
	id, kind, typ, path, title, summary, hubs string
	shown                                     int
	rel                                       float64 // saturated BM25, 0 for graph-only candidates
	alias, prox, scope                        float64
	mention, folder                           float64
	echo                                      bool // the prompt holds its text
	via, viaRel                               string
	score                                     float64
}

// rankable mirrors FORMAT.md's never-ranked list: superseded or retired, byte copies, older versions, sessions and
// days, logs and the archive (a section is judged by its document).
const rankable = `
	LEFT JOIN edge p ON p.src = n.id AND p.rel = 'section_of'
	JOIN node x ON x.id = coalesce(p.dst, n.id)
	LEFT JOIN node_stats s ON s.id = n.id
	WHERE x.status NOT IN ('superseded', 'retired') AND x.canonical_of = '' AND x.type NOT IN ('session', 'day')
	  AND +n.kind IN ('doc', 'section', 'artifact')
	  AND x.path NOT LIKE 'log/%' AND x.path NOT LIKE '%/log/%' AND x.path NOT LIKE '.archive/%' AND x.path NOT LIKE '%/.archive/%'
	  AND x.path NOT LIKE 'memory-import/%' -- a raw imported memory: its filed note is what ranks
	  AND NOT EXISTS (SELECT 1 FROM edge v WHERE v.src = x.id AND v.rel = 'version_of')
	  AND NOT EXISTS (SELECT 1 FROM edge v WHERE v.dst = x.id AND v.rel = 'supersedes')`

const candCols = `n.id, n.kind, n.type, n.path, n.title, n.summary, n.hubs, coalesce(s.shown, 0)`

// Pointers ranks the index for a prompt and picks ≤ 3 pointers, or abstains. nm is the alias gate's dictionary.
func Pointers(db *sql.DB, prompt string, seeds Seeds, nm *Names) (Lookup, error) {
	q, err := readPrompt(db, prompt, seeds.Work, nm)
	if err != nil {
		return Lookup{}, err
	}
	return pointers(db, q, seeds, nm)
}

// promptRead is what a prompt names: nodes by name (alias, id, title), and paths and box names it spells.
type promptRead struct {
	text      string
	hits      map[string]float64
	phrases   []string
	mentioned []string
}

// readPrompt reads the names a prompt spells; a relative path is tried under boxes and the boxes the prompt names.
func readPrompt(db *sql.DB, prompt string, boxes []string, nm *Names) (promptRead, error) {
	q := promptRead{text: cleanPrompt(prompt)}
	if nm != nil {
		// names count in prose only: inside a path they are folder names, which mentions and folders already read
		q.hits, q.phrases = nm.match(tokenRE.FindAllString(strings.ToLower(pathRE.ReplaceAllString(q.text, " ")), -1))
	}
	var err error
	q.mentioned, err = rank.Mentions(db, q.text, boxes)
	return q, err
}

// namedBoxes are the boxes the prompt names, most named first: by a path or box name in them, or by a CARD alias.
func (q promptRead) namedBoxes() []string {
	count, first := map[string]int{}, map[string]int{}
	note := func(box string) {
		if _, ok := first[box]; !ok {
			first[box] = len(first)
		}
		count[box]++
	}
	for _, id := range q.mentioned {
		if b := rank.BoxOf(id); b != "" {
			note(b)
		}
	}
	for id, w := range q.hits {
		if b := rank.BoxOf(id); b != "" && w >= boxNameHit && strings.HasSuffix(id, "/CARD.md") {
			note(b)
		}
	}
	boxes := slices.Collect(maps.Keys(count))
	sort.Slice(boxes, func(i, j int) bool {
		a, b := boxes[i], boxes[j]
		return count[a] > count[b] || (count[a] == count[b] && first[a] < first[b])
	})
	return boxes
}

// pointers ranks for a read prompt. The gate passes when the prompt names something: a note by name, a path or a
// work box.
func pointers(db *sql.DB, q promptRead, seeds Seeds, nm *Names) (Lookup, error) {
	var lk Lookup
	mentioned := rank.Visible(q.mentioned, seeds.Hide)
	folders, err := relatedFolders(db, seeds, mentioned)
	if err != nil {
		return lk, err
	}
	for _, w := range seeds.Work {
		folders["work/"+w] = max(folders["work/"+w], workFolder)
	}
	named, err := fetch(db, q.hits, seeds.Hide)
	if err != nil {
		return lk, err
	}
	direct, err := pointed(db, mentioned, folders, seeds.Hide)
	if err != nil {
		return lk, err
	}
	if len(named)+len(mentioned) == 0 || len(named)+len(direct) == 0 {
		lk.Abstained = true
		return lk, nil
	}
	terms := nm.salientTerms(q.text, q.phrases)
	textCands, err := query(db, terms, seeds.Hide)
	if err != nil {
		return lk, err
	}
	named = merge(named, direct)
	if err := namedRelevance(db, named, terms); err != nil {
		return lk, err
	}
	cands := merge(textCands, named)
	if cands, err = hop(db, cands, seeds); err != nil {
		return lk, err
	}
	usage, err := rank.UsagePrior(db)
	if err != nil {
		return lk, err
	}
	for i := range cands {
		c := &cands[i]
		c.scope = scopeFit(c, seeds)
		// naming a document and naming its folder are one piece of evidence, not two
		c.score = wRelevance*c.rel + wAlias*c.alias + wProximity*c.prox + wScope*c.scope + typePrior[c.typ] +
			max(wMention*c.mention, wFolder*c.folder) + usage[c.id]
	}
	sort.SliceStable(cands, func(i, j int) bool {
		return cands[i].score > cands[j].score || (cands[i].score == cands[j].score && cands[i].id < cands[j].id)
	})
	if cands, err = held(db, dedupe(cands), seeds, q.namedBoxes()); err != nil {
		return lk, err
	}
	for i := 0; i < len(cands) && i < 10; i++ {
		lk.Candidates = append(lk.Candidates, cands[i].id)
	}
	if err := markEchoes(db, cands, q.text); err != nil {
		return lk, err
	}
	var answers []cand
	for _, c := range cands {
		if c.answer() {
			answers = append(answers, c)
		}
	}
	if len(answers) > 0 {
		lk.Best = round3(answers[0].score)
	}
	if lk.Best < Threshold {
		lk.Abstained = true
		return lk, nil
	}
	chosen := explore(choose(answers), answers)
	for _, c := range chosen {
		lk.Chosen = append(lk.Chosen, Pointer{ID: c.id, Path: c.path, Line: oneLine(c), Edge: bestEdge(c, seeds), Score: round3(c.score)})
	}
	return lk, nil
}

// relatedFolders finds folders and documents, in the session's work box or a box the prompt names, named like a
// file the prompt names, by strength.
func relatedFolders(db *sql.DB, seeds Seeds, mentioned []string) (map[string]float64, error) {
	boxes := slices.Clone(seeds.Work)
	var stems []string
	for _, id := range mentioned {
		if b := rank.BoxOf(id); b != "" && !slices.Contains(boxes, b) {
			boxes = append(boxes, b)
		}
		stems = append(stems, strings.TrimSuffix(path.Base(id), path.Ext(id)))
	}
	related, err := rank.MatchFolders(db, boxes, stems)
	if err != nil {
		return nil, err
	}
	if seeds.Hide != nil {
		maps.DeleteFunc(related, func(id string, _ float64) bool { return seeds.Hide(id) })
	}
	return related, nil
}

// pointed loads the documents the prompt names (mention 1), those they link to (linkShare), and the anchor
// documents of named or session folders (folder strength, less for each later anchor).
func pointed(db *sql.DB, mentioned []string, folders map[string]float64, hide func(string) bool) ([]cand, error) {
	mention, folder := map[string]float64{}, map[string]float64{}
	for _, id := range mentioned {
		if isDir(db, id) {
			folders[id] = 1
		} else {
			mention[id] = 1
		}
	}
	named := slices.Collect(maps.Keys(mention))
	for dir, strength := range folders {
		if !isDir(db, dir) {
			folder[dir] = max(folder[dir], strength)
			continue
		}
		anchors, err := rank.Anchors(db, dir, anchorsPer, hide)
		if err != nil {
			return nil, err
		}
		for i, a := range anchors {
			folder[a] = max(folder[a], strength*(1-anchorStep*float64(i)))
		}
	}
	for _, id := range named {
		rows, err := db.Query(`SELECT dst FROM edge WHERE src = ? AND rel = 'links_to' LIMIT ?`, id, maxLinks)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var dst string
			if err := rows.Scan(&dst); err != nil {
				rows.Close()
				return nil, err
			}
			mention[dst] = max(mention[dst], linkShare)
		}
		rows.Close()
	}
	// fetch keeps the strongest maxNamedIDs: a brief names many files and their links
	ids := map[string]float64{}
	for id, w := range mention {
		ids[id] = max(ids[id], w)
	}
	for id, w := range folder {
		ids[id] = max(ids[id], w)
	}
	out, err := fetch(db, ids, hide)
	for i := range out {
		out[i].alias = 0
		out[i].mention, out[i].folder = mention[out[i].id], folder[out[i].id]
	}
	return out, err
}

func isDir(db *sql.DB, id string) bool {
	var kind string
	return db.QueryRow(`SELECT kind FROM node WHERE id = ?`, id).Scan(&kind) == nil && kind == "dir"
}

// fetch loads the named nodes that may be ranked, strongest names first.
func fetch(db *sql.DB, hits map[string]float64, hide func(string) bool) ([]cand, error) {
	ids := make([]string, 0, len(hits))
	for id := range hits {
		if hide == nil || !hide(id) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		return hits[ids[i]] > hits[ids[j]] || (hits[ids[i]] == hits[ids[j]] && ids[i] < ids[j])
	})
	if len(ids) > maxNamedIDs {
		ids = ids[:maxNamedIDs]
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.Query(`SELECT `+candCols+` FROM node n `+rankable+` AND n.id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.kind, &c.typ, &c.path, &c.title, &c.summary, &c.hubs, &c.shown); err != nil {
			return nil, err
		}
		c.alias = hits[c.id]
		out = append(out, c)
	}
	return out, rows.Err()
}

// query ranks by BM25 over title, aliases and summary (DESIGN §5) for any of the terms.
func query(db *sql.DB, terms []string, hide func(string) bool) ([]cand, error) {
	if len(terms) == 0 {
		return nil, nil
	}
	limit := textCands
	if hide != nil {
		limit = ftsTop // hidden rows must not take the places of rows a pre-session index would rank
	}
	rows, err := db.Query(`SELECT `+candCols+`, m.b FROM (SELECT rowid AS r, bm25(fts, 0.0, 10.0, 8.0, 4.0, 0.0) AS b
		FROM fts WHERE fts MATCH ? ORDER BY b LIMIT ?) m JOIN node n ON n.rowid = m.r `+rankable+` ORDER BY m.b LIMIT ?`,
		matchExpr(terms), ftsTop, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []cand
	for rows.Next() && len(out) < textCands {
		var c cand
		var b float64
		if err := rows.Scan(&c.id, &c.kind, &c.typ, &c.path, &c.title, &c.summary, &c.hubs, &c.shown, &b); err != nil {
			return nil, err
		}
		if hide != nil && hide(c.id) {
			continue
		}
		c.rel = -b / (-b + relHalf)
		out = append(out, c)
	}
	return out, rows.Err()
}

// namedRelevance scores the named nodes with the same BM25 as the text query, even when they rank below its cut.
func namedRelevance(db *sql.DB, named []cand, terms []string) error {
	if len(terms) == 0 || len(named) == 0 {
		return nil
	}
	args := []any{matchExpr(terms)}
	at := map[string]int{}
	for i, c := range named {
		args = append(args, c.id)
		at[c.id] = i
	}
	rows, err := db.Query(`SELECT n.id, bm25(fts, 0.0, 10.0, 8.0, 4.0, 0.0) FROM fts JOIN node n ON n.rowid = fts.rowid
		WHERE fts MATCH ? AND n.id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(named)), ",")+`)`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var b float64
		if err := rows.Scan(&id, &b); err != nil {
			return err
		}
		named[at[id]].rel = -b / (-b + relHalf)
	}
	return rows.Err()
}

func matchExpr(terms []string) string {
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + t + `"`
	}
	return "{title aliases summary} : (" + strings.Join(quoted, " OR ") + ")"
}

// merge adds the named candidates to the text matches, keeping each node's best relevance and its name hit.
func merge(text, named []cand) []cand {
	at := map[string]int{}
	for i, c := range text {
		at[c.id] = i
	}
	for _, c := range named {
		if i, ok := at[c.id]; ok {
			text[i].alias = math.Max(text[i].alias, c.alias)
			text[i].rel = math.Max(text[i].rel, c.rel)
			text[i].mention = math.Max(text[i].mention, c.mention)
			text[i].folder = math.Max(text[i].folder, c.folder)
			continue
		}
		at[c.id] = len(text)
		text = append(text, c)
	}
	return text
}

// hopRels are the edges one hop may follow: authored relations, text links and folder membership.
const hopRels = `('hubs', 'about', 'evidence', 'supersedes', 'sources', 'related', 'broader', 'links_to', 'part_of')`

// hop spreads one step from the seeds — named nodes, the best matches, the active work folders and their hubs: a
// neighbour gains weight/√degree from each seed, capped at 1. Nodes reached only this way join as candidates.
func hop(db *sql.DB, cands []cand, seeds Seeds) ([]cand, error) {
	weight := map[string]float64{}
	for i := 0; i < len(cands) && i < hopSeeds; i++ {
		weight[cands[i].id] += cands[i].rel / 2
	}
	for _, c := range cands {
		if w := max(c.alias, c.mention, c.folder); w > 0 {
			weight[c.id] = math.Max(weight[c.id], w)
		}
	}
	for _, w := range seeds.Work {
		weight["work/"+w] += 1
	}
	for _, h := range seeds.Hubs {
		weight[h] += 1
	}
	if len(weight) == 0 {
		return cands, nil
	}
	ids := make([]any, 0, len(weight))
	for id := range weight {
		ids = append(ids, id)
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	rows, err := db.Query(`SELECT src, dst, rel FROM edge WHERE src IN (`+marks+`) AND rel IN `+hopRels+`
		UNION ALL SELECT src, dst, rel FROM edge WHERE dst IN (`+marks+`) AND rel IN `+hopRels+` LIMIT 20000`,
		append(ids, ids...)...)
	if err != nil {
		return nil, err
	}
	type link struct{ seed, other, rel string }
	var links []link
	degree := map[string]int{}
	for rows.Next() {
		var src, dst, rel string
		if err := rows.Scan(&src, &dst, &rel); err != nil {
			rows.Close()
			return nil, err
		}
		if seeds.Hide != nil && (seeds.Hide(src) || seeds.Hide(dst)) {
			continue
		}
		if _, ok := weight[src]; ok {
			links = append(links, link{src, dst, rel})
			degree[src]++
		}
		if _, ok := weight[dst]; ok && dst != src {
			links = append(links, link{dst, src, rel})
			degree[dst]++
		}
	}
	rows.Close()
	prox, via, viaGain := map[string]float64{}, map[string]link{}, map[string]float64{}
	for _, l := range links {
		if degree[l.seed] > maxSeedDegree {
			continue // a big folder or a much-cited node spreads nothing useful
		}
		if namesakes(l.seed, l.other) {
			continue // a spec and the report named like it: their link is the shared name, counted once already
		}
		gain := weight[l.seed] / math.Sqrt(float64(degree[l.seed]))
		prox[l.other] += gain
		if gain > viaGain[l.other] {
			viaGain[l.other], via[l.other] = gain, l
		}
	}
	have := map[string]int{}
	for i := range cands {
		have[cands[i].id] = i
	}
	type extra struct {
		id string
		p  float64
	}
	var extras []extra
	for id, p := range prox {
		if i, ok := have[id]; ok {
			cands[i].prox = math.Min(p, 1)
			cands[i].via, cands[i].viaRel = via[id].seed, via[id].rel
		} else {
			extras = append(extras, extra{id, p})
		}
	}
	sort.Slice(extras, func(i, j int) bool {
		return extras[i].p > extras[j].p || (extras[i].p == extras[j].p && extras[i].id < extras[j].id)
	})
	if len(extras) > hopCands {
		extras = extras[:hopCands]
	}
	if len(extras) == 0 {
		return cands, nil
	}
	args := make([]any, len(extras))
	for i, e := range extras {
		args[i] = e.id
	}
	rows, err = db.Query(`SELECT `+candCols+` FROM node n `+rankable+` AND n.id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.kind, &c.typ, &c.path, &c.title, &c.summary, &c.hubs, &c.shown); err != nil {
			return nil, err
		}
		c.prox = math.Min(prox[c.id], 1)
		c.via, c.viaRel = via[c.id].seed, via[c.id].rel
		cands = append(cands, c)
	}
	return cands, rows.Err()
}

// maxEchoReads bounds the bodies one prompt reads to find echoes: the top candidates, where the pointers come from.
const maxEchoReads = 8

// markEchoes flags the candidates that could be chosen whose text the prompt already holds.
func markEchoes(db *sql.DB, cands []cand, prompt string) error {
	var shingles map[uint64]bool
	read := 0
	for i := range cands {
		c := &cands[i]
		if read == maxEchoReads || c.score < exploreFrac*Threshold {
			break
		}
		if c.typ == "hub" {
			continue
		}
		if shingles == nil {
			shingles = rank.Shingles(prompt)
		}
		body, err := rank.Body(db, c.id)
		if err != nil {
			return err
		}
		read++
		c.echo = rank.Echo(body, shingles) >= rank.EchoShare
	}
	return nil
}

// answer: a hub is context, not an answer (it seeds the hop and reaches the session through the digest), and a
// document the prompt already holds (the brief a dispatch was pasted from) tells the session nothing new.
func (c cand) answer() bool { return c.typ != "hub" && !c.echo }

// scopeFit: the candidate lives in an active work box or sits in one of its hubs.
func scopeFit(c *cand, seeds Seeds) float64 {
	for _, w := range seeds.Work {
		if strings.HasPrefix(c.path, "work/"+w+"/") {
			return 1
		}
	}
	for _, h := range seeds.Hubs {
		if c.id == h || strings.Contains(","+c.hubs+",", ","+h+",") {
			return 1
		}
	}
	return 0
}

// dedupe keeps one candidate per document (the doc or its best section) and per title+summary (snapshots).
func dedupe(cands []cand) []cand {
	seenDoc, seenText := map[string]bool{}, map[string]bool{}
	out := cands[:0]
	for _, c := range cands {
		doc, _, _ := strings.Cut(c.id, "#")
		text := c.title + "\x00" + c.summary
		if seenDoc[doc] || seenText[text] {
			continue
		}
		seenDoc[doc], seenText[text] = true, true
		out = append(out, c)
	}
	return out
}

// loadedFiles reach every session at L0 already.
var loadedFiles = map[string]bool{"memory/MEMORY.md": true, "BRAIN.md": true, "hot.md": true}

// held drops what the session already holds or should not see: its own work boxes (the digest carries them), the
// files every session loads, and boxes whose CARD is paused or done unless the prompt names them.
func held(db *sql.DB, cands []cand, seeds Seeds, named []string) ([]cand, error) {
	var boxes []any
	seen := map[string]bool{}
	for _, c := range cands {
		card := "work/" + rank.BoxOf(c.path) + "/CARD.md"
		if rank.BoxOf(c.path) != "" && !seen[card] && (seeds.Hide == nil || !seeds.Hide(card)) {
			seen[card] = true
			boxes = append(boxes, card)
		}
	}
	idle := map[string]bool{}
	if len(boxes) > 0 {
		rows, err := db.Query(`SELECT path FROM node WHERE status IN ('paused', 'done') AND path IN (`+
			strings.TrimSuffix(strings.Repeat("?,", len(boxes)), ",")+`)`, boxes...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return nil, err
			}
			idle[rank.BoxOf(p)] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return slices.DeleteFunc(cands, func(c cand) bool {
		b := rank.BoxOf(c.path)
		return loadedFiles[c.path] || slices.Contains(seeds.Work, b) || (idle[b] && !slices.Contains(named, b))
	}), nil
}

// fact: a claim or procedure the brain learned, the kind of pointer a session cannot find in its own files.
var fact = map[string]bool{"lesson": true, "anti-pattern": true, "playbook": true, "decision": true, "finding": true}

func (c cand) tier() int {
	switch {
	case strings.HasPrefix(c.path, "notes/") && fact[c.typ]:
		return 0
	case !strings.HasPrefix(c.path, "work/"):
		return 1
	}
	return 2
}

// choose takes ≤ 3 answers at or above the threshold, facts first; a work doc only when the prompt names it (its
// path or a real name of it) or it is the single best match.
func choose(answers []cand) []cand {
	var chosen []cand
	for _, c := range answers {
		named := c.mention >= 1 || c.alias >= nameHit
		if c.score >= Threshold && (c.tier() < 2 || named || c.id == answers[0].id) {
			chosen = append(chosen, c)
		}
	}
	sort.SliceStable(chosen, func(i, j int) bool { return chosen[i].tier() < chosen[j].tier() })
	return chosen[:min(len(chosen), config.MaxPointers)]
}

// explore gives the last slot to a note shown fewer than three times, so new notes get a chance (DESIGN §5), when
// it scores at least exploreFrac of the pointer it replaces.
func explore(chosen, cands []cand) []cand {
	under := func(c cand) bool { return strings.HasPrefix(c.path, "notes/") && c.shown < exploreShown }
	for _, c := range chosen {
		if under(c) {
			return chosen
		}
	}
	picked := map[string]bool{}
	for _, c := range chosen {
		picked[c.id] = true
	}
	floor := exploreFrac * Threshold
	if len(chosen) == config.MaxPointers {
		floor = max(floor, exploreFrac*chosen[len(chosen)-1].score)
	}
	for _, c := range cands {
		if !picked[c.id] && under(c) && c.answer() && c.score >= floor {
			if len(chosen) < config.MaxPointers {
				return append(chosen, c)
			}
			chosen[len(chosen)-1] = c
			return chosen
		}
	}
	return chosen
}

func oneLine(c cand) string {
	text := c.summary
	if text == "" || text == c.title {
		text = c.title
	} else if c.title != "" && !strings.HasPrefix(c.path, "notes/") && !strings.HasPrefix(c.path, "hubs/") {
		text = c.title + " — " + c.summary
	}
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > maxLineChars {
		text = string(r[:maxLineChars-1]) + "…"
	}
	return text
}

// bestEdge names how the pointer connects to this session: its hub, the seed it was reached from, else its own hub
// or folder.
func bestEdge(c cand, seeds Seeds) string {
	hubs := strings.Split(c.hubs, ",")
	for _, h := range seeds.Hubs {
		for _, have := range hubs {
			if have == h {
				return "hub " + h
			}
		}
	}
	if c.via != "" && c.prox > 0.3 {
		return c.viaRel + " · " + c.via
	}
	if hubs[0] != "" {
		return "hub " + hubs[0]
	}
	return "in " + path.Dir(c.path)
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// Text renders pointers for the model, ≤ 300 tokens (≈ 4 chars each).
func (lk Lookup) Text() string {
	if lk.Abstained || len(lk.Chosen) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Brain pointers (brain_show <id> opens one; ignore them if unrelated):\n")
	for _, p := range lk.Chosen {
		fmt.Fprintf(&b, "- %s — %s [%s]\n", p.ID, p.Line, p.Edge)
	}
	return cutRunes(strings.TrimRight(b.String(), "\n"), config.PointerMaxTokens*4)
}

// Prompt runs the UserPromptSubmit hook: pointers when live, and on a session's first prompt the work part of its
// digest; in shadow only the lookup rows. The prompt text is never written anywhere.
func Prompt(env Env, in Input) ([]byte, error) {
	started := env.Now()
	ix, err := env.openIndex()
	if err != nil || ix == nil {
		return nil, err
	}
	defer ix.Close()
	session := in.session()
	st, ok := env.loadState(session)
	withHot := false
	if !ok && env.statePath(session) != "" {
		// no start record (its hook ran out of time, or the host has none): this prompt owes the whole digest
		st, withHot = state{DigestOwed: true, CwdWork: folderWork(env, in.cwd())}, true
	}
	nm, stale := env.loadNames(ix.DB())
	if stale && env.Rebuild != nil {
		env.Rebuild()
	}
	owed := st.DigestOwed
	d, lk, err := env.turn(ix.DB(), nm, in.Prompt, &st, withHot)
	if err != nil {
		return nil, err
	}
	live := env.Live()
	if err := record(ix.DB(), env, session, owed, d, lk, live, env.Now().Sub(started)); err != nil {
		return nil, err
	}
	if len(lk.Chosen) > 0 {
		if st.Shown == nil {
			st.Shown = map[string]string{}
		}
		for _, p := range lk.Chosen {
			st.Shown[p.ID] = env.absPath(p.Path)
		}
	}
	if session != "" && (owed || len(lk.Chosen) > 0) {
		_ = env.saveState(session, st)
	}
	text := strings.TrimSpace(d.Text + "\n\n" + lk.Text())
	if !live || text == "" {
		return nil, nil
	}
	event := in.Event
	if event == "" {
		event = "UserPromptSubmit"
	}
	return Output(event, text), nil
}

// turn is one prompt's work. A first prompt picks the session's work (the boxes it names, else the cwd's) and builds
// the digest's work part in what start left of the L0 budget; every prompt then gets its pointers.
func (e Env) turn(db *sql.DB, nm *Names, prompt string, st *state, withHot bool) (Digest, Lookup, error) {
	boxes := st.Work
	if st.DigestOwed {
		boxes = st.CwdWork
	}
	q, err := readPrompt(db, prompt, boxes, nm)
	if err != nil {
		return Digest{}, Lookup{}, err
	}
	var d Digest
	if st.DigestOwed {
		header := taskHeader
		if withHot {
			header = startHeader
		}
		d = composeDigest(e, header, withHot, pickWork(e, q.namedBoxes(), st.CwdWork, nil), config.DigestMaxChars-st.DigestChars, nil)
		st.Work, st.Hubs, st.DigestOwed, st.CwdWork, st.DigestChars = d.Work, d.Hubs, false, nil, 0
	}
	lk := Lookup{Abstained: true}
	if nm != nil {
		lk, err = pointers(db, q, Seeds{Work: st.Work, Hubs: st.Hubs}, nm)
	}
	return d, lk, err
}

// record writes the lookup rows (the digest a first prompt owed, then the pointers) and, when live, bumps
// node_stats.shown for what was injected.
func record(db *sql.DB, env Env, session string, owed bool, d Digest, lk Lookup, live bool, took time.Duration) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if owed {
		digest := map[string]any{"chars": utf8.RuneCountInString(d.Text), "sources": nonNil(d.Sources)}
		if err := logLookup(tx, env.Now(), session, "digest", nonNil(d.Sources), digest, d.Text == "", 0); err != nil {
			return err
		}
	}
	chosen := []string{}
	for _, p := range lk.Chosen {
		chosen = append(chosen, p.ID)
	}
	if err := logLookup(tx, env.Now(), session, "prompt", nonNil(lk.Candidates), chosen, lk.Abstained, took); err != nil {
		return err
	}
	if live {
		now := env.Now().UTC().Format(time.RFC3339)
		for _, id := range chosen {
			if _, err := tx.Exec(`INSERT INTO node_stats(id, shown, last_shown_at) VALUES (?, 1, ?)
				ON CONFLICT(id) DO UPDATE SET shown = shown + 1, last_shown_at = excluded.last_shown_at`, id, now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// namesakes: two documents with the same file stem in different folders (worker-specs/x.md and recon/x.md).
func namesakes(a, b string) bool {
	if path.Dir(a) == path.Dir(b) || !strings.Contains(a, "/") || !strings.Contains(b, "/") {
		return false
	}
	sa, sb := strings.TrimSuffix(path.Base(a), path.Ext(a)), strings.TrimSuffix(path.Base(b), path.Ext(b))
	return sa != "" && strings.EqualFold(sa, sb)
}
