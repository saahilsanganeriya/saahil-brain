package evals

import (
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"saahil.local/brain/internal/config"
)

// fileTimes is statTimes, swapped in tests: a test cannot set a file's birth time.
var fileTimes = statTimes

// existence answers "did this node's file exist before t", which is all a brain as of t could hold. Birth time alone
// overstates newness on this disk: Claude Code's Edit writes a new inode, and a synced copy is born on arrival. So a
// file counts as older than t when it was born before t, last modified before t (a synced copy keeps its mtime), or
// read or written by a session that ended before t.
type existence struct {
	root    string
	db      *sql.DB
	touched map[string]time.Time // node path → end of the earliest session that read or wrote it
	paths   map[string]string    // node id → path, "" for nodes without a file (tickets, PRs, links)
	times   map[string]fileDates
}

type fileDates struct {
	born, modified time.Time
	keep           bool // no way to date it: kept as old
}

func newExistence(root string, db *sql.DB) (*existence, error) {
	x := &existence{root: root, db: db, touched: map[string]time.Time{}, paths: map[string]string{}, times: map[string]fileDates{}}
	err := filepath.WalkDir(filepath.Join(root, "sessions"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		fm, _, ok := splitFrontmatter(data)
		var s struct {
			Ended    time.Time `yaml:"ended"`
			Touched  []string  `yaml:"touched"`
			Produced []string  `yaml:"produced"`
		}
		if !ok || yaml.Unmarshal(fm, &s) != nil || s.Ended.IsZero() {
			return nil
		}
		for _, f := range append(s.Touched, s.Produced...) {
			if t, seen := x.touched[f]; !seen || s.Ended.Before(t) {
				x.touched[f] = s.Ended
			}
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	return x, err // x is usable either way
}

// before reports whether the node path p existed before t. A file not where its path says (a staged copy, a moved or
// deleted file) is dated by the mtime the index recorded; a file on a filesystem without birth times is kept, since
// nothing says it is new.
func (x *existence) before(p string, t time.Time) bool {
	d, ok := x.times[p]
	if !ok {
		born, modified := fileTimes(x.abs(p))
		switch {
		case born.IsZero() && !modified.IsZero():
			d.keep = true
		case modified.IsZero():
			var ns int64
			if x.db.QueryRow(`SELECT mtime FROM node WHERE path = ? AND kind != 'section' LIMIT 1`, p).Scan(&ns) == nil && ns > 0 {
				d.modified = time.Unix(0, ns)
			}
			d.keep = d.modified.IsZero()
		default:
			d.born, d.modified = born, modified
		}
		x.times[p] = d
	}
	if d.keep || t.IsZero() {
		return true
	}
	first, seen := x.touched[p]
	return (!d.born.IsZero() && d.born.Before(t)) || d.modified.Before(t) || (seen && first.Before(t))
}

// nodeBefore is before for a node id (a section by its document's file); nodes without a file are always kept.
func (x *existence) nodeBefore(id string, t time.Time) bool {
	p, ok := x.paths[id]
	if !ok {
		var kind string
		if x.db.QueryRow(`SELECT path, kind FROM node WHERE id = ?`, id).Scan(&p, &kind) != nil ||
			!(kind == "doc" || kind == "section" || kind == "artifact" || kind == "dir") {
			p = ""
		}
		x.paths[id] = p
	}
	return p == "" || x.before(p, t)
}

// abs is a node path on this machine's disk: work/… lives in the shadow saahil-docs until the cut-over fills
// <brain>/work, and memory-import/<project>/… in that project's Claude Code memory folder.
func (x *existence) abs(p string) string {
	if rest, ok := strings.CutPrefix(p, "work/"); ok {
		if _, err := os.Stat(filepath.Join(x.root, ".brain", "live")); err != nil {
			return filepath.Join(config.Home(), "Documents", "work-repo", "saahil-docs", rest)
		}
	}
	if rest, ok := strings.CutPrefix(p, "memory-import/"); ok {
		project, file, _ := strings.Cut(rest, "/")
		return filepath.Join(config.ClaudeDir(), "projects", project, "memory", file)
	}
	return filepath.Join(x.root, p)
}
