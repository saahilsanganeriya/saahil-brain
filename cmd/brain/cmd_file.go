package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/filing"
	"saahil.local/brain/internal/llm"
	"saahil.local/brain/skills"
	"saahil.local/brain/templates"
)

func init() {
	register("file", "file imported memories into notes/: brain file [--machine devbox|laptop|all] [--batch 20] "+
		"[--limit N] [--dry-run] [--model haiku|opus] [--seed-hubs]", cmdFile)
	register("render memory-index", "render memory/MEMORY.md (≤ 200 lines / 25 KB) from global notes and hubs", cmdRenderMemoryIndex)
	// the dream's render step runs `brain render`; it renders every baseline this build knows
	register("render", "render the baselines (today: memory/MEMORY.md)", cmdRenderMemoryIndex)
}

func cmdFile(args []string) int {
	fset := flag.NewFlagSet("file", flag.ContinueOnError)
	machine := fset.String("machine", "all", "devbox | laptop | all")
	batch := fset.Int("batch", 20, "memories per call (≤ 20)")
	limit := fset.Int("limit", 0, "file at most N memories this run (0: all)")
	parallel := fset.Int("parallel", 4, "model calls in flight at once (results applied in batch order)")
	dryRun := fset.Bool("dry-run", false, "print the batch plan and the reconcile; call no model, write nothing")
	model := fset.String("model", "haiku", "haiku | opus")
	budget := fset.Float64("budget", 15, "stop before a call would take this run past $N")
	seed := fset.Bool("seed-hubs", false, "copy the seed hubs into <brain>/hubs/ where missing, then stop")
	relink := fset.Bool("relink", false, "rewrite [[old-memory-name]] links in notes to the notes they became, then stop")
	if err := fset.Parse(args); err != nil {
		return 2
	}
	root := config.BrainRoot()
	if *seed {
		return seedHubs(root, *dryRun)
	}
	if *relink {
		rep, err := filing.Relink(root)
		if err != nil {
			return fail("relink: %v", err)
		}
		out, _ := json.Marshal(rep)
		fmt.Println(string(out))
		return 0
	}
	machines := []string{*machine}
	if *machine == "all" {
		machines = []string{string(config.Devbox), string(config.Laptop)}
	}
	modelID := map[string]string{"haiku": config.ModelBulk, "opus": config.ModelJudgment}[*model]
	if modelID == "" || *batch < 1 || *batch > 20 {
		return fail("--model is haiku or opus; --batch is 1–20")
	}
	if *dryRun {
		plan, err := filing.NewPlan(root, machines, *batch, *limit)
		if err != nil {
			return fail("%v", err)
		}
		printPlan(root, machines, plan, *batch, *limit, modelID)
		return 0
	}
	if hubs, err := filing.Hubs(root); err != nil || len(hubs) == 0 {
		return fail("no hubs under %s/hubs (%v): run brain file --seed-hubs first", root, err)
	}
	skill, err := fs.ReadFile(skills.FS, "file.md")
	if err != nil {
		return fail("%v", err)
	}
	ledger, err := llm.OpenLedger(filepath.Join(root, ".index", "brain.db"), filepath.Join(root, ".data", "llm"))
	if err != nil {
		return fail("%v", err)
	}
	defer ledger.Close()
	res, err := filing.Run(context.Background(), filing.Config{Root: root, Machines: machines, Batch: *batch,
		Limit: *limit, Parallel: *parallel, Model: modelID, Runner: llm.DefaultClaude(),
		Ledger: ledger, Skill: string(skill), BudgetUSD: *budget, Now: time.Now()})
	res.Skips = nil // the full list is in .brain/filing-report.json
	out, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(out))
	if err != nil {
		return fail("%v", err)
	}
	return 0
}

func printPlan(root string, machines []string, p filing.Plan, batch, limit int, model string) {
	fmt.Printf("filing plan for %s (%s), batch %d, limit %d, model %s — dry run: no model called, nothing written\n\n",
		strings.Join(machines, "+"), root, batch, limit, model)
	for i, b := range p.Batches {
		var bytes int64
		for _, it := range b {
			if fi, err := os.Stat(filepath.Join(root, it.Path)); err == nil {
				bytes += fi.Size()
			}
		}
		fmt.Printf("  batch %3d: %2d memories, %6.1f KB\n", i+1, len(b), float64(bytes)/1024)
	}
	r := p.Reconcile
	var per []string
	for _, m := range machines {
		per = append(per, fmt.Sprintf("%s %d", m, r.ByMachine[m]))
	}
	reasons := make([]string, 0, len(r.Skipped))
	skipped := 0
	for reason, n := range r.Skipped {
		reasons, skipped = append(reasons, fmt.Sprintf("%s %d", reason, n)), skipped+n
	}
	sort.Strings(reasons)
	balanced := "balanced ✓"
	if !r.Balanced() {
		balanced = "NOT BALANCED ✗"
	}
	fmt.Printf("\nreconcile\n  imported        %5d  (%s; manifest entries)\n", r.Imported, strings.Join(per, " + "))
	fmt.Printf("  unique memories %5d  (already filed %d, this run %d, waiting %d)\n", r.Unique, r.AlreadyFiled, r.ThisRun, r.Waiting)
	fmt.Printf("  duplicates      %5d  (kept copy on the other machine: %d) — each memory is one note\n", r.Duplicates, r.CrossMachine)
	fmt.Printf("  skipped         %5d  (%s)\n", skipped, strings.Join(reasons, "; "))
	fmt.Printf("  not in manifest %5d  (on disk only; outside the count)\n", r.NotInManifest)
	fmt.Printf("  orphan index lines %2d  (MEMORY.md lines whose file is gone)\n\n", r.OrphanIndexLines)
	fmt.Printf("  %d imported = %d filed + %d this run + %d waiting + %d duplicates + %d skipped   %s\n",
		r.Imported, r.AlreadyFiled, r.ThisRun, r.Waiting, r.Duplicates, skipped, balanced)
}

func seedHubs(root string, dryRun bool) int {
	entries, err := fs.ReadDir(templates.FS, "hubs")
	if err != nil {
		return fail("%v", err)
	}
	var created, kept int
	for _, e := range entries {
		dst := filepath.Join(root, "hubs", e.Name())
		if _, err := os.Stat(dst); err == nil {
			kept++
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return fail("%v", err)
		}
		created++
		if dryRun {
			continue
		}
		b, _ := fs.ReadFile(templates.FS, "hubs/"+e.Name())
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fail("%v", err)
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return fail("%v", err)
		}
	}
	fmt.Printf("seed hubs: %d created, %d already there (never overwritten)%s\n", created, kept,
		map[bool]string{true: " — dry run, nothing written"}[dryRun])
	return 0
}

func cmdRenderMemoryIndex([]string) int {
	root := config.BrainRoot()
	if err := filing.RenderMemoryIndex(root); err != nil {
		return fail("%v", err)
	}
	fmt.Println("rendered", filepath.Join(root, "memory", "MEMORY.md"))
	return 0
}
