package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"saahil.local/brain/internal/backfill"
	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/dream"
	"saahil.local/brain/internal/llm"
)

func init() {
	register("dream run", "run a dream now on the devbox's Temporal: brain dream run [--now] [--shadow]", cmdDreamRun)
	register("dream status", "last dreams, the next scheduled one, LLM cost today", cmdDreamStatus)
	register("dream schedule", "create or update the hourly dream schedule (brain-dream-hourly)", cmdDreamSchedule)
}

func dialDream() (client.Client, error) {
	return client.Dial(client.Options{HostPort: dream.HostPort, Namespace: dream.Namespace})
}

func cmdDreamRun(args []string) int {
	fs := flag.NewFlagSet("dream run", flag.ContinueOnError)
	now := fs.Bool("now", false, "skip the 12 h / 30-session wait (the nothing-new skip and the lock still apply)")
	shadow := fs.Bool("shadow", false, "also read the shadow doc roots (shadow_docs in brain identity), read-only")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c, err := dialDream()
	if err != nil {
		return fail("temporal at %s: %v", dream.HostPort, err)
	}
	defer c.Close()
	ctx := context.Background()
	id := dream.WorkflowID(time.Now())
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                       id,
		TaskQueue:                dream.TaskQueue,
		WorkflowIDReusePolicy:    enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
		WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
	}, dream.DreamWorkflow, dream.Input{Force: *now, Shadow: *shadow})
	if err != nil {
		return fail("start %s: %v", id, err)
	}
	fmt.Fprintf(os.Stderr, "dream %s (run %s) started; waiting…\n", id, run.GetRunID())
	var rep dream.Report
	err = run.Get(ctx, &rep)
	out, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Println(string(out))
	if err != nil {
		return fail("dream %s: %v", id, err)
	}
	return 0
}

func cmdDreamStatus([]string) int {
	root := config.BrainRoot()
	reports, _ := filepath.Glob(filepath.Join(root, ".brain", "dream-runs", "*.json"))
	sort.Sort(sort.Reverse(sort.StringSlice(reports))) // run ids start with the UTC start time
	fmt.Println("last dreams:")
	if len(reports) == 0 {
		fmt.Println("  none yet")
	}
	for i, p := range reports {
		if i == 5 {
			break
		}
		var rep dream.Report
		if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &rep) == nil {
			fmt.Printf("  %s  %-9s %2d edits  %d calls (%d cached)  $%.2f  deletions %d\n", rep.RunID, rep.Status,
				rep.Edits, rep.LLMCalls, rep.LLMCached, rep.CostUSD, rep.Deletions)
		}
	}
	var skip dream.Report
	if b, err := os.ReadFile(filepath.Join(root, ".brain", "dream-last-skip.json")); err == nil &&
		json.Unmarshal(b, &skip) == nil {
		fmt.Printf("last skip: %s  %s\n", skip.Started.Format(time.RFC3339), skip.Skipped)
	}
	if b, err := os.ReadFile(filepath.Join(root, ".brain", "dream.lock")); err == nil {
		fmt.Printf("lock: %s\n", b)
	}

	if ledger, err := llm.OpenLedger(filepath.Join(root, ".index", "brain.db"), filepath.Join(root, ".data", "llm")); err == nil {
		today := time.Now().UTC().Truncate(24 * time.Hour)
		if spent, err := ledger.SpentSince(today); err == nil {
			fmt.Printf("llm cost today: $%.2f of $%.2f\n", spent, dream.DailyCapUSD)
		}
		ledger.Close()
	}

	c, err := dialDream()
	if err != nil {
		fmt.Printf("schedule: temporal at %s unreachable: %v\n", dream.HostPort, err)
		return 1
	}
	defer c.Close()
	desc, err := c.ScheduleClient().GetHandle(context.Background(), dream.ScheduleID).Describe(context.Background())
	if err != nil {
		fmt.Printf("schedule %s: %v (create it with `brain dream schedule`)\n", dream.ScheduleID, err)
		return 1
	}
	next := "none"
	if len(desc.Info.NextActionTimes) > 0 {
		next = desc.Info.NextActionTimes[0].UTC().Format(time.RFC3339)
	}
	paused := desc.Schedule.State != nil && desc.Schedule.State.Paused
	fmt.Printf("schedule %s: next %s, %d runs so far, paused=%v\n", dream.ScheduleID, next, desc.Info.NumActions, paused)
	return 0
}

func cmdDreamSchedule(args []string) int {
	fs := flag.NewFlagSet("dream schedule", flag.ContinueOnError)
	shadow := fs.Bool("shadow", !backfill.Live(config.BrainRoot()), "scheduled dreams also read the shadow doc roots (default: until the cut-over)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	c, err := dialDream()
	if err != nil {
		return fail("temporal at %s: %v", dream.HostPort, err)
	}
	defer c.Close()
	ctx := context.Background()
	spec := client.ScheduleSpec{Intervals: []client.ScheduleIntervalSpec{{Every: time.Hour}}}
	action := &client.ScheduleWorkflowAction{ID: "dream-tick", Workflow: dream.DreamTickWorkflow,
		Args: []any{dream.Input{Shadow: *shadow}}, TaskQueue: dream.TaskQueue}
	policy := client.SchedulePolicies{Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP, CatchupWindow: 10 * time.Minute}

	_, err = c.ScheduleClient().Create(ctx, client.ScheduleOptions{ID: dream.ScheduleID, Spec: spec, Action: action,
		Overlap: policy.Overlap, CatchupWindow: policy.CatchupWindow})
	switch {
	case err == nil:
		fmt.Printf("created schedule %s: hourly DreamTickWorkflow (shadow=%v)\n", dream.ScheduleID, *shadow)
	case errors.Is(err, temporal.ErrScheduleAlreadyRunning):
		err = c.ScheduleClient().GetHandle(ctx, dream.ScheduleID).Update(ctx, client.ScheduleUpdateOptions{
			DoUpdate: func(in client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
				s := in.Description.Schedule
				s.Spec, s.Action, s.Policy = &spec, action, &policy
				return &client.ScheduleUpdate{Schedule: &s}, nil
			}})
		if err != nil {
			return fail("update schedule %s: %v", dream.ScheduleID, err)
		}
		fmt.Printf("updated schedule %s: hourly DreamTickWorkflow (shadow=%v)\n", dream.ScheduleID, *shadow)
	default:
		return fail("create schedule %s: %v", dream.ScheduleID, err)
	}
	return 0
}
