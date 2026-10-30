// Package dream is the brain's hourly consolidation: DreamWorkflow and its activities (DESIGN §7), run by brain-worker
// on the devbox's local Temporal. In shadow it writes only inside the brain root.
package dream

import (
	"fmt"
	"slices"
	"strings"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/delta"
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
	held     []heldEdit // edits the cap held from a step that will not propose them again
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
	if workflow.GetVersion(ctx, "land-held", workflow.DefaultVersion, 1) == 1 {
		r.landHeld()
	}
	r.brain("render", []string{"render"})
	r.check()
	r.llm(verifyStep, nil)
	return r.commit(start)
}

// capStopped says whether an earlier refusal at a cap skips a step without asking it. Since cap-stop-per-step every
// step asks: its activity checks the caps against its own smallest call, so a $2.50 step that did not fit no longer
// cancels a cheaper one after it (verify's $1.00 batches, $0.50 summaries). The refusal stays in r.stopLLM, which
// keeps learn's sessions for the next dream.
func (r *run) capStopped() bool {
	return r.stopLLM != "" &&
		workflow.GetVersion(r.ctx, "cap-stop-per-step", workflow.DefaultVersion, 1) == workflow.DefaultVersion
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
	if r.capStopped() {
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
		Called: out.Called, Cached: out.Cached, Refused: out.Refused}
	switch {
	case len(out.Edits) == 0:
	case s == fileStep && len(material) > 0 &&
		workflow.GetVersion(r.ctx, "file-holds-whole-capture", workflow.DefaultVersion, 1) == 1:
		var waiting int
		if rec.Edits, waiting = r.fileCaptures(material, out.Edits); waiting > 0 {
			rec.Reason = strings.TrimPrefix(rec.Reason+fmt.Sprintf(
				"; %d captures wait for the next dream: the edit cap has no room for the next one whole", waiting), "; ")
		}
	default:
		r.proposed = append(r.proposed, out.Edits...)
		rec.Edits = r.apply(s.name, out.Edits)
	}
	r.record(rec, began)
}

// fileCaptures applies the file step's answer one capture at a time, oldest first, each whole, as learn applies its
// sessions: the first capture the cap has no room for, and every later one, wait for the next dream (the watermark
// stops before it) instead of being lost.
func (r *run) fileCaptures(captures []Material, edits []Edit) (applied, waiting int) {
	for i, unit := range captureUnits(captures, edits) {
		if len(unit) == 0 {
			continue
		}
		in := applyInput(r.rep.RunID, fileStep.name, unit, r.applied)
		in.Whole = true
		n, held := r.applyUnit(in)
		if held && n == 0 {
			r.gate.Next[fileStep.area] = captures[i].MTime.Add(-time.Nanosecond)
			return applied, len(captures) - i
		}
		r.proposed = append(r.proposed, unit...)
		applied += n
	}
	return applied, 0
}

// captureUnits splits the file step's edits by the capture they file: the oldest of the run's captures an edit's
// sources name, so a note filed from two captures lands with the first; an edit that names none goes with the newest.
func captureUnits(captures []Material, edits []Edit) [][]Edit {
	index := make(map[string]int, len(captures))
	for i, c := range captures {
		index[c.Rel] = i
	}
	units := make([][]Edit, len(captures))
	for _, e := range edits {
		at := len(captures) - 1
		if n, err := delta.Parse([]byte(e.Content)); err == nil {
			for _, src := range n.Sources {
				if i, ok := index[strings.SplitN(src, "#", 2)[0]]; ok && i < at {
					at = i
				}
			}
		}
		units[at] = append(units[at], e)
	}
	return units
}

// curationSteps share what learn and file leave of the cap; learn and file consume their input, so they keep first
// claim (a learn session or a filed capture the cap has no room for waits whole, unconsumed). Of the curation steps
// only merge and cards rebuild their edits from the whole brain each run, so only their held edits return; link, hubs
// and verify judge this run's proposals, so an edit the cap holds there is lost unless landHeld finds room for it
// after summarise.
var curationSteps = []string{linkStep.name, hubsStep.name, mergeStepName, cardsStepName, verifyStep.name}

var rebuildsEachRun = map[string]bool{mergeStepName: true, cardsStepName: true}

// editBudget is how many more files a step may change this run: a curation step gets an equal share of what is left
// with the curation steps after it, so the first one cannot starve the rest; a share it does not use carries on.
func editBudget(step string, applied int) int {
	left := MaxEdits - applied
	if i := slices.Index(curationSteps, step); i >= 0 && left > 0 {
		after := len(curationSteps) - i
		return (left + after - 1) / after
	}
	return left
}

// applyInput charges a step's share in files: a file the run already changed costs nothing to change again.
func applyInput(runID, step string, edits []Edit, applied []Applied) ApplyInput {
	changed := changedFiles(applied)
	return ApplyInput{RunID: runID, Step: step, Edits: edits, Remaining: editBudget(step, len(changed)), Changed: changed}
}

func changedFiles(applied []Applied) []string {
	var files []string
	seen := map[string]bool{}
	for _, ap := range applied {
		if !seen[ap.Path] {
			seen[ap.Path] = true
			files = append(files, ap.Path)
		}
	}
	return files
}

func (r *run) apply(step string, edits []Edit) int {
	applied, _ := r.applyUnit(applyInput(r.rep.RunID, step, edits, r.applied))
	return applied
}

// applyUnit runs ApplyEdits and records its result; held says the edit cap held at least one edit.
func (r *run) applyUnit(in ApplyInput) (applied int, held bool) {
	var res ApplyResult
	if err := workflow.ExecuteActivity(r.det, a.ApplyEdits, in).Get(r.ctx, &res); err != nil {
		r.rep.Rejected = append(r.rep.Rejected, Rejection{Step: in.Step, Reason: "apply failed: " + err.Error()})
		return 0, false
	}
	r.applied = append(r.applied, res.Applied...)
	r.rep.Rejected = append(r.rep.Rejected, res.Rejected...)
	for _, rej := range res.Rejected {
		if rej.Action == "delete" {
			r.rep.DeletionsRejected++
		}
		held = held || rej.Held
		if rej.Held && rej.Lost {
			for _, e := range slices.Backward(in.Edits) {
				if e.Path == rej.Path {
					r.held = append(r.held, heldEdit{Step: in.Step, Edit: e, After: len(r.applied)})
					break
				}
			}
		}
	}
	return len(res.Applied), held
}

const heldStepName = "held"

type heldEdit struct {
	Step  string
	Edit  Edit
	After int // len(run.applied) when the cap held it: a write at or after this index makes it stale
}

// landable is the held edits that can still land: no later write to their file (the edit was made against the file as
// it was), and for a file held more than once only the last.
func landable(held []heldEdit, applied []Applied) []heldEdit {
	var out []heldEdit
	for i, h := range held {
		stale := slices.ContainsFunc(applied[min(h.After, len(applied)):], func(ap Applied) bool { return ap.Path == h.Edit.Path })
		heldAgain := slices.ContainsFunc(held[i+1:], func(later heldEdit) bool { return later.Edit.Path == h.Edit.Path })
		if !stale && !heldAgain {
			out = append(out, h)
		}
	}
	return out
}

// landHeld gives the held edits of steps that judge only this run's proposals a second chance once the steps before
// verify have run: those landable take an equal share with verify of what the cap left, before check lints them.
// What still does not fit stays lost.
func (r *run) landHeld() {
	candidates := landable(r.held, r.applied)
	if len(candidates) == 0 {
		return
	}
	began := workflow.Now(r.ctx)
	changed := changedFiles(r.applied)
	in := ApplyInput{RunID: r.rep.RunID, Step: heldStepName, Remaining: max(MaxEdits-len(changed)+1, 0) / 2, Changed: changed}
	for _, h := range candidates {
		in.Edits = append(in.Edits, h.Edit)
	}
	var res ApplyResult
	if err := workflow.ExecuteActivity(r.det, a.ApplyEdits, in).Get(r.ctx, &res); err != nil {
		r.record(StepRecord{Name: heldStepName, Status: "failed", Reason: err.Error()}, began)
		return
	}
	r.applied = append(r.applied, res.Applied...)
	landed := map[string]bool{}
	for _, ap := range res.Applied {
		for _, h := range candidates {
			if h.Edit.Path == ap.Path {
				landed[h.Step+"\x00"+ap.Path] = true
			}
		}
	}
	for i := len(r.rep.Rejected) - 1; i >= 0; i-- { // the latest record of a file is the edit that landed
		rej := &r.rep.Rejected[i]
		if key := rej.Step + "\x00" + rej.Path; rej.Lost && landed[key] {
			rej.Lost, rej.Reason = false, rej.Reason+"; landed after summarise in what the cap left"
			delete(landed, key)
		}
	}
	r.record(StepRecord{Name: heldStepName, Status: "ok", Edits: len(res.Applied), Reason: fmt.Sprintf(
		"%d of %d held edits landed (share %d new files)", len(res.Applied), len(candidates), in.Remaining)}, began)
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
	paths := changedFiles(r.applied)
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
