package dream

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"

	"saahil.local/brain/internal/llm"
)

// newBrain builds a synthetic brain with one session, one memory capture and every dream skill, wired to a fake LLM
// that writes one note per call (so every later step has input).
func newBrain(t *testing.T, costPerCall float64) (*Activities, *llm.Fake) {
	t.Helper()
	brain, skills := t.TempDir(), t.TempDir()
	write(t, filepath.Join(brain, "sessions/devbox/2026/10/2026-10-08-0000abcd-sample.md"), "---\ntype: session\n---\nran tests\n")
	write(t, filepath.Join(brain, "memory/feedback-sample.md"), "prefer table-driven tests\n")
	for _, s := range []string{"distil", "file", "link", "hubs", "summarise", "verify"} {
		write(t, filepath.Join(skills, s+".md"), "# skill "+s+"\n")
	}
	fake := &llm.Fake{CostUSD: costPerCall, Respond: func(c llm.Call) llm.Result {
		name := strings.TrimPrefix(strings.SplitN(c.Skill, "\n", 2)[0], "# skill ")
		out, _ := json.Marshal(map[string]any{"edits": []Edit{{Path: "notes/finding-from-" + name + ".md",
			Action: "write", Content: "---\nid: finding-from-" + name + "\n---\nsynthetic\n"}}})
		return llm.Result{JSON: out}
	}}
	ledger, err := llm.OpenLedger(filepath.Join(brain, ".index", "brain.db"), filepath.Join(brain, ".data", "llm"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ledger.Close() })
	cfg := Config{BrainRoot: brain, SkillsDir: skills, BrainBin: filepath.Join(brain, "no-brain-cli")}
	return &Activities{Cfg: cfg, LLM: fake, Ledger: ledger}, fake
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := writeFile(path, []byte(content)); err != nil {
		t.Fatal(err)
	}
}

func runDream(t *testing.T, acts *Activities, in Input, start time.Time) Report {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetStartTime(start)
	env.RegisterWorkflow(DreamWorkflow)
	env.RegisterWorkflow(LearnSessionWorkflow)
	env.RegisterActivity(acts)
	env.ExecuteWorkflow(DreamWorkflow, in)
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow: completed=%v err=%v", env.IsWorkflowCompleted(), env.GetWorkflowError())
	}
	var rep Report
	if err := env.GetWorkflowResult(&rep); err != nil {
		t.Fatal(err)
	}
	return rep
}

func step(rep Report, name string) StepRecord {
	for _, s := range rep.Steps {
		if s.Name == name {
			return s
		}
	}
	return StepRecord{}
}

// TestDreamSkipsWhenNothingNew catches a dream that runs (and pays) every hour with nothing new — in particular one
// whose own writes to notes/ count as new material and wake the next dream.
func TestDreamSkipsWhenNothingNew(t *testing.T) {
	acts, fake := newBrain(t, 0.01)
	t0 := time.Now().UTC()
	first := runDream(t, acts, Input{}, t0)
	if first.Status != "completed" || first.Edits == 0 {
		t.Fatalf("first dream should run and write: %+v", first)
	}
	calls := fake.Calls()

	second := runDream(t, acts, Input{}, t0.Add(13*time.Hour))
	if second.Status != "skipped" || !strings.Contains(second.Skipped, "nothing new") || fake.Calls() != calls {
		t.Fatalf("second dream: status=%s skipped=%q calls %d→%d", second.Status, second.Skipped, calls, fake.Calls())
	}
	if _, err := os.Stat(filepath.Join(acts.Cfg.BrainRoot, ".brain", "dream-runs", second.RunID+".json")); err == nil {
		t.Fatal("a skipped dream wrote a run report")
	}
}

// TestDreamIsIdempotent catches a retry or rerun that pays twice or rewrites files: the same material must map to the
// same idempotency keys (answered from the ledger for $0) and the same content must be a no-op, not an edit.
func TestDreamIsIdempotent(t *testing.T) {
	acts, fake := newBrain(t, 0.01)
	t0 := time.Now().UTC()
	first := runDream(t, acts, Input{Force: true}, t0)
	if first.LLMCalls == 0 || first.Edits == 0 {
		t.Fatalf("first dream did nothing: %+v", first)
	}
	calls := fake.Calls()

	// Same inputs: forget the dream happened, so the gate offers exactly the same material again.
	if err := os.Remove(filepath.Join(acts.Cfg.BrainRoot, ".brain", "dream-state.json")); err != nil {
		t.Fatal(err)
	}
	second := runDream(t, acts, Input{Force: true}, t0.Add(time.Hour))
	if fake.Calls() != calls || second.LLMCalls != 0 || second.LLMCached != first.LLMCalls {
		t.Fatalf("replay made new LLM calls: fake %d→%d, report calls=%d cached=%d", calls, fake.Calls(),
			second.LLMCalls, second.LLMCached)
	}
	if second.Edits != 0 || second.CostUSD != 0 {
		t.Fatalf("replay edited %d files for $%.2f", second.Edits, second.CostUSD)
	}
}

// TestCostCapStopsLLMSteps catches a run that keeps calling the model past $8: once spent + the next step's budget
// would pass the cap, every later LLM step is skipped with the reason, and the deterministic steps still finish.
func TestCostCapStopsLLMSteps(t *testing.T) {
	// learn has nothing to judge in the sample session; file (haiku) → $3, link (opus) → $6, hubs would need $8.50
	acts, fake := newBrain(t, 3.0)
	rep := runDream(t, acts, Input{Force: true}, time.Now().UTC())
	if fake.Calls() != 2 || rep.CostUSD > RunCapUSD {
		t.Fatalf("calls=%d cost=$%.2f", fake.Calls(), rep.CostUSD)
	}
	for _, name := range []string{"curate.hubs", "verify"} {
		if s := step(rep, name); s.Status != "skipped" || !strings.Contains(s.Reason, "run cap") {
			t.Fatalf("%s: %+v", name, s)
		}
	}
	if rep.Status != "completed" || step(rep, "commit").Status != "ok" {
		t.Fatalf("deterministic steps did not finish: %+v", rep)
	}
}

// TestApplyEditsStaysInsideTheBrain catches the dream deleting a file or writing outside the brain's knowledge
// folders — above all into saahil-docs through a linked work/ folder — and checks each refusal is counted.
func TestApplyEditsStaysInsideTheBrain(t *testing.T) {
	acts, _ := newBrain(t, 0)
	shadow := t.TempDir()
	acts.Cfg.Forbidden = []string{shadow}
	if err := os.Symlink(shadow, filepath.Join(acts.Cfg.BrainRoot, "work")); err != nil {
		t.Fatal(err)
	}
	edits := []Edit{
		{Path: "memory/feedback-sample.md", Action: "delete"},
		{Path: "../outside.md", Action: "write", Content: "x"},
		{Path: "work/box/STATE.md", Action: "write", Content: "x"},
		{Path: ".index/brain.md", Action: "write", Content: "x"},
		{Path: "notes/finding-ok.md", Action: "write", Content: "ok"},
	}
	res, err := acts.ApplyEdits(t.Context(), ApplyInput{RunID: "r", Step: "file", Edits: edits, Remaining: MaxEdits})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 1 || res.Applied[0].Path != "notes/finding-ok.md" || len(res.Rejected) != 4 {
		t.Fatalf("applied=%+v rejected=%+v", res.Applied, res.Rejected)
	}
	if _, err := os.Stat(filepath.Join(acts.Cfg.BrainRoot, "memory/feedback-sample.md")); err != nil {
		t.Fatal("the delete went through")
	}
	if entries, _ := os.ReadDir(shadow); len(entries) != 0 {
		t.Fatal("wrote into the read-only root")
	}
}

// TestMaterialWaitsWhenItsStepDidNotRun catches a dream that advances a watermark past material its step never
// processed (skill missing, cost cap, failure): that material would be dropped instead of waiting for the next dream.
func TestMaterialWaitsWhenItsStepDidNotRun(t *testing.T) {
	acts, _ := newBrain(t, 0.01)
	skill := filepath.Join(acts.Cfg.SkillsDir, "file.md")
	if err := os.Rename(skill, skill+".off"); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now().UTC()
	first := runDream(t, acts, Input{Force: true}, t0)
	if s := step(first, "file"); s.Status != "skipped" || !strings.Contains(s.Reason, "skill missing") {
		t.Fatalf("file step: %+v", s)
	}
	if err := os.Rename(skill+".off", skill); err != nil {
		t.Fatal(err)
	}
	second := runDream(t, acts, Input{Force: true}, t0.Add(time.Hour))
	if second.Material.Captures != 1 || !step(second, "file").Called {
		t.Fatalf("the capture did not wait: material=%+v file=%+v", second.Material, step(second, "file"))
	}
}

// TestShadowDreamNeverWritesWork catches a shadow-mode dream writing a CARD or STATE into work/, which would make the
// cut-over's one-rename swap of saahil-docs into an empty work/ fail.
func TestShadowDreamNeverWritesWork(t *testing.T) {
	acts, _ := newBrain(t, 0)
	if err := os.MkdirAll(filepath.Join(acts.Cfg.BrainRoot, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	edit := []Edit{{Path: "work/box/CARD.md", Action: "write", Content: "card"}}
	res, err := acts.ApplyEdits(t.Context(), ApplyInput{RunID: "r", Step: "render", Edits: edit, Remaining: MaxEdits})
	if err != nil || len(res.Applied) != 0 {
		t.Fatalf("shadow dream wrote work/: applied=%+v err=%v", res.Applied, err)
	}
	if err := os.WriteFile(filepath.Join(acts.Cfg.BrainRoot, ".brain", "live"), nil, 0o644); err != nil {
		if err := os.MkdirAll(filepath.Join(acts.Cfg.BrainRoot, ".brain"), 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(acts.Cfg.BrainRoot, ".brain", "live"), nil, 0o644)
	}
	res, err = acts.ApplyEdits(t.Context(), ApplyInput{RunID: "r2", Step: "render", Edits: edit, Remaining: MaxEdits})
	if err != nil || len(res.Applied) != 1 {
		t.Fatalf("live dream could not write work/: applied=%+v rejected=%+v err=%v", res.Applied, res.Rejected, err)
	}
}
