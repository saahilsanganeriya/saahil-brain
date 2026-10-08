package dream

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"

	"saahil.local/brain/internal/evals"
)

// TestEvalWorkflowMeasuresAFreshBrain catches a weekly eval that runs on stale or missing sets (it must index the
// brain and rebuild them) or that finishes without writing its reports.
func TestEvalWorkflowMeasuresAFreshBrain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BRAIN_HOME", home)
	brain := filepath.Join(home, "brain")
	write(t, filepath.Join(brain, "notes/decision-ember-queue.md"), "---\nid: decision-ember-queue\ntype: decision\n"+
		"status: current\nsummary: Ember jobs share one queue with a fixed width of four\nhubs: [index]\n---\nBody.\n")
	acts := &Activities{Cfg: Config{BrainRoot: brain}}

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(EvalWorkflow)
	env.RegisterActivity(acts)
	env.ExecuteWorkflow(EvalWorkflow, EvalInput{})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var out EvalSummary
	if err := env.GetWorkflowResult(&out); err != nil {
		t.Fatal(err)
	}
	answerable := -1
	for _, m := range out.Metrics {
		if m.Eval == "qa" && m.Name == "recall@3 (brain_search)" {
			answerable = m.All.N
		}
	}
	if answerable != 1 {
		t.Fatalf("qa recall measured over %d questions, want the one note's: %+v", answerable, out)
	}
	for _, name := range evals.Names {
		if _, err := os.Stat(filepath.Join(out.Reports, name+".md")); err != nil {
			t.Errorf("report %s: %v", name, err)
		}
	}
	if day := time.Now().UTC().Format(time.DateOnly); filepath.Base(out.Reports) != day {
		t.Errorf("reports in %s, want a folder for %s", out.Reports, day)
	}
}
