package backfill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		lines = append(lines, fmt.Sprintf("- `f%d.md` — file %d", i, i))
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
