package cutover

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	TextMaxBytes  = 10 << 20
	binarySniff   = 8 << 10
	maxShebangLen = 127 // the kernel truncates longer #! lines on older Linux; pip switches to '''exec' past this
)

var binaryExt = setOf(".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tif", ".tiff", ".heic", ".ico", ".pdf",
	".zip", ".gz", ".tgz", ".bz2", ".xz", ".zst", ".7z", ".rar", ".tar", ".whl", ".jar", ".class", ".parquet", ".arrow",
	".feather", ".npy", ".npz", ".pkl", ".pickle", ".pt", ".onnx", ".safetensors", ".h5", ".so", ".dylib", ".a", ".o",
	".exe", ".bin", ".db", ".sqlite", ".pyc", ".woff", ".woff2", ".ttf", ".otf", ".mp3", ".mp4", ".wav", ".mov", ".avi",
	".docx", ".xlsx", ".pptx", ".doc", ".xls", ".ppt", ".dmg", ".iso")

// History records what was true and is never rewritten or reported: git internals, box logs, the archive, session
// transcripts (<uuid>.jsonl) and data manifests.
var (
	historyDirs    = setOf(".git", "log", ".archive", "transcripts")
	transcriptName = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.jsonl$`)
	manifestName   = regexp.MustCompile(`(?i)manifest.*\.(tsv|csv|json|jsonl|txt)$`)
)

func setOf(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[s] = true
	}
	return m
}

func isHistoryFile(base string) bool {
	return transcriptName.MatchString(base) || manifestName.MatchString(base)
}

type scanItem struct {
	abs, rel, category string
	isRoot             bool
	mode               fs.FileMode
}

// scanTree walks each root (no symlinked directories followed, history pruned) and runs job on the pool, handing
// results to emit in walk order. It returns how many entries the walk itself could not read.
func scanTree[T any](roots []string, workers int, onError func(string, error), job func(scanItem) T, emit func(T) error) (int, error) {
	var walkErr error
	walkErrors := 0
	venvs := map[string]bool{}
	isVenv := func(dir string) bool {
		v, ok := venvs[dir]
		if !ok {
			_, err := os.Stat(filepath.Join(dir, "pyvenv.cfg"))
			v = err == nil
			venvs[dir] = v
		}
		return v
	}
	err := runOrdered(clampWorkers(workers), func(submit func(func() T) bool) error {
		for _, root := range roots {
			err := filepath.WalkDir(root, func(abs string, d fs.DirEntry, err error) error {
				if err != nil {
					if abs == root {
						return err
					}
					onError(abs, err)
					walkErrors++
					return nil
				}
				name := d.Name()
				if abs != root && (d.IsDir() && historyDirs[name] || !d.IsDir() && isHistoryFile(name)) {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				it := scanItem{abs: abs, rel: filepath.ToSlash(strings.TrimPrefix(abs, root+string(filepath.Separator))),
					isRoot: abs == root, mode: d.Type()}
				it.category = categorize(abs, d, isVenv)
				if !submit(func() T { return job(it) }) {
					return filepath.SkipAll
				}
				return nil
			})
			if err != nil {
				walkErr = errors.Join(walkErr, err)
			}
		}
		return walkErr
	}, emit)
	return walkErrors, err
}

// categorize names what a file is for the per-category counts; venv files get their own names because a moved venv
// breaks unless all of them follow it.
func categorize(abs string, d fs.DirEntry, isVenv func(string) bool) string {
	base, dir := d.Name(), filepath.Dir(abs)
	switch {
	case base == "pyvenv.cfg":
		return "venv:pyvenv.cfg"
	case filepath.Base(dir) == "bin" && !d.IsDir() && isVenv(filepath.Dir(dir)):
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			return "venv:symlink"
		case strings.HasPrefix(base, "activate"):
			return "venv:activate"
		}
		return "venv:script"
	case d.Type()&fs.ModeSymlink != 0:
		return "symlink"
	}
	if ext := strings.ToLower(filepath.Ext(base)); ext != "" && len(ext) <= 12 {
		return ext
	}
	return "(no ext)"
}

type textRead int

const (
	readText textRead = iota
	readBinary
	readLarge
)

// readTextFile returns the content of a text file ≤ 10 MB; binaries (known extension, or a NUL in the first 8 KB) are
// never read past the sniff.
func readTextFile(abs string) ([]byte, fs.FileInfo, textRead, error) {
	if binaryExt[strings.ToLower(filepath.Ext(abs))] {
		return nil, nil, readBinary, nil
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, nil, readText, err
	}
	if info.Size() > TextMaxBytes {
		return nil, info, readLarge, nil
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return nil, info, readText, err
	}
	if bytes.IndexByte(content[:min(len(content), binarySniff)], 0) >= 0 {
		return nil, info, readBinary, nil
	}
	return content, info, readText, nil
}

// ---- rewrite-paths ----

type RewriteOptions struct {
	Root      string
	From      []string // old prefixes; '*' matches one path component (a worktree slug)
	To        string
	Apply     bool
	BackupDir string
	Workers   int
	Progress  io.Writer
	OnError   func(path string, err error)
}

type FileChange struct {
	Path     string
	Category string
	Count    int
}

type RewriteStats struct {
	Scanned, Text, Binary, Large, Changed, Occurrences, Errors int
	ByCategory                                                 map[string]int
}

type rewriteResult struct {
	change    *FileChange
	read      textRead
	isText    bool
	backupRow string
	err       error
}

// Rewrite replaces every old prefix with the new one in text files and symlink targets under Root. A dry run only
// counts. Apply copies each original into BackupDir/files/ (bytes, mode, mtime) and records it in BackupDir/manifest.tsv
// before atomically replacing the file (temp + rename, mode kept, mtime now).
func Rewrite(opt RewriteOptions, emit func(FileChange)) (RewriteStats, error) {
	stats := RewriteStats{ByCategory: map[string]int{}}
	root, err := resolveRoot(opt.Root)
	if err != nil {
		return stats, err
	}
	pattern, err := prefixPattern(opt.From)
	if err != nil {
		return stats, err
	}
	onError := opt.OnError
	if onError == nil {
		onError = func(string, error) {}
	}
	var backup *bufio.Writer
	if opt.Apply {
		f, err := openBackup(root, opt.BackupDir)
		if err != nil {
			return stats, err
		}
		defer f.Close()
		backup = bufio.NewWriter(f)
		defer backup.Flush()
	}
	rw := rewriter{pattern: pattern, to: []byte(opt.To), apply: opt.Apply, backupDir: opt.BackupDir}
	walkErrors, err := scanTree([]string{root}, opt.Workers, onError, rw.process, func(r rewriteResult) error {
		stats.Scanned++
		if opt.Progress != nil && stats.Scanned%100_000 == 0 {
			fmt.Fprintf(opt.Progress, "rewrite-paths: %d entries scanned, %d text, %d would change\n", stats.Scanned, stats.Text, stats.Changed)
		}
		switch {
		case r.err != nil:
			stats.Errors++
			onError(r.change.Path, r.err)
			return nil
		case r.read == readBinary:
			stats.Binary++
		case r.read == readLarge:
			stats.Large++
		case r.isText:
			stats.Text++
		}
		if r.change == nil || r.change.Count == 0 {
			return nil
		}
		stats.Changed++
		stats.Occurrences += r.change.Count
		stats.ByCategory[r.change.Category]++
		emit(*r.change)
		if backup != nil {
			_, err := backup.WriteString(r.backupRow + "\n")
			return err
		}
		return nil
	})
	stats.Errors += walkErrors
	return stats, err
}

func openBackup(root, dir string) (*os.File, error) {
	if dir == "" {
		return nil, errors.New("--apply needs a backup dir")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if abs == root || strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return nil, fmt.Errorf("backup dir %s is inside the tree being rewritten", abs)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(abs, "manifest.tsv"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, fmt.Errorf("backup manifest: %w (use a fresh backup dir)", err)
	}
	_, err = f.WriteString("# path\tkind\toccurrences\tmtime_before\tsha256_before\tsha256_after\ttarget_before\ttarget_after\n")
	return f, err
}

// prefixPattern matches any of the old prefixes, longest first so a prefix that extends another wins.
func prefixPattern(prefixes []string) (*regexp.Regexp, error) {
	if len(prefixes) == 0 {
		return nil, errors.New("no --from prefix")
	}
	sorted := append([]string(nil), prefixes...)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	alts := make([]string, len(sorted))
	for i, p := range sorted {
		alts[i] = strings.ReplaceAll(regexp.QuoteMeta(p), `\*`, `[^/\s"'<>]+`)
	}
	return regexp.Compile(strings.Join(alts, "|"))
}

type rewriter struct {
	pattern   *regexp.Regexp
	to        []byte
	apply     bool
	backupDir string
}

func (rw rewriter) replace(content []byte) ([]byte, int) {
	n := 0
	out := rw.pattern.ReplaceAllFunc(content, func([]byte) []byte { n++; return rw.to })
	return out, n
}

func (rw rewriter) process(it scanItem) rewriteResult {
	change := &FileChange{Path: it.rel, Category: it.category}
	res := rewriteResult{change: change}
	switch {
	case it.isRoot || it.mode.IsDir():
		return rewriteResult{}
	case it.mode&fs.ModeSymlink != 0:
		res.err = rw.symlink(it, &res)
	case it.mode.IsRegular():
		res.err = rw.file(it, &res)
	}
	return res
}

func (rw rewriter) symlink(it scanItem, res *rewriteResult) error {
	target, err := os.Readlink(it.abs)
	if err != nil {
		return err
	}
	newTarget, n := rw.replace([]byte(target))
	if res.change.Count = n; n == 0 {
		return nil
	}
	info, err := os.Lstat(it.abs)
	if err != nil {
		return err
	}
	res.backupRow = backupRow(it.rel, "symlink", n, info.ModTime(), "-", "-", quoteField(target), quoteField(string(newTarget)))
	if !rw.apply {
		return nil
	}
	tmp := filepath.Join(filepath.Dir(it.abs), ".brain-rewrite-"+filepath.Base(it.abs))
	if err := os.Symlink(string(newTarget), tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, it.abs); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (rw rewriter) file(it scanItem, res *rewriteResult) error {
	content, info, kind, err := readTextFile(it.abs)
	if res.read = kind; err != nil || kind != readText {
		return err
	}
	res.isText = true
	out, n := rw.replace(content)
	if n == 0 {
		return nil
	}
	if line, _, _ := bytes.Cut(out, []byte("\n")); bytes.HasPrefix(line, []byte("#!")) && len(line) > maxShebangLen {
		return fmt.Errorf("rewritten shebang would be %d bytes (> %d); left unchanged", len(line), maxShebangLen)
	}
	res.change.Count = n
	res.backupRow = backupRow(it.rel, "file", n, info.ModTime(), hashBytes(content), hashBytes(out), "-", "-")
	if !rw.apply {
		return nil
	}
	if err := saveBackup(filepath.Join(rw.backupDir, "files", filepath.FromSlash(it.rel)), content, info); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	return replaceFile(it.abs, out, info)
}

func backupRow(rel, kind string, n int, mtime time.Time, shaBefore, shaAfter, targetBefore, targetAfter string) string {
	return strings.Join([]string{quoteField(rel), kind, fmt.Sprint(n), mtime.UTC().Format(time.RFC3339Nano),
		shaBefore, shaAfter, targetBefore, targetAfter}, "\t")
}

func saveBackup(dst string, content []byte, info fs.FileInfo) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, info.ModTime(), info.ModTime())
}

// replaceFile writes content beside path and renames it over, unless the file changed since it was read (a live
// writer); the rename is the only moment the path changes, so a reader sees the old or the new file, never half.
func replaceFile(path string, content []byte, read fs.FileInfo) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".brain-rewrite-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(read.Mode().Perm()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	now, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if now.Size() != read.Size() || !now.ModTime().Equal(read.ModTime()) {
		return errors.New("changed while being rewritten; left unchanged")
	}
	return os.Rename(tmp.Name(), path)
}

// ---- find-names ----

type Hit struct {
	Path     string // absolute
	What     string // "name:<kind>", "text" or "link-target"
	Count    int
	Category string
}

type FindStats struct {
	Scanned, TextFiles, LinkTargets, Occurrences, Errors int
	Names, ByCategory                                    map[string]int
}

// FindNames lists every non-history entry named needle, and every non-history text file or symlink target containing
// it.
func FindNames(roots []string, needle string, workers int, progress io.Writer, onError func(string, error), emit func(Hit)) (FindStats, error) {
	stats := FindStats{Names: map[string]int{}, ByCategory: map[string]int{}}
	if onError == nil {
		onError = func(string, error) {}
	}
	type found struct {
		hits []Hit
		err  error
		path string
	}
	walkErrors, err := scanTree(roots, workers, onError, func(it scanItem) found {
		var f found
		if filepath.Base(it.abs) == needle {
			f.hits = append(f.hits, Hit{Path: it.abs, What: "name:" + string(kindOf(it.mode))})
		}
		if it.mode&fs.ModeSymlink != 0 {
			target, err := os.Readlink(it.abs)
			if n := strings.Count(target, needle); n > 0 {
				f.hits = append(f.hits, Hit{Path: it.abs, What: "link-target", Count: n, Category: it.category})
			}
			f.err, f.path = err, it.abs
			return f
		}
		if !it.mode.IsRegular() {
			return f
		}
		content, _, kind, err := readTextFile(it.abs)
		if err != nil || kind != readText {
			f.err, f.path = err, it.abs
			return f
		}
		if n := bytes.Count(content, []byte(needle)); n > 0 {
			f.hits = append(f.hits, Hit{Path: it.abs, What: "text", Count: n, Category: it.category})
		}
		return f
	}, func(f found) error {
		stats.Scanned++
		if progress != nil && stats.Scanned%100_000 == 0 {
			fmt.Fprintf(progress, "find-names: %d entries scanned, %d text files with hits\n", stats.Scanned, stats.TextFiles)
		}
		if f.err != nil {
			stats.Errors++
			onError(f.path, f.err)
		}
		for _, h := range f.hits {
			switch h.What {
			case "text":
				stats.TextFiles++
				stats.Occurrences += h.Count
				stats.ByCategory[h.Category]++
			case "link-target":
				stats.LinkTargets++
			default:
				stats.Names[h.What]++
			}
			emit(h)
		}
		return nil
	})
	stats.Errors += walkErrors
	return stats, err
}

func kindOf(mode fs.FileMode) Kind {
	switch {
	case mode.IsDir():
		return KindDir
	case mode&fs.ModeSymlink != 0:
		return KindSymlink
	case mode.IsRegular():
		return KindFile
	}
	return KindOther
}
