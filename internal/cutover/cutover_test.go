package cutover

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const oldPrefix = "/old/home/docs/saahil-docs/"
const newPrefix = "/new/brain/work/"

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, t0, t0); err != nil {
			t.Fatal(err)
		}
	}
}

func manifestOf(t *testing.T, root string) string {
	t.Helper()
	var out bytes.Buffer
	if _, err := WriteManifest(&out, ManifestOptions{Root: root, Workers: 3, HashMaxBytes: 64}); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func rewriteOpts(root string, apply bool, backup string) RewriteOptions {
	return RewriteOptions{Root: root, From: []string{oldPrefix}, To: newPrefix, Apply: apply, BackupDir: backup, Workers: 3}
}

// TestCompareCatchesMissingFile: a move that lost a file must fail the proof. The tree holds a directory "a" next to a
// file "a-c" ('-' sorts before '/'), so a manifest reader that assumed plain string order would misalign the merge and
// report phantom missing/extra rows; a symlinked directory must be listed, never walked into.
func TestCompareCatchesMissingFile(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a/x.md": "x", "a/y.md": "y", "a-c.md": "c", "big.bin": strings.Repeat("b", 100),
	})
	if err := os.Symlink("a", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	before := manifestOf(t, root)
	if strings.Contains(before, "link/x.md") || !strings.Contains(before, "link\t") {
		t.Fatalf("directory symlink must be one row, not walked:\n%s", before)
	}

	if err := os.Remove(filepath.Join(root, "a/y.md")); err != nil {
		t.Fatal(err)
	}
	writeTree(t, root, map[string]string{"a-c.md": "edited later"})
	if err := os.Chtimes(filepath.Join(root, "a-c.md"), t0.Add(time.Hour), t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	after := manifestOf(t, root)

	var listed []string
	res, err := Compare(strings.NewReader(before), strings.NewReader(after), true, func(cat Category, path string) {
		listed = append(listed, string(cat)+" "+path)
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Missing != 1 || res.ChangedLater != 1 || res.Changed != 0 || res.Extra != 0 || res.Same != 4 {
		t.Fatalf("got %+v, listed %v", res, listed)
	}
	if !res.Failed() || strings.Join(listed, ",") != "missing a/y.md,changed-later a-c.md" {
		t.Fatalf("listed %v", listed)
	}
}

// TestReconcileMovesOnlyThisMachinesLoser: the newer copy wins, and --apply on one machine renames aside only that
// machine's losing copies; renaming the winner, or the other machine's files, would destroy the edit that should win.
// A file on one side that is a directory on the other is reported, not silently dropped.
func TestReconcileMovesOnlyThisMachinesLoser(t *testing.T) {
	laptop, devbox := t.TempDir(), t.TempDir()
	writeTree(t, laptop, map[string]string{"a.md": "laptop newer", "b.md": "laptop older", "same.md": "s", "kind": "f"})
	writeTree(t, devbox, map[string]string{"a.md": "devbox older", "b.md": "devbox newer", "same.md": "s", "kind/x": "d"})
	later := t0.Add(2 * time.Hour)
	_ = os.Chtimes(filepath.Join(laptop, "a.md"), later, later)
	_ = os.Chtimes(filepath.Join(devbox, "b.md"), later, later)

	res, err := Reconcile(strings.NewReader(manifestOf(t, laptop)), strings.NewReader(manifestOf(t, devbox)))
	if err != nil {
		t.Fatal(err)
	}
	if res.Same != 1 || len(res.Conflicts) != 2 || len(res.KindDiffers) != 1 || res.OnlyDevbox != 1 {
		t.Fatalf("got %+v", res)
	}
	a, b := res.Conflicts[0], res.Conflicts[1]
	if a.Path != "a.md" || a.Loser != "devbox" || a.LoserCopy != "a.md.conflict-devbox-20261001T120000Z" ||
		b.Path != "b.md" || b.Loser != "laptop" {
		t.Fatalf("conflicts %+v", res.Conflicts)
	}

	moved, err := ApplyReconcile(devbox, "devbox", res.Conflicts)
	if err != nil || len(moved) != 1 {
		t.Fatalf("moved %v err %v", moved, err)
	}
	if readFile(t, filepath.Join(devbox, a.LoserCopy)) != "devbox older" || readFile(t, filepath.Join(devbox, "b.md")) != "devbox newer" {
		t.Fatal("wrong file moved aside")
	}
	if _, err := os.Stat(filepath.Join(devbox, "a.md")); !os.IsNotExist(err) {
		t.Fatal("loser should be renamed, not copied")
	}
}

// TestRewriteSkipsBinaryAndHistory: only live text is rewritten; binaries (by NUL byte or extension), git internals,
// box history under log/ and session transcripts keep the old paths byte for byte, because they record what was true;
// .env files are secrets and are never opened.
func TestRewriteSkipsBinaryAndHistory(t *testing.T) {
	root := t.TempDir()
	untouched := map[string]string{
		"img/scan.png":            "PNG " + oldPrefix + "x",
		"data/blob.dat":           "head\x00" + oldPrefix + "x",
		".git/config":             oldPrefix,
		"box/.env":                "DOCS=" + oldPrefix,
		"box/log/2026-10-01.md":   oldPrefix,
		"box/transcripts/t.jsonl": oldPrefix,
		"box/0a1b2c3d-1111-2222-3333-444455556666.jsonl": oldPrefix,
	}
	writeTree(t, root, untouched)
	writeTree(t, root, map[string]string{"box/STATE.md": "see " + oldPrefix + "box/a and " + oldPrefix + "box/b\n"})

	var changed []FileChange
	stats, err := Rewrite(rewriteOpts(root, true, t.TempDir()), func(c FileChange) { changed = append(changed, c) })
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0].Path != "box/STATE.md" || changed[0].Count != 2 || stats.Errors != 0 {
		t.Fatalf("changed %+v stats %+v", changed, stats)
	}
	if got := readFile(t, filepath.Join(root, "box/STATE.md")); got != "see "+newPrefix+"box/a and "+newPrefix+"box/b\n" {
		t.Fatalf("rewritten %q", got)
	}
	for rel, content := range untouched {
		if readFile(t, filepath.Join(root, rel)) != content {
			t.Errorf("%s was rewritten", rel)
		}
	}
}

// TestRewriteSkipAndMinAge: a skipped top-level entry and a file written within MinAge (an agent may be mid-edit) stay
// untouched, while a loose top-level file is still rewritten; splitting the walk into one root per entry had silently
// skipped every top-level file.
func TestRewriteSkipAndMinAge(t *testing.T) {
	root := t.TempDir()
	line := "see " + oldPrefix + "box/a\n"
	writeTree(t, root, map[string]string{"STATE.md": line, "busy/x.md": line, "tools/old.sh": line, "tools/fresh.sh": line})
	now := time.Now()
	_ = os.Chtimes(filepath.Join(root, "tools/fresh.sh"), now, now)

	opts := rewriteOpts(root, true, filepath.Join(t.TempDir(), "rewrite-1"))
	opts.Skip, opts.MinAge = []string{"busy"}, 30*time.Minute
	var changed []string
	stats, err := Rewrite(opts, func(c FileChange) { changed = append(changed, c.Path) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(changed, ",") != "STATE.md,tools/old.sh" || stats.Recent != 1 {
		t.Fatalf("changed %v stats %+v", changed, stats)
	}
	for _, rel := range []string{"busy/x.md", "tools/fresh.sh"} {
		if readFile(t, filepath.Join(root, rel)) != line {
			t.Errorf("%s was rewritten", rel)
		}
	}
}

// TestRewriteKeepsBackup: a dry run writes nothing, and --apply copies each original (bytes, mode, mtime) into the
// backup dir and records sha256 before/after and the old mtime before replacing the file, keeping its mode.
func TestRewriteKeepsBackup(t *testing.T) {
	root, backup := t.TempDir(), filepath.Join(t.TempDir(), "rewrite-1")
	original := "path " + oldPrefix + "box/x\n"
	writeTree(t, root, map[string]string{"box/run.sh": original})
	path := filepath.Join(root, "box/run.sh")
	_ = os.Chmod(path, 0o750)
	_ = os.Chtimes(path, t0, t0)

	stats, err := Rewrite(rewriteOpts(root, false, backup), func(FileChange) {})
	if err != nil || stats.Changed != 1 {
		t.Fatalf("dry run: %+v %v", stats, err)
	}
	if readFile(t, path) != original {
		t.Fatal("dry run wrote the file")
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatal("dry run created the backup dir")
	}

	if _, err := Rewrite(rewriteOpts(root, true, backup), func(FileChange) {}); err != nil {
		t.Fatal(err)
	}
	rewritten := readFile(t, path)
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o750 || rewritten != "path "+newPrefix+"box/x\n" {
		t.Fatalf("mode %v content %q", info.Mode(), rewritten)
	}
	saved := filepath.Join(backup, "files", "box/run.sh")
	if info, _ := os.Stat(saved); readFile(t, saved) != original || !info.ModTime().Equal(t0) {
		t.Fatal("backup copy must hold the original bytes and mtime")
	}
	sc := bufio.NewScanner(strings.NewReader(readFile(t, filepath.Join(backup, "manifest.tsv"))))
	var row string
	for sc.Scan() {
		if !strings.HasPrefix(sc.Text(), "#") {
			row = sc.Text()
		}
	}
	want := strings.Join([]string{"box/run.sh", "file", "1", t0.Format(time.RFC3339Nano), sha(original), sha(rewritten), "-", "-"}, "\t")
	if row != want {
		t.Fatalf("manifest row\n got %q\nwant %q", row, want)
	}
	if _, err := Rewrite(rewriteOpts(root, true, backup), func(FileChange) {}); err == nil {
		t.Fatal("a second apply must refuse to reuse a backup dir that already holds a manifest")
	}
}

// TestVenvShebangRewritten: a moved venv only runs if its pyvenv.cfg, activate scripts, both shebang forms pip writes
// (#!<venv>/bin/python and the long-path ”'exec' form) and absolute interpreter symlinks all point at the new place;
// a rewrite that would push a shebang past the kernel's 127-byte limit is refused rather than written broken.
func TestVenvShebangRewritten(t *testing.T) {
	root := t.TempDir()
	venv := oldPrefix + "box/venv"
	writeTree(t, root, map[string]string{
		"box/venv/pyvenv.cfg":      "home = /usr/bin\ncommand = /usr/bin/python3 -m venv " + venv + "\n",
		"box/venv/bin/activate":    "VIRTUAL_ENV=" + venv + "\nexport VIRTUAL_ENV\n",
		"box/venv/bin/pip":         "#!" + venv + "/bin/python3\nimport sys\n",
		"box/venv/bin/pip3.12":     "#!/bin/sh\n'''exec' " + venv + "/bin/python3 \"$0\" \"$@\"\n' '''\nimport sys\n",
		"box/venv/bin/real-python": "#!/bin/sh\n",
	})
	if err := os.Symlink(venv+"/bin/real-python", filepath.Join(root, "box/venv/bin/python3")); err != nil {
		t.Fatal(err)
	}

	categories := map[string]string{}
	if _, err := Rewrite(rewriteOpts(root, true, t.TempDir()), func(c FileChange) { categories[c.Path] = c.Category }); err != nil {
		t.Fatal(err)
	}
	newVenv := newPrefix + "box/venv"
	if got := readFile(t, filepath.Join(root, "box/venv/bin/pip")); got != "#!"+newVenv+"/bin/python3\nimport sys\n" {
		t.Fatalf("pip shebang %q", got)
	}
	if got := readFile(t, filepath.Join(root, "box/venv/bin/pip3.12")); !strings.Contains(got, "'''exec' "+newVenv+"/bin/python3 ") {
		t.Fatalf("exec-form shebang %q", got)
	}
	for _, rel := range []string{"box/venv/pyvenv.cfg", "box/venv/bin/activate"} {
		if got := readFile(t, filepath.Join(root, rel)); strings.Contains(got, oldPrefix) || !strings.Contains(got, newVenv) {
			t.Fatalf("%s: %q", rel, got)
		}
	}
	if target, _ := os.Readlink(filepath.Join(root, "box/venv/bin/python3")); target != newVenv+"/bin/real-python" {
		t.Fatalf("interpreter symlink -> %q", target)
	}
	want := map[string]string{"box/venv/pyvenv.cfg": "venv:pyvenv.cfg", "box/venv/bin/activate": "venv:activate",
		"box/venv/bin/pip": "venv:script", "box/venv/bin/pip3.12": "venv:script", "box/venv/bin/python3": "venv:symlink"}
	for rel, cat := range want {
		if categories[rel] != cat {
			t.Errorf("%s category %q, want %q", rel, categories[rel], cat)
		}
	}

	long := t.TempDir()
	writeTree(t, long, map[string]string{"venv/bin/pip": "#!" + oldPrefix + "venv/bin/python3\n"})
	opt := rewriteOpts(long, true, t.TempDir())
	opt.To = "/" + strings.Repeat("deep/", 30)
	stats, _ := Rewrite(opt, func(FileChange) {})
	if stats.Errors != 1 || readFile(t, filepath.Join(long, "venv/bin/pip")) != "#!"+oldPrefix+"venv/bin/python3\n" {
		t.Fatalf("an over-long shebang must be refused: %+v", stats)
	}
}
