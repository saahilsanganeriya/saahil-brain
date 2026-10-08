package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/cutover"
)

func init() {
	register("cutover manifest", "TSV of every entry under <root> (sha256 ≤ 2 MB): <root> --out F [--skip-hash-under GLOB]... [--jobs N]", cmdCutoverManifest)
	register("cutover compare", "prove a move: <before.tsv> <after.tsv> [--allow-newer]; exit 1 on any missing file", cmdCutoverCompare)
	register("cutover reconcile", "laptop vs devbox text conflicts: <laptop.tsv> <devbox.tsv> --out R.md [--apply --machine M --root DIR]", cmdCutoverReconcile)
	register("cutover rewrite-paths", "old saahil-docs paths → new in text files and symlinks: <root> --from P... --to P [--apply] [--backup-dir D]", cmdCutoverRewrite)
	register("cutover find-names", "every non-history name, text mention and link target of saahil-docs: <root>... [--out F]", cmdCutoverFindNames)
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func printErr(path string, err error) { fmt.Fprintf(os.Stderr, "brain: %s: %v\n", path, err) }

func cmdCutoverManifest(args []string) int {
	fs := flag.NewFlagSet("cutover manifest", flag.ContinueOnError)
	out := fs.String("out", "", "manifest file to create (refused if it exists or lies inside <root>)")
	jobs := fs.Int("jobs", 4, "hashing workers (1..8)")
	var skip multiFlag
	fs.Var(&skip, "skip-hash-under", "doublestar glob of paths recorded by size+mtime only (repeatable), e.g. '*/artifacts/**'")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 1 || *out == "" {
		return fail("cutover manifest <root> --out <file.tsv> [--skip-hash-under GLOB]... [--jobs 1..8]")
	}
	if err := refuseInside(*out, pos[0]); err != nil {
		return fail("%v", err)
	}
	start := time.Now()
	var stats cutover.ManifestStats
	err = createExclusive(*out, func(w io.Writer) (werr error) {
		stats, werr = cutover.WriteManifest(w, cutover.ManifestOptions{Root: pos[0], Workers: *jobs, SkipHashUnder: skip,
			Progress: os.Stderr, OnError: printErr})
		return werr
	})
	if err != nil {
		return fail("%v", err)
	}
	fmt.Printf("%s: %d rows (%d files, %d dirs, %d symlinks); %d hashed (%.2f GB of %.2f GB); %d vanished mid-walk; %d errors; %s\n",
		*out, stats.Rows, stats.Files, stats.Dirs, stats.Symlinks, stats.Hashed, float64(stats.HashedBytes)/(1<<30),
		float64(stats.Bytes)/(1<<30), stats.Vanished, stats.Errors, time.Since(start).Round(time.Second))
	if stats.Errors > 0 {
		return fail("manifest incomplete: %d entries could not be read", stats.Errors)
	}
	return 0
}

// refuseInside keeps an output file out of the tree it describes; tool output never lands in a saahil-docs box.
func refuseInside(out, root string) error {
	o, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	r, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	for _, base := range []string{r, evalOrSelf(r)} {
		if dir := evalOrSelf(filepath.Dir(o)); dir == base || strings.HasPrefix(dir, base+string(filepath.Separator)) {
			return fmt.Errorf("%s is inside %s", out, root)
		}
	}
	return nil
}

func evalOrSelf(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}

// createExclusive writes path through path.partial and renames it into place only when write succeeds.
func createExclusive(path string, write func(io.Writer) error) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s exists; manifests are evidence and are never overwritten", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	partial := path + ".partial"
	f, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if err := write(f); err != nil {
		f.Close()
		return fmt.Errorf("%w (partial output left at %s)", err, partial)
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(partial, path)
}

const extraShown = 20

func cmdCutoverCompare(args []string) int {
	fs := flag.NewFlagSet("cutover compare", flag.ContinueOnError)
	allowNewer := fs.Bool("allow-newer", false, "a file with a newer mtime is a later edit (listed), not a failure")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 2 {
		return fail("cutover compare <before.tsv> <after.tsv> [--allow-newer]")
	}
	before, err := os.Open(pos[0])
	if err != nil {
		return fail("%v", err)
	}
	defer before.Close()
	after, err := os.Open(pos[1])
	if err != nil {
		return fail("%v", err)
	}
	defer after.Close()
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	extras := 0
	res, err := cutover.Compare(before, after, *allowNewer, func(cat cutover.Category, path string) {
		if cat == cutover.Extra {
			if extras++; extras > extraShown {
				return
			}
		}
		fmt.Fprintf(out, "%s\t%s\n", cat, path)
	})
	if err != nil {
		return fail("%v", err)
	}
	if extras > extraShown {
		fmt.Fprintf(out, "extra\t… and %d more\n", extras-extraShown)
	}
	fmt.Fprintf(out, "same %d · changed-later %d · changed %d · missing %d · extra %d\n",
		res.Same, res.ChangedLater, res.Changed, res.Missing, res.Extra)
	if res.Failed() {
		out.Flush()
		return fail("move NOT proven: %d missing, %d changed without a later mtime", res.Missing, res.Changed)
	}
	return 0
}

func cmdCutoverReconcile(args []string) int {
	fs := flag.NewFlagSet("cutover reconcile", flag.ContinueOnError)
	out := fs.String("out", "", "markdown report to write")
	apply := fs.Bool("apply", false, "rename THIS machine's losing copies aside (never touches the winner)")
	machine := fs.String("machine", "", "this machine, with --apply: laptop | devbox")
	root := fs.String("root", "", "this machine's saahil-docs root, with --apply")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 2 || *out == "" {
		return fail("cutover reconcile <laptop.tsv> <devbox.tsv> --out <report.md> [--apply --machine laptop|devbox --root DIR]")
	}
	if *apply && (*root == "" || (*machine != string(config.Laptop) && *machine != string(config.Devbox))) {
		return fail("--apply needs --machine laptop|devbox and --root")
	}
	laptop, err := os.Open(pos[0])
	if err != nil {
		return fail("%v", err)
	}
	defer laptop.Close()
	devbox, err := os.Open(pos[1])
	if err != nil {
		return fail("%v", err)
	}
	defer devbox.Close()
	res, err := cutover.Reconcile(laptop, devbox)
	if err != nil {
		return fail("%v", err)
	}
	report, err := os.Create(*out)
	if err != nil {
		return fail("%v", err)
	}
	if err := cutover.WriteReconcileReport(report, res); err != nil {
		report.Close()
		return fail("%v", err)
	}
	if err := report.Close(); err != nil {
		return fail("%v", err)
	}
	fmt.Printf("%s: same %d · conflicts %d · only-laptop %d · only-devbox %d · unhashed-differ %d · binary-differ %d · kind-differ %d\n",
		*out, res.Same, len(res.Conflicts), res.OnlyLaptop, res.OnlyDevbox, len(res.Unhashed), len(res.NonText), len(res.KindDiffers))
	if !*apply {
		return 0
	}
	moved, err := cutover.ApplyReconcile(*root, *machine, res.Conflicts)
	for _, m := range moved {
		fmt.Println("renamed aside:", m)
	}
	if err != nil {
		return fail("%v", err)
	}
	return 0
}

func cmdCutoverRewrite(args []string) int {
	fs := flag.NewFlagSet("cutover rewrite-paths", flag.ContinueOnError)
	var from multiFlag
	fs.Var(&from, "from", "old prefix (repeatable); '*' matches one path component")
	to := fs.String("to", "", "new prefix")
	apply := fs.Bool("apply", false, "write the changes (default: dry run, nothing written)")
	backupDir := fs.String("backup-dir", "", "where originals and manifest.tsv go with --apply (default <brain>/.archive/cutover/rewrite-<UTC>)")
	jobs := fs.Int("jobs", 4, "workers (1..8)")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) != 1 || len(from) == 0 || *to == "" {
		return fail("cutover rewrite-paths <root> --from <old-prefix> [--from ...] --to <new-prefix> [--apply] [--backup-dir D]")
	}
	if *apply && *backupDir == "" {
		*backupDir = filepath.Join(config.BrainRoot(), ".archive", "cutover", "rewrite-"+time.Now().UTC().Format("20060102T150405Z"))
	}
	verb := "would change"
	if *apply {
		verb = "changed"
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	stats, err := cutover.Rewrite(cutover.RewriteOptions{Root: pos[0], From: from, To: *to, Apply: *apply, BackupDir: *backupDir,
		Workers: *jobs, Progress: os.Stderr, OnError: printErr}, func(c cutover.FileChange) {
		fmt.Fprintf(out, "%s\t%d\t%s\t%s\n", verb, c.Count, c.Category, c.Path)
	})
	if err != nil {
		out.Flush()
		return fail("%v", err)
	}
	fmt.Fprintf(out, "\n%d entries scanned · %d text · %d binary · %d over 10 MB · %d errors\n", stats.Scanned, stats.Text,
		stats.Binary, stats.Large, stats.Errors)
	fmt.Fprintf(out, "%d files/links %s (%d occurrences):\n", stats.Changed, verb, stats.Occurrences)
	printCounts(out, stats.ByCategory)
	if *apply {
		fmt.Fprintf(out, "backup: %s\n", *backupDir)
	}
	if stats.Errors > 0 {
		out.Flush()
		return fail("%d entries not handled (see stderr)", stats.Errors)
	}
	return 0
}

func printCounts(w io.Writer, counts map[string]int) {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		fmt.Fprintf(w, "  %7d  %s\n", counts[k], k)
	}
}

func cmdCutoverFindNames(args []string) int {
	fs := flag.NewFlagSet("cutover find-names", flag.ContinueOnError)
	needle := fs.String("needle", "saahil-docs", "name and text to look for")
	outPath := fs.String("out", "", "write the hit list here instead of stdout")
	jobs := fs.Int("jobs", 4, "workers (1..8)")
	roots, err := parseInterleaved(fs, args)
	if err != nil || len(roots) == 0 {
		return fail("cutover find-names <root>... [--needle saahil-docs] [--out F] [--jobs 1..8]")
	}
	var hits io.Writer = os.Stdout
	if *outPath != "" {
		f, err := os.Create(*outPath)
		if err != nil {
			return fail("%v", err)
		}
		defer f.Close()
		hits = f
	}
	bw := bufio.NewWriter(hits)
	stats, err := cutover.FindNames(roots, *needle, *jobs, os.Stderr, printErr, func(h cutover.Hit) {
		fmt.Fprintf(bw, "%s\t%d\t%s\t%s\n", h.What, h.Count, h.Category, h.Path)
	})
	if ferr := bw.Flush(); err == nil {
		err = ferr
	}
	if err != nil {
		return fail("%v", err)
	}
	names := 0
	for _, n := range stats.Names {
		names += n
	}
	fmt.Printf("\n%d entries scanned · %d named %q · %d text files mention it (%d occurrences) · %d symlink targets · %d errors\n",
		stats.Scanned, names, *needle, stats.TextFiles, stats.Occurrences, stats.LinkTargets, stats.Errors)
	fmt.Println("names by kind:")
	printCounts(os.Stdout, stats.Names)
	fmt.Println("text files by category:")
	printCounts(os.Stdout, stats.ByCategory)
	return 0
}
