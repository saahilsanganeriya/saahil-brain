package doctor

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"saahil.local/brain/internal/delta"
	"saahil.local/brain/internal/index"
)

// Catches graph health reporting green over a broken brain: a dangling link, a superseded note still marked
// current, and a note past its 4 KB cap must each turn their check, with the measured count in the detail.
func TestGraphChecksReportBreaches(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BRAIN_HOME", home)
	root := filepath.Join(home, "brain")
	note := func(id, status, body string, supersedes ...string) string {
		return string(delta.Render(delta.Note{ID: id, Type: "decision", Status: status, Summary: id, Scope: "hub",
			Hubs: []string{"image-lane"}, Supersedes: supersedes, Body: body}))
	}
	for rel, body := range map[string]string{
		"hubs/index.md":            string(delta.Render(delta.Note{ID: "index", Type: "hub", Status: "current", Summary: "home"})),
		"hubs/image-lane.md":       string(delta.Render(delta.Note{ID: "image-lane", Type: "hub", Status: "current", Summary: "images", Broader: []string{"index"}})),
		"notes/decision-old.md":    note("decision-old", "current", "See [[decision-gone]].\n"),
		"notes/decision-new.md":    note("decision-new", "current", "", "decision-old"),
		"notes/decision-ledger.md": note("decision-ledger", "current", strings.Repeat("a long ledger line\n", 300)),
	} {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ix, err := index.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	if _, err := ix.Update(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		run  func(*sql.DB) (Status, string, error)
		want Status
		says string
	}{
		"dangling":     {dangling, Warn, "1 links"},
		"supersession": {supersession, Fail, "1 of 1 supersedes"},
		"coverage":     {hubCoverage, OK, "100.0% of 3"},
		"orphans":      {orphans, OK, "0 of 5"},
	} {
		st, detail, err := c.run(ix.DB())
		if err != nil || st != c.want || !strings.Contains(detail, c.says) {
			t.Errorf("%s: %s %q (%v), want %s %q", name, st, detail, err, c.want, c.says)
		}
	}
	if st, detail := caps(root); st != Fail || !strings.Contains(detail, "notes/decision-ledger.md") {
		t.Errorf("caps: %s %q", st, detail)
	}
}
