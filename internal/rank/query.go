package rank

import (
	"database/sql"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
)

var TokenRE = regexp.MustCompile(`[\p{L}\p{N}_]+`)

// stopwords: function words, question templates and the chatter of prompts ("please", "now", "can you").
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
		www com github true`) {
		stopwords[w] = true
	}
}

func Stopword(w string) bool { return stopwords[w] }

// Words are the query's distinct lowercase words in order, without stopwords and one-letter words.
func Words(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range TokenRE.FindAllString(strings.ToLower(text), -1) {
		if len(w) < 2 || stopwords[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

const vocabTable = `CREATE VIRTUAL TABLE IF NOT EXISTS temp.fts_vocab USING fts5vocab(main, 'fts', 'row')`

// DocFreqs guesses each word's document frequency through its porter stem: the word itself, a prefix at most three
// letters shorter, or the word with a final y turned to i (one vocabulary range read per word). Unknown words get 0.
func DocFreqs(db *sql.DB, words []string) (map[string]int, error) {
	out := map[string]int{}
	if len(words) == 0 {
		return out, nil
	}
	if _, err := db.Exec(vocabTable); err != nil {
		return nil, err
	}
	for _, w := range words {
		lo := w[:max(min(3, len(w)), len(w)-3)]
		rows, err := db.Query(`SELECT term, doc FROM temp.fts_vocab WHERE term >= ? AND term <= ?`, lo, w)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var term string
			var doc int
			if err := rows.Scan(&term, &doc); err != nil {
				rows.Close()
				return nil, err
			}
			if strings.HasPrefix(w, term) || (strings.HasSuffix(w, "y") && term == strings.TrimSuffix(w, "y")+"i") {
				out[w] = max(out[w], doc)
			}
		}
		rows.Close()
	}
	return out, nil
}

// Salient splits the words worth a BM25 pass, each in query order: rare ones, matched in every column, are the
// rarest first, at most n and within budget postings, but never fewer than minTerms while words are left (BM25
// scores every row any term matches, so common words cost the most while telling documents apart the least); the
// rest of the first n are common, matched only in the short columns. Words the index has never seen are dropped
// unless nothing else is left.
func Salient(words []string, df map[string]int, n, budget int) (rare, common []string) {
	type term struct {
		w   string
		df  int
		pos int
	}
	var known, unknown []term
	for i, w := range words {
		if df[w] > 0 {
			known = append(known, term{w, df[w], i})
		} else {
			unknown = append(unknown, term{w, 0, i})
		}
	}
	if len(known) == 0 {
		known = unknown
	}
	sort.SliceStable(known, func(i, j int) bool { return known[i].df < known[j].df })
	known = known[:min(n, len(known))]
	split := 0
	for split < len(known) && (known[split].df <= budget || split < minTerms) {
		budget -= known[split].df
		split++
	}
	inOrder := func(ts []term) []string {
		ts = slices.Clone(ts)
		sort.SliceStable(ts, func(i, j int) bool { return ts[i].pos < ts[j].pos })
		out := make([]string, len(ts))
		for i, t := range ts {
			out[i] = t.w
		}
		return out
	}
	return inOrder(known[:split]), inOrder(known[split:])
}

// ---- mentions: paths and folders a prompt names ----

var (
	pathRE  = regexp.MustCompile(`[\w.~@-]*/[\w./~@-]*\w|\b[\w.-]+\.(?:md|html|json|jsonl|txt|csv|tsv|py|go|toml|ya?ml|sh)\b`)
	boxRE   = regexp.MustCompile(`\b[a-zA-Z0-9]+(?:[-_][a-zA-Z0-9]+)+\b`)
	homeRE  = regexp.MustCompile(`^(?:/Users/[^/]+|/home/[^/]+|~)/`)
	workDir = "work/"
)

const maxMentions = 40

// Mentions resolves the paths and work-box names a text spells out to node ids, in the order they first appear: a
// saahil-docs path is its work/ path, a brain path loses its home prefix, and a relative path ("plans/SPEC.md") is
// tried under the boxes the text names and the given ones.
func Mentions(db *sql.DB, text string, boxes []string) ([]string, error) {
	var rel []string
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	tokens := pathRE.FindAllString(text, maxMentions)
	for _, m := range boxRE.FindAllString(text, maxMentions) {
		if !strings.Contains(m, "/") {
			tokens = append(tokens, workDir+m)
		}
	}
	var haveBox = map[string]bool{}
	for _, b := range boxes {
		haveBox[b] = true
	}
	for _, m := range tokens {
		p, absolute := presented(m)
		if p == "" {
			continue
		}
		id, err := lookupPath(db, p)
		if err != nil {
			return nil, err
		}
		if id == "" {
			if !absolute && !strings.HasPrefix(p, workDir) {
				rel = append(rel, p)
			}
			continue
		}
		add(id)
		if b := BoxOf(id); b != "" && !haveBox[b] {
			haveBox[b] = true
			boxes = append(boxes, b)
		}
	}
	for _, p := range rel {
		// a brief may spell its box as a placeholder ("BOX/plans/SPEC.md"): retry without the first segment
		tries := []string{p}
		if _, rest, ok := strings.Cut(p, "/"); ok && rest != "" {
			tries = append(tries, rest)
		}
	box:
		for _, b := range boxes {
			for _, t := range tries {
				id, err := lookupPath(db, workDir+b+"/"+t)
				if err != nil {
					return nil, err
				}
				if id != "" {
					add(id)
					break box
				}
			}
		}
	}
	return out, nil
}

// presented turns a spelled path into the index's form; absolute reports a path anchored at a root.
func presented(m string) (string, bool) {
	m = strings.TrimRight(m, "./")
	if i := strings.Index(m, "saahil-docs/"); i >= 0 {
		return workDir + m[i+len("saahil-docs/"):], true
	}
	if loc := homeRE.FindStringIndex(m); loc != nil {
		rest := m[loc[1]:]
		if b, ok := strings.CutPrefix(rest, "brain/"); ok {
			return b, true
		}
		if strings.HasPrefix(rest, ".claude/projects/") {
			parts := strings.SplitN(strings.TrimPrefix(rest, ".claude/projects/"), "/", 3)
			if len(parts) == 3 && parts[1] == "memory" {
				return "memory-import/" + parts[0] + "/" + parts[2], true
			}
		}
		return "", true
	}
	return strings.TrimPrefix(m, "./"), strings.HasPrefix(m, "/")
}

func lookupPath(db *sql.DB, p string) (string, error) {
	var id string
	err := db.QueryRow(`SELECT id FROM node WHERE path = ? AND kind IN ('doc', 'artifact', 'dir') LIMIT 1`, p).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

// BoxOf is a work node's box name, or "" outside work/.
func BoxOf(id string) string {
	parts := strings.SplitN(id, "/", 3)
	if len(parts) < 2 || parts[0] != "work" {
		return ""
	}
	return parts[1]
}

// ---- anchors: what to open first in a folder ----

// anchorRank orders a folder's documents: the baselines a box keeps (CARD, STATE, GOAL), then its live spec or plan,
// its report or readme, then other docs; data files last.
func anchorRank(kind, typ, p string) int {
	if kind != "doc" {
		return 8
	}
	base := strings.ToUpper(path.Base(p))
	switch {
	case typ == "card":
		return 0
	case typ == "state", strings.HasPrefix(base, "GOAL"):
		return 1
	case typ == "spec", typ == "plan":
		return 2
	case typ == "report", typ == "readme":
		return 3
	case typ == "handoff", typ == "review":
		return 4
	}
	return 5
}

// Anchors lists a folder's rankable documents (and those of its plans/ folder) in anchorRank order, newest first
// within a rank, at most n.
func Anchors(db *sql.DB, dir string, n int) ([]string, error) {
	rows, err := db.Query(`SELECT n.id, n.kind, n.type, n.path FROM edge e JOIN node n ON n.id = e.src `+rankable+`
		AND e.rel = 'part_of' AND (e.dst = ?1 OR e.dst = ?1 || '/plans') ORDER BY n.mtime DESC, n.id LIMIT 400`, dir)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type doc struct {
		id   string
		rank int
	}
	var docs []doc
	for rows.Next() {
		var id, kind, typ, p string
		if err := rows.Scan(&id, &kind, &typ, &p); err != nil {
			return nil, err
		}
		docs = append(docs, doc{id, anchorRank(kind, typ, p)})
	}
	sort.SliceStable(docs, func(i, j int) bool { return docs[i].rank < docs[j].rank })
	var out []string
	for i := 0; i < len(docs) && i < n; i++ {
		out = append(out, docs[i].id)
	}
	return out, rows.Err()
}

// ---- folders a session's name points at ----

var (
	yearRE = regexp.MustCompile(`^(19|20)\d\d$`)
	dateRE = regexp.MustCompile(`(19|20)\d\d-?[01]\d-?[0-3]\d(T\d+Z?)?`)
)

// folderNoise are words every worktree or folder path carries.
var folderNoise = map[string]bool{"saahil": true, "docs": true, "worktree": true, "worktrees": true, "workspaces": true,
	"main": true, "data": true, "acquisition": true, "md": true}

var camelRE = regexp.MustCompile(`(\p{Ll})(\p{Lu})`)

// FolderWords are a folder or file name's words for matching names: camelCase split, lowercase, without noise,
// stopwords, dates and years, keeping ids like 7266, b2, the 5 of cascade-5 or the e of e-design.
func FolderWords(name string) []string {
	var out []string
	name = dateRE.ReplaceAllString(camelRE.ReplaceAllString(name, "$1 $2"), " ")
	for _, w := range TokenRE.FindAllString(strings.ToLower(name), -1) {
		if folderNoise[w] || (len(w) > 1 && stopwords[w]) || yearRE.MatchString(w) {
			continue
		}
		if !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

const (
	maxFolderDepth = 3 // below the box: work/<box>/a/b/c
	maxSameName    = 3 // a brief, its evidence folder and its scratch folder share a name; more is a pattern
)

// MatchFolders finds the folders and documents inside the given boxes whose name (a document's without its
// extension) is spelled by one of the names (a worktree, a brief's file name): every word of the folder's name is
// among the name's words, one of them is distinctive (has a digit or four letters), and no other folder or document
// in the box has the same name more than twice; a one-word name must carry a digit (b2, f41). The strength is ¾
// for a one-word name and 1 for longer ones: a family prefix many sibling worktrees share (image-ovn-attack-) must
// not dilute an exact match.
func MatchFolders(db *sql.DB, boxes []string, names []string) (map[string]float64, error) {
	out := map[string]float64{}
	var nameWords [][]string
	var distinctive []string
	for _, n := range names {
		if w := FolderWords(n); len(w) > 0 {
			nameWords = append(nameWords, w)
			distinctive = append(distinctive, slices.DeleteFunc(slices.Clone(w), func(w string) bool { return !isDistinctive(w) })...)
		}
	}
	if len(distinctive) == 0 {
		return out, nil
	}
	// a folder matches only if one of its words is a distinctive name word: a substring test skips the rest cheaply
	maybe := func(name string) bool {
		name = strings.ToLower(name)
		return slices.ContainsFunc(distinctive, func(w string) bool { return strings.Contains(name, w) })
	}
	for _, box := range boxes {
		prefix := workDir + box + "/"
		// unary + keeps the planner on the path range, not node_kind (every dir and doc)
		rows, err := db.Query(`SELECT path FROM node WHERE +kind IN ('dir', 'doc') AND path > ? AND path < ?`, prefix, workDir+box+"0")
		if err != nil {
			return nil, err
		}
		var dirs []string
		count := map[string]int{}
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				rows.Close()
				return nil, err
			}
			count[stem(p)]++
			if strings.Count(p[len(prefix):], "/") < maxFolderDepth {
				dirs = append(dirs, p)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for _, d := range dirs {
			if count[stem(d)] > maxSameName || !maybe(stem(d)) {
				continue
			}
			dw := FolderWords(stem(d))
			// one generic word ("spec", "notes") names too much; a one-word name needs a digit (b2, f41)
			if !slices.ContainsFunc(dw, isDistinctive) || len(dw) == 1 && !strings.ContainsAny(dw[0], "0123456789") {
				continue
			}
			for _, nw := range nameWords {
				if !subset(dw, nw) {
					continue
				}
				out[d] = max(out[d], 0.5+0.25*float64(min(len(dw), 2)))
			}
		}
	}
	return out, nil
}

func subset(a, b []string) bool {
	for _, w := range a {
		if !slices.Contains(b, w) {
			return false
		}
	}
	return len(a) > 0
}

func stem(p string) string { b := path.Base(p); return strings.TrimSuffix(b, path.Ext(b)) }

func isDistinctive(w string) bool { return len(w) >= 4 || strings.ContainsAny(w, "0123456789") }
