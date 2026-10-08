package hooks

import (
	"database/sql"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"saahil.local/brain/internal/config"
)

// Ranking for L1 (DESIGN §5). The alias gate comes first: a prompt that names nothing the brain knows by alias,
// hub id or title gets no pointers. Then BM25 over title, aliases and summary, one graph hop from the named nodes,
// the best matches, the active work and its hubs, scope fit and a type prior.
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
	exploreFrac = 0.85 // the explore slot takes a note scoring ≥ this share of the threshold
)

// Threshold is the score the best pointer must reach or the hook abstains. Calibrated on the 209 real prompts
// (brain-redesign scratch/real-prompts.txt): 0.40 abstains on 195; of the 14 that get pointers, 13 point at the
// note the prompt is about. 0.35 adds as many wrong pointers as right ones.
var Threshold = 0.40

// typePrior: knowledge notes first; hubs and work baselines already reach the session through the start digest.
var typePrior = map[string]float64{
	"decision": 0.06, "finding": 0.05, "lesson": 0.06, "playbook": 0.05, "anti-pattern": 0.05, "system": 0.03,
	"concept": 0.03, "feedback": 0.02, "reference": 0.02, "project": 0.02, "user": 0.02,
	"spec": 0.02, "plan": 0.02, "hub": -0.1, "card": -0.05, "state": -0.05,
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

// Seeds are the session's context: its work boxes and their hubs.
type Seeds struct{ Work, Hubs []string }

type cand struct {
	id, kind, typ, path, title, summary, hubs string
	shown                                     int
	rel                                       float64 // saturated BM25, 0 for graph-only candidates
	alias, prox, scope                        float64
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
	  AND NOT EXISTS (SELECT 1 FROM edge v WHERE v.src = x.id AND v.rel = 'version_of')
	  AND NOT EXISTS (SELECT 1 FROM edge v WHERE v.dst = x.id AND v.rel = 'supersedes')`

const candCols = `n.id, n.kind, n.type, n.path, n.title, n.summary, n.hubs, coalesce(s.shown, 0)`

// Pointers ranks the index for a prompt and picks ≤ 3 pointers, or abstains. nm is the alias gate's dictionary.
func Pointers(db *sql.DB, prompt string, seeds Seeds, nm *Names) (Lookup, error) {
	var lk Lookup
	text := cleanPrompt(prompt)
	hits, phrases := nm.match(tokenRE.FindAllString(strings.ToLower(text), -1))
	if len(hits) == 0 {
		lk.Abstained = true
		return lk, nil
	}
	named, err := fetch(db, hits)
	if err != nil {
		return lk, err
	}
	if len(named) == 0 {
		lk.Abstained = true
		return lk, nil
	}
	terms := nm.salientTerms(text, phrases)
	textCands, err := query(db, terms)
	if err != nil {
		return lk, err
	}
	if err := namedRelevance(db, named, terms); err != nil {
		return lk, err
	}
	cands := merge(textCands, named)
	if cands, err = hop(db, cands, seeds); err != nil {
		return lk, err
	}
	for i := range cands {
		c := &cands[i]
		c.scope = scopeFit(c, seeds)
		c.score = wRelevance*c.rel + wAlias*c.alias + wProximity*c.prox + wScope*c.scope + typePrior[c.typ]
		if strings.HasPrefix(c.path, "memory-import/") {
			c.score -= 0.05 // the filed note in notes/ is the canonical form
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		return cands[i].score > cands[j].score || (cands[i].score == cands[j].score && cands[i].id < cands[j].id)
	})
	cands = dedupe(cands)
	for i := 0; i < len(cands) && i < 10; i++ {
		lk.Candidates = append(lk.Candidates, cands[i].id)
	}
	for _, c := range cands {
		if c.typ != "hub" {
			lk.Best = round3(c.score)
			break
		}
	}
	if lk.Best < Threshold {
		lk.Abstained = true
		return lk, nil
	}
	var chosen []cand
	for _, c := range cands {
		// a hub is context, not an answer: it seeds the hop and reaches the session through the digest
		if c.typ != "hub" && c.score >= Threshold && len(chosen) < config.MaxPointers {
			chosen = append(chosen, c)
		}
	}
	chosen = explore(chosen, cands)
	for _, c := range chosen {
		lk.Chosen = append(lk.Chosen, Pointer{ID: c.id, Path: c.path, Line: oneLine(c), Edge: bestEdge(c, seeds), Score: round3(c.score)})
	}
	return lk, nil
}

// fetch loads the named nodes that may be ranked, strongest names first.
func fetch(db *sql.DB, hits map[string]float64) ([]cand, error) {
	ids := make([]string, 0, len(hits))
	for id := range hits {
		ids = append(ids, id)
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
func query(db *sql.DB, terms []string) ([]cand, error) {
	if len(terms) == 0 {
		return nil, nil
	}
	rows, err := db.Query(`SELECT `+candCols+`, m.b FROM (SELECT rowid AS r, bm25(fts, 0.0, 10.0, 8.0, 4.0, 0.0) AS b
		FROM fts WHERE fts MATCH ? ORDER BY b LIMIT ?) m JOIN node n ON n.rowid = m.r `+rankable+` ORDER BY m.b LIMIT ?`,
		matchExpr(terms), ftsTop, textCands)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []cand
	for rows.Next() {
		var c cand
		var b float64
		if err := rows.Scan(&c.id, &c.kind, &c.typ, &c.path, &c.title, &c.summary, &c.hubs, &c.shown, &b); err != nil {
			return nil, err
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
			text[i].alias = c.alias
			text[i].rel = math.Max(text[i].rel, c.rel)
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
		if c.alias > 0 {
			weight[c.id] = math.Max(weight[c.id], c.alias)
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

// explore gives the last slot to a note shown fewer than three times, so new notes get a chance (DESIGN §5).
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
	for _, c := range cands {
		if !picked[c.id] && under(c) && c.typ != "hub" && c.score >= exploreFrac*Threshold {
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

// bestEdge names how the pointer connects to this session: its hub or work, the seed it was reached from, else
// its own hub or folder.
func bestEdge(c cand, seeds Seeds) string {
	hubs := strings.Split(c.hubs, ",")
	for _, h := range seeds.Hubs {
		for _, have := range hubs {
			if have == h {
				return "hub " + h
			}
		}
	}
	for _, w := range seeds.Work {
		if strings.HasPrefix(c.path, "work/"+w+"/") {
			return "work " + w
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

// Prompt runs the UserPromptSubmit hook: pointers when live; in shadow only the lookup row. The prompt text is
// never written anywhere.
func Prompt(env Env, in Input) ([]byte, error) {
	started := env.Now()
	ix, err := env.openIndex()
	if err != nil || ix == nil {
		return nil, err
	}
	defer ix.Close()
	session := in.session()
	st, ok := env.loadState(session)
	if !ok {
		if box := workByFolder(env, in.cwd(), nil); box != "" {
			st.Work, st.Hubs = []string{box}, hubsByName(env.Root, box)
		}
	}
	nm, stale := env.loadNames(ix.DB())
	if stale && env.Rebuild != nil {
		env.Rebuild()
	}
	lk := Lookup{Abstained: true}
	if nm != nil {
		if lk, err = Pointers(ix.DB(), in.Prompt, Seeds{Work: st.Work, Hubs: st.Hubs}, nm); err != nil {
			return nil, err
		}
	}
	if err != nil {
		return nil, err
	}
	live := env.Live()
	if err := record(ix.DB(), env, session, lk, live, env.Now().Sub(started)); err != nil {
		return nil, err
	}
	if len(lk.Chosen) > 0 && session != "" {
		if st.Shown == nil {
			st.Shown = map[string]string{}
		}
		for _, p := range lk.Chosen {
			st.Shown[p.ID] = env.absPath(p.Path)
		}
		_ = env.saveState(session, st)
	}
	if !live || lk.Text() == "" {
		return nil, nil
	}
	event := in.Event
	if event == "" {
		event = "UserPromptSubmit"
	}
	return Output(event, lk.Text()), nil
}

// record writes the lookup row and, when live, bumps node_stats.shown for what was injected.
func record(db *sql.DB, env Env, session string, lk Lookup, live bool, took time.Duration) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
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
