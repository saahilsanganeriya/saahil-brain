package dream

import (
	"context"
	"encoding/json"
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
// refused delete goes uncounted — with SkillStep wired in under the LLMStep activity name, as brain-worker does.
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
