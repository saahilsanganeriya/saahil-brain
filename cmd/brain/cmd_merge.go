package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/delta"
	"saahil.local/brain/internal/dream"
	"saahil.local/brain/internal/llm"
)

func init() {
	register("dream merge", "dry run of the dream's curate.merge: what it would merge, narrow, supersede and ask; "+
		"never applies", cmdDreamMerge)
}

type mergeDryOptions struct {
	budgetUSD, capUSD float64
	ledgerDir         string // outside the brain; "" = a temp dir removed afterwards
	findAnswer        string // replay a recorded answer instead of calling the model
	bodiesAnswer      string
	out               string // the delta as JSON, for brain apply-delta
	today             string
}

func cmdDreamMerge(args []string) int {
	fs := flag.NewFlagSet("dream merge", flag.ContinueOnError)
	var o mergeDryOptions
	fs.Float64Var(&o.budgetUSD, "budget", 1.0, "USD ceiling per model call (--max-budget-usd)")
	fs.Float64Var(&o.capUSD, "cap", 2.0, "USD ceiling for the whole dry run")
	fs.StringVar(&o.ledgerDir, "ledger", "", "keep the ledger and answers in this folder outside the brain (a rerun is then $0)")
	fs.StringVar(&o.findAnswer, "find-answer", "", "replay this find answer (plan JSON or claude JSON output); no model call")
	fs.StringVar(&o.bodiesAnswer, "bodies-answer", "", "replay this bodies answer, with --find-answer")
	fs.StringVar(&o.out, "out", "", "write the delta here (outside the brain)")
	fs.StringVar(&o.today, "today", time.Now().UTC().Format("2006-01-02"), "the date on narrowing lines")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return dreamMergeDry(os.Stdout, config.BrainRoot(), o)
}

// dreamMergeDry runs the merge step's activities without Temporal and prints what the step would do; it writes
// nothing under the brain: the ledger lives outside it, no edit is applied, no owner question is queued.
func dreamMergeDry(w io.Writer, root string, o mergeDryOptions) int {
	for _, p := range []string{o.ledgerDir, o.out} {
		if p != "" && inside(p, root) {
			return fail("%s is inside the brain; a dry run writes nothing there", p)
		}
	}
	dir := o.ledgerDir
	if dir == "" {
		tmp, err := os.MkdirTemp("", "brain-merge-dry-")
		if err != nil {
			return fail("%v", err)
		}
		defer os.RemoveAll(tmp)
		dir = tmp
	}
	ledger, err := llm.OpenLedger(filepath.Join(dir, "brain.db"), filepath.Join(dir, "llm"))
	if err != nil {
		return fail("%v", err)
	}
	defer ledger.Close()
	var runner llm.Runner = llm.DefaultClaude()
	if o.findAnswer != "" {
		r, err := newReplayRunner(o.findAnswer, o.bodiesAnswer)
		if err != nil {
			return fail("%v", err)
		}
		runner = r
	}
	acts := &dream.Activities{LLM: runner, Ledger: ledger, Cfg: dream.Config{BrainRoot: root}}
	ctx := context.Background()

	fmt.Fprintf(w, "brain dream merge — dry run on %s; nothing is applied\n", root)
	gate, err := acts.MergeGate(ctx, time.Now().UTC())
	if err != nil {
		return fail("gate: %v", err)
	}
	verdict := "would run"
	if gate.Skip != "" {
		verdict = "would skip (" + gate.Skip + "); running anyway"
	}
	fmt.Fprintf(w, "gate: %s; %d current global notes\n", verdict, gate.Notes)

	res, err := acts.MergeStep(ctx, dream.MergeStepInput{BudgetUSD: o.budgetUSD, CapUSD: o.capUSD, Today: o.today})
	fmt.Fprintf(w, "model: %d calls, %d from the ledger, $%.2f; find %s %s\n", res.Calls, res.Cached, res.CostUSD,
		res.Status, res.Reason)
	if err != nil {
		return fail("merge: %v", err)
	}
	if res.Status != "ok" {
		return 1
	}
	p := res.Plan
	fmt.Fprintf(w, "plan (after code's checks): %d duplicate groups, %d conflicts, %d stale\n", len(p.Duplicates),
		len(p.Conflicts), len(p.Stale))
	for _, g := range p.Duplicates {
		fmt.Fprintf(w, "  duplicate: keep %s, supersede %s — %s\n", g.Keep, strings.Join(g.Supersede, ", "), g.Why)
	}
	for _, c := range p.Conflicts {
		fmt.Fprintf(w, "  conflict %s: %s over %s — %s\n", c.Resolution, c.Newer, c.Older, c.Why)
	}
	for _, s := range p.Stale {
		fmt.Fprintf(w, "  stale: %s replaced by %s — %s\n", s.ID, s.ReplacedBy, s.Why)
	}
	fmt.Fprintf(w, "would change %d files:\n", len(res.Edits))
	for _, e := range res.Edits {
		old, _ := os.ReadFile(filepath.Join(root, e.Path))
		was, _ := delta.Parse(old)
		now, _ := delta.Parse([]byte(e.Content))
		fmt.Fprintf(w, "  %s: %s\n", e.Path, describeChange(was, now))
	}
	fmt.Fprintf(w, "owner questions it would queue (pairs asked before are skipped): %d\n", len(res.Owner))
	for _, q := range res.Owner {
		fmt.Fprintf(w, "  %s\n", q.Question)
	}
	for _, r := range res.Refused {
		fmt.Fprintf(w, "refused by the applier: %s\n", r)
	}
	if !res.Complete {
		fmt.Fprintf(w, "incomplete: %s\n", res.Reason)
	}
	if o.out != "" {
		b, _ := json.MarshalIndent(res.Delta, "", " ")
		if err := os.WriteFile(o.out, append(b, '\n'), 0o644); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(w, "delta (%d ops) written to %s\n", len(res.Delta.Ops), o.out)
	}
	return 0
}

func describeChange(was, now delta.Note) string {
	var parts []string
	if was.Status != now.Status {
		parts = append(parts, "status "+was.Status+" → "+now.Status)
	}
	for _, id := range now.Supersedes {
		if !slices.Contains(was.Supersedes, id) {
			parts = append(parts, "supersedes "+id)
		}
	}
	for _, r := range now.Related {
		if !slices.ContainsFunc(was.Related, func(x delta.Related) bool { return x.ID == r.ID }) {
			parts = append(parts, "related "+r.ID)
		}
	}
	switch {
	case was.Body == now.Body:
	case strings.HasPrefix(now.Body, strings.TrimRight(was.Body, "\n")):
		parts = append(parts, fmt.Sprintf("appended %q", strings.TrimSpace(strings.TrimPrefix(now.Body, strings.TrimRight(was.Body, "\n")))))
	default:
		parts = append(parts, fmt.Sprintf("body replaced (%d → %d bytes)", len(was.Body), len(now.Body)))
	}
	return strings.Join(parts, "; ")
}

func inside(path, root string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return true
	}
	for _, r := range []string{root, config.Canonical(root)} {
		if real, err := filepath.EvalSymlinks(r); err == nil {
			r = real
		}
		if abs == r || strings.HasPrefix(abs, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// replayRunner answers each stage from a recorded answer file, so tonight's manual sweep can be replayed through the
// step's code with no model.
type replayRunner struct{ find, bodies json.RawMessage }

func newReplayRunner(findPath, bodiesPath string) (*replayRunner, error) {
	r := &replayRunner{}
	var err error
	if r.find, err = readAnswer(findPath); err != nil {
		return nil, err
	}
	if bodiesPath != "" {
		r.bodies, err = readAnswer(bodiesPath)
	}
	return r, err
}

// readAnswer takes either the answer JSON itself or claude's --output-format json envelope around it.
func readAnswer(path string) (json.RawMessage, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var env struct {
		Result           *string         `json:"result"`
		StructuredOutput json.RawMessage `json:"structured_output"`
	}
	if json.Unmarshal(b, &env) == nil {
		switch {
		case len(env.StructuredOutput) > 0 && string(env.StructuredOutput) != "null":
			return env.StructuredOutput, nil
		case env.Result != nil:
			return json.RawMessage(*env.Result), nil
		}
	}
	return b, nil
}

func (r *replayRunner) Run(_ context.Context, c llm.Call) (llm.Result, error) {
	var in struct{ Stage string }
	_ = json.Unmarshal([]byte(c.Input), &in)
	switch {
	case in.Stage == "find":
		return llm.Result{JSON: r.find}, nil
	case in.Stage == "bodies" && r.bodies != nil:
		return llm.Result{JSON: r.bodies}, nil
	}
	return llm.Result{}, errors.New("no recorded answer for stage " + in.Stage)
}
