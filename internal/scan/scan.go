// Package scan walks the brain's roots read-only and loads files under size caps. It never follows a symlink and
// never opens anything but regular md, html and data files.
package scan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"saahil.local/brain/internal/config"
)

const (
	MaxDocBytes     = 2_500_000 // markdown/html read cap; the rest of a bigger file is not indexed
	MaxShapeBytes   = 64 << 10  // data files: shape from this prefix
	maxHashedData   = 16 << 20  // bigger data files are not hashed, so they never join a copy group
	bulkDataEntries = 64        // a subtree with no docs and more data files than this is bulk
	bulkAnyEntries  = 2000
	censusWorkers   = 8
)

// Root is a folder presented under Prefix in node paths ("" for the brain itself, "work" for saahil-docs).
type Root struct{ Abs, Prefix string }

// DefaultRoots: the brain; the shadow doc root (shadow_docs) as work/ until a cut-over fills <brain>/work; every
// Claude memory folder.
func DefaultRoots(brainRoot string) []Root {
	roots := []Root{{Abs: brainRoot}}
	shadow := config.ShadowDocRoot()
	if work, _ := os.ReadDir(filepath.Join(brainRoot, "work")); len(work) == 0 && shadow != "" && isDir(shadow) {
		roots = append(roots, Root{Abs: shadow, Prefix: "work"})
	}
	mems, _ := filepath.Glob(filepath.Join(config.ClaudeDir(), "projects", "*", "memory"))
	sort.Strings(mems)
	for _, m := range mems {
		roots = append(roots, Root{Abs: m, Prefix: "memory-import/" + filepath.Base(filepath.Dir(m))})
	}
	return roots
}

func isDir(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() }

type Kind int

const (
	File Kind = iota // Class md | html | data
	Bulk             // a folder recorded with its shape, not descended; Class data | repo
	Link             // a symlink, never followed
	Skip             // Reason says why
)

type Entry struct {
	Path   string // presented, slash-separated
	Abs    string
	Kind   Kind
	Class  string
	Size   int64 // bytes; entries for Bulk
	MTime  int64 // unix nanoseconds
	Reason string
	Shape  string // Bulk: subtree counts when the census read it; BulkShape adds the listing
	Target string // Link: presented path when inside a root, else the canonical absolute path
}

type Stats struct{ Ignored int }

var skipDirs = map[string]bool{"node_modules": true, "venv": true, "__pycache__": true, "site-packages": true}

// ClassOf says how a file is indexed by its name: md, html, data, or "" (not indexed).
func ClassOf(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown":
		return "md"
	case ".html", ".htm":
		return "html"
	case ".json", ".jsonl", ".ndjson", ".csv", ".tsv", ".parquet":
		return "data"
	}
	return ""
}

func isBulkName(name string) bool {
	return name == "artifacts" || name == ".data" || strings.HasPrefix(name, "cache")
}

// Walk emits every entry under roots in sorted order. emit runs on the caller's goroutine.
func Walk(ctx context.Context, roots []Root, emit func(Entry) error) (Stats, error) {
	w := &walker{ctx: ctx, roots: roots, emit: emit, sem: make(chan struct{}, censusWorkers),
		bulk: map[string]bulkDir{}, listings: map[string][]os.DirEntry{}}
	for _, r := range roots {
		list, err := os.ReadDir(r.Abs)
		if err != nil {
			continue
		}
		w.census(r.Abs, list)
		if err := w.dir(r.Abs, r.Prefix, list); err != nil {
			return w.stats, err
		}
	}
	return w.stats, nil
}

type walker struct {
	ctx      context.Context
	roots    []Root
	emit     func(Entry) error
	stats    Stats
	sem      chan struct{}
	mu       sync.Mutex
	bulk     map[string]bulkDir       // by absolute path, from the census
	listings map[string][]os.DirEntry // census reads of folders with docs below, reused by the walk
}

func (w *walker) dir(abs, presented string, list []os.DirEntry) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	for _, de := range list {
		name := de.Name()
		childAbs, childPath := filepath.Join(abs, name), path.Join(presented, name)
		mode := de.Type()
		var err error
		switch {
		case mode&os.ModeSymlink != 0:
			err = w.emit(Entry{Path: childPath, Abs: childAbs, Kind: Link, Target: w.linkTarget(abs, childAbs)})
		case mode.IsDir():
			err = w.child(name, childAbs, childPath)
		case strings.HasPrefix(name, "."):
			// dotfiles (.env, .DS_Store) are never opened
		case ClassOf(name) == "", config.IsSyncConflictCopy(name):
			// a conflict copy is a sync's older loser, kept on disk for the owner, never live content
			w.stats.Ignored++
		case !mode.IsRegular():
			err = w.emit(Entry{Path: childPath, Abs: childAbs, Kind: Skip, Reason: "not-regular"})
		default:
			fi, ierr := de.Info()
			if ierr != nil {
				continue // vanished mid-scan; the next run converges
			}
			err = w.emit(Entry{Path: childPath, Abs: childAbs, Kind: File, Class: ClassOf(name), Size: fi.Size(),
				MTime: fi.ModTime().UnixNano()})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *walker) child(name, abs, presented string) error {
	if skippedDir(name) {
		return nil
	}
	if b, ok := w.bulk[abs]; ok {
		e := Entry{Path: presented, Abs: abs, Kind: Bulk, Class: b.class, Size: int64(b.subtree.files)}
		if b.subtree.files > 0 {
			e.Shape = fmt.Sprintf("%d data files, %d files, %d folders below", b.subtree.data, b.subtree.files, b.subtree.dirs)
		}
		if fi, err := os.Lstat(abs); err == nil {
			e.MTime = fi.ModTime().UnixNano()
		}
		return w.emit(e)
	}
	list, cached := w.listings[abs]
	if !cached {
		var err error
		if list, err = os.ReadDir(abs); err != nil {
			return w.emit(Entry{Path: presented, Abs: abs, Kind: Skip, Reason: "unreadable-dir"})
		}
	}
	delete(w.listings, abs)
	return w.dir(abs, presented, list)
}

func skippedDir(name string) bool {
	return skipDirs[name] || (strings.HasPrefix(name, ".") && name != ".data" && name != ".archive")
}

var repoMarkers = map[string]bool{".git": true, "go.mod": true, "package.json": true, "Cargo.toml": true,
	"pyproject.toml": true, "setup.py": true}

type tally struct{ docs, data, files, dirs int }

type bulkDir struct {
	class   string // data | repo
	subtree tally  // zero for folders bulk by name, which are never read
}

// census counts every subtree once, before the walk, and marks the folders recorded as a single node: code
// checkouts, folders bulk by name, and subtrees with no docs anywhere below but many data files (run outputs).
// It reads folders on up to censusWorkers goroutines and keeps the listings of non-bulk folders for the walk.
func (w *walker) census(abs string, list []os.DirEntry) tally {
	var t tally
	var mu sync.Mutex
	var wg sync.WaitGroup
	add := func(ct tally) {
		mu.Lock()
		t.docs, t.data, t.files, t.dirs = t.docs+ct.docs, t.data+ct.data, t.files+ct.files, t.dirs+ct.dirs
		mu.Unlock()
	}
	for _, de := range list {
		name := de.Name()
		switch {
		case de.Type()&os.ModeSymlink != 0:
		case de.IsDir():
			if skippedDir(name) {
				continue
			}
			add(tally{dirs: 1})
			child := filepath.Join(abs, name)
			select {
			case w.sem <- struct{}{}:
				wg.Add(1)
				go func() {
					defer func() { <-w.sem; wg.Done() }()
					add(w.censusDir(name, child))
				}()
			default:
				add(w.censusDir(name, child))
			}
		default:
			ct := tally{files: 1}
			switch ClassOf(name) {
			case "md", "html":
				ct.docs = 1
			case "data":
				ct.data = 1
			}
			add(ct)
		}
	}
	wg.Wait()
	return t
}

func (w *walker) censusDir(name, abs string) tally {
	if isBulkName(name) {
		w.mark(abs, bulkDir{class: "data"})
		return tally{}
	}
	sub, err := readDirUnsorted(abs)
	if err != nil {
		return tally{}
	}
	if slices.ContainsFunc(sub, func(de os.DirEntry) bool { return repoMarkers[de.Name()] }) {
		w.mark(abs, bulkDir{class: "repo"})
		return tally{}
	}
	ct := w.census(abs, sub)
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case ct.docs == 0 && (ct.data > bulkDataEntries || ct.files > bulkAnyEntries):
		w.bulk[abs] = bulkDir{class: "data", subtree: ct}
	case ct.docs > 0: // never inside a bulk subtree, so the walk will want it
		slices.SortFunc(sub, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
		w.listings[abs] = sub
	}
	return ct
}

func (w *walker) mark(abs string, b bulkDir) {
	w.mu.Lock()
	w.bulk[abs] = b
	w.mu.Unlock()
}

func readDirUnsorted(abs string) ([]os.DirEntry, error) {
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}

// BulkShape describes a bulk folder from its names alone (one readdir, even for a 400k-file cache); the graph
// calls it only when the folder changed.
func BulkShape(e Entry) string {
	list, err := readDirUnsorted(e.Abs)
	if err != nil {
		return e.Shape
	}
	if e.Shape != "" {
		return e.Shape + "; here " + listingShape(list)
	}
	return listingShape(list)
}

func listingShape(list []os.DirEntry) string {
	counts := map[string]int{}
	for _, de := range list {
		key := strings.ToLower(path.Ext(de.Name()))
		if de.IsDir() {
			key = "dirs"
		} else if key == "" {
			key = "other"
		}
		counts[key]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return counts[keys[i]] > counts[keys[j]] || (counts[keys[i]] == counts[keys[j]] && keys[i] < keys[j])
	})
	parts := []string{}
	for i, k := range keys {
		if i == 5 {
			break
		}
		parts = append(parts, strings.TrimPrefix(k, ".")+" "+strconv.Itoa(counts[k]))
	}
	return strconv.Itoa(len(list)) + " entries: " + strings.Join(parts, ", ")
}

func (w *walker) linkTarget(dir, link string) string {
	target, err := os.Readlink(link)
	if err != nil {
		return ""
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}
	if p, ok := Presented(w.roots, target); ok {
		return p
	}
	return config.Canonical(target)
}

// Presented maps an absolute path (either home spelling) to its node path when it lies inside a root.
func Presented(roots []Root, abs string) (string, bool) {
	abs = config.Canonical(abs)
	best := -1
	for i, r := range roots {
		ra := config.Canonical(r.Abs)
		if (abs == ra || strings.HasPrefix(abs, ra+"/")) && (best < 0 || len(ra) > len(roots[best].Abs)) {
			best = i
		}
	}
	if best < 0 {
		return "", false
	}
	rel, _ := filepath.Rel(config.Canonical(roots[best].Abs), abs)
	return path.Join(roots[best].Prefix, filepath.ToSlash(rel)), true
}

// Content is a loaded file: Data is capped, SHA covers the whole file (empty for data files too big to hash).
type Content struct {
	Data      []byte
	SHA       string
	Truncated bool
	Skip      string // binary | invalid-utf8 | unreadable
}

// Load reads e under its class's cap. A file that changed size mid-read is still loaded; the next run converges.
func Load(e Entry) Content {
	limit, hashAll := int64(MaxDocBytes), true
	if e.Class == "data" {
		limit, hashAll = MaxShapeBytes, e.Size <= maxHashedData
	}
	f, err := os.Open(e.Abs)
	if err != nil {
		return Content{Skip: "unreadable"}
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return Content{Skip: "not-regular"}
	}
	h := sha256.New()
	buf, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return Content{Skip: "unreadable"}
	}
	c := Content{Data: buf, Truncated: e.Size > limit}
	if hashAll {
		h.Write(buf)
		n, _ := io.Copy(h, f)
		c.Truncated = n > 0
		c.SHA = hex.EncodeToString(h.Sum(nil))
	}
	if e.Class == "data" {
		return c
	}
	if bytes.IndexByte(buf[:min(len(buf), 8192)], 0) >= 0 {
		return Content{Skip: "binary"}
	}
	if c.Truncated {
		c.Data = trimPartialRune(c.Data)
	}
	if !utf8.Valid(c.Data) {
		return Content{Skip: "invalid-utf8"}
	}
	return c
}

func trimPartialRune(b []byte) []byte {
	for i := 0; i < utf8.UTFMax && len(b) > 0; i++ {
		if r, _ := utf8.DecodeLastRune(b); r != utf8.RuneError {
			return b
		}
		b = b[:len(b)-1]
	}
	return b
}
