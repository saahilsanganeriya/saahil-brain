package dream

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
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

// runDreamDeployed runs a forced dream with SkillStep under the LLMStep activity name, as brain-worker registers it.
func runDreamDeployed(t *testing.T, acts *Activities) Report {
	t.Helper()
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

// TestCostCapStopsLLMSteps catches a run that keeps calling the model past $8: a step whose spent + budget
// would pass the cap is skipped with the reason, and the deterministic steps still finish.
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

// pricedFake charges each skill its own price, so a test can place the run's spend exactly against the cap.
type pricedFake struct {
	llm.Fake
	price map[string]float64
}

func (f *pricedFake) Run(ctx context.Context, c llm.Call) (llm.Result, error) {
	res, err := f.Fake.Run(ctx, c)
	res.CostUSD = f.price[strings.TrimPrefix(strings.SplitN(c.Skill, "\n", 2)[0], "# skill ")]
	return res, err
}

// TestCapRefusalSkipsOnlyStepsThatCannotFit catches one step's refusal at the $8 cap skipping every later model step:
// curate.merge's $2.50 budget did not fit $2.07 left, and verify, whose batches need $1.00, was skipped with it, so a
// dream's 50 learn edits went unverified. A later step still runs when its smallest call fits what the run has left.
func TestCapRefusalSkipsOnlyStepsThatCannotFit(t *testing.T) {
	for _, tc := range []struct {
		name        string
		filePrice   float64 // file and link each cost this; hubs ($2.50) never fits after them
		verifyRuns  bool
		wantCostUSD string
	}{
		{"$2.00 left fits a verify batch", 3.00, true, "6.40"},
		{"$0.80 left fits no verify batch", 3.60, false, "7.20"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			brain, skills := t.TempDir(), t.TempDir()
			write(t, filepath.Join(brain, "memory/feedback-rebase.md"), "rebase before push\n")
			write(t, filepath.Join(brain, "hubs/index.md"), "---\nid: index\ntype: hub\nstatus: current\nsummary: home\n---\n")
			write(t, filepath.Join(brain, "hubs/git.md"), "---\nid: git\ntype: hub\nstatus: current\nsummary: git\nbroader: [index]\n---\n")
			for _, s := range []string{"distil", "file", "link", "hubs", "summarise", "verify"} {
				write(t, filepath.Join(skills, s+".md"), "# skill "+s+"\n")
			}
			fake := &pricedFake{price: map[string]float64{"file": tc.filePrice, "link": tc.filePrice, "verify": 0.40}}
			fake.Respond = func(c llm.Call) llm.Result {
				switch {
				case strings.HasPrefix(c.Skill, "# skill file"):
					return llm.Result{JSON: json.RawMessage(`{"ops":[{"op":"add_note","note":{"id":"lesson-rebase-before-push",
 "type":"lesson","status":"current","summary":"rebase before push","scope":"global","hubs":["git"],
 "evidence":["memory/feedback-rebase.md"],"body":"When pushing → rebase first, because CI ran on a stale base."}}]}`)}
				case strings.HasPrefix(c.Skill, "# skill verify"):
					return llm.Result{JSON: json.RawMessage(`{"ops":[],"verdicts":[{"id":"lesson-rebase-before-push","verdict":"keep"}]}`)}
				}
				return llm.Result{JSON: json.RawMessage(`{"ops":[]}`)}
			}
			ledger, err := llm.OpenLedger(filepath.Join(brain, ".index", "brain.db"), filepath.Join(brain, ".data", "llm"))
			if err != nil {
				t.Fatal(err)
			}
			defer ledger.Close()
			acts := &Activities{Cfg: Config{BrainRoot: brain, SkillsDir: skills, BrainBin: filepath.Join(brain, "none")},
				LLM: fake, Ledger: ledger}

			rep := runDreamDeployed(t, acts)

			if s := step(rep, "curate.hubs"); s.Status != "skipped" || !strings.Contains(s.Reason, "run cap") {
				t.Fatalf("curate.hubs should be refused at the cap: %+v", s)
			}
			v := step(rep, "verify")
			if ran := v.Status == "ok" && v.Called; ran != tc.verifyRuns {
				t.Fatalf("verify ran=%v, want %v: %+v", ran, tc.verifyRuns, v)
			}
			if !tc.verifyRuns && !strings.Contains(v.Reason, "run cap") {
				t.Fatalf("verify skipped without naming the cap: %+v", v)
			}
			if got := fmt.Sprintf("%.2f", rep.CostUSD); got != tc.wantCostUSD || rep.CostUSD > RunCapUSD {
				t.Fatalf("run cost $%s, want $%s (never over $%.2f)", got, tc.wantCostUSD, RunCapUSD)
			}
		})
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

// Catches the run's edit cap going first come, first served: on 2026-10-12 learn, file and curate.link filled the 50
// edits in all three dreams, so curate.hubs, curate.merge, curate.cards and verify landed nothing, run after run.
func TestCurationStepsShareTheEditCap(t *testing.T) {
	proposed := []struct {
		step string
		n    int
	}{{"learn", 10}, {"file", 19}, {"curate.link", 22}, {"curate.hubs", 7}, {"curate.merge", 5}, {"curate.cards", 3},
		{"verify", 5}}
	applied, landed := 0, map[string]int{}
	for _, p := range proposed {
		landed[p.step] = min(p.n, editBudget(p.step, applied))
		applied += landed[p.step]
	}
	if landed["learn"] != 10 || landed["file"] != 19 {
		t.Fatalf("learn and file consume their input and keep first claim: landed %v", landed)
	}
	for _, p := range proposed[2:] {
		if landed[p.step] == 0 {
			t.Errorf("%s landed 0 of %d edits: %v", p.step, p.n, landed)
		}
	}
	if applied > MaxEdits {
		t.Errorf("%d edits applied, cap %d", applied, MaxEdits)
	}
}

// Catches the run's edit cap counting a rewrite as a new edit: on 2026-10-14T23 and 2026-10-15T11 curate.link lost 15
// edits to its share, each a link on a note learn or file had written earlier in the same run, while those runs
// changed only 31 and 28 files of the 50 the cap allows.
func TestTheCapCountsFilesTheRunChanges(t *testing.T) {
	acts, _ := newBrain(t, 0)
	var applied []Applied
	for i := range 45 {
		applied = append(applied, Applied{Path: fmt.Sprintf("notes/finding-%02d.md", i%40), Created: true})
	}
	edits := []Edit{{Path: "notes/finding-00.md", Action: "write", Content: "linked"},
		{Path: "notes/finding-01.md", Action: "write", Content: "linked"},
		{Path: "notes/finding-new-1.md", Action: "write", Content: "n1"},
		{Path: "notes/finding-new-2.md", Action: "write", Content: "n2"},
		{Path: "notes/finding-new-3.md", Action: "write", Content: "n3"}}
	res, err := acts.ApplyEdits(t.Context(), applyInput("r", "curate.link", edits, applied))
	if err != nil {
		t.Fatal(err)
	}
	var landed []string
	for _, ap := range res.Applied {
		landed = append(landed, ap.Path)
	}
	// 40 files changed of 50: curate.link's share is ceil(10 / 5) = 2 new files, and the two rewrites cost nothing
	want := []string{"notes/finding-00.md", "notes/finding-01.md", "notes/finding-new-1.md", "notes/finding-new-2.md"}
	if !slices.Equal(landed, want) || len(res.Rejected) != 1 || res.Rejected[0].Path != "notes/finding-new-3.md" {
		t.Errorf("landed %v, rejected %+v; want %v and finding-new-3 held", landed, res.Rejected, want)
	}
}

// Catches an edit the run's cap holds back reading like any other rejection: brain metrics counts only edits marked
// held, and on 2026-10-12 every hub, merge, card and verify edit was held while every metric read clean.
func TestEditOverTheCapIsMarkedHeld(t *testing.T) {
	acts, _ := newBrain(t, 0)
	edits := []Edit{{Path: "notes/finding-a.md", Action: "write", Content: "a"},
		{Path: "notes/finding-b.md", Action: "write", Content: "b"}}
	res, err := acts.ApplyEdits(t.Context(), ApplyInput{RunID: "r", Step: "curate.hubs", Edits: edits, Remaining: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 1 || len(res.Rejected) != 1 || !res.Rejected[0].Held {
		t.Fatalf("applied=%+v rejected=%+v, want the second edit held by the cap", res.Applied, res.Rejected)
	}
}

// Catches a held edit's record claiming it returns: on 2026-10-14 both dreams logged curate.link edits as "run edit
// cap 50 reached; waits for the next dream" while each run left 13-14 edits unused, and curate.link judges only its own
// run's proposals, so those edits never came back. curate.merge rebuilds from the whole brain, so its held edits do.
func TestHeldEditSaysWhetherItReturns(t *testing.T) {
	acts, _ := newBrain(t, 0)
	for _, c := range []struct {
		step string
		lost bool
		fate string
	}{{"curate.link", true, "lost"}, {"verify", true, "lost"}, {"curate.merge", false, "waits for the next dream"}} {
		edits := []Edit{{Path: "notes/finding-a-" + c.step + ".md", Action: "write", Content: "a"},
			{Path: "notes/finding-b-" + c.step + ".md", Action: "write", Content: "b"}}
		res, err := acts.ApplyEdits(t.Context(), ApplyInput{RunID: "r", Step: c.step, Edits: edits, Remaining: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Rejected) != 1 {
			t.Fatalf("%s: rejected=%+v, want one held edit", c.step, res.Rejected)
		}
		rej := res.Rejected[0]
		if rej.Lost != c.lost || !strings.Contains(rej.Reason, c.fate) || !strings.Contains(rej.Reason, "share") {
			t.Errorf("%s: held edit recorded lost=%v %q, want lost=%v, its share and %q", c.step, rej.Lost, rej.Reason,
				c.lost, c.fate)
		}
	}
}

// Catches a unit larger than a whole run waiting for ever: held whole, a learn session that changes more files than
// the cap allows would stop learn at the same session in every dream, and no later session would be learned again.
func TestWholeUnitLargerThanTheCapAppliesWhatFits(t *testing.T) {
	acts, _ := newBrain(t, 0)
	var edits []Edit
	for i := range MaxEdits + 1 {
		edits = append(edits, Edit{Path: fmt.Sprintf("notes/finding-%02d.md", i), Action: "write", Content: "x"})
	}
	res, err := acts.ApplyEdits(t.Context(), ApplyInput{RunID: "r", Step: "learn", Edits: edits, Remaining: MaxEdits,
		Whole: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != MaxEdits || len(res.Rejected) != 1 || !res.Rejected[0].Lost {
		t.Fatalf("applied %d, rejected %+v; want %d applied and the last edit lost", len(res.Applied), res.Rejected, MaxEdits)
	}
}
