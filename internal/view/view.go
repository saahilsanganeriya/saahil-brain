// Package view builds a read-only folder for a markdown viewer such as Obsidian: links to the brain's text folders,
// and work/ as a mirror of the boxes' markdown files only, so the bulk (artifacts, venvs, builds) never reaches the
// viewer while every ../work/<box>/… link in a note still resolves.
package view

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"saahil.local/brain/internal/config"
)

// Top are the brain's own entries linked whole into the view.
var Top = []string{"BRAIN.md", "memory", "hubs", "notes", "sessions", "journal", "inbox"}

type Stats struct {
	Linked, Kept, Removed int
}

// Build creates or refreshes dir from the brain at root. It only ever creates folders and symlinks in dir, and only
// removes symlinks (and folders they leave empty); a real file in dir is an error, never overwritten.
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
