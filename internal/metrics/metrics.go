// Package metrics takes the brain's daily snapshot from what it already records: the dream runs, the model-call
// ledger, the hook lookups and the newest eval reports. The daily brain-care agent compares snapshots day over day to
// pick what to improve and to see whether yesterday's change helped.
package metrics

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

const Window = 24 * time.Hour

type Snapshot struct {
	Machine string             `json:"machine"`
	Version string             `json:"version"`
	At      time.Time          `json:"at"`
	Dreams  Dreams             `json:"dreams"`
	LLM     map[string]Spend   `json:"llm"`   // by workflow, last 24 h
	Hooks   Hooks              `json:"hooks"` // last 24 h
	Pulls   map[string]int     `json:"pulls"` // "<verb>/<mcp|cli>" calls, last 24 h
	Evals   map[string]float64 `json:"evals"` // "<eval>/<all|dev|test>/<metric>" from the newest reports
	EvalsAt string             `json:"evals_at,omitempty"`
}

type Dreams struct {
	Runs        int      `json:"runs"`
	Completed   int      `json:"completed"`
	Edits       int      `json:"edits"`
	Deletions   int      `json:"deletions"`
	CostUSD     float64  `json:"cost_usd"`
	FailedSteps []string `json:"failed_steps,omitempty"` // "<run> <step>: <reason>"
	Refused     int      `json:"refused"`                // units the applier refused inside ok steps
	Held        int      `json:"held"`                   // edits the run's edit cap held for a later dream
}

type Spend struct {
	Calls   int     `json:"calls"`
	CostUSD float64 `json:"cost_usd"`
}

type Hooks struct {
	Prompts      int     `json:"prompts"`
	PromptP50Ms  float64 `json:"prompt_p50_ms"`
	PromptP95Ms  float64 `json:"prompt_p95_ms"`
	AbstainRate  float64 `json:"abstain_rate"`
	Sessions     int     `json:"sessions"`
	PullSessions int     `json:"pull_sessions"`
	Shown        int     `json:"shown"`
	Opened       int     `json:"opened"`
	Ignored      int     `json:"ignored"`
	// prompts whose row times the hook's parts (open, names, read, digest, pointers), and each part's p95 over them
	PromptsTimed      int                `json:"prompts_timed"`
	PromptsStaleNames int                `json:"prompts_stale_names"`
	PromptPartsP95Ms  map[string]float64 `json:"prompt_parts_p95_ms,omitempty"`
}

// promptParts are the parts of the prompt hook that lookups.phases times, in the order they run.
var promptParts = []string{"open", "names", "read", "digest", "pointers"}

// Take reads the snapshot of the brain at root as of now.
func Take(root string, db *sql.DB, machine, version string, now time.Time) (Snapshot, error) {
	s := Snapshot{Machine: machine, Version: version, At: now.UTC(), LLM: map[string]Spend{}, Pulls: map[string]int{},
		Evals: map[string]float64{}}
	from := now.Add(-Window).UTC().Format(time.RFC3339Nano)
	if err := s.readDreams(root, now.Add(-Window)); err != nil {
		return s, err
	}
	if err := s.readLLM(db, from); err != nil {
		return s, err
	}
	if err := s.readHooks(db, from); err != nil {
		return s, err
	}
	s.readEvals(root)
	return s, nil
}

func (s *Snapshot) readDreams(root string, since time.Time) error {
	files, _ := filepath.Glob(filepath.Join(root, ".brain", "dream-runs", "*.json"))
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var run struct {
			RunID     string    `json:"run_id"`
			Status    string    `json:"status"`
			Started   time.Time `json:"started"`
			Edits     int       `json:"edits"`
			Deletions int       `json:"deletions"`
			CostUSD   float64   `json:"cost_usd"`
			Steps     []struct {
				Name, Status, Reason string
				Refused              int
			} `json:"steps"`
			Rejected []struct {
				Held bool `json:"held"`
			} `json:"rejected"`
		}
		if json.Unmarshal(raw, &run) != nil || run.Started.Before(since) {
			continue
		}
		s.Dreams.Runs++
		if run.Status == "completed" {
			s.Dreams.Completed++
		}
		s.Dreams.Edits += run.Edits
		s.Dreams.Deletions += run.Deletions
		s.Dreams.CostUSD += run.CostUSD
		for _, r := range run.Rejected {
			if r.Held {
				s.Dreams.Held++
			}
		}
		for _, st := range run.Steps {
			s.Dreams.Refused += st.Refused
			if st.Status == "failed" {
				s.Dreams.FailedSteps = append(s.Dreams.FailedSteps, fmt.Sprintf("%s %s: %.120s", run.RunID, st.Name, st.Reason))
			}
		}
	}
	return nil
}

func (s *Snapshot) readLLM(db *sql.DB, from string) error {
	rows, err := db.Query(`SELECT workflow, count(*), COALESCE(sum(cost_usd), 0) FROM llm_calls WHERE started >= ? GROUP BY workflow`, from)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var wf string
		var sp Spend
		if err := rows.Scan(&wf, &sp.Calls, &sp.CostUSD); err != nil {
			return err
		}
		s.LLM[wf] = sp
	}
	return rows.Err()
}

func (s *Snapshot) readHooks(db *sql.DB, from string) error {
	rows, err := db.Query(`SELECT session, hook, candidates, chosen, abstained, ms, phases FROM lookups WHERE time >= ?`, from)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ms []float64
	parts := map[string][]float64{}
	abstained := 0
	sessions, pulled := map[string]bool{}, map[string]bool{}
	shown, opened := map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var session, hook, candJSON, chosenJSON, phases string
		var abst bool
		var took float64
		if err := rows.Scan(&session, &hook, &candJSON, &chosenJSON, &abst, &took, &phases); err != nil {
			return err
		}
		var cands, chosen []string
		_ = json.Unmarshal([]byte(candJSON), &cands)
		_ = json.Unmarshal([]byte(chosenJSON), &chosen)
		switch hook {
		case "prompt":
			ms = append(ms, took)
			var timed map[string]any
			if json.Unmarshal([]byte(phases), &timed) == nil && timed != nil {
				s.Hooks.PromptsTimed++
				if stale, _ := timed["stale_names"].(bool); stale {
					s.Hooks.PromptsStaleNames++
				}
				for _, part := range promptParts {
					v, _ := timed[part].(float64)
					parts[part] = append(parts[part], v)
				}
			}
			if abst {
				abstained++
			}
			sessions[session] = true
			for _, id := range chosen {
				shown[session+"\x00"+id] = true
			}
		case "post":
			pulled[session] = true
			for _, id := range chosen {
				opened[session+"\x00"+id] = true
			}
		case "pull":
			pulled[session] = true
			channel := "unknown" // rows from before the channel was logged
			if len(cands) > 1 {
				channel = cands[1]
			}
			if len(cands) > 0 {
				s.Pulls[cands[0]+"/"+channel]++
			}
		case "end":
			s.Hooks.Ignored += len(chosen)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.Hooks.Prompts = len(ms)
	s.Hooks.Sessions = len(sessions)
	for session := range pulled {
		if sessions[session] {
			s.Hooks.PullSessions++
		}
	}
	s.Hooks.Shown = len(shown)
	for k := range opened {
		if shown[k] {
			s.Hooks.Opened++
		}
	}
	if len(ms) > 0 {
		sort.Float64s(ms)
		s.Hooks.PromptP50Ms = quantile(ms, 0.50)
		s.Hooks.PromptP95Ms = quantile(ms, 0.95)
		s.Hooks.AbstainRate = float64(abstained) / float64(len(ms))
	}
	if s.Hooks.PromptsTimed > 0 {
		s.Hooks.PromptPartsP95Ms = map[string]float64{}
		for part, v := range parts {
			sort.Float64s(v)
			s.Hooks.PromptPartsP95Ms[part] = quantile(v, 0.95)
		}
	}
	return nil
}

func quantile(sorted []float64, q float64) float64 {
	return sorted[int(q*float64(len(sorted)-1)+0.5)]
}

// readEvals takes each eval's headline rates from the newest dated reports folder that holds it.
func (s *Snapshot) readEvals(root string) {
	dirs, _ := filepath.Glob(filepath.Join(root, ".brain", "evals", "reports", "*"))
	slices.Sort(dirs)
	slices.Reverse(dirs)
	for _, name := range []string{"navigation", "qa", "ab", "human"} {
		for _, dir := range dirs {
			raw, err := os.ReadFile(filepath.Join(dir, name+".json"))
			if err != nil {
				continue
			}
			var r struct {
				Ran     string `json:"ran"`
				Metrics []struct {
					Name string `json:"name"`
					All  struct {
						Value float64 `json:"value"`
						N     int     `json:"n"`
					} `json:"all"`
				} `json:"metrics"`
				Splits []struct {
					Name  string `json:"name"`
					Split string `json:"split"`
					All   struct {
						Value float64 `json:"value"`
						N     int     `json:"n"`
					} `json:"all"`
				} `json:"split_metrics"`
			}
			if json.Unmarshal(raw, &r) != nil {
				continue
			}
			for _, m := range r.Metrics {
				if m.All.N > 0 {
					s.Evals[name+"/all/"+m.Name] = m.All.Value
				}
			}
			for _, m := range r.Splits {
				if m.All.N > 0 {
					s.Evals[name+"/"+m.Split+"/"+m.Name] = m.All.Value
				}
			}
			if r.Ran > s.EvalsAt {
				s.EvalsAt = r.Ran
			}
			break
		}
	}
}

// Dir holds the daily snapshots: <machine>-<YYYY-MM-DD>.json.
func Dir(root string) string { return filepath.Join(root, ".brain", "metrics") }

// Write saves the snapshot as the day's file (a later run the same day replaces it).
func Write(root string, s Snapshot) (string, error) {
	if err := os.MkdirAll(Dir(root), 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(Dir(root), s.Machine+"-"+s.At.Format("2006-01-02")+".json")
	raw, _ := json.MarshalIndent(s, "", "  ")
	return path, os.WriteFile(path, raw, 0o644)
}

// Previous is this machine's newest snapshot from an earlier day, if any.
func Previous(root string, s Snapshot) (Snapshot, bool) {
	files, _ := filepath.Glob(filepath.Join(Dir(root), s.Machine+"-*.json"))
	slices.Sort(files)
	today := s.Machine + "-" + s.At.Format("2006-01-02") + ".json"
	for i := len(files) - 1; i >= 0; i-- {
		if filepath.Base(files[i]) >= today {
			continue
		}
		var prev Snapshot
		if raw, err := os.ReadFile(files[i]); err == nil && json.Unmarshal(raw, &prev) == nil {
			return prev, true
		}
	}
	return Snapshot{}, false
}

// Table prints the snapshot's numbers, each with its change since prev when there is one.
func Table(cur, prev Snapshot, hasPrev bool) string {
	var b strings.Builder
	row := func(name string, now, before float64, format string) {
		cell := fmt.Sprintf(format, now)
		if hasPrev {
			cell += fmt.Sprintf("  (%+"+strings.TrimPrefix(format, "%")+")", now-before)
		}
		fmt.Fprintf(&b, "  %-44s %s\n", name, cell)
	}
	fmt.Fprintf(&b, "brain metrics, %s %s, last 24 h (as of %s)\n", cur.Machine, cur.Version, cur.At.Format(time.RFC3339))
	row("dreams run", float64(cur.Dreams.Runs), float64(prev.Dreams.Runs), "%.0f")
	row("dreams completed", float64(cur.Dreams.Completed), float64(prev.Dreams.Completed), "%.0f")
	row("dream edits", float64(cur.Dreams.Edits), float64(prev.Dreams.Edits), "%.0f")
	row("dream deletions", float64(cur.Dreams.Deletions), float64(prev.Dreams.Deletions), "%.0f")
	row("dream failed steps", float64(len(cur.Dreams.FailedSteps)), float64(len(prev.Dreams.FailedSteps)), "%.0f")
	row("dream units refused", float64(cur.Dreams.Refused), float64(prev.Dreams.Refused), "%.0f")
	row("dream edits held by the cap", float64(cur.Dreams.Held), float64(prev.Dreams.Held), "%.0f")
	total := func(m map[string]Spend) (calls int, cost float64) {
		for _, sp := range m {
			calls, cost = calls+sp.Calls, cost+sp.CostUSD
		}
		return calls, cost
	}
	cc, cu := total(cur.LLM)
	pc, pu := total(prev.LLM)
	row("model calls", float64(cc), float64(pc), "%.0f")
	row("model spend USD", cu, pu, "%.2f")
	row("prompts seen by the hook", float64(cur.Hooks.Prompts), float64(prev.Hooks.Prompts), "%.0f")
	row("prompt hook p95 ms", cur.Hooks.PromptP95Ms, prev.Hooks.PromptP95Ms, "%.0f")
	if cur.Hooks.PromptsTimed > 0 {
		row("prompts with their parts timed", float64(cur.Hooks.PromptsTimed), float64(prev.Hooks.PromptsTimed), "%.0f")
		for _, part := range promptParts {
			row("  p95 ms, "+part, cur.Hooks.PromptPartsP95Ms[part], prev.Hooks.PromptPartsP95Ms[part], "%.0f")
		}
		row("  names cache stale (rebuild started)", float64(cur.Hooks.PromptsStaleNames),
			float64(prev.Hooks.PromptsStaleNames), "%.0f")
	}
	row("prompts with no pointer (abstain rate)", cur.Hooks.AbstainRate, prev.Hooks.AbstainRate, "%.2f")
	row("sessions with prompts", float64(cur.Hooks.Sessions), float64(prev.Hooks.Sessions), "%.0f")
	row("sessions pulling from the brain", float64(cur.Hooks.PullSessions), float64(prev.Hooks.PullSessions), "%.0f")
	row("pointers shown", float64(cur.Hooks.Shown), float64(prev.Hooks.Shown), "%.0f")
	row("pointers opened", float64(cur.Hooks.Opened), float64(prev.Hooks.Opened), "%.0f")
	row("pointers ignored (session ended)", float64(cur.Hooks.Ignored), float64(prev.Hooks.Ignored), "%.0f")
	sum := func(m map[string]int, channel string) (n int) {
		for k, v := range m {
			if strings.HasSuffix(k, "/"+channel) {
				n += v
			}
		}
		return n
	}
	row("brain tool calls through MCP", float64(sum(cur.Pulls, "mcp")), float64(sum(prev.Pulls, "mcp")), "%.0f")
	row("brain tool calls through the CLI", float64(sum(cur.Pulls, "cli")), float64(sum(prev.Pulls, "cli")), "%.0f")
	if n := sum(cur.Pulls, "unknown"); n > 0 {
		row("brain tool calls, channel not logged (older)", float64(n), float64(sum(prev.Pulls, "unknown")), "%.0f")
	}
	keys := make([]string, 0, len(cur.Evals))
	for k := range cur.Evals {
		if strings.Contains(k, "/test/") || strings.HasPrefix(k, "human/all/") || strings.HasPrefix(k, "qa/all/recall@3 (brain_search)") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		row(k, cur.Evals[k], prev.Evals[k], "%.3f")
	}
	for _, f := range cur.Dreams.FailedSteps {
		fmt.Fprintf(&b, "  failed: %s\n", f)
	}
	return b.String()
}
