package metrics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"saahil.local/brain/internal/llm"
	"saahil.local/brain/internal/store"
)

// TestSnapshotCountsTheLastDayFromEverySource catches a snapshot the daily brain-care agent would misread: a dream or
// a model call older than the window counted, MCP calls not told from CLI ones, a pointer opened without being shown
// counted as an open, the eval numbers taken from an older report than the newest, or the units an ok step refused
// left uncounted (curate.merge dropped the same unit for twelve dreams and every metric read clean).
func TestSnapshotCountsTheLastDayFromEverySource(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".brain/dream-runs/a.json", `{"run_id":"a","status":"completed","started":"2026-10-09T10:00:00Z","edits":5,"deletions":0,"cost_usd":1.5,
		"steps":[{"name":"reindex","status":"failed","reason":"brain index: exit 1"},{"name":"curate.merge","status":"ok","refused":2}]}`)
	write(".brain/dream-runs/old.json", `{"run_id":"old","status":"completed","started":"2026-10-07T10:00:00Z","edits":50,"cost_usd":7,
		"steps":[{"name":"curate.merge","status":"ok","refused":5}]}`)
	write(".brain/evals/reports/2026-10-08/navigation.json", `{"ran":"2026-10-08T00:00:00Z","metrics":[{"name":"top-3 hit rate (pointer ranker)","all":{"value":0.5,"n":10}}]}`)
	write(".brain/evals/reports/2026-10-09/navigation.json", `{"ran":"2026-10-09T00:00:00Z","metrics":[{"name":"top-3 hit rate (pointer ranker)","all":{"value":0.7,"n":10}}],
		"split_metrics":[{"name":"top-3 hit rate (pointer ranker)","split":"test","all":{"value":0.75,"n":4}}]}`)

	dbPath := filepath.Join(root, ".index", "brain.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ledger, err := llm.OpenLedger(dbPath, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	for _, c := range []struct{ started string }{{"2026-10-09T09:00:00Z"}, {"2026-10-07T09:00:00Z"}} {
		if _, err := st.DB.Exec(`INSERT INTO llm_calls(key, workflow, step, model, cost_usd, tokens_in, tokens_out, started, ended, status)
			VALUES (?, 'dream', 'file', 'opus', 0.25, 1, 1, ?, ?, 'ok')`, c.started, c.started, c.started); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []struct{ at, session, hook, cands, chosen string }{
		{"2026-10-09T11:00:00Z", "s1", "prompt", `[]`, `["a","b"]`},
		{"2026-10-09T11:01:00Z", "s1", "post", `["opened"]`, `["a"]`},
		{"2026-10-09T11:02:00Z", "s1", "post", `["opened"]`, `["z"]`},
		{"2026-10-09T11:03:00Z", "s1", "pull", `["brain_search","mcp"]`, `[]`},
		{"2026-10-09T11:04:00Z", "s1", "pull", `["brain_search","mcp"]`, `[]`},
		{"2026-10-09T11:05:00Z", "s1", "pull", `["brain_show","cli"]`, `[]`},
		{"2026-10-09T11:06:00Z", "s1", "end", `["ignored"]`, `["b"]`},
		{"2026-10-07T11:00:00Z", "s0", "prompt", `[]`, `["c"]`},
	} {
		if _, err := st.DB.Exec(`INSERT INTO lookups(time, session, hook, candidates, chosen, abstained, ms) VALUES (?,?,?,?,?,0,40)`,
			r.at, r.session, r.hook, r.cands, r.chosen); err != nil {
			t.Fatal(err)
		}
	}

	s, err := Take(root, st.DB, "devbox", "test", now)
	if err != nil {
		t.Fatal(err)
	}
	if s.Dreams.Runs != 1 || s.Dreams.Edits != 5 || len(s.Dreams.FailedSteps) != 1 || s.Dreams.Refused != 2 {
		t.Errorf("dreams %+v, want the one run of the last day with its failed step and its 2 refused units", s.Dreams)
	}
	if s.LLM["dream"].Calls != 1 {
		t.Errorf("llm %+v, want the one call of the last day", s.LLM)
	}
	if s.Hooks.Prompts != 1 || s.Hooks.Shown != 2 || s.Hooks.Opened != 1 || s.Hooks.Ignored != 1 || s.Hooks.PullSessions != 1 {
		t.Errorf("hooks %+v, want 1 prompt, 2 shown, 1 opened (z was never shown), 1 ignored, 1 pulling session", s.Hooks)
	}
	if s.Pulls["brain_search/mcp"] != 2 || s.Pulls["brain_show/cli"] != 1 {
		t.Errorf("pulls %v", s.Pulls)
	}
	if s.Evals["navigation/all/top-3 hit rate (pointer ranker)"] != 0.7 || s.Evals["navigation/test/top-3 hit rate (pointer ranker)"] != 0.75 {
		t.Errorf("evals %v, want the newest report's numbers", s.Evals)
	}

	if _, err := Write(root, s); err != nil {
		t.Fatal(err)
	}
	tomorrow := s
	tomorrow.At = now.Add(24 * time.Hour)
	tomorrow.Hooks.Opened = 3
	prev, ok := Previous(root, tomorrow)
	if !ok || !strings.Contains(Table(tomorrow, prev, ok), "pointers opened                              3  (+2)") {
		t.Errorf("the next day's table must show the change since this snapshot:\n%s", Table(tomorrow, prev, ok))
	}
}

// TestPromptPartsP95FromRowsThatTimedThem catches a breakdown the care agent would misread: rows written before the
// hook timed its parts counted as 0 ms, which pulls every part's p95 down on a machine with few new prompts.
func TestPromptPartsP95FromRowsThatTimedThem(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, ".index", "brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	type promptRow struct {
		ms     float64
		phases string
	}
	rows := []promptRow{
		{300, `{"open":20,"names":5,"read":10,"digest":0,"pointers":260,"stale_names":true}`},
		{60, `{"open":10,"names":5,"read":10,"digest":0,"pointers":30}`},
	}
	for range 20 {
		rows = append(rows, promptRow{90, ``})
	}
	for _, r := range rows {
		if _, err := st.DB.Exec(`INSERT INTO lookups(time, session, hook, candidates, chosen, abstained, ms, phases)
			VALUES ('2026-10-09T11:00:00Z', 's1', 'prompt', '[]', '[]', 1, ?, ?)`, r.ms, r.phases); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Take(root, st.DB, "laptop", "test", time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Hooks.PromptPartsP95Ms; got["pointers"] != 260 || got["open"] != 20 || got["names"] != 5 {
		t.Errorf("parts p95 %v, want the slow timed prompt's pointers 260, open 20, names 5", got)
	}
	if s.Hooks.PromptsTimed != 2 || s.Hooks.PromptsStaleNames != 1 {
		t.Errorf("timed %d, stale names %d, want 2 timed prompts and 1 with a stale names cache", s.Hooks.PromptsTimed,
			s.Hooks.PromptsStaleNames)
	}
}
