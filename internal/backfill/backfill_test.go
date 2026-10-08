package backfill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Catches good CARDs refused twice (2 of 82 boxes on the real backfill) because the model listed 8 key files or a
// path that does not exist: code drops the bad lines and keeps at most 7 valid ones.
func TestCardKeyFilesNormalized(t *testing.T) {
	dir := t.TempDir()
	for i := range 9 {
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%d.md", i)), []byte("x"), 0o644)
	}
	var lines []string
	for i := range 9 {
		lines = append(lines, fmt.Sprintf("- `f%d.md` — file %d, see `abc1234` and the old `gone.md`", i, i))
	}
	body := "## What it is\nx\n## Key files\n- `missing.md` — gone\n- `artifacts/` — bulk\n" + strings.Join(lines, "\n") + "\n## Next\ny\n"
	got := normalizeKeyFiles(body, Box{Name: "box", Abs: dir})
	keys := KeyFiles(got, "box")
	if len(keys) != maxKeyFiles || strings.Contains(got, "missing.md") || strings.Contains(got, "`artifacts/`") {
		t.Fatalf("keys %v\n%s", keys, got)
	}
	if !strings.Contains(got, "## Next\ny") {
		t.Fatal("other sections changed")
	}
}

// TestPlanIncludePaused: a parked campaign (quiet box, CARD status paused) got a CARD but never its notes, because
// only a done CARD or a STATE saying "merged/shipped" counted as finished; --include-paused takes it as a campaign.
func TestPlanIncludePaused(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-60 * 24 * time.Hour)
	for rel, body := range map[string]string{".brain/live": "", "work/parked/STATE.md": "# State — PARKED\n",
		"work/parked/CARD.md": "---\nid: card-parked\ntype: card\nstatus: paused\nsummary: parked\n---\nbody\n"} {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(body), 0o644)
		if !strings.HasSuffix(rel, "CARD.md") {
			_ = os.Chtimes(p, old, old)
		}
	}
	for _, include := range []bool{false, true} {
		_, plan, err := Plan(root, "", nil, time.Now(), include)
		if err != nil || len(plan) != 1 {
			t.Fatalf("plan %+v err %v", plan, err)
		}
		if got := plan[0].Action == "campaign"; got != include {
			t.Errorf("include-paused=%v: action %q", include, plan[0].Action)
		}
	}
}
