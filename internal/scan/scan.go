// Package scan walks the brain's roots read-only and loads files under size caps. It never follows a symlink and
// never opens anything but regular md, html and data files.
package scan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"saahil.local/brain/internal/config"
)

const (
	MaxDocBytes     = 2_500_000 // markdown/html read cap; the rest of a bigger file is not indexed
	MaxShapeBytes   = 64 << 10  // data files: shape from this prefix
	maxHashedData   = 16 << 20  // bigger data files are not hashed, so they never join a copy group
	bulkDataEntries = 64        // a folder of more data files than this, and no docs, is bulk
	bulkAnyEntries  = 2000
)

// Root is a folder presented under Prefix in node paths ("" for the brain itself, "work" for saahil-docs).
type Root struct{ Abs, Prefix string }

// DefaultRoots: the brain; saahil-docs as work/ until the cut-over fills <brain>/work; every Claude memory folder.
func DefaultRoots(brainRoot string) []Root {
	roots := []Root{{Abs: brainRoot}}
	shadow := filepath.Join(config.Home(), "Documents", "work-repo", "saahil-docs")
	if work, _ := os.ReadDir(filepath.Join(brainRoot, "work")); len(work) == 0 && isDir(shadow) {
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
	Shape  string // Bulk: what the folder holds
	Target string // Link: presented path when inside a root, else the canonical absolute path
}

type Stats struct{ Dirs, Ignored int }

var skipDirs = map[string]bool{"node_modules": true, "venv": true, "__pycache__": true, "site-packages": true}

func classOf(name string) string {
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
	w := &walker{ctx: ctx, roots: roots, emit: emit}
	for _, r := range roots {
		list, err := os.ReadDir(r.Abs)
		if err != nil {
			continue
		}
		if err := w.dir(r.Abs, r.Prefix, list); err != nil {
			return w.stats, err
		}
	}
	return w.stats, nil
}

type walker struct {
	ctx   context.Context
	roots []Root
	emit  func(Entry) error
	stats Stats
}

func (w *walker) dir(abs, presented string, list []os.DirEntry) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	w.stats.Dirs++
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
		case classOf(name) == "":
			w.stats.Ignored++
		case !mode.IsRegular():
			err = w.emit(Entry{Path: childPath, Abs: childAbs, Kind: Skip, Reason: "not-regular"})
		default:
			fi, ierr := de.Info()
			if ierr != nil {
				continue // vanished mid-scan; the next run converges
			}
			err = w.emit(Entry{Path: childPath, Abs: childAbs, Kind: File, Class: classOf(name), Size: fi.Size(),
				MTime: fi.ModTime().UnixNano()})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *walker) child(name, abs, presented string) error {
	if skipDirs[name] || (strings.HasPrefix(name, ".") && name != ".data" && name != ".archive") {
		return nil
	}
	list, err := os.ReadDir(abs)
	if err != nil {
		return w.emit(Entry{Path: presented, Abs: abs, Kind: Skip, Reason: "unreadable-dir"})
	}
	if bulk, class := isBulk(name, list); bulk {
		e := Entry{Path: presented, Abs: abs, Kind: Bulk, Class: class, Size: int64(len(list)), Shape: listingShape(list)}
		if fi, err := os.Lstat(abs); err == nil {
			e.MTime = fi.ModTime().UnixNano()
		}
		return w.emit(e)
	}
	return w.dir(abs, presented, list)
}

func isBulk(name string, list []os.DirEntry) (bool, string) {
	data, docs := 0, 0
	for _, de := range list {
		if de.Name() == ".git" {
			return true, "repo"
		}
		switch classOf(de.Name()) {
		case "md", "html":
			docs++
		case "data":
			data++
		}
	}
	return isBulkName(name) || (docs == 0 && (data > bulkDataEntries || len(list) > bulkAnyEntries)), "data"
}

// listingShape summarises a folder from its names alone, so a 400k-file cache costs one readdir.
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

// Absolute maps a node path back to the file it came from.
func Absolute(roots []Root, presented string) (string, bool) {
	best := -1
	for i, r := range roots {
		if (r.Prefix == "" || presented == r.Prefix || strings.HasPrefix(presented, r.Prefix+"/")) &&
			(best < 0 || len(r.Prefix) > len(roots[best].Prefix)) {
			best = i
		}
	}
	if best < 0 {
		return "", false
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(presented, roots[best].Prefix), "/")
	return filepath.Join(roots[best].Abs, filepath.FromSlash(rel)), true
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
