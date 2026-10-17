package dream

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	"saahil.local/brain/internal/llm"
)

// TestSkillStepFilesThroughDeltas catches a dream whose skill answers (deltas) never reach notes/ through the
// workflow's own ApplyEdits (backups, caps, revert), whose later steps cannot see the hubs a note may join, or whose
// refused delete goes uncounted, or whose other refused ops reach no step record (they showed only in the reason
// text, so "dream units refused" read 0) — with SkillStep wired in under the LLMStep activity name, as brain-worker does.
func TestSkillStepFilesThroughDeltas(t *testing.T) {
	brain, skills := t.TempDir(), t.TempDir()
	write(t, filepath.Join(brain, "memory/feedback-rebase.md"), "rebase before push\n")
	write(t, filepath.Join(brain, "hubs/index.md"), "---\nid: index\ntype: hub\nstatus: current\nsummary: home\n---\n")
	write(t, filepath.Join(brain, "hubs/git.md"), "---\nid: git\ntype: hub\nstatus: current\nsummary: git\nbroader: [index]\n---\n")
	for _, s := range []string{"distil", "file", "link", "hubs", "summarise", "verify"} {
		write(t, filepath.Join(skills, s+".md"), "# skill "+s+"\n")
	}
	var fileInput string
	fake := &llm.Fake{Respond: func(c llm.Call) llm.Result {
		if !strings.HasPrefix(c.Skill, "# skill file") {
			return llm.Result{JSON: json.RawMessage(`{"ops":[]}`)}
		}
		fileInput = c.Input
		return llm.Result{JSON: json.RawMessage(`{"ops":[
 {"op":"add_note","note":{"id":"lesson-rebase-before-push","type":"lesson","status":"current","summary":"rebase before push",
  "scope":"global","hubs":["git"],"evidence":["memory/feedback-rebase.md"],"sources":["memory/feedback-rebase.md"],
  "body":"When pushing → rebase first, because CI ran on a stale base."}},
 {"op":"add_note","note":{"id":"lesson-rebase-twice","type":"lesson","status":"current","summary":"rebase twice",
  "scope":"global","hubs":["git"],"evidence":["memory/feedback-rebase.md"],"body":"` + strings.Repeat("x", 301) + `"}},
 {"op":"delete","id":"lesson-rebase-before-push"}]}`)}
	}}
	ledger, err := llm.OpenLedger(filepath.Join(brain, ".index", "brain.db"), filepath.Join(brain, ".data", "llm"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	acts := &Activities{Cfg: Config{BrainRoot: brain, SkillsDir: skills, BrainBin: filepath.Join(brain, "none")},
		LLM: fake, Ledger: ledger}

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetStartTime(time.Now().UTC())
	env.RegisterWorkflow(DreamWorkflow)
	env.RegisterActivity(acts)
	env.OnActivity(acts.LLMStep, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, in LLMStepInput) (LLMStepResult, error) { return acts.SkillStep(ctx, in) })
	env.ExecuteWorkflow(DreamWorkflow, Input{Force: true})
	var rep Report
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if err := env.GetWorkflowResult(&rep); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(fileInput, `"hubs":[{"id":"git"`) {
		t.Errorf("the file skill was not given the hub list: %s", fileInput)
	}
	if rep.DeletionsRejected != 1 || rep.Edits != 1 || rep.EditedPaths[0] != "notes/lesson-rebase-before-push.md" {
		t.Fatalf("report: edits %d %v, deletions rejected %d, steps %+v", rep.Edits, rep.EditedPaths,
			rep.DeletionsRejected, rep.Steps)
	}
	for _, st := range rep.Steps {
		if st.Name == "file" && st.Refused != 1 {
			t.Errorf("file step refused %d ops, want 1 (the lesson over its cap): %+v", st.Refused, st)
		}
	}
	b, err := os.ReadFile(filepath.Join(brain, "notes/lesson-rebase-before-push.md"))
	if err != nil || !strings.Contains(string(b), "hubs: [git]") {
		t.Fatalf("note not written by the delta: %v\n%s", err, b)
	}
}

// TestSkillStepRetriesABrokenAnswer catches a skill answer that is not a delta being kept in the ledger as done: every
// later dream would then get the same broken answer for $0, fail the step, and never advance past that material.
func TestSkillStepRetriesABrokenAnswer(t *testing.T) {
	brain, skills := t.TempDir(), t.TempDir()
	write(t, filepath.Join(brain, "memory/feedback-x.md"), "x\n")
	write(t, filepath.Join(skills, "file.md"), "# skill file\n")
	fake := &llm.Fake{Respond: func(llm.Call) llm.Result { return llm.Result{Text: "not json"} }}
	ledger, err := llm.OpenLedger(filepath.Join(brain, ".index", "brain.db"), filepath.Join(brain, ".data", "llm"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	acts := &Activities{Cfg: Config{BrainRoot: brain, SkillsDir: skills}, LLM: fake, Ledger: ledger}
	in := LLMStepInput{Workflow: "dream", Step: "file", Skill: "file", Model: "m", BudgetUSD: 0.5,
		Material: []Material{{Rel: "memory/feedback-x.md", Abs: filepath.Join(brain, "memory/feedback-x.md")}}}
	for i := 0; i < 2; i++ {
		out, err := acts.SkillStep(context.Background(), in)
		if err != nil || out.Status != "failed" || out.Cached {
			t.Fatalf("attempt %d: %+v %v", i, out, err)
		}
	}
	if fake.Calls() != 2 {
		t.Fatalf("model asked %d times; a broken answer must not be served from the ledger", fake.Calls())
	}
}

// TestVerifyJudgesClaimsInBatches catches verify sending every claim of a dream in one call: on 2026-10-17 a dream
// with 40 changed claims overran that call's $2.50 ceiling and lost every verdict, and one with 57 was skipped
// because $5.72 spent + $2.50 did not fit the $8 run cap. Each batch holds at most verifyBatchNotes claims (their
// latest version only, no hub pages) with the evidence they cite, and its ceiling is what the run cap leaves.
func TestVerifyJudgesClaimsInBatches(t *testing.T) {
	brain, skills := t.TempDir(), t.TempDir()
	write(t, filepath.Join(skills, "verify.md"), "# skill verify\n")
	var proposed []Edit
	note := func(i int, summary string) Edit {
		id := fmt.Sprintf("finding-claim-%02d", i)
		ev := fmt.Sprintf("work/box/reports/r%02d.md", i)
		return Edit{Path: "notes/" + id + ".md", Action: "write", Content: "---\nid: " + id +
			"\ntype: finding\nstatus: current\nsummary: " + summary + "\nscope: hub\nhubs: [git]\nevidence: [" + ev +
			"]\n---\nbody\n"}
	}
	for i := 0; i < 25; i++ {
		write(t, filepath.Join(brain, fmt.Sprintf("work/box/reports/r%02d.md", i)), fmt.Sprintf("report %02d\n", i))
		proposed = append(proposed, note(i, "first version"))
	}
	proposed = append(proposed, note(3, "latest version"),
		Edit{Path: "hubs/git.md", Action: "write", Content: "---\nid: git\ntype: hub\nstatus: current\nsummary: git\n---\n"})

	var inputs []skillPayload
	var ceilings []float64
	fake := &llm.Fake{CostUSD: 0.8, Respond: func(c llm.Call) llm.Result {
		var p skillPayload
		if err := json.Unmarshal([]byte(c.Input), &p); err != nil {
			t.Fatal(err)
		}
		inputs, ceilings = append(inputs, p), append(ceilings, c.MaxBudgetUSD)
		var verdicts []string
		for _, e := range p.Proposed {
			verdicts = append(verdicts, `{"id":"`+strings.TrimSuffix(filepath.Base(e.Path), ".md")+`","verdict":"keep"}`)
		}
		return llm.Result{JSON: json.RawMessage(`{"ops":[],"verdicts":[` + strings.Join(verdicts, ",") + `]}`)}
	}}
	ledger, err := llm.OpenLedger(filepath.Join(brain, ".index", "brain.db"), filepath.Join(brain, ".data", "llm"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	acts := &Activities{Cfg: Config{BrainRoot: brain, SkillsDir: skills}, LLM: fake, Ledger: ledger}
	in := LLMStepInput{Workflow: "dream", Step: "verify", Skill: "verify", Model: "m", BudgetUSD: 2.5,
		RunSpentUSD: 4.0, Proposed: proposed}

	out, err := acts.SkillStep(context.Background(), in)
	if err != nil || out.Status != "ok" {
		t.Fatalf("%+v %v", out, err)
	}
	if len(inputs) != 3 || !strings.Contains(out.Reason, "25 verdicts") || fmt.Sprintf("%.2f", out.CostUSD) != "2.40" {
		t.Fatalf("%d calls, cost %.2f, reason %q; want 3 batches of ≤ %d claims, 25 verdicts, $2.40", len(inputs),
			out.CostUSD, out.Reason, verifyBatchNotes)
	}
	for i, p := range inputs {
		if len(p.Proposed) > verifyBatchNotes || len(p.Evidence) != len(p.Proposed) {
			t.Errorf("batch %d: %d notes, %d evidence files", i, len(p.Proposed), len(p.Evidence))
		}
		for _, e := range p.Proposed {
			if e.Path == "hubs/git.md" || strings.Contains(e.Content, "first version") && e.Path == "notes/finding-claim-03.md" {
				t.Errorf("batch %d was sent %s (a hub page or a stale version)", i, e.Path)
			}
		}
	}
	if want := []float64{2.5, 2.5, 2.4}; fmt.Sprint(roundAll(ceilings)) != fmt.Sprint(want) {
		t.Errorf("ceilings %v, want %v (the step budget, then what the $8 run cap leaves after $4.00 + 2 × $0.80)",
			ceilings, want)
	}

	in.RunSpentUSD = 6.5
	fake2 := &llm.Fake{CostUSD: 0.8, Respond: fake.Respond}
	inputs, ceilings, acts.LLM = nil, nil, fake2
	in.Proposed = nil
	for i := 0; i < 25; i++ {
		in.Proposed = append(in.Proposed, note(i, "changed again")) // new keys: nothing comes from the ledger
	}
	out, err = acts.SkillStep(context.Background(), in)
	if err != nil || out.Status != "ok" || !out.StopLLM || !strings.Contains(out.Reason, "17 of 25 claims unverified: run cap") {
		t.Fatalf("$6.50 spent: %+v %v; want the first batch judged and the rest named as unverified", out, err)
	}
}

func roundAll(xs []float64) []float64 {
	for i, x := range xs {
		xs[i] = float64(int(x*100+0.5)) / 100
	}
	return xs
}
