package hooks

import (
	"slices"
	"testing"
	"time"
)

// TestFreshOutputsFillEmptySlots catches the gap the navigation eval measured (2026-10-09: 10 of 24 DEV misses were
// evidence, reports and PR bodies the box got in the hours before the session, linked from nothing it named), and
// the two ways a fix goes wrong: a fresh file taking a slot a pointer earned without its freshness (the first weight
// tried lost a session its SPEC), and a replay counting a file written after the session started.
func TestFreshOutputsFillEmptySlots(t *testing.T) {
	const fresh, sessionOut = "work/ocr-census/runs/handoff-7311.md", "work/ocr-census/runs/live-notes.md"
	f := newFixture(t, true, map[string]string{
		fresh:      "---\ntype: report\nstatus: live\nsummary: Sweep 7311 handoff, deskew recall per batch\n---\n# Sweep 7311 handoff\n\nDeskew recall per batch.\n",
		sessionOut: "# Live notes\n\nWritten by the session.\n",
	})
	written := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	setMtime := func(p string, at time.Time) {
		if _, err := f.db.Exec(`UPDATE node SET mtime = ? WHERE path = ?`, at.UnixNano(), p); err != nil {
			t.Fatal(err)
		}
	}
	setMtime(fresh, written)
	nm := f.names(t)
	const ask = "Check work/ocr-census/STATE.md, then pick up #7311 where its handoff left it."
	pointed := func(asOf time.Time, hubs ...string) bool {
		t.Helper()
		lk, err := Pointers(f.db, ask, Seeds{Work: []string{"ocr-census"}, Hubs: hubs, AsOf: asOf}, nm)
		if err != nil {
			t.Fatal(err)
		}
		return slices.Contains(ids(lk), fresh)
	}
	if !pointed(written.Add(10 * time.Minute)) {
		t.Error("a file the box got 10 minutes before the first prompt, matching it, is not pointed at")
	}
	if pointed(time.Time{}) || pointed(written.Add(-time.Minute)) {
		t.Error("a fresh file is pointed at on a later prompt, or before it was written")
	}
	if pointed(written.Add(10*time.Minute), "image-lane") {
		t.Error("a fresh file displaced the third of three pointers chosen without it (the hub's decision, score 0.40)")
	}

	// A replay: the guard hides the session's own output, its first write after it started.
	asOf := written.Add(10 * time.Minute)
	setMtime(sessionOut, asOf)
	r, err := NewEvalRanker(f.env, f.db)
	if err != nil {
		t.Fatal(err)
	}
	hidden := func(id string) bool { return id == sessionOut }
	if got := r.Pointers(ask, f.cwd, hidden); !slices.Contains(got, fresh) {
		t.Errorf("replay pointers %v: want the file written before the session's start", got)
	}
	setMtime(fresh, asOf.Add(time.Minute))
	if got := r.Pointers(ask, f.cwd, hidden); slices.Contains(got, fresh) {
		t.Errorf("replay pointers %v: a file written after the session's start counts as fresh", got)
	}
}
