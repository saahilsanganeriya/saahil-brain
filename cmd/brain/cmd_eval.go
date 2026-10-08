package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"slices"
	"sort"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"saahil.local/brain/internal/dream"
	"saahil.local/brain/internal/evals"
	"saahil.local/brain/internal/index"
)

func init() {
	register("eval build", "build the eval sets from history into ~/brain/.brain/evals: brain eval build [--cut D] [--nav-from D]", cmdEvalBuild)
	register("eval navigation", "first prompt → files the session used: top-3 pointers and within 3 tool calls [--split dev|test]", cmdEvalOne("navigation"))
	register("eval qa", "answer-from-brain: recall@3 of the right note, abstain on unknowns [--split dev|test]", cmdEvalOne("qa"))
	register("eval mistakes", "repeat-mistake: does a pointer surface the lesson before a past correction", cmdEvalOne("mistakes"))
	register("eval ab", "offline A/B: navigation with the brain vs a budget-matched keyword grep", cmdEvalOne("ab"))
	register("eval all", "run every eval on the built sets and write reports: brain eval all [--build]", cmdEvalAll)
	register("eval schedule", "create or update the weekly eval schedule (brain-eval-weekly)", cmdEvalSchedule)
}

func cmdEvalBuild(args []string) int {
	fs := flag.NewFlagSet("eval build", flag.ContinueOnError)
	cut := fs.String("cut", evals.DefaultCut.Format(time.DateOnly), "repeat-mistake set: corrections in sessions from this date")
	navFrom := fs.String("nav-from", evals.DefaultNavFrom.Format(time.DateOnly), "navigation set: sessions from this date")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	opt := evals.BuildOptions{Now: time.Now()}
	var err error
	if opt.Cut, err = time.Parse(time.DateOnly, *cut); err != nil {
		return fail("--cut: %v", err)
	}
	if opt.NavFrom, err = time.Parse(time.DateOnly, *navFrom); err != nil {
		return fail("--nav-from: %v", err)
	}
	return withIndex(func(ix *index.Index) int {
		sets, err := evals.Build(ix, opt)
		if err != nil {
			return fail("eval build: %v", err)
		}
		m := sets.Manifest
		fmt.Printf("built eval sets in %s (navigation from %s, repeat-mistake cut %s)\n", evals.Dir(ix.Root), m.NavFrom, m.Cut)
		dispatched := 0
		for _, it := range sets.Navigation {
			if it.Dispatched {
				dispatched++
			}
		}
		kinds := map[evals.QAKind]int{}
		for _, it := range sets.QA {
			kinds[it.Kind]++
		}
		withLessons := 0
		for _, it := range sets.Mistakes {
			if len(it.Lessons) > 0 {
				withLessons++
			}
		}
		fmt.Printf("  navigation %5d  (human-typed %d, dispatched %d)\n", len(sets.Navigation), len(sets.Navigation)-dispatched, dispatched)
		fmt.Printf("  mistakes   %5d  (%d with a lesson learned from their session)\n", len(sets.Mistakes), withLessons)
		fmt.Printf("  qa         %5d  (fact %d, as_of %d, current %d, unknown %d)\n", len(sets.QA), kinds[evals.QAFact],
			kinds[evals.QAAsOf], kinds[evals.QACurrent], kinds[evals.QAUnknown])
		reasons := make([]string, 0, len(m.Skipped))
		for r := range m.Skipped {
			reasons = append(reasons, r)
		}
		sort.Strings(reasons)
		for _, r := range reasons {
			fmt.Printf("  skipped %5d  %s\n", m.Skipped[r], r)
		}
		return 0
	})
}

func withEvalEnv(build bool, run func(*evals.Env) int) int {
	return withIndex(func(ix *index.Index) int {
		sets := evals.Sets{}
		if !build {
			var err error
			if sets, err = evals.LoadSets(ix.Root); err != nil {
				return fail("%v", err)
			}
		}
		return run(evals.NewEnv(ix, sets, time.Now()))
	})
}

func printReports(reports []evals.Report, root string) {
	for _, r := range reports {
		for _, m := range append(r.Metrics, r.SplitMetrics...) {
			fmt.Println(m.Line())
		}
	}
	if len(reports) > 0 {
		fmt.Printf("reports: %s\n", evals.ReportsDir(root, reports[0].Ran))
	}
}

func cmdEvalOne(name string) func([]string) int {
	return func(args []string) int {
		fs := flag.NewFlagSet("eval "+name, flag.ContinueOnError)
		only := fs.String("split", "", "run only the dev or the test half of the set")
		if err := fs.Parse(args); err != nil {
			return 2
		}
		if *only != "" && !slices.Contains(evals.Splits, *only) {
			return fail("--split: dev or test, not %q", *only)
		}
		return withEvalEnv(false, func(env *evals.Env) int {
			if *only != "" {
				env.Only(*only)
			}
			rep, err := env.Run(name)
			if err != nil {
				return fail("eval %s: %v", name, err)
			}
			if _, err := rep.Write(env.Index.Root); err != nil {
				return fail("write report: %v", err)
			}
			printReports([]evals.Report{rep}, env.Index.Root)
			return 0
		})
	}
}

func cmdEvalAll(args []string) int {
	fs := flag.NewFlagSet("eval all", flag.ContinueOnError)
	build := fs.Bool("build", false, "rebuild the sets first")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return withEvalEnv(*build, func(env *evals.Env) int {
		reports, err := evals.RunAll(env, *build)
		printReports(reports, env.Index.Root)
		if err != nil {
			return fail("eval all: %v", err)
		}
		return 0
	})
}

func cmdEvalSchedule(args []string) int {
	if len(args) > 0 {
		return fail("eval schedule takes no arguments")
	}
	c, err := dialDream()
	if err != nil {
		return fail("temporal at %s: %v", dream.HostPort, err)
	}
	defer c.Close()
	ctx := context.Background()
	// Sunday 05:00 UTC: after the night's dreams, before the week starts.
	spec := client.ScheduleSpec{Calendars: []client.ScheduleCalendarSpec{{
		DayOfWeek: []client.ScheduleRange{{Start: 0}}, Hour: []client.ScheduleRange{{Start: 5}}}}}
	action := &client.ScheduleWorkflowAction{ID: "eval-weekly", Workflow: dream.EvalWorkflow,
		Args: []any{dream.EvalInput{}}, TaskQueue: dream.TaskQueue}
	policy := client.SchedulePolicies{Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP, CatchupWindow: time.Hour}
	_, err = c.ScheduleClient().Create(ctx, client.ScheduleOptions{ID: dream.EvalScheduleID, Spec: spec, Action: action,
		Overlap: policy.Overlap, CatchupWindow: policy.CatchupWindow})
	switch {
	case err == nil:
		fmt.Printf("created schedule %s: weekly EvalWorkflow (Sunday 05:00 UTC)\n", dream.EvalScheduleID)
	case errors.Is(err, temporal.ErrScheduleAlreadyRunning):
		err = c.ScheduleClient().GetHandle(ctx, dream.EvalScheduleID).Update(ctx, client.ScheduleUpdateOptions{
			DoUpdate: func(in client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
				s := in.Description.Schedule
				s.Spec, s.Action, s.Policy = &spec, action, &policy
				return &client.ScheduleUpdate{Schedule: &s}, nil
			}})
		if err != nil {
			return fail("update schedule %s: %v", dream.EvalScheduleID, err)
		}
		fmt.Printf("updated schedule %s: weekly EvalWorkflow (Sunday 05:00 UTC)\n", dream.EvalScheduleID)
	default:
		return fail("create schedule %s: %v", dream.EvalScheduleID, err)
	}
	return 0
}
