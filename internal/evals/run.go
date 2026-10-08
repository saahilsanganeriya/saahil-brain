package evals

import (
	"fmt"
	"path"
	"saahil.local/brain/internal/hooks"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"saahil.local/brain/internal/index"
	"saahil.local/brain/internal/rank"
)

// Ranker is the prompt-pointer ranker under test: at most three node ids for a prompt, or none (it abstains). hide
// drops what the brain could not have held when the prompt was typed; nil keeps all.
type Ranker interface {
	Pointers(prompt, cwd string, hide func(id string) bool) []string
}

// SearchRanker stands in for the hook ranker when its name dictionary cannot be built: the top three of brain_search.
type SearchRanker struct{ Index *index.Index }

func (r SearchRanker) Pointers(prompt, _ string, hide func(id string) bool) []string {
	hits, err := r.Index.Search(prompt, index.SearchOpts{Limit: 3, Hide: hide})
	if err != nil {
		return nil
	}
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.ID
	}
	return ids
}

// NewRanker picks the ranker every eval measures. Wiring point: return the hook ranker (the `brain hook prompt` code
// path, live mode, no injection) here once internal/hooks is merged.
var NewRanker = func(ix *index.Index) Ranker {
	r, err := hooks.NewEvalRanker(hooks.DefaultEnv(), ix.DB())
	if err != nil {
		return SearchRanker{Index: ix}
	}
	return r
}

type Env struct {
	Index  *index.Index
	Ranker Ranker
	Now    time.Time
	sets   Sets
	nodes  map[string]nodeFacts
	exist  *existence
	filed  map[string][]string // memory key (<project>/<file>) → notes filed from that memory
}

type nodeFacts struct {
	kind     string
	pagerank float64
	ok       bool
}

func NewEnv(ix *index.Index, sets Sets, now time.Time) *Env {
	return &Env{Index: ix, Ranker: NewRanker(ix), Now: now, sets: sets, nodes: map[string]nodeFacts{}}
}

// searchLimit is brain_search's default result count in the Claude Code mod; the grep baseline gets the same budget.
const searchLimit = 8

// maxCalls is the navigation budget; a target not reached within it scores missCalls in the means.
const (
	maxCalls  = 3
	missCalls = maxCalls + 1
)

// ---- queries ----

const taskMarker = "=== TASK ==="

// taskText cuts an Orca dispatch prompt down to its task: the text after the preamble's last "=== TASK ===" line, up
// to the end of the pasted brief. The preamble is the same CLI boilerplate in every dispatch, so it says nothing about
// where the work is. A prompt without the marker is returned whole.
func taskText(prompt string) string {
	i := strings.LastIndex(prompt, taskMarker)
	if i < 0 {
		return prompt
	}
	rest := prompt[i+len(taskMarker):]
	if j := strings.Index(rest, "</pasted_content"); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

// firstPrompt is what the ranker would have seen: the full first owner prompt from the transcript when it is on this
// machine (the note keeps 500 characters), cut to its task text. It is used in memory only, never written.
func firstPrompt(transcript string, notePrompts []string) (q, source string) {
	if p := ownerPrompts(transcript, 1); len(p) == 1 {
		return taskText(p[0]), "transcript"
	}
	if len(notePrompts) > 0 {
		return taskText(notePrompts[0]), "note"
	}
	return "", ""
}

func (e *Env) navQuery(it NavItem) (q, source string, err error) {
	if q, source = firstPrompt(it.Transcript, nil); source == "" {
		s, _ := readSession(e.Index.Root, it.Session)
		q, source = firstPrompt("", s.Prompts)
	}
	if source == "" {
		return "", "", fmt.Errorf("%s: no owner prompt", it.Session)
	}
	return q, source, nil
}

// ---- matching ----

// targetSet answers "is this node one of the targets": by id, by path, and a section by its document.
type targetSet struct{ ids, paths map[string]bool }

// targetsByPath is the set of target files; an imported memory is also reached through a note filed from it (its
// sources name the memory's .import copy), as QA counts it.
func (e *Env) targetsByPath(paths []string) targetSet {
	t := targetSet{ids: map[string]bool{}, paths: map[string]bool{}}
	for _, p := range paths {
		t.paths[p] = true
		var id string
		if e.Index.DB().QueryRow(`SELECT id FROM node WHERE path = ? AND kind != 'section' LIMIT 1`, p).Scan(&id) == nil {
			t.ids[id] = true
		}
		if k, ok := strings.CutPrefix(p, "memory-import/"); ok {
			for _, note := range e.filedFrom()[k] {
				t.ids[note] = true
			}
		}
	}
	return t
}

func (e *Env) filedFrom() map[string][]string {
	if e.filed != nil {
		return e.filed
	}
	e.filed = map[string][]string{}
	rows, err := e.Index.DB().Query(`SELECT src, dst FROM edge WHERE rel = 'sources' AND dst >= '.import/' AND dst < '.import0'`)
	if err != nil {
		return e.filed
	}
	defer rows.Close()
	for rows.Next() {
		var src, dst string
		if rows.Scan(&src, &dst) == nil {
			if k, ok := memoryKey(dst); ok {
				e.filed[k] = append(e.filed[k], src)
			}
		}
	}
	return e.filed
}

func (e *Env) targetsByID(ids []string) targetSet {
	t := targetSet{ids: map[string]bool{}, paths: map[string]bool{}}
	for _, id := range ids {
		t.ids[id] = true
		var p string
		if e.Index.DB().QueryRow(`SELECT path FROM node WHERE id = ?`, id).Scan(&p) == nil {
			t.paths[p] = true
		}
	}
	return t
}

func (t targetSet) has(id string) bool {
	if t.ids[id] || t.paths[id] {
		return true
	}
	if i := strings.IndexByte(id, '#'); i > 0 {
		return t.paths[id[:i]] || t.ids[id[:i]]
	}
	return false
}

func (t targetSet) any(ids []string) bool { return slices.ContainsFunc(ids, t.has) }

// leakGuard hides what the brain could only know from the evaluated session or later: files that did not exist
// before it started, session notes from that day on, and notes whose sources name the session. The index itself is
// live (it keeps no history), so the ranker and the navigator see it through this guard: the brain as of the
// session's start.
type leakGuard struct {
	day     string
	sourced map[string]bool
	start   time.Time
	exist   *existence
}

func (e *Env) existence() *existence {
	if e.exist == nil {
		// an unreadable sessions folder only loses the "read by an earlier session" evidence
		e.exist, _ = newExistence(e.Index.Root, e.Index.DB())
	}
	return e.exist
}

func (e *Env) guardFor(sessionPath string, started time.Time) leakGuard {
	g := leakGuard{day: started.UTC().Format(time.DateOnly), sourced: map[string]bool{}, start: started, exist: e.existence()}
	rows, err := e.Index.DB().Query(`SELECT src FROM edge WHERE rel IN ('sources', 'learned_from') AND dst = ?`, sessionPath)
	if err != nil {
		return g
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			g.sourced[id] = true
		}
	}
	return g
}

func (g leakGuard) hides(id string) bool {
	if g.sourced[id] {
		return true
	}
	if g.exist != nil && !g.exist.nodeBefore(id, g.start) {
		return true
	}
	if !strings.HasPrefix(id, "sessions/") {
		return false
	}
	base := path.Base(id)
	return len(base) >= 10 && base[:10] >= g.day
}

func (e *Env) facts(id string) nodeFacts {
	if f, ok := e.nodes[id]; ok {
		return f
	}
	var f nodeFacts
	f.ok = e.Index.DB().QueryRow(`SELECT kind, pagerank FROM node WHERE id = ?`, id).Scan(&f.kind, &f.pagerank) == nil
	e.nodes[id] = f
	return f
}

// ---- the scripted navigators ----

// brainCalls replays an agent with the brain: call 1 brain_search(query); call 2 brain_show of the best hit; call 3
// brain_show of one neighbour (the best-ranked search hit among its neighbours, else its highest-PageRank neighbour).
// It returns the call whose output first names a target, or 0 when none does within three calls.
func (e *Env) brainCalls(query string, t targetSet, g leakGuard) int {
	hits, err := e.Index.Search(query, index.SearchOpts{Limit: searchLimit, Hide: g.hides})
	if err != nil {
		return 0
	}
	var ids []string
	for _, h := range hits {
		ids = append(ids, h.ID)
	}
	if t.any(ids) {
		return 1
	}
	if len(ids) == 0 {
		return 0
	}
	shown, err := e.Index.Show(ids[0])
	if err != nil {
		return 0
	}
	neighbours := edgeEnds(shown, g)
	if t.any(neighbours) {
		return 2
	}
	next := e.pickHop(neighbours, ids[1:])
	if next == "" {
		return 0
	}
	if again, err := e.Index.Show(next); err == nil && t.any(edgeEnds(again, g)) {
		return 3
	}
	return 0
}

func edgeEnds(n index.Node, g leakGuard) []string {
	var out []string
	for _, ed := range n.Out {
		if !g.hides(ed.Dst) {
			out = append(out, ed.Dst)
		}
	}
	for _, ed := range n.In {
		if !g.hides(ed.Src) {
			out = append(out, ed.Src)
		}
	}
	return out
}

// pickHop chooses the next node without looking at the targets: a neighbour the search also ranked, else the
// neighbour with the highest global PageRank among indexed docs, artifacts and folders.
func (e *Env) pickHop(neighbours, ranked []string) string {
	near := map[string]bool{}
	for _, n := range neighbours {
		near[n] = true
	}
	for _, id := range ranked {
		if near[id] {
			return id
		}
	}
	best, bestPR := "", -1.0
	for _, n := range neighbours {
		f := e.facts(n)
		if f.ok && (f.kind == "doc" || f.kind == "artifact" || f.kind == "dir") && (f.pagerank > bestPR || (f.pagerank == bestPR && n < best)) {
			best, bestPR = n, f.pagerank
		}
	}
	return best
}

// grepCorpus is the baseline without the brain: a case-insensitive whole-word grep over the same files the brain
// ranks (their paths and text), restricted to the query words so it stays small. A file scores the number of distinct
// query words it contains; ties go to the shorter path, then the path.
type grepCorpus struct {
	ids   []string
	index map[string]int32
	post  map[string][]int32
}

const rankableFile = `kind IN ('doc', 'artifact') AND canonical_of = '' AND status NOT IN ('superseded', 'retired')
	AND type NOT IN ('session', 'day') AND path NOT LIKE 'log/%' AND path NOT LIKE '%/log/%'
	AND path NOT LIKE '.archive/%' AND path NOT LIKE '%/.archive/%'`

func (e *Env) loadGrep(vocab map[string]bool) (*grepCorpus, error) {
	g := &grepCorpus{index: map[string]int32{}, post: map[string][]int32{}}
	rows, err := e.Index.DB().Query(`SELECT id FROM node WHERE ` + rankableFile + ` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		g.index[id] = int32(len(g.ids))
		g.ids = append(g.ids, id)
	}
	rows.Close()
	seen := make([]bool, len(g.ids))
	add := func(doc int32, text string) {
		for _, w := range words(text) {
			if vocab[w] {
				if p := g.post[w]; len(p) == 0 || p[len(p)-1] != doc {
					g.post[w] = append(p, doc)
				}
			}
		}
	}
	rows, err = e.Index.DB().Query(`SELECT id, body FROM fts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, body string
		if err := rows.Scan(&id, &body); err != nil {
			return nil, err
		}
		if doc, ok := g.index[id]; ok && !seen[doc] {
			seen[doc] = true
			add(doc, id+"\n"+body)
		}
	}
	for doc, done := range seen {
		if !done {
			add(int32(doc), g.ids[doc])
		}
	}
	for w := range g.post {
		slices.Sort(g.post[w])
		g.post[w] = slices.Compact(g.post[w])
	}
	return g, rows.Err()
}

func (g *grepCorpus) top(keywords []string, k int, hide func(string) bool) []string {
	count := map[int32]int{}
	for _, w := range keywords {
		for _, d := range g.post[w] {
			if hide == nil || !hide(g.ids[d]) {
				count[d]++
			}
		}
	}
	docs := make([]int32, 0, len(count))
	for d := range count {
		docs = append(docs, d)
	}
	sort.Slice(docs, func(i, j int) bool {
		a, b := docs[i], docs[j]
		if count[a] != count[b] {
			return count[a] > count[b]
		}
		if len(g.ids[a]) != len(g.ids[b]) {
			return len(g.ids[a]) < len(g.ids[b])
		}
		return g.ids[a] < g.ids[b]
	})
	out := make([]string, 0, k)
	for _, d := range docs[:min(k, len(docs))] {
		out = append(out, g.ids[d])
	}
	return out
}

// grepCalls replays an agent without the brain on the same budget: call 1 grep (searchLimit files); call 2 read the
// best file (what it links to); call 3 read one of those links (one grep also found, else the first by path).
func (e *Env) grepCalls(g *grepCorpus, keywords []string, t targetSet, guard leakGuard) int {
	found := g.top(keywords, searchLimit, guard.hides)
	if t.any(found) {
		return 1
	}
	if len(found) == 0 {
		return 0
	}
	links := rank.Visible(e.linksOf(found[0]), guard.hides)
	if t.any(links) {
		return 2
	}
	next := ""
	for _, f := range found[1:] {
		if slices.Contains(links, f) {
			next = f
			break
		}
	}
	if next == "" {
		for _, l := range links {
			if _, ok := g.index[l]; ok {
				next = l
				break
			}
		}
	}
	if next != "" && t.any(rank.Visible(e.linksOf(next), guard.hides)) {
		return 3
	}
	return 0
}

func (e *Env) linksOf(id string) []string {
	rows, err := e.Index.DB().Query(`SELECT dst FROM edge WHERE src = ? AND rel = 'links_to' ORDER BY dst`, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if rows.Scan(&d) == nil {
			out = append(out, d)
		}
	}
	return out
}

var grepStopwords = map[string]bool{"the": true, "and": true, "for": true, "with": true, "what": true, "how": true,
	"why": true, "does": true, "this": true, "that": true, "are": true, "was": true, "from": true, "into": true}

func words(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
}

// keywordsOf mirrors what brain search reads of a query (its first 16 words of two or more characters, stopwords
// dropped), so both arms see the same words.
func keywordsOf(query string) []string {
	var out []string
	for _, w := range words(query) {
		if len(out) == 16 {
			break
		}
		if len(w) >= 2 && !grepStopwords[w] {
			out = append(out, w)
		}
	}
	return out
}

// ---- the evals ----

type NavRow struct {
	ID         string   `json:"id"`
	Split      string   `json:"split"`
	Session    string   `json:"session"`
	Dispatched bool     `json:"dispatched"`
	Query      string   `json:"query_from"` // transcript | note (the text itself is never kept)
	Targets    int      `json:"targets"`
	Top3       bool     `json:"top3"`
	BoxTop3    bool     `json:"box_top3"` // a pointer lands in a target's work box
	Calls      int      `json:"calls"`    // tool calls to the first target; 0 = not within 3
	Pointers   []string `json:"pointers"`
	Error      string   `json:"error,omitempty"`
}

func (e *Env) Navigation() (Report, error) {
	rows := make([]NavRow, 0, len(e.sets.Navigation))
	for _, it := range e.sets.Navigation {
		r := NavRow{ID: it.ID, Split: SplitOf(it.Session), Session: it.Session, Dispatched: it.Dispatched, Targets: len(it.Targets)}
		q, src, err := e.navQuery(it)
		if err != nil {
			r.Error = err.Error()
			rows = append(rows, r)
			continue
		}
		r.Query = src
		t := e.targetsByPath(it.Targets)
		g := e.guardFor(it.Session, it.Started)
		r.Pointers = e.Ranker.Pointers(q, it.CWD, g.hides)
		r.Top3 = t.any(r.Pointers)
		r.BoxTop3 = sharesBox(it.Targets, r.Pointers)
		r.Calls = e.brainCalls(q, t, g)
		rows = append(rows, r)
	}
	ok := func(r NavRow) bool { return r.Error == "" }
	metrics, splits := perSplit(rows, func(r NavRow) string { return r.Split }, func(rows []NavRow) []Metric {
		return []Metric{
			split("navigation", "top-3 hit rate (pointer ranker)", rows, ok, func(r NavRow) bool { return r.Top3 }, func(r NavRow) bool { return r.Dispatched }, 0.70),
			split("navigation", "reachable within 3 tool calls", rows, ok, func(r NavRow) bool { return r.Calls > 0 }, func(r NavRow) bool { return r.Dispatched }, 0.80),
			split("navigation", "top-3 in a target's work box (diagnostic)", rows, ok, func(r NavRow) bool { return r.BoxTop3 }, func(r NavRow) bool { return r.Dispatched }, 0),
		}
	})
	worst := slices.Clone(rows)
	sort.SliceStable(worst, func(i, j int) bool {
		a, b := worst[i], worst[j]
		if (a.Calls == 0) != (b.Calls == 0) {
			return a.Calls == 0
		}
		if a.Top3 != b.Top3 {
			return !a.Top3
		}
		if a.Targets != b.Targets {
			return a.Targets > b.Targets
		}
		return a.ID < b.ID
	})
	worst = slices.DeleteFunc(worst, func(r NavRow) bool { return r.Top3 && r.Calls > 0 })
	return newReport(e, "navigation", metrics, splits, rows, worst[:min(20, len(worst))], e.navNotes(rows)), nil
}

// boxOf is a work doc's box (work/<box>), or "" outside work/.
func boxOf(id string) string {
	parts := strings.SplitN(id, "/", 3)
	if len(parts) < 3 || parts[0] != "work" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

func sharesBox(targets, pointers []string) bool {
	boxes := map[string]bool{}
	for _, t := range targets {
		if b := boxOf(t); b != "" {
			boxes[b] = true
		}
	}
	return slices.ContainsFunc(pointers, func(p string) bool { return boxes[boxOf(p)] })
}

func (e *Env) navNotes(rows []NavRow) []string {
	fromTranscript, failed := 0, 0
	for _, r := range rows {
		if r.Query == "transcript" {
			fromTranscript++
		}
		if r.Error != "" {
			failed++
		}
	}
	return []string{
		fmt.Sprintf("query: the session's first owner prompt, from the transcript for %d of %d items (the note's 500 characters otherwise); an Orca dispatch brief is cut to its TASK text", fromTranscript, len(rows)),
		fmt.Sprintf("%d items had no readable prompt and are left out of every rate", failed),
		"brain search reads only the first 16 words of a query, so long prompts are ranked on their opening words",
		"index: the live index seen as of the session's start: the ranker and the navigator skip files with no sign of existing before it (born, last modified, or read by a session that ended, earlier), session notes from its day on and notes sourced from it",
		"targets: indexed docs and data files the session read or wrote and that existed before it, without generic files (README, CLAUDE.md, MEMORY.md, …), folders, bulk-folder summaries, sessions, repo code or files the first prompt already holds (the brief it was pasted from); an imported memory is also hit through a note filed from it",
		"diagnostic: a pointer into the same work box as a target, though not a target file",
	}
}

type QARow struct {
	ID       string   `json:"id"`
	Split    string   `json:"split"`
	Kind     QAKind   `json:"kind"`
	Source   string   `json:"source"`
	Question string   `json:"question"`
	Target   string   `json:"target,omitempty"`
	Hit      bool     `json:"hit"` // found in the top 3, or abstained on an unknown
	TopicHit bool     `json:"topic_hit"`
	Returned []string `json:"returned"`
}

func (e *Env) QA() (Report, error) {
	rows := make([]QARow, 0, len(e.sets.QA))
	for _, it := range e.sets.QA {
		r := QARow{ID: it.ID, Split: SplitOf(it.ID), Kind: it.Kind, Source: it.Source, Question: it.Question, Target: it.Target}
		if it.Kind == QAUnknown {
			r.Returned = e.Ranker.Pointers(it.Question, "", nil)
			r.Hit = len(r.Returned) == 0
			rows = append(rows, r)
			continue
		}
		hits, err := e.Index.Search(it.Question, index.SearchOpts{Limit: 3})
		if err != nil {
			return Report{}, err
		}
		t := e.targetsByID(append([]string{it.Target}, it.Accept...))
		for _, h := range hits {
			r.Returned = append(r.Returned, h.ID)
		}
		r.Hit = t.any(r.Returned)
		if bare, err := e.Index.Search(it.Topic, index.SearchOpts{Limit: 3}); err == nil {
			r.TopicHit = slices.ContainsFunc(bare, func(h index.Hit) bool { return t.has(h.ID) })
		}
		if !r.Hit && it.Kind == QAAsOf {
			// search never ranks a superseded note; its successor's supersedes edge is the way to it
			for _, id := range r.Returned {
				if n, err := e.Index.Show(id); err == nil && slices.ContainsFunc(n.Out, func(ed index.Edge) bool { return ed.Rel == "supersedes" && t.has(ed.Dst) }) {
					r.Hit = true
				}
			}
		}
		rows = append(rows, r)
	}
	answerable := func(r QARow) bool { return r.Kind != QAUnknown }
	metrics, splits := perSplit(rows, func(r QARow) string { return r.Split }, func(rows []QARow) []Metric {
		metrics := []Metric{rate("qa", "recall@3 (brain_search)", rows, answerable, func(r QARow) bool { return r.Hit }, 0.80)}
		for _, src := range [][2]string{{"note", "notes"}, {"memory", "imported memories"}} {
			src, label := src[0], src[1]
			metrics = append(metrics, rate("qa", "recall@3, questions from "+label, rows,
				func(r QARow) bool { return answerable(r) && r.Source == src }, func(r QARow) bool { return r.Hit }, 0))
		}
		for _, k := range []QAKind{QAAsOf, QACurrent} {
			metrics = append(metrics, rate("qa", "recall@3, "+string(k)+" questions", rows,
				func(r QARow) bool { return r.Kind == k }, func(r QARow) bool { return r.Hit }, 0))
		}
		metrics = append(metrics, rate("qa", "recall@3, topic words only (diagnostic)", rows, answerable,
			func(r QARow) bool { return r.TopicHit }, 0))
		return append(metrics, rate("qa", "abstain on unknown (pointer ranker)", rows,
			func(r QARow) bool { return r.Kind == QAUnknown }, func(r QARow) bool { return r.Hit }, 0))
	})
	worst := slices.DeleteFunc(slices.Clone(rows), func(r QARow) bool { return r.Hit })
	sort.SliceStable(worst, func(i, j int) bool {
		if (worst[i].Kind == QAUnknown) != (worst[j].Kind == QAUnknown) {
			return worst[j].Kind == QAUnknown
		}
		return worst[i].ID < worst[j].ID
	})
	notes := []string{
		"questions: a template per type around the first clause of the note's (or memory's) own summary, so they share its words; recall here is an upper bound on recall for paraphrased questions",
		"a memory and the note filed from it answer each other's question",
		"brain_search has no abstention (it falls back to partial matches), so unknowns are asked of the pointer ranker",
		"diagnostic: the same questions without their template words; brain search requires every query word of two or more letters (bar a few stopwords), so a question's filler words ('do', 'know', 'about') can sink it",
	}
	return newReport(e, "qa", metrics, splits, rows, worst[:min(20, len(worst))], notes), nil
}

type MistakeRow struct {
	ID         string   `json:"id"`
	Split      string   `json:"split"`
	Session    string   `json:"session"`
	Dispatched bool     `json:"dispatched"`
	Lessons    int      `json:"lessons"`
	Surfaced   bool     `json:"surfaced"`
	Pointers   []string `json:"pointers"`
}

func (e *Env) Mistakes() (Report, error) {
	var rows []MistakeRow
	for _, it := range e.sets.Mistakes {
		if len(it.Lessons) == 0 {
			continue
		}
		var prompts []string
		if it.From == "transcript" {
			prompts = ownerPrompts(it.Transcript, it.Correction+1)
		} else if s, err := readSession(e.Index.Root, it.Session); err == nil {
			prompts = s.Prompts
		}
		if it.Correction < 1 || it.Correction >= len(prompts) {
			continue
		}
		r := MistakeRow{ID: it.ID, Split: SplitOf(it.Session), Session: it.Session, Dispatched: it.Dispatched, Lessons: len(it.Lessons)}
		r.Pointers = e.Ranker.Pointers(taskText(prompts[it.Correction-1]), it.CWD, nil)
		r.Surfaced = e.targetsByID(it.Lessons).any(r.Pointers)
		rows = append(rows, r)
	}
	metrics, splits := perSplit(rows, func(r MistakeRow) string { return r.Split }, func(rows []MistakeRow) []Metric {
		m := split("mistakes", "relevant lesson surfaced before the correction", rows, func(MistakeRow) bool { return true },
			func(r MistakeRow) bool { return r.Surfaced }, func(r MistakeRow) bool { return r.Dispatched }, 0)
		m.Mark, m.Pass = "> 0 (the replay without the brain surfaces none)", boolPtr(m.All.N > 0 && m.All.Value > 0)
		if len(rows) == 0 {
			m.NA = fmt.Sprintf("no lesson or anti-pattern names any of the %d corrected sessions among its sources yet (the dream has not learned from sessions)", len(e.sets.Mistakes))
			m.Pass = nil
		}
		return []Metric{m}
	})
	worst := slices.DeleteFunc(slices.Clone(rows), func(r MistakeRow) bool { return r.Surfaced })
	return newReport(e, "mistakes", metrics, splits, rows, worst[:min(20, len(worst))], []string{
		"the replayed moment is the owner prompt just before the correction; a hit is a pointer to a lesson or anti-pattern learned from that session",
	}), nil
}

type ABRow struct {
	ID         string `json:"id"`
	Split      string `json:"split"`
	Dispatched bool   `json:"dispatched"`
	BrainTop3  bool   `json:"brain_top3"`
	GrepTop3   bool   `json:"grep_top3"`
	BrainCalls int    `json:"brain_calls"` // 0 = not within 3
	GrepCalls  int    `json:"grep_calls"`
}

func (e *Env) AB() (Report, error) {
	type prepared struct {
		it  NavItem
		q   string
		kws []string
	}
	var items []prepared
	vocab := map[string]bool{}
	for _, it := range e.sets.Navigation {
		q, _, err := e.navQuery(it)
		if err != nil {
			continue
		}
		p := prepared{it: it, q: q, kws: keywordsOf(q)}
		for _, w := range p.kws {
			vocab[w] = true
		}
		items = append(items, p)
	}
	g, err := e.loadGrep(vocab)
	if err != nil {
		return Report{}, err
	}
	rows := make([]ABRow, 0, len(items))
	for _, p := range items {
		t := e.targetsByPath(p.it.Targets)
		guard := e.guardFor(p.it.Session, p.it.Started)
		rows = append(rows, ABRow{ID: p.it.ID, Split: SplitOf(p.it.Session), Dispatched: p.it.Dispatched,
			BrainTop3: t.any(e.Ranker.Pointers(p.q, p.it.CWD, guard.hides)), GrepTop3: t.any(g.top(p.kws, 3, guard.hides)),
			BrainCalls: e.brainCalls(p.q, t, guard), GrepCalls: e.grepCalls(g, p.kws, t, guard)})
	}
	all := func(ABRow) bool { return true }
	dispatched := func(r ABRow) bool { return r.Dispatched }
	calls := func(n int) float64 {
		if n == 0 {
			return missCalls
		}
		return float64(n)
	}
	metrics, splits := perSplit(rows, func(r ABRow) string { return r.Split }, func(rows []ABRow) []Metric {
		return []Metric{
			lift("ab", "top-3 hit rate lift (brain − grep)", rows, dispatched, func(r ABRow) float64 { return b2f(r.BrainTop3) - b2f(r.GrepTop3) }),
			lift("ab", "within-3-calls lift (brain − grep)", rows, dispatched, func(r ABRow) float64 { return b2f(r.BrainCalls > 0) - b2f(r.GrepCalls > 0) }),
			mean("ab", "tool calls to first relevant file, brain (miss = 4)", rows, dispatched, func(r ABRow) float64 { return calls(r.BrainCalls) }),
			mean("ab", "tool calls to first relevant file, grep (miss = 4)", rows, dispatched, func(r ABRow) float64 { return calls(r.GrepCalls) }),
			split("ab", "top-3 hit rate, grep baseline", rows, all, func(r ABRow) bool { return r.GrepTop3 }, dispatched, 0),
		}
	})
	worst := slices.Clone(rows)
	sort.SliceStable(worst, func(i, j int) bool {
		di, dj := calls(worst[i].BrainCalls)-calls(worst[i].GrepCalls), calls(worst[j].BrainCalls)-calls(worst[j].GrepCalls)
		if di != dj {
			return di > dj
		}
		return worst[i].ID < worst[j].ID
	})
	worst = slices.DeleteFunc(worst, func(r ABRow) bool { return calls(r.BrainCalls) <= calls(r.GrepCalls) && (r.BrainTop3 || !r.GrepTop3) })
	return newReport(e, "ab", metrics, splits, rows, worst[:min(20, len(worst))], []string{
		"same navigation items and query words for both arms; budget-matched: the same number of results per call (8 for a search or grep, 3 for top-3) and the same 3-call budget",
		"grep arm: whole-word, case-insensitive match over the same files' paths and text, ranked by distinct query words found; a read reveals the file's links",
		"worst rows: items where the brain needed more calls than grep, or grep had a top-3 hit the brain missed",
	}), nil
}

func (e *Env) Run(name string) (Report, error) {
	switch name {
	case "navigation":
		return e.Navigation()
	case "qa":
		return e.QA()
	case "mistakes":
		return e.Mistakes()
	case "ab":
		return e.AB()
	}
	return Report{}, fmt.Errorf("unknown eval %q (navigation, qa, mistakes, ab)", name)
}

var Names = []string{"navigation", "qa", "mistakes", "ab"}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func boolPtr(b bool) *bool { return &b }
