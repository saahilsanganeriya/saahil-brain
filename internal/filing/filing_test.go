package filing

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"saahil.local/brain/internal/llm"
)

func put(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func memory(modified, body string) string {
	return "---\nname: x\ndescription: d\nmetadata:\n  type: feedback\n  modified: " + modified + "\n---\n" + body + "\n"
}

// syntheticImport: the same memory on both machines (laptop copy with other frontmatter), a byte copy inside the
// devbox, two folder indexes, one listed file missing on disk and one stray file outside the manifest.
func syntheticImport(t *testing.T) string {
	root := t.TempDir()
	put(t, root, "hubs/index.md", "---\nid: index\ntype: hub\nstatus: current\nsummary: home\n---\n")
	put(t, root, "hubs/git.md", "---\nid: git\ntype: hub\nstatus: current\nsummary: git practice\naliases: [rebase]\nbroader: [index]\n---\n")
	files := map[string]string{
		"devbox/repo-a/rebase-first.md":    memory("2026-09-01T00:00:00Z", "Rebase before push."),
		"laptop/repo-a/rebase-first.md":    memory("2026-09-02T00:00:00Z", "Rebase before push."),
		"devbox/repo-b/rebase-first.md":    memory("2026-09-01T00:00:00Z", "Rebase before push."),
		"devbox/repo-a/stack-prs.md":       memory("2026-09-03T00:00:00Z", "Stack dependent PRs."),
		"laptop/repo-a/tabs-not-spaces.md": memory("2026-09-04T00:00:00Z", "Go uses tabs."),
		"devbox/repo-a/MEMORY.md":          "- [Rebase](rebase-first.md)\n- [Gone](deleted-memory.md)\n",
		"laptop/repo-a/MEMORY.md":          "- [Rebase](rebase-first.md)\n",
	}
	manifest := map[string]string{}
	for rel, content := range files {
		put(t, root, ".import/"+rel, content)
		machine, path, _ := strings.Cut(rel, "/")
		manifest[machine] += path + "\t" + fmt.Sprint(len(content)) + "\t-\n"
	}
	manifest["devbox"] += "repo-a/listed-but-missing.md\t10\t-\n"
	put(t, root, ".import/devbox/repo-a/stack-prs.md.bak", "old")
	for machine, m := range manifest {
		put(t, root, ".import/"+machine+"-manifest.tsv", m)
	}
	return root
}

// TestReconcileCountsCopiesOnce catches filing that turns the laptop and devbox copies of one memory into two notes,
// files a folder index as a memory, or reports counts that do not add up to what was imported.
func TestReconcileCountsCopiesOnce(t *testing.T) {
	root := syntheticImport(t)
	plan, err := NewPlan(root, []string{"devbox", "laptop"}, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	r := plan.Reconcile
	if r.Imported != 8 || r.Unique != 3 || r.ThisRun != 3 || r.Duplicates != 2 || r.CrossMachine != 2 {
		t.Fatalf("reconcile: %+v", r)
	}
	if r.Skipped["folder index (MEMORY.md)"] != 2 || r.Skipped["listed in the manifest, missing on disk"] != 1 ||
		r.NotInManifest != 1 || r.OrphanIndexLines != 1 {
		t.Fatalf("skips: %+v", r)
	}
	if !r.Balanced() {
		t.Fatalf("imported %d != filed + this run + waiting + skipped: %+v", r.Imported, r)
	}
	for _, it := range plan.Batches[0] {
		if it.Name == "rebase-first.md" && (it.AsOf != "2026-09-02" || len(it.Copies) != 2) {
			t.Errorf("canonical copy should be the newest, carrying the other two: %+v", it)
		}
	}
}

// TestFilingResumesWithoutPayingTwice catches a filing run that re-sends files it already filed, marks a file filed
// when no applied note cites it as a source, or retries a left-out file forever (it gets one more paid attempt).
func TestFilingResumesWithoutPayingTwice(t *testing.T) {
	root := syntheticImport(t)
	fake := &llm.Fake{CostUSD: 0.01, Respond: func(c llm.Call) llm.Result {
		var in struct {
			Material []struct{ Path, Name string } `json:"material"`
		}
		_ = json.Unmarshal([]byte(c.Input), &in)
		var ops []map[string]any
		for _, m := range in.Material {
			if strings.Contains(m.Path, "tabs-not-spaces") {
				continue // the model forgot one file: it must stay unfiled
			}
			slug := strings.TrimSuffix(filepath.Base(m.Path), ".md")
			ops = append(ops, map[string]any{"op": "add_note", "note": map[string]any{"id": "lesson-" + slug,
				"type": "lesson", "status": "current", "summary": slug, "scope": "global", "hubs": []string{"git"},
				"evidence": []string{m.Path}, "sources": []string{m.Path}, "body": "when x → y, because z."}})
		}
		out, _ := json.Marshal(map[string]any{"ops": ops})
		return llm.Result{JSON: out}
	}}
	ledger, err := llm.OpenLedger(filepath.Join(root, ".index", "brain.db"), filepath.Join(root, ".data", "llm"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	cfg := Config{Root: root, Machines: []string{"devbox", "laptop"}, Batch: 2, Model: "haiku", Runner: fake,
		Ledger: ledger, Skill: "file skill", BudgetUSD: 1, Now: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)}

	first, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first.FiledNow != 2 || first.Unfiled != 1 || fake.Calls() != 2 {
		t.Fatalf("first run: %+v, calls %d", first, fake.Calls())
	}
	second, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if second.Reconcile.AlreadyFiled != 2 || second.Reconcile.ThisRun != 1 || fake.Calls() != 3 {
		t.Fatalf("second run re-sent filed files: %+v, calls %d", second.Reconcile, fake.Calls())
	}
	third, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if third.Reconcile.ThisRun != 0 || third.Reconcile.Skipped[reasonGaveUp] != 1 || fake.Calls() != 3 {
		t.Fatalf("third run: %+v, calls %d", third.Reconcile, fake.Calls())
	}
	if _, err := os.Stat(filepath.Join(root, "notes", "lesson-rebase-first.md")); err != nil {
		t.Fatal(err)
	}
}

// TestRenderMemoryIndexCaps catches a MEMORY.md that outgrows Claude Code's load window (200 lines / 25 KB) when
// there are many global notes with long summaries, drops the hub entry lines, or writes descriptions instead of facts.
func TestRenderMemoryIndexCaps(t *testing.T) {
	root := t.TempDir()
	put(t, root, "hubs/index.md", "---\nid: index\ntype: hub\nstatus: current\nsummary: home\n---\n")
	for i := 0; i < 40; i++ {
		put(t, root, fmt.Sprintf("hubs/hub-%02d.md", i), fmt.Sprintf(
			"---\nid: hub-%02d\ntype: hub\nstatus: current\nsummary: hub %02d %s\nbroader: [index]\n---\n", i, i, strings.Repeat("h", 120)))
	}
	for i := 0; i < 500; i++ {
		status := "current"
		if i%50 == 0 {
			status = "superseded"
		}
		put(t, root, fmt.Sprintf("notes/lesson-%03d.md", i), fmt.Sprintf("---\nid: lesson-%03d\ntype: lesson\nstatus: %s\n"+
			"summary: fact %03d %s\nscope: global\nhubs: [hub-01]\nas_of: 2026-10-01\n---\nbody\n", i, status, i, strings.Repeat("f", 150)))
	}
	put(t, root, "notes/finding-local.md", "---\nid: finding-local\ntype: finding\nstatus: current\nsummary: only for one hub\n"+
		"scope: hub\nhubs: [hub-01]\n---\n")
	if err := RenderMemoryIndex(root); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "memory", "MEMORY.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if lines := strings.Count(text, "\n"); lines > 200 || len(b) > 25*1024 {
		t.Fatalf("MEMORY.md is %d lines, %d bytes", lines, len(b))
	}
	if n := strings.Count(text, "](../hubs/"); n < 25 {
		t.Errorf("only %d hub entry lines", n)
	}
	if n := strings.Count(text, "](../notes/"); n < 80 {
		t.Errorf("only %d fact lines", n)
	}
	for _, absent := range []string{"fact 000", "only for one hub"} { // superseded, and hub-scoped
		if strings.Contains(text, absent) {
			t.Errorf("MEMORY.md carries %q", absent)
		}
	}
	if !strings.Contains(text, "- fact 001 "+strings.Repeat("f", 150)) {
		t.Errorf("fact lines must carry the summary itself")
	}
}
