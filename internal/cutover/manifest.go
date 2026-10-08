// Package cutover is the tooling for moving saahil-docs into ~/brain/work: manifests that prove a move, a
// laptop/devbox reconcile, an old-path rewrite and a leftover-name finder. Everything is read-only unless the caller
// asks to apply, and every write keeps a backup.
package cutover

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Kind string

const (
	KindFile    Kind = "file"
	KindDir     Kind = "dir"
	KindSymlink Kind = "symlink"
	KindOther   Kind = "other"
)

// Entry is one manifest row. Path is slash-separated and relative to the manifest root; SHA is empty when the file
// was not hashed (bulk, or over the size cap), and then size + mtime stand in for it.
type Entry struct {
	Path   string
	Kind   Kind
	Size   int64
	MTime  time.Time
	SHA    string
	Target string
}

const (
	DefaultHashMaxBytes = 2 << 20
	MaxWorkers          = 8
	manifestHeader      = "# path\tsize\tmtime\tsha256\ttype\ttarget"
)

type ManifestOptions struct {
	Root          string
	Workers       int
	HashMaxBytes  int64
	SkipHashUnder []string // doublestar globs over the relative path, e.g. "*/artifacts/**"
	Progress      io.Writer
	OnError       func(path string, err error)
}

type ManifestStats struct {
	Rows, Files, Dirs, Symlinks, Hashed, Vanished, Errors int
	Bytes, HashedBytes                                    int64
}

// WriteManifest streams one row per entry under Root in walk order; directory symlinks are rows, never walked.
// Memory stays bounded by the pipeline depth, not the tree size.
func WriteManifest(w io.Writer, opt ManifestOptions) (ManifestStats, error) {
	var stats ManifestStats
	root, err := resolveRoot(opt.Root)
	if err != nil {
		return stats, err
	}
	if opt.HashMaxBytes == 0 {
		opt.HashMaxBytes = DefaultHashMaxBytes
	}
	onError := opt.OnError
	if onError == nil {
		onError = func(string, error) {}
	}
	type row struct {
		entry    Entry
		err      error
		vanished bool
	}
	bw := bufio.NewWriterSize(w, 1<<20)
	fmt.Fprintln(bw, manifestHeader)
	walkErrors := 0 // the walk runs beside emit, so it keeps its own count
	err = runOrdered(clampWorkers(opt.Workers), func(submit func(func() row) bool) error {
		return filepath.WalkDir(root, func(abs string, d fs.DirEntry, walkErr error) error {
			if abs == root {
				return walkErr
			}
			if walkErr != nil {
				onError(abs, walkErr)
				walkErrors++
				return nil
			}
			rel := filepath.ToSlash(strings.TrimPrefix(abs, root+string(filepath.Separator)))
			hash := d.Type().IsRegular() && !matchAny(opt.SkipHashUnder, rel)
			if !submit(func() row {
				e, err := statEntry(abs, rel, hash, opt.HashMaxBytes)
				return row{entry: e, err: err, vanished: errors.Is(err, fs.ErrNotExist)}
			}) {
				return filepath.SkipAll
			}
			return nil
		})
	}, func(r row) error {
		if r.vanished {
			stats.Vanished++
			return nil
		}
		if r.err != nil {
			onError(r.entry.Path, r.err)
			stats.Errors++
		}
		stats.count(r.entry)
		if opt.Progress != nil && stats.Rows%100_000 == 0 {
			fmt.Fprintf(opt.Progress, "manifest: %d rows, %.1f GB seen, %.1f GB hashed\n", stats.Rows, gb(stats.Bytes), gb(stats.HashedBytes))
		}
		_, err := fmt.Fprintln(bw, formatEntry(r.entry))
		return err
	})
	stats.Errors += walkErrors
	if err != nil {
		return stats, err
	}
	return stats, bw.Flush()
}

func (s *ManifestStats) count(e Entry) {
	s.Rows++
	switch e.Kind {
	case KindFile:
		s.Files++
		s.Bytes += e.Size
		if e.SHA != "" {
			s.Hashed++
			s.HashedBytes += e.Size
		}
	case KindDir:
		s.Dirs++
	case KindSymlink:
		s.Symlinks++
	}
}

func gb(n int64) float64 { return float64(n) / (1 << 30) }

// resolveRoot follows a symlinked root (the compat link after the swap) so the walk lists the real tree.
func resolveRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

func clampWorkers(n int) int { return min(max(n, 1), MaxWorkers) }

func statEntry(abs, rel string, hash bool, hashMax int64) (Entry, error) {
	e := Entry{Path: rel}
	info, err := os.Lstat(abs)
	if err != nil {
		return e, err
	}
	e.MTime, e.Size = info.ModTime().UTC(), info.Size()
	switch mode := info.Mode(); {
	case mode.IsRegular():
		e.Kind = KindFile
		if hash && e.Size <= hashMax {
			e.SHA, err = hashFile(abs)
		}
	case mode.IsDir():
		e.Kind, e.Size = KindDir, 0
	case mode&fs.ModeSymlink != 0:
		e.Kind = KindSymlink
		e.Target, err = os.Readlink(abs)
	default:
		e.Kind = KindOther
	}
	return e, err
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func formatEntry(e Entry) string {
	size, sha, target := "-", "-", "-"
	if e.Kind != KindDir {
		size = strconv.FormatInt(e.Size, 10)
	}
	if e.SHA != "" {
		sha = e.SHA
	}
	if e.Kind == KindSymlink {
		target = quoteField(e.Target)
	}
	return strings.Join([]string{quoteField(e.Path), size, e.MTime.Format(time.RFC3339Nano), sha, string(e.Kind), target}, "\t")
}

// quoteField Go-quotes a value a TSV reader would misread: control characters, a backslash, "-" (the empty
// marker), or a leading '#' or '"' (comment and quote markers).
func quoteField(s string) string {
	if s == "" || s == "-" || s[0] == '#' || s[0] == '"' || strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == '\\' }) >= 0 {
		return strconv.Quote(s)
	}
	return s
}

func unquoteField(s string) (string, error) {
	if strings.HasPrefix(s, `"`) {
		return strconv.Unquote(s)
	}
	return s, nil
}

func parseEntry(line string) (Entry, error) {
	f := strings.Split(line, "\t")
	if len(f) != 6 {
		return Entry{}, fmt.Errorf("want 6 fields, got %d", len(f))
	}
	var e Entry
	var err error
	if e.Path, err = unquoteField(f[0]); err != nil {
		return e, err
	}
	if f[1] != "-" {
		if e.Size, err = strconv.ParseInt(f[1], 10, 64); err != nil {
			return e, err
		}
	}
	if e.MTime, err = time.Parse(time.RFC3339Nano, f[2]); err != nil {
		return e, err
	}
	if f[3] != "-" {
		e.SHA = f[3]
	}
	e.Kind = Kind(f[4])
	if f[5] != "-" {
		e.Target, err = unquoteField(f[5])
	}
	return e, err
}

// manifestReader yields rows in order and refuses a manifest that is not in walk order, since the merge-joins in
// Compare and Reconcile silently misreport on unsorted input.
type manifestReader struct {
	sc   *bufio.Scanner
	name string
	line int
	prev string
}

func newManifestReader(r io.Reader, name string) *manifestReader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	return &manifestReader{sc: sc, name: name}
}

func (r *manifestReader) next() (*Entry, error) {
	for r.sc.Scan() {
		r.line++
		text := r.sc.Text()
		if text == "" || text[0] == '#' {
			continue
		}
		e, err := parseEntry(text)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", r.name, r.line, err)
		}
		if r.prev != "" && comparePaths(r.prev, e.Path) >= 0 {
			return nil, fmt.Errorf("%s:%d: %q is out of walk order after %q", r.name, r.line, e.Path, r.prev)
		}
		r.prev = e.Path
		return &e, nil
	}
	return nil, r.sc.Err()
}

// comparePaths orders paths component by component, which is the order filepath.WalkDir emits them in ("a/x" before
// "a-c", although '-' < '/').
func comparePaths(a, b string) int {
	for {
		ai, bi := strings.IndexByte(a, '/'), strings.IndexByte(b, '/')
		as, bs := a, b
		if ai >= 0 {
			as = a[:ai]
		}
		if bi >= 0 {
			bs = b[:bi]
		}
		if c := strings.Compare(as, bs); c != 0 {
			return c
		}
		switch {
		case ai < 0 && bi < 0:
			return 0
		case ai < 0:
			return -1
		case bi < 0:
			return 1
		}
		a, b = a[ai+1:], b[bi+1:]
	}
}

func matchAny(globs []string, rel string) bool {
	for _, g := range globs {
		if matchGlob(strings.Split(g, "/"), strings.Split(rel, "/")) {
			return true
		}
	}
	return false
}

// matchGlob matches path.Match per component, with "**" spanning zero or more components.
func matchGlob(pattern, parts []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			for i := 0; i <= len(parts); i++ {
				if matchGlob(pattern[1:], parts[i:]) {
					return true
				}
			}
			return false
		}
		if len(parts) == 0 {
			return false
		}
		if ok, _ := path.Match(pattern[0], parts[0]); !ok {
			return false
		}
		pattern, parts = pattern[1:], parts[1:]
	}
	return len(parts) == 0
}

// runOrdered runs submitted jobs on `workers` goroutines and hands their results to emit in submission order. At most
// 4×workers jobs are in flight, so memory stays bounded however many the producer submits. submit reports false once
// emit has failed, and the producer should stop.
func runOrdered[T any](workers int, produce func(submit func(func() T) bool) error, emit func(T) error) error {
	type job struct {
		fn  func() T
		out chan T
	}
	order := make(chan *job, 4*workers)
	work := make(chan *job, 4*workers)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for j := range work {
				j.out <- j.fn()
			}
		})
	}
	emitted := make(chan error, 1)
	go func() {
		var err error
		for j := range order {
			v := <-j.out
			if err == nil {
				if err = emit(v); err != nil {
					close(stop)
				}
			}
		}
		emitted <- err
	}()
	submit := func(fn func() T) bool {
		j := &job{fn: fn, out: make(chan T, 1)}
		select {
		case <-stop:
			return false
		case order <- j:
		}
		work <- j
		return true
	}
	produceErr := produce(submit)
	close(work)
	close(order)
	wg.Wait()
	if err := <-emitted; err != nil {
		return err
	}
	return produceErr
}
