package hooks

import (
	"bufio"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// The files earlier sessions on a work box read most, counted from their notes' works and touched lists: a worker
// on a box usually needs what the box's earlier workers kept opening (another box's STATE, a ledger), which neither
// the CARD nor a prompt names. The live hooks read the counts from a cache RebuildNames writes; a replay counts only
// the session notes it may see.
const (
	oftenListed      = 5 // per box in the digest
	oftenCached      = 8 // per box in the cache: the digest skips what it quotes
	oftenMinSessions = 2
	oftenChars       = 800
	oftenCacheFile   = "often.tsv"
	sessionReadBytes = 16 << 10 // touched holds up to 60 paths
)

type oftenFile struct {
	path     string
	sessions int
}

// oftenSource gives a work box's often-read files, most sessions first; nil gives none.
type oftenSource func(box string) []oftenFile

// sessionReads is what one session note says it worked on and read.
type sessionReads struct {
	note           string // brain path of the note
	works, touched []string
}

// readSessionReads parses the works and touched lists of every session note under root.
func readSessionReads(root string) []sessionReads {
	files, _ := filepath.Glob(filepath.Join(root, "sessions", "*", "[0-9][0-9][0-9][0-9]", "[0-9][0-9]", "*.md"))
	var out []sessionReads
	for _, f := range files {
		head, err := readHead(f, sessionReadBytes)
		if err != nil || !strings.HasPrefix(head, "---") {
			continue
		}
		var s sessionReads
		sc := bufio.NewScanner(strings.NewReader(head))
		sc.Buffer(make([]byte, 0, 64<<10), sessionReadBytes)
		for i := 0; sc.Scan(); i++ {
			line := sc.Text()
			if i > 0 && line == "---" {
				break
			}
			if v, ok := strings.CutPrefix(line, "works:"); ok {
				s.works = flowList(v)
			} else if v, ok := strings.CutPrefix(line, "touched:"); ok {
				s.touched = flowList(v)
			}
		}
		if rel, err := filepath.Rel(root, f); err == nil && len(s.works) > 0 && len(s.touched) > 0 {
			s.note = filepath.ToSlash(rel)
			out = append(out, s)
		}
	}
	return out
}

// countOften counts, for box, the brain files each visible session on it read (once per session), keeps those read
// by at least oftenMinSessions that may be ranked, and returns the oftenCached most read.
func countOften(db *sql.DB, reads []sessionReads, box string, visible func(string) bool) ([]oftenFile, error) {
	count := map[string]int{}
	for _, s := range reads {
		if !slices.Contains(s.works, box) || (visible != nil && !visible(s.note)) {
			continue
		}
		seen := map[string]bool{}
		for _, p := range s.touched {
			if !seen[p] && (strings.HasPrefix(p, "work/") || strings.HasPrefix(p, "notes/") || strings.HasPrefix(p, "hubs/")) {
				seen[p] = true
				count[p]++
			}
		}
	}
	var args []any
	for p, n := range count {
		if n >= oftenMinSessions {
			args = append(args, p)
		}
	}
	if len(args) == 0 {
		return nil, nil
	}
	// folders (a bulk folder's summary too), files gone since, byte copies and superseded notes drop out here
	rows, err := db.Query(`SELECT DISTINCT n.path FROM node n `+rankable+` AND n.kind IN ('doc', 'artifact')
		AND n.type NOT IN ('bulk', 'repo') AND n.path IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []oftenFile
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, oftenFile{p, count[p]})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].sessions > out[j].sessions || (out[i].sessions == out[j].sessions && out[i].path < out[j].path)
	})
	return out[:min(len(out), oftenCached)], rows.Err()
}

// writeOften caches every box's often-read files for the live hooks, stamped like the names cache.
func writeOften(db *sql.DB, root, path, stamp string) error {
	reads := readSessionReads(root)
	var boxes []string
	for _, s := range reads {
		boxes = appendNew(boxes, s.works...)
	}
	sort.Strings(boxes)
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", stamp)
	for _, box := range boxes {
		files, err := countOften(db, reads, box, nil)
		if err != nil {
			return err
		}
		for _, f := range files {
			fmt.Fprintf(&b, "%s\t%s\t%d\n", box, f.path, f.sessions)
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// oftenCache reads the cache RebuildNames wrote; without one the digest lists nothing.
func (e Env) oftenCache() oftenSource {
	f, err := os.Open(filepath.Join(e.StateDir, oftenCacheFile))
	if err != nil {
		return nil
	}
	defer f.Close()
	byBox := map[string][]oftenFile{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		box, rest, _ := strings.Cut(sc.Text(), "\t")
		p, n, _ := strings.Cut(rest, "\t")
		if sessions, err := strconv.Atoi(n); err == nil && !strings.HasPrefix(box, "#") {
			byBox[box] = append(byBox[box], oftenFile{p, sessions})
		}
	}
	return func(box string) []oftenFile { return byBox[box] }
}

// sessionReadsMemo keeps one root's session notes parsed: a replay rebuilds the digest once per evaluated session.
var sessionReadsMemo struct {
	sync.Mutex
	root  string
	reads []sessionReads
}

// oftenAsOf counts from the session notes visible says a replayed session could have seen.
func oftenAsOf(env Env, db *sql.DB, visible func(string) bool) oftenSource {
	sessionReadsMemo.Lock()
	if sessionReadsMemo.root != env.Root || sessionReadsMemo.reads == nil {
		sessionReadsMemo.root, sessionReadsMemo.reads = env.Root, readSessionReads(env.Root)
	}
	reads := sessionReadsMemo.reads
	sessionReadsMemo.Unlock()
	return func(box string) []oftenFile {
		files, _ := countOften(db, reads, box, visible)
		return files
	}
}

// oftenText lists a box's often-read files the digest does not already quote, as brain-path links.
func oftenText(files []oftenFile, quoted []string) string {
	var b strings.Builder
	listed := 0
	for _, f := range files {
		if listed == oftenListed || slices.Contains(quoted, f.path) {
			continue
		}
		if listed == 0 {
			b.WriteString("Read most by earlier sessions on this work (sessions that read it):\n")
		}
		fmt.Fprintf(&b, "- [[%s]] (%d)\n", f.path, f.sessions)
		listed++
	}
	return b.String()
}
