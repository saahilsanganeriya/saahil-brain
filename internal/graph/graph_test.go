package graph

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"saahil.local/brain/internal/scan"
	"saahil.local/brain/internal/store"
)

func put(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func build(t *testing.T, dbPath string, roots []scan.Root, full bool) *sql.DB {
	t.Helper()
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := Update(context.Background(), st.DB, roots, full); err != nil {
		t.Fatal(err)
	}
	return st.DB
}

// dump renders every index table as sorted text so two databases compare byte for byte (rowids excluded).
func dump(t *testing.T, db *sql.DB) string {
	t.Helper()
	queries := []string{
		`SELECT * FROM node ORDER BY id`,
		`SELECT src, dst, rel, why, source FROM edge ORDER BY src, rel, dst, source`,
		`SELECT * FROM section ORDER BY doc, idx`,
		`SELECT id, title, aliases, summary, body FROM fts ORDER BY id`,
		`SELECT key, value FROM meta WHERE key NOT LIKE 'run.%' ORDER BY key`,
	}
	var b strings.Builder
	for _, q := range queries {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintln(&b, vals...)
		}
		rows.Close()
	}
	return b.String()
}

func corpus(t *testing.T) (string, string) {
	brain, docs := t.TempDir(), t.TempDir()
	put(t, brain, "notes/finding-ocr.md", "---\nid: finding-ocr\ntype: finding\nstatus: current\nhubs: [image-lane]\n"+
		"evidence: [work/box/reports/ocr.md#recall]\n---\nOCR recall is 0.91 on the gate set.\n")
	put(t, brain, "hubs/image-lane.md", "# Image lane\n\nSee [[finding-ocr]].\n")
	put(t, docs, "box/plans/DESIGN.md", "# Design\n\nCurrent. Links [ocr](../reports/ocr.md) and [gone](../missing.md).\n")
	put(t, docs, "box/plans/DESIGN.v1.md", "# Design v1\n\nOld.\n")
	put(t, docs, "box/reports/ocr.md", "# OCR\n\n## recall\n\nPR #3488 measured it.\n")
	put(t, docs, "box/.data/run-1/result.json", `{"recall": 0.91}`)
	put(t, docs, "box/evidence/metrics.json", `{"recall": 0.91, "n": 120}`)
	return brain, docs
}

// TestIncrementalEqualsFull catches an incremental update that diverges from a rebuild after adds, edits, deletes,
// renames and new copies — stale edges of a deleted file, a copy group that keeps a renamed canonical, a section
// map that is not replaced.
func TestIncrementalEqualsFull(t *testing.T) {
	brain, docs := corpus(t)
	roots := []scan.Root{{Abs: brain}, {Abs: docs, Prefix: "work"}}
	incDB := filepath.Join(t.TempDir(), "inc.db")
	build(t, incDB, roots, true)

	put(t, docs, "box/plans/PLAN.md", "# Plan\n\nNew file citing ABC-7.\n")                            // add
	put(t, docs, "box/reports/ocr.md", "# OCR\n\n## recall\n\nRe-measured.\n\n## precision\n\nAlso.\n") // edit
	later := time.Now().Add(time.Hour)
	_ = os.Chtimes(filepath.Join(docs, "box/reports/ocr.md"), later, later)
	if err := os.Remove(filepath.Join(docs, "box/plans/DESIGN.v1.md")); err != nil { // delete
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(brain, "hubs/image-lane.md"), filepath.Join(brain, "hubs/image.md")); err != nil {
		t.Fatal(err)
	}
	put(t, docs, "other/copy-of-design.md", "# Design\n\nCurrent. Links [ocr](../reports/ocr.md) and [gone](../missing.md).\n")

	inc := build(t, incDB, roots, false)
	full := build(t, filepath.Join(t.TempDir(), "full.db"), roots, true)
	if a, b := dump(t, inc), dump(t, full); a != b {
		t.Fatalf("incremental != full\n--- incremental\n%s\n--- full\n%s", a, b)
	}
}

// TestCopyOfCollapses catches byte copies counted as separate live nodes: every copy must point at one canonical
// node (the oldest) through canonical_of and a copy_of edge, and the canonical must point at nothing.
func TestCopyOfCollapses(t *testing.T) {
	brain, docs := corpus(t)
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(docs, "box/plans/DESIGN.md"), old, old)
	for _, p := range []string{"snap/plans/DESIGN.md", "box/handoff/DESIGN.md", "box/log/DESIGN.md"} {
		put(t, docs, p, "# Design\n\nCurrent. Links [ocr](../reports/ocr.md) and [gone](../missing.md).\n")
	}
	// a history copy older than the live file must never become the canonical one (the dream's .archive copies did)
	older := old.Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(docs, "box/log/DESIGN.md"), older, older)
	db := build(t, filepath.Join(t.TempDir(), "b.db"), []scan.Root{{Abs: brain}, {Abs: docs, Prefix: "work"}}, true)

	rows, err := db.Query(`SELECT id, canonical_of FROM node WHERE path LIKE '%DESIGN.md' AND kind = 'doc' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for rows.Next() {
		var id, canon string
		_ = rows.Scan(&id, &canon)
		got[id] = canon
	}
	rows.Close()
	want := map[string]string{"work/box/plans/DESIGN.md": "", "work/snap/plans/DESIGN.md": "work/box/plans/DESIGN.md",
		"work/box/handoff/DESIGN.md": "work/box/plans/DESIGN.md", "work/box/log/DESIGN.md": "work/box/plans/DESIGN.md"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("canonical_of = %v, want %v", got, want)
	}
	var edges int
	_ = db.QueryRow(`SELECT count(*) FROM edge WHERE rel = 'copy_of' AND dst = 'work/box/plans/DESIGN.md'`).Scan(&edges)
	if edges != 3 {
		t.Fatalf("copy_of edges = %d, want 3", edges)
	}
}
