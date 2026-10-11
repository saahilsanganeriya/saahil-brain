// Package dream is the brain's hourly consolidation: DreamWorkflow and its activities (DESIGN §7), run by brain-worker
// on the devbox's local Temporal. In shadow it writes only inside the brain root.
package dream

import (
	"fmt"
	"strings"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"saahil.local/brain/internal/config"
)

const (
	HostPort   = "127.0.0.1:7233" // the local dev server only; never Temporal Cloud
	Namespace  = "brain"
	TaskQueue  = "brain-dream"
	QueueOpus  = "brain-llm-opus"
	QueueHaiku = "brain-llm-haiku"
	ScheduleID = "brain-dream-hourly"

	RunCapUSD   = 8.0
	DailyCapUSD = 100.0
	MaxEdits    = 50
	MaxSessions = 60
	MaxCaptures = 40
	MaxBigDocs  = 10

	MinGap         = 12 * time.Hour
	EarlySessions  = 30
	LockStaleAfter = 3 * time.Hour
)

// Per-call ceilings passed to --max-budget-usd; the run cap is checked against spent + this before each call, so a
// run can never pass $8 by more than the CLI's own overshoot.
var stepBudgetUSD = map[string]float64{config.ModelJudgment: 2.50, config.ModelBulk: 0.50}

type Input struct {
	Force  bool `json:"force"`  // skip the 12 h / 30-session wait; the nothing-new skip and the lock still apply
	Shadow bool `json:"shadow"` // also read the shadow doc roots (saahil-docs), read-only
}

// WorkflowID names a dream by its UTC hour, so one hour holds at most one scheduled dream.
func WorkflowID(t time.Time) string { return "dream/" + t.UTC().Format("2006-01-02T15") }

type Report struct {
	RunID             string         `json:"run_id"`
	WorkflowID        string         `json:"workflow_id"`
	Status            string         `json:"status"` // completed | skipped | failed
	Skipped           string         `json:"skipped,omitempty"`
	Error             string         `json:"error,omitempty"`
	Shadow            bool           `json:"shadow"`
	Started           time.Time      `json:"started"`
	Ended             time.Time      `json:"ended"`
	Material          MaterialCounts `json:"material"`
	Steps             []StepRecord   `json:"steps"`
	CostUSD           float64        `json:"cost_usd"`
	LLMCalls          int            `json:"llm_calls"`
	LLMCached         int            `json:"llm_cached"`
	Edits             int            `json:"edits"`
	EditedPaths       []string       `json:"edited_paths,omitempty"`
	Rejected          []Rejection    `json:"rejected,omitempty"`
	Deletions         int            `json:"deletions"` // always 0: the dream never deletes; refused deletes are below
	DeletionsRejected int            `json:"deletions_rejected"`
	Reverted          int            `json:"reverted"`
	SnapshotCommit    string         `json:"snapshot_commit,omitempty"`
	Commit            string         `json:"commit,omitempty"`
}

type StepRecord struct {
	Name       string  `json:"name"`
	Status     string  `json:"status"` // ok | skipped | failed
	Reason     string  `json:"reason,omitempty"`
	DurationMS int64   `json:"duration_ms"`
	CostUSD    float64 `json:"cost_usd,omitempty"`
	Called     bool    `json:"called,omitempty"`
	Cached     bool    `json:"cached,omitempty"`
	Edits      int     `json:"edits,omitempty"`
	Refused    int     `json:"refused,omitempty"` // units the applier refused; the step can still be ok
}

// area names the watermark a step consumes; "" for steps that read this run's proposed edits.
type llmStep struct{ name, skill, model, area string }

var (
	learnStep     = llmStep{"learn", "distil", config.ModelJudgment, "sessions"}
	fileStep      = llmStep{"file", "file", config.ModelBulk, "captures"}
	linkStep      = llmStep{"curate.link", "link", config.ModelJudgment, ""}
	hubsStep      = llmStep{"curate.hubs", "hubs", config.ModelJudgment, ""}
	summariseStep = llmStep{"summarise", "summarise", config.ModelBulk, "docs"}
	verifyStep    = llmStep{"verify", "verify", config.ModelJudgment, ""}
)

var (
	detOptions = workflow.ActivityOptions{
		StartToCloseTimeout: 15 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{InitialInterval: 5 * time.Second, MaximumAttempts: 3},
	}
	// A retry may pay again (a crash after the model answered), so LLM attempts are few.
	llmOptions = workflow.ActivityOptions{
		StartToCloseTimeout: 20 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{InitialInterval: 30 * time.Second, MaximumAttempts: 2},
	}
)

// run is one DreamWorkflow execution's in-memory state; every field is rebuilt identically on replay.
type run struct {
	ctx      workflow.Context
	det      workflow.Context
	rep      *Report
	gate     GateResult
	applied  []Applied
	proposed []Edit
	stopLLM  string
	consumed map[string]bool // material areas whose step ran; only these watermarks advance
}

var a *Activities // activity references only; never called directly in the workflow

func DreamWorkflow(ctx workflow.Context, in Input) (Report, error) {
	info := workflow.GetInfo(ctx)
	start := workflow.Now(ctx).UTC()
	r := &run{ctx: ctx, det: workflow.WithActivityOptions(ctx, detOptions), consumed: map[string]bool{}, rep: &Report{
		RunID:      runID(start, info.WorkflowExecution.RunID),
		WorkflowID: info.WorkflowExecution.ID,
		Shadow:     in.Shadow,
		Started:    start,
	}}

	began := workflow.Now(ctx)
	err := workflow.ExecuteActivity(r.det, a.Gate, GateInput{RunID: r.rep.RunID, Now: start, Force: in.Force,
		Shadow: in.Shadow}).Get(ctx, &r.gate)
	if err != nil {
		return r.fail("gate", began, err)
	}
	r.rep.Material = r.gate.Counts
	if r.gate.Skip != "" {
		r.record(StepRecord{Name: "gate", Status: "skipped", Reason: r.gate.Skip}, began)
		r.rep.Status, r.rep.Skipped, r.rep.Ended = "skipped", r.gate.Skip, workflow.Now(ctx).UTC()
		_ = workflow.ExecuteActivity(r.det, a.WriteReport, *r.rep).Get(ctx, nil)
		return *r.rep, nil
	}
	r.record(StepRecord{Name: "gate", Status: "ok"}, began)
	defer func() {
		dctx, _ := workflow.NewDisconnectedContext(r.det)
		_ = workflow.ExecuteActivity(dctx, a.ReleaseLock, r.rep.RunID).Get(dctx, nil)
	}()

	// No snapshot, no edits: a failed snapshot ends the run before anything is written.
	began = workflow.Now(ctx)
	var snap SnapshotResult
	if err := workflow.ExecuteActivity(r.det, a.Snapshot, r.rep.RunID).Get(ctx, &snap); err != nil {
		return r.fail("snapshot", began, err)
	}
	r.rep.SnapshotCommit = snap.Commit
	r.record(StepRecord{Name: "snapshot", Status: "ok", Reason: snap.Commit}, began)

	r.outcome("reconcile", workflow.ExecuteActivity(r.det, a.Reconcile))
	r.brain("capture", []string{"session", "backfill"}, "--machine", "devbox")
	// histories recorded before per-session learning replay the batched call
	if workflow.GetVersion(ctx, "learn-per-session", workflow.DefaultVersion, 1) == workflow.DefaultVersion {
		r.llm(learnStep, r.gate.Sessions)
	} else {
		r.learnSessions(r.gate.Sessions)
	}
	if workflow.GetVersion(ctx, "demote-unopened", workflow.DefaultVersion, 1) == 1 {
		r.outcome("demote", workflow.ExecuteActivity(r.det, a.Demote))
	}
	r.llm(fileStep, r.gate.Captures)
	r.llm(linkStep, nil)
	r.llm(hubsStep, nil)
	// histories recorded before curate.merge replay without it
	if workflow.GetVersion(ctx, "curate-merge", workflow.DefaultVersion, 1) == 1 {
		r.merge(start)
	}
	// histories recorded before curate.cards replay without it
	if workflow.GetVersion(ctx, "curate-cards", workflow.DefaultVersion, 1) == 1 {
		r.cards()
	}
	r.llm(summariseStep, r.gate.BigDocs)
	r.brain("render", []string{"render"})
	r.check()
	r.llm(verifyStep, nil)
	return r.commit(start)
}

func (r *run) llm(s llmStep, material []Material) {
	began := workflow.Now(r.ctx)
	var proposed []Edit
	if s.area == "" {
		proposed = r.proposed
	}
	if len(material) == 0 && len(proposed) == 0 {
		r.consumed[s.area] = true
		r.record(StepRecord{Name: s.name, Status: "skipped", Reason: "no input"}, began)
		return
	}
	if r.stopLLM != "" {
		r.record(StepRecord{Name: s.name, Status: "skipped", Reason: r.stopLLM}, began)
		return
	}
	opts := llmOptions
	opts.TaskQueue = QueueHaiku
	if s.model == config.ModelJudgment {
		opts.TaskQueue = QueueOpus
	}
	var out LLMStepResult
	err := workflow.ExecuteActivity(workflow.WithActivityOptions(r.ctx, opts), a.LLMStep, LLMStepInput{
		Workflow: "dream", Step: s.name, Skill: s.skill, Model: s.model, BudgetUSD: stepBudgetUSD[s.model],
		RunSpentUSD: r.rep.CostUSD, Material: material, Proposed: proposed,
	}).Get(r.ctx, &out)
	if err != nil {
		r.record(StepRecord{Name: s.name, Status: "failed", Reason: err.Error()}, began)
		return
	}
	r.rep.CostUSD += out.CostUSD
	if out.Called {
		r.rep.LLMCalls++
	}
	if out.Cached {
		r.rep.LLMCached++
	}
	if out.StopLLM {
		r.stopLLM = out.Reason
	}
	if out.Status == "ok" {
		r.consumed[s.area] = true
	}
	rec := StepRecord{Name: s.name, Status: out.Status, Reason: out.Reason, CostUSD: out.CostUSD,
		Called: out.Called, Cached: out.Cached}
	if len(out.Edits) > 0 {
		r.proposed = append(r.proposed, out.Edits...)
		rec.Edits = r.apply(s.name, out.Edits)
	}
	r.record(rec, began)
}

func (r *run) apply(step string, edits []Edit) int {
	var res ApplyResult
	err := workflow.ExecuteActivity(r.det, a.ApplyEdits, ApplyInput{RunID: r.rep.RunID, Step: step, Edits: edits,
		Remaining: MaxEdits - len(r.applied)}).Get(r.ctx, &res)
	if err != nil {
		r.rep.Rejected = append(r.rep.Rejected, Rejection{Step: step, Reason: "apply failed: " + err.Error()})
		return 0
	}
	r.applied = append(r.applied, res.Applied...)
	r.rep.Rejected = append(r.rep.Rejected, res.Rejected...)
	for _, rej := range res.Rejected {
		if rej.Action == "delete" {
			r.rep.DeletionsRejected++
		}
	}
	return len(res.Applied)
}

func (r *run) brain(step string, command []string, args ...string) StepOutcome {
	return r.outcome(step, workflow.ExecuteActivity(r.det, a.BrainCommand, BrainCommandInput{Command: command, Args: args}))
}

func (r *run) outcome(step string, f workflow.Future) StepOutcome {
	began := workflow.Now(r.ctx)
	var out StepOutcome
	if err := f.Get(r.ctx, &out); err != nil {
		out = StepOutcome{Status: "failed", Reason: err.Error()}
	}
	r.record(StepRecord{Name: step, Status: out.Status, Reason: out.Reason}, began)
	return out
}

// check lints what this run wrote; a failing lint (exit ≥ 2) puts every edited file back as the snapshot had it.
func (r *run) check() {
	// with nothing written there is nothing to check, and lint without paths walks the whole brain, work/ included
	if len(r.applied) == 0 && workflow.GetVersion(r.ctx, "check-only-edits", workflow.DefaultVersion, 1) == 1 {
		r.record(StepRecord{Name: "check", Status: "skipped", Reason: "no edits"}, workflow.Now(r.ctx))
		return
	}
	paths := make([]string, len(r.applied))
	for i, ap := range r.applied {
		paths[i] = ap.Path
	}
	began := workflow.Now(r.ctx)
	var out StepOutcome
	err := workflow.ExecuteActivity(r.det, a.BrainCommand, BrainCommandInput{Command: []string{"lint"}, Args: paths}).
		Get(r.ctx, &out)
	if err != nil {
		out = StepOutcome{Status: "failed", Reason: err.Error()}
	}
	if out.ExitCode == 1 {
		out.Status = "ok" // warnings only
	}
	if out.ExitCode >= 2 && len(r.applied) > 0 {
		var reverted int
		if err := workflow.ExecuteActivity(r.det, a.Revert, RevertInput{RunID: r.rep.RunID, Applied: r.applied}).
			Get(r.ctx, &reverted); err != nil {
			out.Reason += "; revert failed: " + err.Error()
		} else {
			r.rep.Reverted += reverted
			r.applied = nil
			out.Reason += fmt.Sprintf("; reverted %d edits to the snapshot", reverted)
		}
	}
	r.record(StepRecord{Name: "check", Status: out.Status, Reason: out.Reason}, began)
}

func (r *run) commit(start time.Time) (Report, error) {
	began := workflow.Now(r.ctx)
	r.brain("reindex", []string{"index"})
	r.rep.Status = "completed"
	r.rep.Edits = len(r.applied)
	for _, ap := range r.applied {
		r.rep.EditedPaths = append(r.rep.EditedPaths, ap.Path)
	}
	r.rep.Ended = workflow.Now(r.ctx).UTC()
	r.record(StepRecord{Name: "commit", Status: "ok"}, began)
	state := State{LastRunID: r.rep.RunID, LastRun: start, Watermarks: map[string]time.Time{}}
	for area, prev := range r.gate.Prev {
		state.Watermarks[area] = prev
	}
	for area, next := range r.gate.Next {
		if r.consumed[area] {
			state.Watermarks[area] = next
		}
	}
	var commit string
	if err := workflow.ExecuteActivity(r.det, a.Commit, CommitInput{Report: *r.rep, State: state}).
		Get(r.ctx, &commit); err != nil {
		r.rep.Steps = r.rep.Steps[:len(r.rep.Steps)-1]
		return r.fail("commit", began, err)
	}
	r.rep.Commit = commit
	return *r.rep, nil
}

func (r *run) record(rec StepRecord, began time.Time) {
	rec.DurationMS = workflow.Now(r.ctx).Sub(began).Milliseconds()
	r.rep.Steps = append(r.rep.Steps, rec)
}

// fail writes the report of a run that cannot go on; the state is not advanced, so its material waits for the next.
func (r *run) fail(step string, began time.Time, err error) (Report, error) {
	r.record(StepRecord{Name: step, Status: "failed", Reason: err.Error()}, began)
	r.rep.Status, r.rep.Error, r.rep.Ended = "failed", step+": "+err.Error(), workflow.Now(r.ctx).UTC()
	_ = workflow.ExecuteActivity(r.det, a.WriteReport, *r.rep).Get(r.ctx, nil)
	return *r.rep, err
}

func runID(start time.Time, temporalRunID string) string {
	short := strings.ReplaceAll(temporalRunID, "-", "")
	if len(short) > 8 {
		short = short[:8]
	}
	return start.Format("2006-01-02T150405Z") + "-" + short
}

// DreamTickWorkflow is what the hourly Schedule starts (a Schedule appends a timestamp to its own workflow ids); it
// runs the dream as a child named dream/<UTC hour>.
func DreamTickWorkflow(ctx workflow.Context, in Input) (Report, error) {
	id := WorkflowID(workflow.Now(ctx))
	cctx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowID:            id,
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
	})
	var rep Report
	err := workflow.ExecuteChildWorkflow(cctx, DreamWorkflow, in).Get(ctx, &rep)
	if temporal.IsWorkflowExecutionAlreadyStartedError(err) {
		return Report{WorkflowID: id, Status: "skipped", Skipped: id + " already ran or is running"}, nil
	}
	return rep, err
}
