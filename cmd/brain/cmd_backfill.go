package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.temporal.io/sdk/client"

	"saahil.local/brain/internal/backfill"
	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/dream"
)

func init() {
	register("backfill run", "a CARD for every work box + notes for finished campaigns: [--boxes a,b] [--budget 80] [--dry-run]", cmdBackfillRun)
	register("backfill status", "the last backfill runs and what the backfill has cost so far", cmdBackfillStatus)
	register("backfill promote-cards", "after the cut-over: move staged CARDs into work/<box>/ (a CARD there goes to .archive/)", cmdBackfillPromote)
}

func shadowDocs() string {
	return filepath.Join(config.Home(), "Documents", "work-repo", "saahil-docs")
}

func cmdBackfillRun(args []string) int {
	fs := flag.NewFlagSet("backfill run", flag.ContinueOnError)
	boxes := fs.String("boxes", "", "comma-separated box names (default: every box)")
	budget := fs.Float64("budget", dream.BackfillBudgetUSD, "USD cap on every backfill call, earlier runs included")
	dry := fs.Bool("dry-run", false, "list every box with its planned action; no model, no Temporal")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var only []string
	if *boxes != "" {
		only = strings.Split(*boxes, ",")
	}
	if *dry {
		return backfillDryRun(only, *budget)
	}
	c, err := dialDream()
	if err != nil {
		return fail("temporal at %s: %v", dream.HostPort, err)
	}
	defer c.Close()
	id := "backfill/" + time.Now().UTC().Format("2006-01-02T150405Z")
	run, err := c.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{ID: id, TaskQueue: dream.TaskQueue},
		dream.BackfillWorkflow, dream.BackfillInput{Boxes: only, BudgetUSD: *budget})
	if err != nil {
		return fail("start %s: %v", id, err)
	}
	fmt.Fprintf(os.Stderr, "backfill %s (run %s) started; waiting… (brain backfill status in another shell)\n", id, run.GetRunID())
	var rep backfill.Report
	if err := run.Get(context.Background(), &rep); err != nil {
		return fail("backfill %s: %v", id, err)
	}
	printBackfill(rep)
	return 0
}

// backfillDryRun plans in-process: the plan is read-only code, so it needs neither the worker nor Temporal.
func backfillDryRun(only []string, budget float64) int {
	root, now := config.BrainRoot(), time.Now().UTC()
	live, plan, err := backfill.Plan(root, shadowDocs(), only, now)
	if err != nil {
		return fail("plan: %v", err)
	}
	rep := backfill.Report{ID: "backfill-dry-" + now.Format("2006-01-02T150405Z"), DryRun: true, Live: live,
		BudgetUSD: budget, Started: now, Ended: time.Now().UTC()}
	for _, p := range plan {
		status := "planned"
		if p.Action == "skip" {
			status = "skipped"
		}
		rep.Add(backfill.BoxResult{Box: p.Box.Name, Action: p.Action, Status: status, Reason: p.Reason, Finished: p.Finished})
	}
	for _, p := range plan {
		fmt.Printf("  %-58s %-8s files %-6d newest %s  finished=%v\n", p.Box.Name, p.Action, p.Box.Files,
			p.Box.Newest.Format("2006-01-02"), p.Finished)
	}
	printBackfill(rep)
	b, _ := json.MarshalIndent(rep, "", "  ")
	path := backfill.ReportPath(root, rep.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fail("%v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fail("%v", err)
	}
	fmt.Println("report:", path)
	return 0
}

func printBackfill(rep backfill.Report) {
	actions := map[string]int{}
	for _, b := range rep.Boxes {
		actions[b.Action+"/"+b.Status]++
	}
	keys := make([]string, 0, len(actions))
	for k := range actions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Printf("%s live=%v dry_run=%v: %d boxes, %d CARDs written, %d notes, %d finished campaigns, $%.2f (budget $%.0f)\n",
		rep.ID, rep.Live, rep.DryRun, len(rep.Boxes), rep.CardsWritten, rep.NotesAdded, rep.Finished, rep.CostUSD, rep.BudgetUSD)
	for _, k := range keys {
		fmt.Printf("  %-18s %d\n", k, actions[k])
	}
	if rep.Stopped != "" {
		fmt.Println("  stopped:", rep.Stopped)
	}
}

func cmdBackfillStatus([]string) int {
	root := config.BrainRoot()
	reports, _ := filepath.Glob(filepath.Join(root, ".brain", "backfill-runs", "*.json"))
	sort.Sort(sort.Reverse(sort.StringSlice(reports)))
	if len(reports) == 0 {
		fmt.Println("no backfill runs yet")
	}
	for i, p := range reports {
		var rep backfill.Report
		if b, err := os.ReadFile(p); i < 5 && err == nil && json.Unmarshal(b, &rep) == nil {
			printBackfill(rep)
		}
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(root, ".index", "brain.db")+"?mode=ro")
	if err != nil {
		return fail("%v", err)
	}
	defer db.Close()
	var spent float64
	var calls int
	if db.QueryRow(`SELECT COALESCE(SUM(cost_usd), 0), COUNT(*) FROM llm_calls WHERE workflow = 'backfill'`).Scan(&spent, &calls) == nil {
		fmt.Printf("backfill so far: %d calls, $%.2f of $%.0f\n", calls, spent, dream.BackfillBudgetUSD)
	}
	return 0
}

func cmdBackfillPromote([]string) int {
	moved, skipped, err := backfill.PromoteCards(config.BrainRoot(), time.Now().UTC().Format("2006-01-02T150405Z"))
	for _, m := range moved {
		fmt.Println("moved", m)
	}
	for _, s := range skipped {
		fmt.Println("skipped", s)
	}
	if err != nil {
		return fail("promote-cards: %v", err)
	}
	fmt.Printf("%d CARDs promoted, %d skipped\n", len(moved), len(skipped))
	return 0
}
