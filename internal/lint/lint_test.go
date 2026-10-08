package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"saahil.local/brain/internal/delta"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// seed lays out a small valid brain written the way the delta applier writes it.
func seed(t *testing.T) string {
	root := t.TempDir()
	write(t, root, "hubs/index.md", string(delta.Render(delta.Note{ID: "index", Type: "hub", Status: "current", Summary: "home"})))
	write(t, root, "hubs/image-lane.md", string(delta.Render(delta.Note{ID: "image-lane", Type: "hub", Status: "current",
		Summary: "image lane", Broader: []string{"index"}, Body: "## Start here\n\n- finding-ocr-recall\n"})))
	write(t, root, "work/box/reports/ocr.md", "---\ntype: report\nstatus: live\nsummary: ocr recall\nhubs: [image-lane]\n---\n## Recall\n\n0.9\n")
	write(t, root, "notes/finding-ocr-recall.md", string(delta.Render(delta.Note{ID: "finding-ocr-recall", Type: "finding",
		Status: "current", Summary: "OCR recall is 0.9 on the census", Scope: "hub", Hubs: []string{"image-lane"},
		Evidence: []string{"work/box/reports/ocr.md#recall"}, Body: "See [the report](../work/box/reports/ocr.md).\n"})))
	write(t, root, "memory/MEMORY.md", "- [OCR](finding-ocr-recall.md)\n")
	write(t, root, "hot.md", "This week: OCR.\n")
	return root
}

func rules(r Report) []string {
	var out []string
	for _, is := range r.Issues {
		out = append(out, string(is.Sev)+" "+is.Rule+" "+is.Path)
	}
	return out
}

// Catches lint disagreeing with the delta applier: a brain written by delta.Render must lint clean, or every dream
// run would look broken.
func TestCleanBrainExitsZero(t *testing.T) {
	r, err := Root(seed(t))
	if err != nil {
		t.Fatal(err)
	}
	if r.ExitCode() != 0 {
		t.Fatalf("exit %d: %v", r.ExitCode(), rules(r))
	}
}

// Catches a new plan under work/ without the brain-ready header (or with a giant section) passing silently: lint
// must warn (exit 1), not error, since docs are accepted as they come.
func TestWarnsMissingDocHeader(t *testing.T) {
	root := seed(t)
	write(t, root, "work/box/plans/PLAN.md", "# Plan\n\n## Step\n\n"+strings.Repeat("x", 21*1024)+"\n")
	r, err := Root(root)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(rules(r), "\n")
	if r.ExitCode() != 1 || !strings.Contains(got, "warn doc-header work/box/plans/PLAN.md") ||
		!strings.Contains(got, "warn doc-section work/box/plans/PLAN.md") {
		t.Fatalf("exit %d, issues:\n%s", r.ExitCode(), got)
	}
}

// Catches graph rules that only show across files: a supersedes cycle, two current notes answering one question,
// and a broader loop between hubs.
func TestGraphRules(t *testing.T) {
	root := seed(t)
	note := func(id string, status string, supersedes ...string) {
		write(t, root, "notes/"+id+".md", string(delta.Render(delta.Note{ID: id, Type: "decision", Status: status,
			Summary: id, Scope: "hub", Hubs: []string{"image-lane"}, Evidence: []string{"work/box/reports/ocr.md"},
			Supersedes: supersedes})))
	}
	note("decision-a", "current", "decision-b")
	note("decision-b", "current", "decision-a")
	write(t, root, "hubs/x.md", string(delta.Render(delta.Note{ID: "x", Type: "hub", Status: "current", Summary: "x", Broader: []string{"y"}})))
	write(t, root, "hubs/y.md", string(delta.Render(delta.Note{ID: "y", Type: "hub", Status: "current", Summary: "y", Broader: []string{"x"}})))
	r, err := Root(root)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(rules(r), "\n")
	for _, want := range []string{"error supersedes-cycle", "error one-current", "error broader-cycle"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// Catches a credential written into the brain (which syncs to both machines) going unreported.
func TestSecretIsAnError(t *testing.T) {
	root := seed(t)
	write(t, root, "inbox/capture.md", "deploy key "+"AKIA"+"ABCDEFGHIJKLMNOP"+" for the box\n")
	r, err := Root(root)
	if err != nil {
		t.Fatal(err)
	}
	if r.ExitCode() != 2 || !strings.Contains(strings.Join(rules(r), "\n"), "error secret inbox/capture.md") {
		t.Fatalf("exit %d: %v", r.ExitCode(), rules(r))
	}
}
