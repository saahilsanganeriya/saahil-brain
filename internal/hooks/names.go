package hooks

import (
	"bufio"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"saahil.local/brain/internal/parse"
)

// Names is the alias gate's dictionary: every name a node answers to (aliases, hub and note ids, multi-word
// titles), as lowercase word sequences. It is built once per index update and cached per machine, so a prompt
// pays one file read, never a file per note.
type Names struct {
	byPhrase map[string][]nameRef
	maxWords int
	df       map[string]int // document frequency of every FTS stem in ≥ 100 rows; rarer stems are absent
	rows     int
}

type nameRef struct {
	id string
	w  float64 // 1 for a multi-word alias or hub id, 0.8 for a note id or title, 0.5 for a one-word alias or hub id
}

const (
	maxNameWords   = 6
	maxAliasReads  = 4000
	maxNamedIDs    = 100
	namesCacheFile = "names.tsv"
	commonShare    = 100 // a stem in more than 1/commonShare of the rows is common
	minDF          = 100 // stems rarer than this are not stored: they are cheap to query
	maxQueryTerms  = 8
	minTerms       = 4     // below this many usable words, common ones fill in
	termBudget     = 12000 // estimated postings one query may read: BM25 scores every row any term matches
)

func (nm *Names) add(phrase, id string, w float64) {
	words := tokenRE.FindAllString(strings.ToLower(phrase), -1)
	if len(words) == 0 || len(words) > maxNameWords || (len(words) == 1 && (len(words[0]) < 3 || stopwords[words[0]] || isDigits(words[0]))) {
		return
	}
	if len(words) == 1 {
		w = min(w, 0.5)
	}
	key := strings.Join(words, " ")
	for i, r := range nm.byPhrase[key] {
		if r.id == id {
			nm.byPhrase[key][i].w = max(r.w, w)
			return
		}
	}
	nm.byPhrase[key] = append(nm.byPhrase[key], nameRef{id, w})
	nm.maxWords = max(nm.maxWords, len(words))
}

// BuildNames reads every doc node's names from the index; aliases come from the files (readFile by node path),
// since the index keeps them as one run of words.
func BuildNames(db *sql.DB, readFile func(string) []byte) (*Names, error) {
	nm := &Names{byPhrase: map[string][]nameRef{}, df: map[string]int{}}
	// notes and hubs first: their aliases are curated, so they are read before any doc's
	rows, err := db.Query(`SELECT n.id, n.type, n.path, n.title, f.aliases != '' FROM node n JOIN fts f ON f.rowid = n.rowid
		WHERE n.kind = 'doc' AND n.type NOT IN ('session', 'day')
		ORDER BY n.path NOT LIKE 'notes/%' AND n.path NOT LIKE 'hubs/%', n.path`)
	if err != nil {
		return nil, err
	}
	type aliased struct{ id, path string }
	var withAliases []aliased
	for rows.Next() {
		var id, typ, p, title string
		var hasAliases bool
		if err := rows.Scan(&id, &typ, &p, &title, &hasAliases); err != nil {
			rows.Close()
			return nil, err
		}
		switch {
		case typ == "hub":
			nm.add(strings.ReplaceAll(id, "-", " "), id, 1)
		case strings.HasPrefix(p, "notes/"):
			nm.add(strings.ReplaceAll(strings.TrimPrefix(id, typ+"-"), "-", " "), id, 0.8)
		}
		if t := strings.TrimSuffix(title, ".md"); len(tokenRE.FindAllString(t, -1)) > 1 {
			nm.add(t, id, 0.8)
		}
		if hasAliases && len(withAliases) < maxAliasReads {
			withAliases = append(withAliases, aliased{id, p})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, a := range withAliases {
		if readFile == nil {
			break
		}
		for _, alias := range parse.Markdown(readFile(a.path)).Aliases {
			nm.add(alias, a.id, 1)
		}
	}
	return nm, nm.loadDF(db)
}

// loadDF reads the document frequency of every stem in ≥ 100 rows (a full vocabulary scan, about a second on the
// real index).
func (nm *Names) loadDF(db *sql.DB) error {
	if err := db.QueryRow(`SELECT count(*) FROM fts`).Scan(&nm.rows); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS temp.fts_vocab USING fts5vocab(main, 'fts', 'row')`); err != nil {
		return err
	}
	rows, err := db.Query(`SELECT term, doc FROM temp.fts_vocab WHERE doc >= ?`, minDF)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var term string
		var doc int
		if err := rows.Scan(&term, &doc); err != nil {
			return err
		}
		nm.df[term] = doc
	}
	return rows.Err()
}

// docFreq guesses a word's document frequency through its porter stem: the word itself, a prefix at most three
// letters shorter, or the word with a final y turned to i. Unknown words are rare.
func (nm *Names) docFreq(word string) int {
	best := nm.df[word]
	if strings.HasSuffix(word, "y") {
		best = max(best, nm.df[strings.TrimSuffix(word, "y")+"i"])
	}
	for k := max(3, len(word)-3); k < len(word); k++ {
		best = max(best, nm.df[word[:k]])
	}
	if best == 0 {
		return minDF / 2
	}
	return best
}

// loadNames returns the cached dictionary and whether it predates the index's last update. A stale or missing
// cache is rebuilt by RebuildNames in the background; a prompt never pays for the build.
func (e Env) loadNames(db *sql.DB) (*Names, bool) {
	nm, stamp, ok := readNames(filepath.Join(e.StateDir, namesCacheFile))
	if !ok {
		return nil, true
	}
	return nm, stamp != indexStamp(db)
}

func indexStamp(db *sql.DB) string {
	var stamp string
	_ = db.QueryRow(`SELECT value FROM meta WHERE key = 'run.updated_at'`).Scan(&stamp)
	return stamp
}

const namesLockTTL = 2 * time.Minute

// RebuildNames builds the dictionary for the current index and caches it, with the work boxes' often-read files; a
// second rebuild while one runs is a no-op.
func RebuildNames(env Env) error {
	if err := os.MkdirAll(env.StateDir, 0o700); err != nil {
		return err
	}
	lock := filepath.Join(env.StateDir, namesCacheFile+".lock")
	if fi, err := os.Stat(lock); err == nil && env.Now().Sub(fi.ModTime()) < namesLockTTL {
		return nil
	}
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		return err
	}
	defer os.Remove(lock)
	ix, err := env.openIndex()
	if err != nil || ix == nil {
		return err
	}
	defer ix.Close()
	stamp := indexStamp(ix.DB())
	nm, err := BuildNames(ix.DB(), func(p string) []byte {
		b, _ := readHead(env.absPath(p), 8<<10)
		return []byte(b)
	})
	if err != nil {
		return err
	}
	if err := nm.save(filepath.Join(env.StateDir, namesCacheFile), stamp); err != nil {
		return err
	}
	return writeOften(ix.DB(), env.Root, filepath.Join(env.StateDir, oftenCacheFile), stamp)
}

func readNames(path string) (*Names, string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	if !sc.Scan() || !strings.HasPrefix(sc.Text(), "# ") {
		return nil, "", false
	}
	stamp := strings.TrimPrefix(sc.Text(), "# ")
	nm := &Names{byPhrase: map[string][]nameRef{}, df: map[string]int{}}
	for sc.Scan() {
		if line, ok := strings.CutPrefix(sc.Text(), "~"); ok {
			stem, n, _ := strings.Cut(line, "\t")
			if stem == "" {
				nm.rows, _ = strconv.Atoi(n)
			} else {
				nm.df[stem], _ = strconv.Atoi(n)
			}
			continue
		}
		phrase, rest, _ := strings.Cut(sc.Text(), "\t")
		id, ws, _ := strings.Cut(rest, "\t")
		w, err := strconv.ParseFloat(ws, 64)
		if err != nil {
			return nil, "", false
		}
		nm.byPhrase[phrase] = append(nm.byPhrase[phrase], nameRef{id, w})
		nm.maxWords = max(nm.maxWords, strings.Count(phrase, " ")+1)
	}
	return nm, stamp, sc.Err() == nil
}

func (nm *Names) save(path, stamp string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	fmt.Fprintf(w, "# %s\n", stamp)
	fmt.Fprintf(w, "~\t%d\n", nm.rows)
	for stem, n := range nm.df {
		fmt.Fprintf(w, "~%s\t%d\n", stem, n)
	}
	for phrase, refs := range nm.byPhrase {
		for _, r := range refs {
			fmt.Fprintf(w, "%s\t%s\t%g\n", phrase, r.id, r.w)
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// commonName is the weight share of a name made only of common words ("saahil docs", "docs register"): every
// brief and constraint block repeats such words, so they say little about what this prompt is about.
const commonName = 0.4

// common: the word is in more than 1/commonShare of the rows.
func (nm *Names) common(word string) bool {
	return nm.rows > 0 && nm.docFreq(word)*commonShare > nm.rows
}

// match returns the ids named in words (any 1–6 word run) with their best weight, and the matched phrases.
func (nm *Names) match(words []string) (map[string]float64, []string) {
	hits, seen := map[string]float64{}, map[string]bool{}
	var phrases []string
	for i := range words {
		for n := 1; n <= nm.maxWords && i+n <= len(words); n++ {
			key := strings.Join(words[i:i+n], " ")
			refs, ok := nm.byPhrase[key]
			if !ok {
				continue
			}
			if !seen[key] {
				seen[key] = true
				phrases = append(phrases, key)
			}
			w := 1.0
			if !slices.ContainsFunc(words[i:i+n], func(w string) bool { return !nm.common(w) }) {
				w = commonName
			}
			for _, r := range refs {
				hits[r.id] = max(hits[r.id], r.w*w)
			}
		}
	}
	return hits, phrases
}

// ---- the prompt ----

var (
	pastedTagRE = regexp.MustCompile(`</?pasted_content[^>]*>`)
	ticketRE    = regexp.MustCompile(`\b[A-Z][A-Z0-9]+-\d+\b|#\d{3,}`)
	pathRE      = regexp.MustCompile(`[\w.~-]*/[\w./~-]+`)
)

// orcaTask is where an Orca dispatch brief's own task starts; everything before it is the fixed preamble.
const orcaTask = "=== TASK ==="

// cleanPrompt drops what every Orca brief shares (the preamble, the paste wrapper) so it does not drown the task.
func cleanPrompt(p string) string {
	if i := strings.Index(p, orcaTask); i >= 0 {
		p = p[i+len(orcaTask):]
	}
	return pastedTagRE.ReplaceAllString(p, " ")
}

// salientTerms picks ≤ 12 query words from the whole prompt. BM25 scores every row any term matches, so the cost
// is the terms' document frequencies: words of matched names go first, then ids (XYZ-123, #9333) and path
// segments, then the rest rarest first, within a postings budget. Words in more than 1% of rows and short numbers
// (dates, counts) are left out unless nothing else is left.
func (nm *Names) salientTerms(text string, phrases []string) []string {
	type term struct {
		w     string
		df    int
		named bool
		pos   int
	}
	var cands, common []term
	seen := map[string]bool{}
	add := func(t string, named, id bool) {
		if seen[t] || stopwords[t] || len(t) < 2 || (len(t) == 2 && !hasDigit(t)) || (isDigits(t) && !(id && len(t) >= 3)) {
			return
		}
		seen[t] = true
		c := term{w: t, df: nm.docFreq(t), named: named, pos: len(seen)}
		if nm.rows > 0 && c.df*commonShare > nm.rows {
			common = append(common, c)
		} else {
			cands = append(cands, c)
		}
	}
	for _, p := range phrases {
		for _, w := range strings.Fields(p) {
			add(w, true, false)
		}
	}
	for _, m := range ticketRE.FindAllString(text, 20) {
		for _, w := range tokenRE.FindAllString(strings.ToLower(m), -1) {
			add(w, true, true)
		}
	}
	for _, m := range pathRE.FindAllString(text, 40) {
		for _, w := range tokenRE.FindAllString(strings.ToLower(m), -1) {
			add(w, false, false)
		}
	}
	for _, w := range tokenRE.FindAllString(strings.ToLower(text), -1) {
		add(w, false, false)
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].named != cands[j].named {
			return cands[i].named
		}
		return cands[i].df < cands[j].df || (cands[i].df == cands[j].df && cands[i].pos < cands[j].pos)
	})
	if len(cands) < minTerms {
		sort.SliceStable(common, func(i, j int) bool { return common[i].df < common[j].df })
		cands = append(cands, common[:min(len(common), minTerms-len(cands))]...)
	}
	var out []string
	budget := termBudget
	for _, c := range cands {
		if len(out) == maxQueryTerms || (c.df > budget && len(out) >= minTerms) {
			break
		}
		budget -= c.df
		out = append(out, c.w)
	}
	return out
}
