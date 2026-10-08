package dream

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"saahil.local/brain/internal/backfill"
	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/delta"
	"saahil.local/brain/internal/filing"
	"saahil.local/brain/internal/llm"
)

// paid is one model call under a cap: the backfill's is its total in the ledger, a learn run's is the dream's $8.
type paid struct {
	workflow, step, model, skill, input, schema string
	spentUSD, capUSD                            float64
}

// paidCall answers a key from the ledger for $0, or pays for one call when the cap and the daily guard allow it.
func (a *Activities) paidCall(ctx context.Context, p paid) (llm.Result, LLMStepResult, error) {
	key := llm.Key(p.workflow, p.step, p.model, p.skill, p.input)
	if res, ok, err := a.Ledger.Done(key); err != nil || ok {
		return res, LLMStepResult{Status: "ok", Reason: "cached", Cached: true}, err
	}
	budget := stepBudgetUSD[p.model]
	if p.spentUSD+budget > p.capUSD {
		return llm.Result{}, LLMStepResult{Status: "skipped", StopLLM: true, Reason: fmt.Sprintf(
			"cost cap: $%.2f spent + $%.2f step budget > $%.2f", p.spentUSD, budget, p.capUSD)}, nil
	}
	now := time.Now().UTC()
	today, err := a.Ledger.SpentSince(now.Truncate(24 * time.Hour))
	if err != nil {
		return llm.Result{}, LLMStepResult{}, err
	}
	if today+budget > DailyCapUSD {
		return llm.Result{}, LLMStepResult{Status: "skipped", StopLLM: true, Reason: fmt.Sprintf(
			"daily cap: $%.2f spent today + $%.2f step budget > $%.2f", today, budget, DailyCapUSD)}, nil
	}
	if err := a.Ledger.Begin(key, p.workflow, p.step, p.model, now); err != nil {
		return llm.Result{}, LLMStepResult{}, err
	}
	res, runErr := a.LLM.Run(ctx, llm.Call{Skill: p.skill, Input: p.input, Model: p.model, MaxBudgetUSD: budget,
		JSONSchema: p.schema})
	out := LLMStepResult{Status: "ok", CostUSD: res.CostUSD, Called: true}
	var modelErr *llm.ModelError
	switch {
	case errors.As(runErr, &modelErr): // it cost money; a retry would pay again
		out.Status, out.Reason = "failed", modelErr.Error()
		return res, out, a.Ledger.Finish(key, res, "failed", time.Now())
	case runErr != nil:
		_ = a.Ledger.Finish(key, res, "failed", time.Now())
		return res, out, runErr
	}
	return res, out, a.Ledger.Finish(key, res, "done", time.Now())
}

func answerJSON(res llm.Result) []byte {
	if len(res.JSON) > 0 {
		return res.JSON
	}
	return []byte(res.Text)
}

// workflowSpent is what every call of a workflow has cost so far, failed attempts and earlier runs included.
func (a *Activities) workflowSpent(workflow string) (float64, error) {
	db, err := sql.Open("sqlite", "file:"+a.path(".index", "brain.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var spent float64
	err = db.QueryRow(`SELECT COALESCE(SUM(cost_usd), 0) FROM llm_calls WHERE workflow = ?`, workflow).Scan(&spent)
	return spent, err
}

func (a *Activities) shadowRoot() string {
	if len(a.Cfg.ShadowDocs) > 0 {
		return a.Cfg.ShadowDocs[0]
	}
	return ""
}

func (a *Activities) hubSet() ([]filing.HubEntry, map[string]bool, error) {
	hubs, err := filing.Hubs(a.Cfg.BrainRoot)
	set := map[string]bool{}
	for _, h := range hubs {
		set[h.ID] = true
	}
	return hubs, set, err
}

// ---- plan and report ----

type PlanInput struct {
	Boxes         []string
	Now           time.Time
	IncludePaused bool
}

type PlanResult struct {
	Live  bool
	Boxes []backfill.Planned
}

func (a *Activities) BackfillPlan(_ context.Context, in PlanInput) (PlanResult, error) {
	live, boxes, err := backfill.Plan(a.Cfg.BrainRoot, a.shadowRoot(), in.Boxes, in.Now, in.IncludePaused)
	return PlanResult{Live: live, Boxes: boxes}, err
}

func (a *Activities) WriteBackfillReport(_ context.Context, rep backfill.Report) error {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(backfill.ReportPath(a.Cfg.BrainRoot, rep.ID), b)
}

// ---- card ----

type BoxInput struct {
	RunID     string
	Box       backfill.Box
	Live      bool
	BudgetUSD float64
}

type BoxStepResult struct {
	LLMStepResult
	Card       string // brain-relative path written
	CardStatus string
	Notes      int
}

// BackfillCard asks the card skill for one box's CARD; an answer that breaks a rule is asked again once, with the
// reason in the input (a new key), and a second refusal writes nothing.
func (a *Activities) BackfillCard(ctx context.Context, in BoxInput) (BoxStepResult, error) {
	skill, err := a.readSkill("card")
	if err != nil {
		return BoxStepResult{}, err
	}
	hubs, hubIDs, err := a.hubSet()
	if err != nil {
		return BoxStepResult{}, err
	}
	payload := map[string]any{"step": "card", "hubs": hubs, "inventory": backfill.BuildInventory(a.Cfg.BrainRoot, in.Box)}
	var out BoxStepResult
	p := paid{workflow: "backfill", step: "card/" + in.Box.Name, model: config.ModelBulk, skill: string(skill),
		schema: backfill.AnswerSchema, capUSD: in.BudgetUSD}
	out.LLMStepResult, err = a.askTwice(ctx, payload, p, func(raw []byte) (string, error) {
		content, status, verr := backfill.RenderCard(raw, in.Box, hubIDs)
		if verr != nil {
			return verr.Error(), nil
		}
		out.CardStatus = status
		out.Card, err = a.writeCard(in, content)
		return "", err
	})
	return out, err
}

// writeCard stages the CARD in shadow or writes it into the box once live; a CARD it replaces is kept in .archive/.
func (a *Activities) writeCard(in BoxInput, content string) (string, error) {
	dst := backfill.CardPath(a.Cfg.BrainRoot, in.Box.Name, in.Live)
	rel, _ := filepath.Rel(a.Cfg.BrainRoot, dst)
	if err := a.insideBrain(filepath.Dir(dst)); err != nil {
		return "", err
	}
	old, err := os.ReadFile(dst)
	if err == nil && string(old) == content {
		return rel, nil
	}
	if err == nil {
		if err := writeFile(a.path(".archive", "backfill", in.RunID, rel), old); err != nil {
			return "", err
		}
	}
	return rel, writeFile(dst, []byte(content))
}

// insideBrain refuses a folder whose real location is outside the brain or inside a read-only root (saahil-docs).
func (a *Activities) insideBrain(dir string) error {
	for {
		if _, err := os.Lstat(dir); err == nil {
			break
		}
		dir = filepath.Dir(dir)
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	realRoot, err := filepath.EvalSymlinks(a.Cfg.BrainRoot)
	if err != nil {
		return err
	}
	forbidden := append([]string{a.shadowRoot()}, a.Cfg.Forbidden...)
	for _, f := range forbidden {
		if realF, err := filepath.EvalSymlinks(f); err == nil && f != "" && within(realDir, realF) {
			return fmt.Errorf("%s resolves into a read-only root", dir)
		}
	}
	if !within(realDir, realRoot) {
		return fmt.Errorf("%s resolves outside the brain root", dir)
	}
	return nil
}

// ---- campaign notes ----

const (
	campaignSessions = 8
	campaignInputCap = 160 * 1024
	campaignAsk      = "This work box is a finished campaign. Write at least one note on what was built and why " +
		"(a finding or decision; evidence → the box's docs as work/<box>/<file>#<heading>; PRs named in the body), " +
		"then the lessons, anti-patterns and playbooks the campaign teaches, each with evidence."
)

// BackfillCampaign runs distil over a finished box's key docs and session notes and applies the delta's notes through
// ApplyEdits (backups, no deletes). No note at all is asked again once, with the reason.
func (a *Activities) BackfillCampaign(ctx context.Context, in BoxInput) (BoxStepResult, error) {
	skill, err := a.readSkill("distil")
	if err != nil {
		return BoxStepResult{}, err
	}
	hubs, _, err := a.hubSet()
	if err != nil {
		return BoxStepResult{}, err
	}
	card, _ := os.ReadFile(backfill.CardPath(a.Cfg.BrainRoot, in.Box.Name, in.Live))
	var material []payloadFile
	total := 0
	add := func(rel, abs string) {
		b, err := os.ReadFile(abs)
		if err != nil || total >= campaignInputCap {
			return
		}
		f := payloadFile{Path: rel, Truncated: len(b) > fileContentCap}
		f.Content = string(b[:min(len(b), fileContentCap)])
		total += len(f.Content)
		material = append(material, f)
	}
	for _, k := range backfill.KeyFiles(string(card), in.Box.Name) {
		add("work/"+in.Box.Name+"/"+k, filepath.Join(in.Box.Abs, k))
	}
	for i, s := range backfill.BoxSessions(a.Cfg.BrainRoot, in.Box.Name) {
		if i < campaignSessions {
			add(s.Rel, a.path(s.Rel))
		}
	}
	payload := map[string]any{"step": "campaign", "today": in.Box.Newest.Format("2006-01-02"), "hubs": hubs,
		"ask": campaignAsk, "card": string(card), "material": material}
	var out BoxStepResult
	p := paid{workflow: "backfill", step: "campaign/" + in.Box.Name, model: config.ModelJudgment, skill: string(skill),
		schema: delta.Schema, capUSD: in.BudgetUSD}
	out.LLMStepResult, err = a.askTwice(ctx, payload, p, func(raw []byte) (string, error) {
		edits, unchanged, why, err := a.deltaEdits(ctx, raw, p.step)
		if err != nil {
			return "", err
		}
		applied, err := a.ApplyEdits(ctx, ApplyInput{RunID: "backfill-" + in.RunID, Step: "campaign", Edits: edits,
			Remaining: MaxEdits})
		for _, ap := range applied.Applied {
			if ap.Created {
				out.Notes++
			}
		}
		if err != nil || len(applied.Applied)+applied.Noop+unchanged > 0 {
			return "", err
		}
		return "no note was kept: " + why, nil
	})
	return out, err
}

// askTwice pays for an answer (under the backfill's ledger total) and lets accept judge it; a refused answer is asked
// again once with the reason in the input, which makes it a new key.
func (a *Activities) askTwice(ctx context.Context, payload map[string]any, p paid, accept func([]byte) (string, error)) (LLMStepResult, error) {
	var out LLMStepResult
	for attempt := 0; attempt < 2; attempt++ {
		input, _ := json.Marshal(payload)
		spent, err := a.workflowSpent(p.workflow)
		if err != nil {
			return out, err
		}
		p.input, p.spentUSD = string(input), spent
		res, step, err := a.paidCall(ctx, p)
		out.CostUSD += step.CostUSD
		out.Called, out.Cached = out.Called || step.Called, out.Cached || step.Cached
		out.Status, out.Reason, out.StopLLM = step.Status, step.Reason, step.StopLLM
		if err != nil || step.Status != "ok" {
			return out, err
		}
		refused, err := accept(answerJSON(res))
		if err != nil || refused == "" {
			return out, err
		}
		payload["rejected"] = refused
		out.Status, out.Reason = "failed", "refused twice: "+refused
	}
	return out, nil
}

// deltaEdits applies a skill's delta in dry run and returns the whole-file writes, so ApplyEdits does the writing,
// and how many ops found their note already as asked (a replayed answer).
func (a *Activities) deltaEdits(ctx context.Context, raw []byte, step string) ([]Edit, int, string, error) {
	var d delta.Delta
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, 0, "answer is not a delta", nil
	}
	rep, err := delta.Apply(ctx, a.Cfg.BrainRoot, d, delta.Options{DryRun: true, Now: time.Now(), WorkRoot: a.shadowRoot()})
	if err != nil {
		return nil, 0, "", err
	}
	var edits []Edit
	for _, f := range rep.Files {
		edits = append(edits, Edit{Path: f.Path, Action: "write", Content: f.Content, Why: step})
	}
	why := fmt.Sprintf("%d ops, none kept", len(d.Ops))
	if len(rep.Rejected) > 0 {
		why = fmt.Sprintf("op %d %s %s: %s", rep.Rejected[0].Index, rep.Rejected[0].Op, rep.Rejected[0].Target,
			rep.Rejected[0].Reason)
	}
	return edits, rep.Noop, why, nil
}
