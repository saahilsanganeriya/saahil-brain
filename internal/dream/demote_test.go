package dream

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"saahil.local/brain/internal/store"
)

// TestDreamDemotesNeverOpenedPointers catches a dream that never demotes (the step not wired), one that demotes a
// pointer agents did open or one shown fewer than 10 times, and a demotion that outlives the node's first open.
func TestDreamDemotesNeverOpenedPointers(t *testing.T) {
	acts, _ := newBrain(t, 0.01)
	st, err := store.Open(filepath.Join(acts.Cfg.BrainRoot, ".index", "brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.DB.Exec(`INSERT INTO node_stats(id, shown, opened, followed) VALUES
		('lesson-noise', 10, 0, 0), ('lesson-read', 14, 1, 0), ('lesson-edited', 12, 0, 1), ('lesson-new', 9, 0, 0)`); err != nil {
		t.Fatal(err)
	}
	rep := runDream(t, acts, Input{}, time.Now().UTC())
	if s := step(rep, "demote"); s.Status != "ok" || !strings.Contains(s.Reason, "demoted 1") {
		t.Fatalf("demote step: %+v", s)
	}
	demoted := func() []string {
		rows, err := st.DB.Query(`SELECT id FROM node_stats WHERE demoted_at != '' ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			rows.Scan(&id)
			ids = append(ids, id)
		}
		return ids
	}
	if got := demoted(); strings.Join(got, ",") != "lesson-noise" {
		t.Fatalf("demoted %v, want only lesson-noise", got)
	}
	if _, err := st.DB.Exec(`UPDATE node_stats SET opened = 1 WHERE id = 'lesson-noise'`); err != nil {
		t.Fatal(err)
	}
	out, err := acts.Demote(context.Background())
	if err != nil || len(demoted()) != 0 || !strings.Contains(out.Reason, "restored 1") {
		t.Fatalf("after an open: demoted %v, %+v (%v)", demoted(), out, err)
	}
}
