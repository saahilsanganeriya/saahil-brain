package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"saahil.local/brain/internal/config"
)

// Catches `brain dream merge` writing into the brain it only reports on — a ledger under .index/, an owner question
// queued under .brain/, a note edited — while it must still print the changes and hand the delta over outside.
func TestDreamMergeDryRunWritesNothing(t *testing.T) {
	root, scratch := t.TempDir(), t.TempDir()
	files := map[string]string{
		"hubs/git.md": "---\nid: git\ntype: hub\nstatus: current\nsummary: git\nbroader: [index]\n---\n",
	}
	for _, n := range []struct{ id, typ, asOf string }{{"anti-pattern-a", "anti-pattern", "2026-10-01"},
		{"anti-pattern-b", "anti-pattern", "2026-10-02"}, {"decision-p", "decision", "2026-10-03"},
		{"decision-q", "decision", "2026-10-03"}} {
		files["notes/"+n.id+".md"] = "---\nid: " + n.id + "\ntype: " + n.typ + "\nstatus: current\nsummary: " + n.id +
			"\nscope: global\nhubs: [git]\nevidence: [hubs/git.md]\nas_of: " + n.asOf + "\n---\nbody of " + n.id + "\n"
	}
	for rel, body := range files {
		os.MkdirAll(filepath.Join(root, filepath.Dir(rel)), 0o755)
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	find := filepath.Join(scratch, "find.json")
	os.WriteFile(find, []byte(`{"duplicates":[{"keep":"anti-pattern-b","supersede":["anti-pattern-a"],"why":"same"}],
"conflicts":[{"newer":"decision-p","older":"decision-q","resolution":"owner","why":"same date"}],"stale":[]}`), 0o644)
	bodies := filepath.Join(scratch, "bodies.json")
	os.WriteFile(bodies, []byte(`{"result":"{\"bodies\":[{\"keep\":\"anti-pattern-b\",\"body\":\"both facts\"}]}"}`), 0o644)
	before := tree(t, root)

	var out bytes.Buffer
	code := dreamMergeDry(&out, root, mergeDryOptions{budgetUSD: 1, capUSD: 2, findAnswer: find, bodiesAnswer: bodies,
		out: filepath.Join(scratch, "delta.json"), today: "2026-10-08"})
	if code != 0 || !strings.Contains(out.String(), "would change 2 files") ||
		!strings.Contains(out.String(), "would queue (pairs asked before are skipped): 1") {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
	if after := tree(t, root); after != before {
		t.Fatalf("the dry run wrote into the brain:\nbefore %s\nafter  %s", before, after)
	}
	if d, err := os.ReadFile(filepath.Join(scratch, "delta.json")); err != nil || !strings.Contains(string(d), `"replace_body": "both facts\n"`) {
		t.Fatalf("delta not handed over: %s %v", d, err)
	}
}

func tree(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := os.ReadFile(p)
		rel, _ := filepath.Rel(root, p)
		b.WriteString(rel + "=" + string(content) + "|")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// Catches --out or --ledger slipping into the live brain under the devbox spelling of home (/home/test-user/brain is the
// same folder as /Users/test-user/brain there).
func TestInsideSeesBothHomeSpellings(t *testing.T) {
	brain := filepath.Join(config.LaptopHome(), "brain")
	for path, want := range map[string]bool{filepath.Join(config.DevboxHome(), "brain", "delta.json"): true,
		filepath.Join(brain, ".index", "x"): true, filepath.Join(config.LaptopHome(), "brain-other", "x"): false} {
		if inside(path, brain) != want {
			t.Errorf("inside(%s) = %v", path, !want)
		}
	}
}
