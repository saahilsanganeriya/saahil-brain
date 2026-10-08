package dream

import (
	"context"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"saahil.local/brain/internal/evals"
	"saahil.local/brain/internal/index"
)

// EvalScheduleID is the weekly Schedule that starts EvalWorkflow (created by `brain eval schedule`).
const EvalScheduleID = "brain-eval-weekly"

type EvalInput struct {
	KeepSets bool `json:"keep_sets"` // run on the stored sets instead of rebuilding them from the brain as it is now
}

type EvalSummary struct {
	Reports string         `json:"reports"` // the folder the run wrote
	Metrics []evals.Metric `json:"metrics"`
	Sizes   map[string]int `json:"sizes"`
}

// EvalWorkflow brings the index up to date, rebuilds the eval sets and runs every eval (DESIGN §9): deterministic code only, no model calls, so
// one activity with a generous timeout and a single retry is enough.
func EvalWorkflow(ctx workflow.Context, in EvalInput) (EvalSummary, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Hour,
		RetryPolicy:         &temporal.RetryPolicy{InitialInterval: time.Minute, MaximumAttempts: 2},
	})
	var out EvalSummary
	err := workflow.ExecuteActivity(ctx, a.RunEvals, in).Get(ctx, &out)
	return out, err
}

func (a *Activities) RunEvals(ctx context.Context, in EvalInput) (EvalSummary, error) {
	ix, err := index.Open(a.Cfg.BrainRoot)
	if err != nil {
		return EvalSummary{}, err
	}
	defer ix.Close()
	if _, err := ix.Update(ctx, false); err != nil {
		return EvalSummary{}, err
	}
	sets := evals.Sets{}
	if in.KeepSets {
		if sets, err = evals.LoadSets(a.Cfg.BrainRoot); err != nil {
			return EvalSummary{}, err
		}
	}
	env := evals.NewEnv(ix, sets, time.Now())
	reports, err := evals.RunAll(env, !in.KeepSets)
	out := EvalSummary{Reports: evals.ReportsDir(a.Cfg.BrainRoot, env.Now), Sizes: env.Sizes()}
	for _, r := range reports {
		out.Metrics = append(out.Metrics, r.Metrics...)
	}
	return out, err
}
