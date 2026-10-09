package doctor

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"saahil.local/brain/internal/store"
)

// TestUsageRatesCountSessionsAndPointersOnce catches pull and open rates that count rows instead of sessions and
// pointers: a pointer shown twice to one session is one pointer, a session that only searched the brain pulled, and
// rows from before the window (shadow, or older than 7 days) count nothing.
func TestUsageRatesCountSessionsAndPointersOnce(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	since := time.Date(2026, 10, 8, 13, 43, 0, 0, time.UTC)
	for _, r := range []struct{ at, session, hook, cands, chosen string }{
		{"2026-10-08T12:00:00Z", "s0", "prompt", `[]`, `["a"]`},
		{"2026-10-08T12:00:01Z", "s0", "post", `["opened"]`, `["a"]`},
		{"2026-10-08T14:00:00Z", "s1", "prompt", `[]`, `["a","b"]`},
		{"2026-10-08T14:01:00Z", "s1", "prompt", `[]`, `["a"]`},
		{"2026-10-08T14:02:00Z", "s1", "post", `["opened"]`, `["a"]`},
		{"2026-10-08T14:03:00Z", "s1", "end", `["ignored"]`, `["b"]`},
		{"2026-10-08T15:00:00Z", "s2", "prompt", `[]`, `[]`},
		{"2026-10-08T15:01:00Z", "s2", "pull", `["brain_search"]`, `[]`},
		{"2026-10-08T16:00:00Z", "s3", "prompt", `[]`, `["c"]`},
	} {
		abstained := r.hook == "prompt" && r.chosen == "[]"
		if _, err := st.DB.Exec(`INSERT INTO lookups(time, session, hook, candidates, chosen, abstained, ms) VALUES (?,?,?,?,?,?,0)`,
			r.at, r.session, r.hook, r.cands, r.chosen, abstained); err != nil {
			t.Fatal(err)
		}
	}
	status, detail, err := usageRates(st.DB, since)
	if err != nil || status != Info {
		t.Fatalf("%s %q (%v)", status, detail, err)
	}
	for _, want := range []string{"pull rate 2 of 3 sessions (67%)", "open rate 1 of 3 pointers shown (33%)", "1 ignored"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail %q lacks %q", detail, want)
		}
	}
}

// TestUsageLabelSaysWhenPullsAndIgnoresBegan catches the label the laptop agent found over-promising on 2026-10-09:
// "since <go-live>" while pull and end rows exist only from the usage logger's deployment hours later, so a session
// that pulled before it was not counted. The label names that later start; opens are logged from go-live.
func TestUsageLabelSaysWhenPullsAndIgnoresBegan(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, r := range []struct{ at, session, hook, chosen string }{
		{"2026-10-08T14:00:00Z", "s1", "prompt", `["a"]`},
		{"2026-10-08T14:01:00Z", "s1", "post", `["a"]`},
		{"2026-10-08T23:40:00Z", "s2", "prompt", `["c"]`},
		{"2026-10-08T23:41:00Z", "s2", "pull", `[]`},
	} {
		if _, err := st.DB.Exec(`INSERT INTO lookups(time, session, hook, candidates, chosen, abstained, ms) VALUES (?,?,?,'[]',?,0,0)`,
			r.at, r.session, r.hook, r.chosen); err != nil {
			t.Fatal(err)
		}
	}
	_, detail, err := usageRates(st.DB, time.Date(2026, 10, 8, 13, 43, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "pulls and ignores counted from 2026-10-08T23:41Z") {
		t.Errorf("detail %q does not say when pull counting began", detail)
	}
}
