package dream

import (
	"strconv"

	"go.temporal.io/sdk/workflow"

	"saahil.local/brain/internal/backfill"
	"saahil.local/brain/internal/config"
)

const (
	BackfillBudgetUSD = 80.0
	backfillBatch     = 8
	batchesPerRun     = 10 // then continue-as-new, so the history stays small over ~90 boxes
)

type BackfillInput struct {
	Boxes     []string `json:"boxes,omitempty"` // empty: every box
	BudgetUSD float64  `json:"budget_usd"`      // total across every backfill run (ledger workflow="backfill")
	DryRun    bool     `json:"dry_run"`
	// IncludePaused also writes notes for quiet boxes whose CARD says paused (parked or stalled campaigns)
	IncludePaused bool `json:"include_paused,omitempty"`

	// carried across continue-as-new
	Live    bool               `json:"live,omitempty"`
	Pending []backfill.Planned `json:"pending,omitempty"`
	Report  *backfill.Report   `json:"report,omitempty"`
}

type BatchInput struct {
	RunID     string
	Live      bool
	BudgetUSD float64
	Boxes     []backfill.Planned
}

type BatchResult struct {
	Boxes   []backfill.BoxResult
	Stopped string
}

func BackfillWorkflow(ctx workflow.Context, in BackfillInput) (backfill.Report, error) {
	det := workflow.WithActivityOptions(ctx, detOptions)
	if in.BudgetUSD <= 0 {
		in.BudgetUSD = BackfillBudgetUSD
	}
	if in.Report == nil {
		now := workflow.Now(ctx).UTC()
		in.Report = &backfill.Report{ID: "backfill-" + runID(now, workflow.GetInfo(ctx).WorkflowExecution.RunID),
			DryRun: in.DryRun, BudgetUSD: in.BudgetUSD, Started: now}
		var plan PlanResult
		if err := workflow.ExecuteActivity(det, a.BackfillPlan, PlanInput{Boxes: in.Boxes, Now: now, IncludePaused: in.IncludePaused}).Get(ctx, &plan); err != nil {
			return *in.Report, err
		}
		in.Live, in.Report.Live = plan.Live, plan.Live
		for _, p := range plan.Boxes {
			switch {
			case p.Action == "skip":
				in.Report.Add(backfill.BoxResult{Box: p.Box.Name, Action: p.Action, Status: "skipped", Reason: p.Reason})
			case in.DryRun:
				in.Report.Add(backfill.BoxResult{Box: p.Box.Name, Action: p.Action, Status: "planned", Reason: p.Reason,
					Finished: p.Finished})
			default:
				in.Pending = append(in.Pending, p)
			}
		}
	}
	rep := in.Report
	for batch := 0; len(in.Pending) > 0 && rep.Stopped == ""; batch++ {
		if batch == batchesPerRun {
			return *rep, workflow.NewContinueAsNewError(ctx, BackfillWorkflow, in)
		}
		n := min(backfillBatch, len(in.Pending))
		rep.Batches++
		cctx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
			WorkflowID: rep.ID + "/batch-" + strconv.Itoa(rep.Batches)})
		var out BatchResult
		err := workflow.ExecuteChildWorkflow(cctx, BackfillBatchWorkflow, BatchInput{RunID: rep.ID, Live: in.Live,
			BudgetUSD: in.BudgetUSD, Boxes: in.Pending[:n]}).Get(ctx, &out)
		if err != nil {
			for _, p := range in.Pending[:n] {
				rep.Add(backfill.BoxResult{Box: p.Box.Name, Action: p.Action, Status: "failed", Reason: err.Error()})
			}
		}
		for _, b := range out.Boxes {
			rep.Add(b)
		}
		rep.Stopped = out.Stopped
		in.Pending = in.Pending[n:]
	}
	for _, p := range in.Pending { // left over after a cost stop
		rep.Add(backfill.BoxResult{Box: p.Box.Name, Action: p.Action, Status: "skipped", Reason: rep.Stopped})
	}
	rep.Ended = workflow.Now(ctx).UTC()
	return *rep, workflow.ExecuteActivity(det, a.WriteBackfillReport, *rep).Get(ctx, nil)
}

// BackfillBatchWorkflow writes the CARDs of up to 8 boxes, one after another so the cost cap holds, and the notes of
// those that are finished campaigns.
func BackfillBatchWorkflow(ctx workflow.Context, in BatchInput) (BatchResult, error) {
	var out BatchResult
	for _, p := range in.Boxes {
		res := backfill.BoxResult{Box: p.Box.Name, Action: p.Action, Status: "ok", Finished: p.Finished}
		if out.Stopped != "" {
			res.Status, res.Reason = "skipped", out.Stopped
			out.Boxes = append(out.Boxes, res)
			continue
		}
		bin := BoxInput{RunID: in.RunID, Box: p.Box, Live: in.Live, BudgetUSD: in.BudgetUSD}
		if p.Action == "card" {
			step := boxStep(ctx, &out, &res, config.ModelBulk, a.BackfillCard, bin)
			res.Card, res.CardStatus = step.Card, step.CardStatus
			res.Finished = res.Finished || step.CardStatus == "done"
		}
		if res.Finished && res.Status == "ok" {
			res.Notes = boxStep(ctx, &out, &res, config.ModelJudgment, a.BackfillCampaign, bin).Notes
		}
		out.Boxes = append(out.Boxes, res)
	}
	return out, nil
}

func boxStep(ctx workflow.Context, out *BatchResult, res *backfill.BoxResult, model string, act any, in BoxInput) BoxStepResult {
	var step BoxStepResult
	if err := workflow.ExecuteActivity(llmContext(ctx, model), act, in).Get(ctx, &step); err != nil {
		res.Status, res.Reason = "failed", err.Error()
		return step
	}
	res.CostUSD += step.CostUSD
	if step.Status != "ok" {
		res.Status, res.Reason = step.Status, step.Reason
	}
	if step.StopLLM {
		out.Stopped = step.Reason
	}
	return step
}
