package doctor

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/hooks"
)

// Hook budgets (FORMAT.md), checked against the last week of lookups; ms there is the hook's own work, without
// process start.
var hookBudgetMs = map[string]float64{"start": 300, "prompt": 150}

func init() {
	Register(Check{Name: "hooks mode", Run: func(context.Context) (Status, string) {
		if _, err := os.Stat(filepath.Join(config.BrainRoot(), ".brain", "live")); err == nil {
			return OK, "live: start digest and pointers are injected"
		}
		return OK, "shadow: hooks log what they would inject and inject nothing (until .brain/live exists)"
	}})
	Register(Check{Name: "hooks latency", Run: func(context.Context) (Status, string) {
		return withGraph(config.BrainRoot(), hookLatency)
	}})
}

func hookLatency(db *sql.DB) (Status, string, error) {
	since := time.Now().Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339)
	rows, err := db.Query(`SELECT hook, ms, abstained FROM lookups WHERE time >= ? AND hook IN ('start', 'prompt')`, since)
	if err != nil {
		return Fail, "", err
	}
	defer rows.Close()
	ms, abstained := map[string][]float64{}, map[string]int{}
	for rows.Next() {
		var hook string
		var m float64
		var abst bool
		if err := rows.Scan(&hook, &m, &abst); err != nil {
			return Fail, "", err
		}
		ms[hook] = append(ms[hook], m)
		if abst {
			abstained[hook]++
		}
	}
	if err := rows.Err(); err != nil {
		return Fail, "", err
	}
	if len(ms) == 0 {
		return Skip, "no hook lookups in the last 7 days", nil
	}
	st := OK
	var parts []string
	for _, hook := range []string{"start", "prompt"} {
		if hook == "prompt" && len(ms[hook]) < minLatencySamples {
			// a p95 of a handful of prompts is the slowest one (often the first after an index or binary change):
			// judge a warm bench of QA questions instead, and show both
			part, slow := benchPrompt(len(ms[hook]))
			if slow {
				st = Warn
			}
			parts = append(parts, part)
			continue
		}
		if len(ms[hook]) == 0 {
			continue
		}
		sort.Float64s(ms[hook])
		p95 := ms[hook][(len(ms[hook])*95)/100]
		if p95 > hookBudgetMs[hook] {
			st = Warn
		}
		parts = append(parts, fmt.Sprintf("%s p95 %.0f ms of %d (budget %.0f; %d%% nothing to inject)", hook, p95,
			len(ms[hook]), hookBudgetMs[hook], 100*abstained[hook]/len(ms[hook])))
	}
	return st, strings.Join(parts, "; "), nil
}

const minLatencySamples = 20

func benchPrompt(real int) (string, bool) {
	prompts, err := hooks.BenchQuestions(config.BrainRoot(), 45)
	if err != nil {
		return fmt.Sprintf("prompt: %d real lookups, too few to judge, and no bench (%v)", real, err), false
	}
	env := hooks.DefaultEnv()
	if _, _, err := hooks.Bench(env, prompts[:5], config.BrainRoot()); err != nil {
		return fmt.Sprintf("prompt: bench failed: %v", err), true
	}
	took, _, err := hooks.Bench(env, prompts[5:], config.BrainRoot())
	if err != nil || len(took) == 0 {
		return fmt.Sprintf("prompt: bench failed: %v", err), true
	}
	p95 := float64(hooks.Quantile(took, 0.95).Microseconds()) / 1000
	return fmt.Sprintf("prompt: %d real lookups (too few for a p95); bench p95 %.0f ms of %d after 5 warm-up (budget %.0f)",
		real, p95, len(took), hookBudgetMs["prompt"]), p95 > hookBudgetMs["prompt"]
}
