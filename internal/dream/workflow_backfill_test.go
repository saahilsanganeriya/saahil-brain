package dream

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"

	"saahil.local/brain/internal/backfill"
	"saahil.local/brain/internal/index"
	"saahil.local/brain/internal/llm"
)

// newBackfillBrain is a shadow brain (no .brain/live) whose saahil-docs sits where the index looks for it. "alpha"
// is a stale box whose STATE says merged (a finished campaign); the others are live. The fake answers the card
// skill with a CARD citing STATE.md, and distil with one finding about the box.
func newBackfillBrain(t *testing.T, costPerCall float64, boxes ...string) (*Activities, *llm.Fake, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("BRAIN_HOME", home)
	brain, shadow := filepath.Join(home, "brain"), filepath.Join(home, "Documents", "work-repo", "saahil-docs")
	write(t, filepath.Join(brain, "hubs", "tabular-lane.md"),
		"---\nid: tabular-lane\ntype: hub\nstatus: current\nsummary: the tabular lane\nbroader: [index]\n---\n")
	for _, b := range boxes {
		state, age := "Work in progress.\n", time.Hour
		if b == "alpha" {
			state, age = "Fix merged; nothing open.\n", 30*24*time.Hour
		}
		for _, f := range []string{"STATE.md", "plans/PLAN.md"} {
			p := filepath.Join(shadow, b, f)
			write(t, p, state)
			old := time.Now().Add(-age)
			if err := os.Chtimes(p, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	fake := &llm.Fake{CostUSD: costPerCall, Respond: func(c llm.Call) llm.Result {
		var in struct {
			Inventory struct{ Box string } `json:"inventory"`
			Card      string               `json:"card"`
		}
		_ = json.Unmarshal([]byte(c.Input), &in)
		if strings.HasPrefix(c.Skill, "# Skill: card") {
			status := map[bool]string{true: "done", false: "live"}[in.Inventory.Box == "alpha"]
			out, _ := json.Marshal(map[string]any{"card": map[string]any{"status": status,
				"summary": "synthetic box " + in.Inventory.Box, "hubs": []string{"tabular-lane"}},
				"body": "## What it is\nA box.\n\n## Key files\n- `STATE.md` — state\n"})
			return llm.Result{JSON: out}
		}
		box := "alpha"
		note := map[string]any{"id": "finding-" + box + "-built", "type": "finding", "status": "current",
			"summary": "what " + box + " built", "scope": "work", "hubs": []string{"tabular-lane"},
			"evidence": []string{"work/" + box + "/STATE.md"}, "as_of": "2026-09-01", "body": "built and merged"}
		out, _ := json.Marshal(map[string]any{"ops": []any{map[string]any{"op": "add_note", "note": note}}})
		return llm.Result{JSON: out}
	}}
	ledger, err := llm.OpenLedger(filepath.Join(brain, ".index", "brain.db"), filepath.Join(brain, ".data", "llm"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ledger.Close() })
	cfg := Config{BrainRoot: brain, ShadowDocs: []string{shadow}, Forbidden: []string{shadow}}
	return &Activities{Cfg: cfg, LLM: fake, Ledger: ledger}, fake, shadow
}

func runBackfill(t *testing.T, acts *Activities, in BackfillInput) backfill.Report {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetStartTime(time.Now())
	env.RegisterWorkflow(BackfillWorkflow)
	env.RegisterWorkflow(BackfillBatchWorkflow)
	env.RegisterActivity(acts)
	env.ExecuteWorkflow(BackfillWorkflow, in)
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow: completed=%v err=%v", env.IsWorkflowCompleted(), env.GetWorkflowError())
	}
	var rep backfill.Report
	if err := env.GetWorkflowResult(&rep); err != nil {
		t.Fatal(err)
	}
	return rep
}

func boxResult(rep backfill.Report, box string) backfill.BoxResult {
	for _, b := range rep.Boxes {
		if b.Box == box {
			return b
		}
	}
	return backfill.BoxResult{}
}

func treeOf(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if fi, err := d.Info(); err == nil {
			fmt.Fprintf(&b, "%s %d %d\n", p, fi.Size(), fi.ModTime().UnixNano())
		}
		return nil
	})
	return b.String()
}

// TestBackfillIsIdempotent catches a rerun that pays twice or rewrites CARDs: a box whose CARD is newer than its files
// is skipped, and with the staged CARDs gone the same inventories map to the same keys, answered for $0.
func TestBackfillIsIdempotent(t *testing.T) {
	acts, fake, _ := newBackfillBrain(t, 0.01, "alpha", "beta")
	first := runBackfill(t, acts, BackfillInput{})
	if first.CardsWritten != 2 || first.NotesAdded != 1 || fake.Calls() != 3 {
		t.Fatalf("first run: cards=%d notes=%d calls=%d", first.CardsWritten, first.NotesAdded, fake.Calls())
	}
	staging := filepath.Join(acts.Cfg.BrainRoot, ".brain", "staging")
	card, _ := os.ReadFile(filepath.Join(staging, "work", "beta", "CARD.md"))

	second := runBackfill(t, acts, BackfillInput{})
	if fake.Calls() != 3 || second.CostUSD != 0 || boxResult(second, "beta").Status != "skipped" {
		t.Fatalf("second run paid or redid a current CARD: calls=%d cost=%.2f boxes=%+v", fake.Calls(), second.CostUSD, second.Boxes)
	}
	if err := os.Rename(staging, staging+".moved"); err != nil {
		t.Fatal(err)
	}
	third := runBackfill(t, acts, BackfillInput{})
	again, _ := os.ReadFile(filepath.Join(staging, "work", "beta", "CARD.md"))
	if fake.Calls() != 3 || third.CardsWritten != 2 || third.CostUSD != 0 || string(again) != string(card) {
		t.Fatalf("rerun of the same inventories: calls=%d cards=%d cost=%.2f same=%v", fake.Calls(), third.CardsWritten,
			third.CostUSD, string(again) == string(card))
	}
}

// TestBackfillCostCap catches a backfill that keeps paying past its budget, or whose budget resets per run: calls stop
// once the ledger's backfill total plus the next step's budget would pass it, and a second run makes no call at all.
func TestBackfillCostCap(t *testing.T) {
	acts, fake, _ := newBackfillBrain(t, 0.40, "beta", "delta", "gamma")
	rep := runBackfill(t, acts, BackfillInput{BudgetUSD: 1.0})
	if fake.Calls() != 2 || rep.CostUSD > 1.0 || !strings.Contains(rep.Stopped, "cost cap") ||
		boxResult(rep, "gamma").Status != "skipped" {
		t.Fatalf("calls=%d cost=%.2f stopped=%q boxes=%+v", fake.Calls(), rep.CostUSD, rep.Stopped, rep.Boxes)
	}
	runBackfill(t, acts, BackfillInput{BudgetUSD: 1.0})
	if spent, _ := acts.workflowSpent("backfill"); fake.Calls() != 2 || spent > 1.0 {
		t.Fatalf("second run: calls=%d, backfill total $%.2f", fake.Calls(), spent)
	}
}

// TestCardsStagedInShadow catches a shadow backfill that writes into saahil-docs or <brain>/work (which the cut-over
// needs empty), an index that cannot find a staged CARD at work/<box>/CARD.md, and a promotion that overwrites a CARD
// without keeping it.
func TestCardsStagedInShadow(t *testing.T) {
	acts, _, shadow := newBackfillBrain(t, 0.01, "alpha", "beta")
	before := treeOf(t, shadow)
	rep := runBackfill(t, acts, BackfillInput{})
	brain := acts.Cfg.BrainRoot
	if after := treeOf(t, shadow); after != before {
		t.Fatalf("saahil-docs changed:\n%s\n---\n%s", before, after)
	}
	if _, err := os.Stat(filepath.Join(brain, "work")); err == nil {
		t.Fatal("shadow backfill created <brain>/work")
	}
	if c := boxResult(rep, "alpha").Card; c != ".brain/staging/work/alpha/CARD.md" {
		t.Fatalf("card path %q", c)
	}
	b, _ := json.MarshalIndent(rep, "", "  ")
	t.Logf("report %s:\n%s", backfill.ReportPath(brain, rep.ID), b)

	ix, err := index.Open(brain)
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	if _, err := ix.Update(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if n, err := ix.Show("work/alpha/CARD.md"); err != nil || n.Type != "card" {
		t.Fatalf("staged CARD not indexed as the box's card: %+v %v", n, err)
	}

	write(t, filepath.Join(brain, ".brain", "live"), "")
	write(t, filepath.Join(brain, "work", "alpha", "CARD.md"), "hand-written\n")
	if moved, _, err := backfill.PromoteCards(brain, "t"); err != nil || len(moved) != 1 {
		t.Fatalf("promote: %v %v", moved, err)
	}
	kept, _ := os.ReadFile(filepath.Join(brain, ".archive", "backfill", "promote-t", "work", "alpha", "CARD.md"))
	if string(kept) != "hand-written\n" {
		t.Fatal("promotion overwrote a CARD without keeping it")
	}
}
