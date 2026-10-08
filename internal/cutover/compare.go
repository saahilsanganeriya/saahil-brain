package cutover

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Category string

const (
	Same         Category = "same"
	ChangedLater Category = "changed-later"
	Changed      Category = "changed"
	Missing      Category = "missing"
	Extra        Category = "extra"
)

type CompareResult struct {
	Same, ChangedLater, Changed, Missing, Extra int
}

// Failed: a move proof fails on any lost path, or any change not explained by a later edit.
func (r CompareResult) Failed() bool { return r.Missing > 0 || r.Changed > 0 }

// Compare proves a move: every path in before must exist in after with the same content. With allowNewer, a file
// whose mtime moved forward is a later edit (changed-later), not a failure. list gets every non-same path.
func Compare(before, after io.Reader, allowNewer bool, list func(Category, string)) (CompareResult, error) {
	var res CompareResult
	err := mergeJoin(newManifestReader(before, "before"), newManifestReader(after, "after"), func(b, a *Entry) {
		var cat Category
		switch {
		case a == nil:
			cat, res.Missing = Missing, res.Missing+1
		case b == nil:
			cat, res.Extra = Extra, res.Extra+1
		case sameContent(b, a):
			res.Same++
			return
		case allowNewer && a.Kind == b.Kind && a.MTime.After(b.MTime):
			cat, res.ChangedLater = ChangedLater, res.ChangedLater+1
		default:
			cat, res.Changed = Changed, res.Changed+1
		}
		path := a
		if path == nil {
			path = b
		}
		list(cat, path.Path)
	})
	return res, err
}

// sameContent compares by sha when both sides hashed the file, else by size + mtime (a rename keeps both).
func sameContent(b, a *Entry) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case KindFile:
		if a.SHA != "" && b.SHA != "" {
			return a.Size == b.Size && a.SHA == b.SHA
		}
		return a.Size == b.Size && a.MTime.Equal(b.MTime)
	case KindSymlink:
		return a.Target == b.Target
	}
	return true
}

// mergeJoin walks two manifests in walk order and calls pair once per path, with nil for the side that lacks it.
func mergeJoin(left, right *manifestReader, pair func(l, r *Entry)) error {
	l, err := left.next()
	if err != nil {
		return err
	}
	r, err := right.next()
	if err != nil {
		return err
	}
	for l != nil || r != nil {
		c := 0
		switch {
		case l == nil:
			c = 1
		case r == nil:
			c = -1
		default:
			c = comparePaths(l.Path, r.Path)
		}
		switch {
		case c < 0:
			pair(l, nil)
			l, err = left.next()
		case c > 0:
			pair(nil, r)
			r, err = right.next()
		default:
			pair(l, r)
			if l, err = left.next(); err == nil {
				r, err = right.next()
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Conflict is a text file both machines hold with different content. The newer copy wins (devbox on a tie, it holds
// the originals); the loser is kept beside it as LoserCopy, named from the loser's own mtime so both machines derive
// the same plan from the same manifests.
type Conflict struct {
	Path                     string
	Winner, Loser            string
	LoserCopy, LoserSHA      string
	LaptopMTime, DevboxMTime time.Time
	Tie                      bool
}

type ReconcileResult struct {
	Same, OnlyLaptop, OnlyDevbox   int
	Conflicts                      []Conflict
	Unhashed, NonText, KindDiffers []string // differences reconcile cannot judge: no sha, a binary, file vs dir/link
}

func Reconcile(laptop, devbox io.Reader) (ReconcileResult, error) {
	var res ReconcileResult
	err := mergeJoin(newManifestReader(laptop, "laptop"), newManifestReader(devbox, "devbox"), func(l, d *Entry) {
		switch {
		case d == nil:
			if l.Kind == KindFile {
				res.OnlyLaptop++
			}
		case l == nil:
			if d.Kind == KindFile {
				res.OnlyDevbox++
			}
		case l.Kind != d.Kind && (l.Kind == KindFile || d.Kind == KindFile):
			res.KindDiffers = append(res.KindDiffers, l.Path)
		case l.Kind != KindFile:
		case sameContent(l, d):
			res.Same++
		case l.SHA == "" || d.SHA == "":
			res.Unhashed = append(res.Unhashed, l.Path)
		case binaryExt[strings.ToLower(filepath.Ext(l.Path))]:
			res.NonText = append(res.NonText, l.Path)
		default:
			res.Conflicts = append(res.Conflicts, conflictOf(l, d))
		}
	})
	return res, err
}

func conflictOf(l, d *Entry) Conflict {
	c := Conflict{Path: l.Path, LaptopMTime: l.MTime, DevboxMTime: d.MTime, Tie: l.MTime.Equal(d.MTime)}
	loser := l
	c.Winner, c.Loser = "devbox", "laptop"
	if l.MTime.After(d.MTime) {
		loser = d
		c.Winner, c.Loser = "laptop", "devbox"
	}
	c.LoserSHA = loser.SHA
	c.LoserCopy = fmt.Sprintf("%s.conflict-%s-%s", l.Path, c.Loser, loser.MTime.UTC().Format("20060102T150405Z"))
	return c
}

// ApplyReconcile renames this machine's losing copies aside under root; the winner arrives by the normal sync. A copy
// that no longer matches its manifest sha, or whose conflict name is taken, is left alone and reported.
func ApplyReconcile(root, machine string, conflicts []Conflict) ([]string, error) {
	var moved []string
	var errs []error
	for _, c := range conflicts {
		if c.Loser != machine {
			continue
		}
		src, dst := filepath.Join(root, filepath.FromSlash(c.Path)), filepath.Join(root, filepath.FromSlash(c.LoserCopy))
		if sum, err := hashFile(src); err != nil || sum != c.LoserSHA {
			errs = append(errs, fmt.Errorf("%s: changed since the manifest (or unreadable: %v); not moved", c.Path, err))
			continue
		}
		if _, err := os.Lstat(dst); !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("%s: %s already exists; not moved", c.Path, c.LoserCopy))
			continue
		}
		if err := os.Rename(src, dst); err != nil {
			errs = append(errs, err)
			continue
		}
		moved = append(moved, c.LoserCopy)
	}
	return moved, errors.Join(errs...)
}

func WriteReconcileReport(w io.Writer, res ReconcileResult) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Reconcile: laptop vs devbox saahil-docs\n\n")
	fmt.Fprintf(&b, "| same | conflicts | only laptop | only devbox | differ, unhashed | differ, binary | file vs dir/link |\n")
	fmt.Fprintf(&b, "|---|---|---|---|---|---|---|\n| %d | %d | %d | %d | %d | %d | %d |\n\n", res.Same, len(res.Conflicts),
		res.OnlyLaptop, res.OnlyDevbox, len(res.Unhashed), len(res.NonText), len(res.KindDiffers))
	if len(res.Conflicts) > 0 {
		fmt.Fprintf(&b, "## Conflicts (newer wins; the loser is renamed aside on its own machine)\n\n")
		fmt.Fprintf(&b, "| path | winner | laptop mtime | devbox mtime | loser kept as |\n|---|---|---|---|---|\n")
		for _, c := range res.Conflicts {
			winner := c.Winner
			if c.Tie {
				winner += " (tie)"
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s | `%s` |\n", c.Path, winner, c.LaptopMTime.Format(time.RFC3339),
				c.DevboxMTime.Format(time.RFC3339), c.LoserCopy)
		}
		b.WriteString("\n")
	}
	for _, list := range []struct {
		title string
		paths []string
	}{{"Differ but unhashed (> 2 MB or bulk): not reconciled", res.Unhashed}, {"Differ, binary: not reconciled", res.NonText},
		{"A file on one machine, a directory or symlink on the other: not reconciled", res.KindDiffers}} {
		if len(list.paths) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## %s\n\n", list.title)
		for _, p := range list.paths {
			fmt.Fprintf(&b, "- `%s`\n", p)
		}
		b.WriteString("\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}
