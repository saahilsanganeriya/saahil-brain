package dream

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/workflow"

	"saahil.local/brain/internal/backfill"
	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/delta"
	"saahil.local/brain/internal/llm"
)

// LearnSessionWorkflow learns from one session (DESIGN §6): signals by code, a Haiku judge when there is something to
// judge, and an Opus reflect (distil) only when the owner corrected the agent or the session failed.

type LearnInput struct {
	Session     string  `json:"session"` // brain-relative session note
	RunSpentUSD float64 `json:"run_spent_usd"`
	Apply       bool    `json:"apply"` // `brain learn`: apply the edits here; under the dream, the dream applies them
}

type Signals struct {
	SessionID    string   `json:"session_id"`
	Corrections  int      `json:"corrections"`
	ToolErrors   int      `json:"tool_errors"`
	TestRuns     int      `json:"test_runs"`
	TestsFixed   bool     `json:"tests_fixed"`   // a failing test run later passed
	TestsFailing bool     `json:"tests_failing"` // the last test run failed
	PRs          []string `json:"prs,omitempty"`
	Shown        []string `json:"shown,omitempty"`  // pointers the prompt hook showed
	Opened       []string `json:"opened,omitempty"` // pointers the agent then opened
}

func (s Signals) worthJudging() bool {
	return len(s.Shown) > 0 || s.ToolErrors > 0 || s.TestRuns > 0 || s.Corrections > 0
}

type LearnResult struct {
	Session   string  `json:"session"`
	Signals   Signals `json:"signals"`
	Outcome   string  `json:"outcome,omitempty"` // success | partial | failure; "" when nothing needed judging
	Reflected bool    `json:"reflected"`
	Edits     []Edit  `json:"edits,omitempty"`
	Applied   int     `json:"applied"`
	CostUSD   float64 `json:"cost_usd"`
	Calls     int     `json:"calls"`
	Cached    int     `json:"cached"`
	StopLLM   bool    `json:"stop_llm,omitempty"`
	Reason    string  `json:"reason,omitempty"`
}

func (r *LearnResult) add(s LLMStepResult) {
	r.CostUSD += s.CostUSD
	if s.Called {
		r.Calls++
	}
	if s.Cached {
		r.Cached++
	}
	r.StopLLM = r.StopLLM || s.StopLLM
	if s.Status != "ok" {
		r.Reason = s.Reason
	}
}

type LearnCallInput struct {
	Session  string
	Signals  Signals
	Outcome  string
	SpentUSD float64
}

type LearnCallResult struct {
	LLMStepResult
	Outcome string
}

// LearnID names a session's learn run by the session's short id, the part of its note's name that never changes.
func LearnID(session string) string {
	base := strings.TrimSuffix(filepath.Base(session), ".md")
	if len(base) >= 19 {
		base = base[11:19]
	}
	return "learn/" + base
}

// llmContext puts a model call on its model's queue (one Opus at a time, two Haiku), with the LLM retry policy.
func llmContext(ctx workflow.Context, model string) workflow.Context {
	opts := llmOptions
	opts.TaskQueue = QueueHaiku
	if model == config.ModelJudgment {
		opts.TaskQueue = QueueOpus
	}
	return workflow.WithActivityOptions(ctx, opts)
}

func LearnSessionWorkflow(ctx workflow.Context, in LearnInput) (LearnResult, error) {
	det := workflow.WithActivityOptions(ctx, detOptions)
	out := LearnResult{Session: in.Session}
	if err := workflow.ExecuteActivity(det, a.LearnSignals, in.Session).Get(ctx, &out.Signals); err != nil {
		return out, err
	}
	if out.Signals.worthJudging() {
		var j LearnCallResult
		err := workflow.ExecuteActivity(llmContext(ctx, config.ModelBulk), a.LearnJudge,
			LearnCallInput{Session: in.Session, Signals: out.Signals, SpentUSD: in.RunSpentUSD}).Get(ctx, &j)
		if err != nil {
			return out, err
		}
		out.add(j.LLMStepResult)
		if out.Outcome = j.Outcome; j.Status != "ok" {
			return out, nil
		}
	}
	if out.Signals.Corrections == 0 && out.Outcome != "failure" && !out.Signals.TestsFailing {
		return out, nil
	}
	var r LearnCallResult
	err := workflow.ExecuteActivity(llmContext(ctx, config.ModelJudgment), a.LearnReflect, LearnCallInput{
		Session: in.Session, Signals: out.Signals, Outcome: out.Outcome, SpentUSD: in.RunSpentUSD + out.CostUSD}).Get(ctx, &r)
	if err != nil {
		return out, err
	}
	out.add(r.LLMStepResult)
	out.Reflected, out.Edits = r.Status == "ok", r.Edits
	if in.Apply && len(out.Edits) > 0 {
		var res ApplyResult
		id := "learn-" + runID(workflow.Now(ctx).UTC(), workflow.GetInfo(ctx).WorkflowExecution.RunID)
		if err := workflow.ExecuteActivity(det, a.ApplyEdits, ApplyInput{RunID: id, Step: "learn", Edits: out.Edits,
			Remaining: MaxEdits}).Get(ctx, &res); err != nil {
			return out, err
		}
		out.Applied = len(res.Applied)
	}
	return out, nil
}

// learnSessions is the dream's learn step: one LearnSessionWorkflow per new session, in turn so the $8 cap holds.
// A session whose child could not run goes to the batched distil call, the path the dream had before.
func (r *run) learnSessions(sessions []Material) {
	began := workflow.Now(r.ctx)
	if len(sessions) == 0 || r.stopLLM != "" {
		r.llm(learnStep, sessions)
		return
	}
	rec := StepRecord{Name: learnStep.name, Status: "ok"}
	var fallback []Material
	done := 0
	for _, m := range sessions {
		if r.stopLLM != "" {
			break
		}
		cctx := workflow.WithChildOptions(r.ctx, workflow.ChildWorkflowOptions{WorkflowID: LearnID(m.Rel),
			WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE})
		var out LearnResult
		err := workflow.ExecuteChildWorkflow(cctx, LearnSessionWorkflow, LearnInput{Session: m.Rel,
			RunSpentUSD: r.rep.CostUSD}).Get(r.ctx, &out)
		if err != nil {
			fallback = append(fallback, m)
			continue
		}
		done++
		r.rep.CostUSD, r.rep.LLMCalls, r.rep.LLMCached = r.rep.CostUSD+out.CostUSD, r.rep.LLMCalls+out.Calls, r.rep.LLMCached+out.Cached
		rec.CostUSD += out.CostUSD
		rec.Called, rec.Cached = rec.Called || out.Calls > 0, rec.Cached || out.Cached > 0
		if out.StopLLM {
			r.stopLLM = out.Reason
		}
		if len(out.Edits) > 0 {
			r.proposed = append(r.proposed, out.Edits...)
			rec.Edits += r.apply(learnStep.name, out.Edits)
		}
	}
	rec.Reason = strconv.Itoa(done) + " sessions learned one by one"
	if r.stopLLM != "" {
		rec.Status, rec.Reason = "skipped", rec.Reason+"; "+r.stopLLM
	}
	r.record(rec, began)
	if len(fallback) > 0 {
		r.llm(llmStep{"learn.batched", learnStep.skill, learnStep.model, learnStep.area}, fallback)
		return
	}
	if r.stopLLM == "" {
		r.consumed[learnStep.area] = true
	}
}

// ---- activities ----

var testCommand = regexp.MustCompile(`\b(go test|gotest|pytest|npm test|pnpm test|yarn test|cargo test|make test)\b`)

// LearnSignals reads what code can tell about a session: its note's header, the tool errors and test runs in its
// transcript, and the pointers the hooks showed and the agent opened (lookups). Nothing here is stored or logged.
func (a *Activities) LearnSignals(_ context.Context, session string) (Signals, error) {
	s, err := backfill.ReadSession(a.Cfg.BrainRoot, session)
	if err != nil {
		return Signals{}, err
	}
	sig := Signals{SessionID: s.SessionID, Corrections: s.Corrections, PRs: s.PRs}
	if f, err := os.Open(s.Transcript); err == nil {
		scanTranscript(f, &sig)
		f.Close()
	}
	db, err := sql.Open("sqlite", "file:"+a.path(".index", "brain.db")+"?_pragma=busy_timeout(5000)&mode=ro")
	if err != nil {
		return sig, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT hook, candidates, chosen FROM lookups WHERE session = ? ORDER BY time`, s.SessionID)
	if err != nil {
		return sig, nil // no index yet: no pointers to judge
	}
	defer rows.Close()
	for rows.Next() {
		var hook, cands, chosen string
		if rows.Scan(&hook, &cands, &chosen) != nil {
			continue
		}
		var ids, fields []string
		_ = json.Unmarshal([]byte(chosen), &ids)
		_ = json.Unmarshal([]byte(cands), &fields)
		switch {
		case hook == "prompt":
			sig.Shown = appendNew(sig.Shown, ids...)
		case hook == "post" && len(fields) == 1 && (fields[0] == "opened" || fields[0] == "followed"):
			sig.Opened = appendNew(sig.Opened, ids...)
		}
	}
	return sig, rows.Err()
}

func scanTranscript(r io.Reader, sig *Signals) {
	type block struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		ToolUseID string          `json:"tool_use_id"`
		IsError   bool            `json:"is_error"`
		Input     json.RawMessage `json:"input"`
		Content   json.RawMessage `json:"content"`
	}
	tests := map[string]bool{}
	failedOnce := false
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		var entry struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		var blocks []block
		if json.Unmarshal(line, &entry) == nil && json.Unmarshal(entry.Message.Content, &blocks) == nil {
			for _, b := range blocks {
				switch {
				case b.Type == "tool_use" && testCommand.Match(b.Input):
					tests[b.ID] = true
				case b.Type == "tool_result":
					failed := b.IsError || (tests[b.ToolUseID] && bytes.Contains(b.Content, []byte("FAIL")))
					if b.IsError {
						sig.ToolErrors++
					}
					if tests[b.ToolUseID] {
						sig.TestRuns++
						sig.TestsFixed = sig.TestsFixed || (failedOnce && !failed)
						failedOnce = failedOnce || failed
						sig.TestsFailing = failed
					}
				}
			}
		}
		if err != nil {
			return
		}
	}
}

func appendNew(list []string, items ...string) []string {
	for _, it := range items {
		if it != "" && !slices.Contains(list, it) {
			list = append(list, it)
		}
	}
	return list
}

const judgeSchema = `{"type":"object","required":["outcome","pointers"],"additionalProperties":false,"properties":{
"outcome":{"type":"string","enum":["success","partial","failure"]},
"pointers":{"type":"array","items":{"type":"object","required":["id","verdict"],"properties":{
"id":{"type":"string"},"verdict":{"type":"string","enum":["helpful","misled","unused"]},"why":{"type":"string"}}}},
"notes":{"type":"string"}}}`

func (a *Activities) sessionPayload(in LearnCallInput, step string) (map[string]any, error) {
	b, err := os.ReadFile(a.path(in.Session))
	if err != nil {
		return nil, err
	}
	f := payloadFile{Path: in.Session, Truncated: len(b) > fileContentCap}
	f.Content = string(b[:min(len(b), fileContentCap)])
	return map[string]any{"step": step, "material": []payloadFile{f}, "signals": in.Signals}, nil
}

// LearnJudge asks Haiku how the session went and which shown pointers helped or misled; the verdicts on pointers the
// hooks really showed become helpful/harmful counters, once per answer.
func (a *Activities) LearnJudge(ctx context.Context, in LearnCallInput) (LearnCallResult, error) {
	skill, err := a.readSkill("judge")
	if err != nil {
		return LearnCallResult{}, err
	}
	payload, err := a.sessionPayload(in, "judge")
	if err != nil {
		return LearnCallResult{}, err
	}
	input, _ := json.Marshal(payload)
	p := paid{workflow: "learn", step: "judge", model: config.ModelBulk, skill: string(skill), input: string(input),
		schema: judgeSchema, spentUSD: in.SpentUSD, capUSD: RunCapUSD}
	res, step, err := a.paidCall(ctx, p)
	out := LearnCallResult{LLMStepResult: step}
	if err != nil || step.Status != "ok" {
		return out, err
	}
	var j struct {
		Outcome  string `json:"outcome"`
		Pointers []struct{ ID, Verdict string }
	}
	if err := json.Unmarshal(answerJSON(res), &j); err != nil {
		out.Status, out.Reason = "failed", "answer is not the judge JSON"
		return out, nil
	}
	out.Outcome = j.Outcome
	var counters []delta.Counter
	for _, ptr := range j.Pointers {
		field := map[string]string{"helpful": "helpful", "misled": "harmful"}[ptr.Verdict]
		if field != "" && slices.Contains(in.Signals.Shown, ptr.ID) {
			counters = append(counters, delta.Counter{ID: ptr.ID, Field: field, Delta: 1})
		}
	}
	if len(counters) == 0 {
		return out, nil
	}
	stats, err := delta.OpenStats(a.path(".index", "brain.db"))
	if err != nil {
		return out, err
	}
	defer stats.Close()
	_, err = stats.AddCounters(llm.Key(p.workflow, p.step, p.model, p.skill, p.input), counters)
	return out, err
}

// LearnReflect runs distil (Opus) over the session note and its signals; the delta comes back as whole-file edits.
func (a *Activities) LearnReflect(ctx context.Context, in LearnCallInput) (LearnCallResult, error) {
	skill, err := a.readSkill("distil")
	if errors.Is(err, os.ErrNotExist) {
		return LearnCallResult{LLMStepResult: LLMStepResult{Status: "skipped", Reason: "skill missing: distil.md"}}, nil
	}
	if err != nil {
		return LearnCallResult{}, err
	}
	payload, err := a.sessionPayload(in, "learn")
	if err != nil {
		return LearnCallResult{}, err
	}
	hubs, _, err := a.hubSet()
	if err != nil {
		return LearnCallResult{}, err
	}
	payload["hubs"], payload["outcome"] = hubs, in.Outcome
	if fi, err := os.Stat(a.path(in.Session)); err == nil {
		payload["today"] = fi.ModTime().UTC().Format("2006-01-02")
	}
	input, _ := json.Marshal(payload)
	res, step, err := a.paidCall(ctx, paid{workflow: "learn", step: "reflect", model: config.ModelJudgment,
		skill: string(skill), input: string(input), schema: delta.Schema, spentUSD: in.SpentUSD, capUSD: RunCapUSD})
	out := LearnCallResult{LLMStepResult: step, Outcome: in.Outcome}
	if err != nil || step.Status != "ok" {
		return out, err
	}
	edits, _, why, err := a.deltaEdits(ctx, answerJSON(res), "learn")
	out.Edits = edits
	if len(edits) == 0 {
		out.Reason = why
	}
	return out, err
}
