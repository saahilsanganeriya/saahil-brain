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
		if !strings.HasPrefix(c.Skill, "# skill file") || strings.Contains(c.Input, `"refused":`) {
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

	rep := runDreamDeployed(t, acts)

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

// TestSkillStepRepairsRefusedOpsOnce catches a skill answer whose refused op is dropped for good: the file step lost
// 9 lessons between 2026-10-14 and 2026-10-25 (bodies 310–381 chars against the 300 cap) because the captures
// watermark moved past them. The step asks the same skill once more with only the refused ops and their reasons.
func TestSkillStepRepairsRefusedOpsOnce(t *testing.T) {
	brain, skills := t.TempDir(), t.TempDir()
	write(t, filepath.Join(brain, "memory/feedback-x.md"), "x\n")
	write(t, filepath.Join(brain, "hubs/git.md"), "---\nid: git\ntype: hub\nstatus: current\nsummary: git\n---\n")
	write(t, filepath.Join(skills, "file.md"), "# skill file\n")
	lesson := func(body string) string {
		return `{"op":"add_note","note":{"id":"lesson-rebase-twice","type":"lesson","status":"current","summary":"rebase twice",
 "scope":"global","hubs":["git"],"evidence":["memory/feedback-x.md"],"body":"` + body + `"}}`
	}
	var repairInput string
	fake := &llm.Fake{CostUSD: 0.01, Respond: func(c llm.Call) llm.Result {
		if strings.Contains(c.Input, `"refused":`) {
			repairInput = c.Input
			return llm.Result{JSON: json.RawMessage(`{"ops":[` + lesson("When a stack moves, rebase twice.") + `]}`)}
		}
		return llm.Result{JSON: json.RawMessage(`{"ops":[` + lesson(strings.Repeat("x", 301)) + `]}`)}
	}}
	ledger, err := llm.OpenLedger(filepath.Join(brain, ".index", "brain.db"), filepath.Join(brain, ".data", "llm"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	acts := &Activities{Cfg: Config{BrainRoot: brain, SkillsDir: skills}, LLM: fake, Ledger: ledger}
	in := LLMStepInput{Workflow: "dream", Step: "file", Skill: "file", Model: "m", BudgetUSD: 0.5,
		Material: []Material{{Rel: "memory/feedback-x.md", Abs: filepath.Join(brain, "memory/feedback-x.md")}}}

	out, err := acts.SkillStep(context.Background(), in)
	if err != nil || out.Status != "ok" || out.Refused != 0 || len(out.Edits) != 1 ||
		out.Edits[0].Path != "notes/lesson-rebase-twice.md" {
		t.Fatalf("the refused lesson was not repaired: %+v %v", out, err)
	}
	if !strings.Contains(repairInput, "a lesson body is ≤ 300 chars") || fake.Calls() != 2 || out.CostUSD != 0.02 {
		t.Fatalf("want one repair call that names the reason, costed: calls %d, cost %.2f, input %s",
			fake.Calls(), out.CostUSD, repairInput)
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

// TestVerifyCarriesClaimsTheRunCapLeftUnverified catches verify judging only its own run's claims: from 2026-11-19
// to 11-26 the run cap left 431 claims unverified (128 of 184 on 11-25) and no later dream judged them, though verify
// fixes 45% of the claims it judges. A claim the cap leaves waits in .brain/verify-carry.json; a later dream judges
// it after its own claims with what the run cap leaves, as the file reads then, for at most carryMaxAge.
func TestVerifyCarriesClaimsTheRunCapLeftUnverified(t *testing.T) {
	brain, skills := t.TempDir(), t.TempDir()
	write(t, filepath.Join(skills, "verify.md"), "# skill verify\n")
	note := func(i int, summary, status string) Edit {
		id := fmt.Sprintf("finding-claim-%02d", i)
		e := Edit{Path: "notes/" + id + ".md", Action: "write", Content: "---\nid: " + id +
			"\ntype: finding\nstatus: " + status + "\nsummary: " + summary + "\nscope: hub\nhubs: [git]\nevidence: [work/box/r.md]\n---\nbody\n"}
		write(t, filepath.Join(brain, e.Path), e.Content) // the dream applies its edits before verify reads them
		return e
	}
	write(t, filepath.Join(brain, "work/box/r.md"), "report\n")
	var sent [][]string
	respond := func(c llm.Call) llm.Result {
		var p skillPayload
		if err := json.Unmarshal([]byte(c.Input), &p); err != nil {
			t.Fatal(err)
		}
		var ids, verdicts []string
		for _, e := range p.Proposed {
			id := strings.TrimSuffix(filepath.Base(e.Path), ".md")
			ids, verdicts = append(ids, id), append(verdicts, `{"id":"`+id+`","verdict":"keep"}`)
		}
		sent = append(sent, ids)
		return llm.Result{JSON: json.RawMessage(`{"ops":[],"verdicts":[` + strings.Join(verdicts, ",") + `]}`)}
	}
	ledger, err := llm.OpenLedger(filepath.Join(brain, ".index", "brain.db"), filepath.Join(brain, ".data", "llm"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	acts := &Activities{Cfg: Config{BrainRoot: brain, SkillsDir: skills}, LLM: &llm.Fake{CostUSD: 0.8, Respond: respond},
		Ledger: ledger}
	in := LLMStepInput{Workflow: "dream", Step: "verify", Skill: "verify", Model: "m", BudgetUSD: 2.5, RunSpentUSD: 6.5}
	for i := 0; i < 25; i++ {
		in.Proposed = append(in.Proposed, note(i, "first run", "current"))
	}
	out, err := acts.SkillStep(context.Background(), in)
	if err != nil || !strings.Contains(out.Reason, "17 of 25 claims unverified: run cap") ||
		!strings.Contains(out.Reason, "17 claims wait for the next dream") {
		t.Fatalf("$6.50 spent: %+v %v; want 8 judged and 17 waiting for the next dream", out, err)
	}

	// The next dream: 2 claims of its own, one of them a waiting claim it rewrote; one waiting claim is now superseded.
	sent = nil
	note(9, "superseded since", "superseded")
	in.RunSpentUSD, in.Proposed = 2.0, []Edit{note(30, "second run", "current"), note(10, "rewritten", "current")}
	out, err = acts.SkillStep(context.Background(), in)
	if err != nil || out.Status != "ok" {
		t.Fatalf("%+v %v", out, err)
	}
	if len(sent) != 3 || fmt.Sprint(sent[0]) != "[finding-claim-30 finding-claim-10]" {
		t.Fatalf("batches %v; want its own 2 claims first, then the 15 waiting ones in 2 batches", sent)
	}
	var carried []string
	for _, b := range sent[1:] {
		carried = append(carried, b...)
	}
	if len(carried) != 15 || slices.Contains(carried, "finding-claim-09") || slices.Contains(carried, "finding-claim-10") {
		t.Errorf("waiting claims judged %v; want 15: not the superseded 09, not 10 twice", carried)
	}
	if !strings.Contains(out.Reason, "15 earlier claims judged") || strings.Contains(out.Reason, "wait for the next dream") {
		t.Errorf("reason %q; want 15 earlier claims judged and none left waiting", out.Reason)
	}
	if _, err := os.Stat(filepath.Join(brain, ".brain", "verify-carry.json")); !os.IsNotExist(err) {
		t.Errorf("carry list still on disk (%v); nothing waits", err)
	}

	// A claim that waited longer than carryMaxAge is dropped and counted, never judged.
	write(t, filepath.Join(brain, ".brain", "verify-carry.json"),
		`[{"path":"notes/finding-claim-20.md","since":"2020-01-01T00:00:00Z"}]`)
	sent, in.Proposed = nil, []Edit{note(31, "third run", "current")}
	out, err = acts.SkillStep(context.Background(), in)
	if err != nil || len(sent) != 1 || !strings.Contains(out.Reason, "1 claims dropped unverified after 24h0m0s") {
		t.Fatalf("batches %v, reason %q, %v; want only its own claim judged and the old one dropped", sent, out.Reason, err)
	}
}

func roundAll(xs []float64) []float64 {
	for i, x := range xs {
		xs[i] = float64(int(x*100+0.5)) / 100
	}
	return xs
}

// TestVerifyLeavesReplacedNotesAlone catches a supersedes edge that points at a live note (doctor: graph
// supersession, 2026-11-17): the merge step superseded a playbook, verify then judged that playbook against its own
// evidence, found nothing there about a replacement, and wrote its status back to current. A note that the run left
// superseded or retired is not a live claim: verify must not judge it, and must still judge the note that replaced it.
func TestVerifyLeavesReplacedNotesAlone(t *testing.T) {
	note := func(id, status string) Edit {
		return Edit{Path: "notes/" + id + ".md", Action: "write", Content: "---\nid: " + id + "\ntype: playbook\nstatus: " +
			status + "\nsummary: s\nscope: global\nhubs: [h]\nevidence: [work/box/r.md]\nas_of: 2026-10-08\n---\nbody\n"}
	}
	claims := latestClaims([]Edit{
		note("playbook-old", "current"),
		note("playbook-new", "current"),
		note("playbook-old", "superseded"),
		note("playbook-wrong", "retired"),
		note("playbook-unsure", "proposed"),
	})
	var got []string
	for _, c := range claims {
		got = append(got, c.Path)
	}
	want := []string{"notes/playbook-new.md", "notes/playbook-unsure.md"}
	if !slices.Equal(got, want) {
		t.Fatalf("verify would judge %v; want %v (a superseded or retired note is not a live claim)", got, want)
	}
}
