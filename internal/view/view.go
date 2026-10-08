// Package view builds a read-only folder for a markdown viewer such as Obsidian: links to the brain's text folders,
// and work/ as a mirror of the boxes' markdown files only, so the bulk (artifacts, venvs, builds) never reaches the
// viewer while every ../work/<box>/… link in a note still resolves.
package view

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/delta"
)

// Top are the brain's own entries linked whole into the view; notes/, hubs/ and memory/ are rendered copies instead.
var Top = []string{"BRAIN.md", "sessions", "journal", "inbox"}

// rendered are the folders the view holds as read-only copies, links made visible to the viewer.
var rendered = []string{"notes", "hubs", "memory"}

type Stats struct {
	Linked, Kept, Removed, Rendered, Copies int
}

// Build creates or refreshes dir from the brain at root: symlinks for the top entries and work/'s markdown, read-only
// rendered copies for notes/, hubs/ and memory/. It removes only symlinks and its own read-only copies (and folders
// they leave empty); a real file where a link belongs is an error, never overwritten.
func Build(root, dir string) (Stats, error) {
	var st Stats
	root, dir = filepath.Clean(root), filepath.Clean(dir)
	if dir == root || strings.HasPrefix(dir, root+string(filepath.Separator)) {
		return st, fmt.Errorf("%s is inside the brain: the view must live outside it", dir)
	}
	if err := os.MkdirAll(filepath.Join(dir, "work"), 0o755); err != nil {
		return st, err
	}
	for _, name := range Top {
		target := filepath.Join(root, name)
		if _, err := os.Stat(target); err != nil {
			continue
		}
		if err := link(target, filepath.Join(dir, name), &st); err != nil {
			return st, err
		}
	}

	for _, name := range rendered {
		n, copies, err := renderFolder(filepath.Join(root, name), filepath.Join(dir, name), name != "memory")
		if err != nil {
			return st, err
		}
		st.Rendered += n
		st.Copies += copies
	}

	want := map[string]string{} // view path → brain file
	work := filepath.Join(root, "work")
	err := filepath.WalkDir(work, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p != work && (strings.HasPrefix(d.Name(), ".") || config.IsSyncBulkDir(d.Name())) {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() && strings.EqualFold(filepath.Ext(d.Name()), ".md") && !strings.HasPrefix(d.Name(), ".") {
			rel, _ := filepath.Rel(work, p)
			want[filepath.Join(dir, "work", rel)] = p
		}
		return nil
	})
	if err != nil {
		return st, err
	}
	paths := make([]string, 0, len(want))
	for p := range want {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return st, err
		}
		if err := link(want[p], p, &st); err != nil {
			return st, err
		}
	}

	// drop links whose file left the mirror, deepest first so emptied folders can go too
	var stale, dirs []string
	_ = filepath.WalkDir(filepath.Join(dir, "work"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, p)
		} else if d.Type()&fs.ModeSymlink != 0 && want[p] == "" {
			stale = append(stale, p)
		}
		return nil
	})
	for _, p := range stale {
		if err := os.Remove(p); err == nil {
			st.Removed++
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, d := range dirs {
		if d != filepath.Join(dir, "work") {
			_ = os.Remove(d) // fails while it still holds something
		}
	}
	return st, nil
}

// link makes at a symlink to target, replacing only a symlink that points elsewhere.
func link(target, at string, st *Stats) error {
	fi, err := os.Lstat(at)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case fi.Mode()&fs.ModeSymlink == 0:
		return fmt.Errorf("%s exists and is not a link; the view never overwrites a real file", at)
	default:
		if cur, _ := os.Readlink(at); cur == target {
			st.Kept++
			return nil
		}
		if err := os.Remove(at); err != nil {
			return err
		}
	}
	if err := os.Symlink(target, at); err != nil {
		return err
	}
	st.Linked++
	return nil
}

const readOnly = 0o444

// renderFolder mirrors src's markdown files into dst as read-only copies (notes rendered with their links as
// [[wikilinks]]); it replaces a symlink left by an earlier view and removes only read-only files it made before.
func renderFolder(src, dst string, asNotes bool) (rewritten, copies int, err error) {
	if fi, err := os.Lstat(dst); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		if err := os.Remove(dst); err != nil {
			return 0, 0, err
		}
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return 0, 0, err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	seen, n := map[string]bool{}, 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			continue
		}
		out := raw
		if asNotes {
			if note, err := delta.Parse(raw); err == nil {
				out = Obsidian(note)
			}
		}
		seen[e.Name()] = true
		at := filepath.Join(dst, e.Name())
		if cur, err := os.ReadFile(at); err == nil && bytes.Equal(cur, out) {
			continue
		}
		tmp := at + ".tmp"
		if err := os.WriteFile(tmp, out, readOnly); err != nil {
			return n, len(seen), err
		}
		if err := os.Rename(tmp, at); err != nil {
			return n, len(seen), err
		}
		n++
	}
	left, _ := os.ReadDir(dst)
	for _, e := range left {
		if info, err := e.Info(); err == nil && !seen[e.Name()] && info.Mode().Perm() == readOnly {
			_ = os.Remove(filepath.Join(dst, e.Name()))
		}
	}
	return n, len(seen), nil
}

// Obsidian renders a note for the viewer: its typed links become [[wikilinks]] in the properties and in a Links
// section, which is how Obsidian draws its graph (the brain itself keeps bare ids).
func Obsidian(n delta.Note) []byte {
	wiki := func(ids []string) []string {
		out := make([]string, 0, len(ids))
		for _, id := range ids {
			out = append(out, "[["+strings.TrimSuffix(id, ".md")+"]]")
		}
		return out
	}
	paths := func(ps []string) []string {
		var out []string
		for _, p := range ps {
			if !strings.HasPrefix(p, ".") { // .import/… copies never reach the view
				out = append(out, "[["+p+"]]")
			}
		}
		return out
	}
	var related []string
	for _, r := range n.Related {
		related = append(related, r.ID)
	}
	type props struct {
		ID         string   `yaml:"id"`
		Type       string   `yaml:"type"`
		Status     string   `yaml:"status"`
		Summary    string   `yaml:"summary"`
		Scope      string   `yaml:"scope,omitempty"`
		AsOf       string   `yaml:"as_of,omitempty"`
		Aliases    []string `yaml:"aliases,omitempty"`
		Hubs       []string `yaml:"hubs,omitempty"`
		About      []string `yaml:"about,omitempty"`
		Broader    []string `yaml:"broader,omitempty"`
		Supersedes []string `yaml:"supersedes,omitempty"`
		Related    []string `yaml:"related,omitempty"`
		Evidence   []string `yaml:"evidence,omitempty"`
		Sources    []string `yaml:"sources,omitempty"`
	}
	fm, _ := yaml.Marshal(props{n.ID, n.Type, n.Status, n.Summary, n.Scope, n.AsOf, n.Aliases, wiki(n.Hubs), wiki(n.About),
		wiki(n.Broader), wiki(n.Supersedes), wiki(related), paths(n.Evidence), paths(n.Sources)})
	var b strings.Builder
	b.WriteString("---\n")
	b.Write(fm)
	b.WriteString("---\n")
	b.WriteString(strings.TrimRight(n.Body, "\n") + "\n")
	var links []string
	for _, r := range n.Related {
		links = append(links, "- related: [["+r.ID+"]] — "+r.Why)
	}
	for _, group := range []struct {
		rel string
		ids []string
	}{{"hub", n.Hubs}, {"supersedes", n.Supersedes}, {"broader", n.Broader}, {"about", n.About}} {
		for _, id := range group.ids {
			links = append(links, "- "+group.rel+": [["+id+"]]")
		}
	}
	if len(links) > 0 {
		b.WriteString("\n## Links\n\n" + strings.Join(links, "\n") + "\n")
	}
	return []byte(b.String())
}
