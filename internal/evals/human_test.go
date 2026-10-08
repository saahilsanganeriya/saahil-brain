package evals

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"saahil.local/brain/internal/index"
	"saahil.local/brain/internal/llm"
)

func jsonLine(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func ownerTurn(t *testing.T, at, text string) string {
	return jsonLine(t, map[string]any{"type": "user", "timestamp": at, "cwd": "/tmp/work", "origin": map[string]string{"kind": "human"},
		"message": map[string]any{"role": "user", "content": text}})
}

func toolTurn(t *testing.T, at string, tools ...map[string]any) string {
	blocks := []map[string]any{}
	for i, tool := range tools {
		blocks = append(blocks, map[string]any{"type": "tool_use", "id": at + string(rune('a'+i)), "name": tool["name"], "input": tool["input"]})
	}
	return jsonLine(t, map[string]any{"type": "assistant", "timestamp": at, "message": map[string]any{"content": blocks}})
}

func use(name string, input map[string]any) map[string]any {
	return map[string]any{"name": name, "input": input}
}

// humanFixture is the navigation fixture plus one devbox transcript whose owner prompts cover every rule of the human
// set: kept prompts, each kind of non-typed turn, a judged 'none', a read past the 10-call window, and a file born
// after its prompt.
func humanFixture(t *testing.T) (*index.Index, string) {
	t.Helper()
	ix := fixture(t)
	t.Setenv("BRAIN_MACHINE", "devbox")
	home := os.Getenv("BRAIN_HOME")
	docs := filepath.Join(home, "Documents", "work-repo", "saahil-docs")
	write(t, filepath.Join(docs, "box-a", "new-report.md"), "# Shard report\n\nWidths measured after the run.\n")
	if _, err := ix.Update(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	stubFileTimes(t, map[string]time.Time{"new-report.md": time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)})

	read := func(rel string) map[string]any {
		return use("Read", map[string]any{"file_path": filepath.Join(docs, rel)})
	}
	ls := use("Bash", map[string]any{"command": "ls"})
	brief := "Please carry out this task from my Orca coordinator. You are working inside Orca. === TASK === shard plan " + promptMarker
	lines := []string{
		`{"type":"summary","summary":"x"}`,
		ownerTurn(t, "2026-10-05T09:00:00Z", brief), // turn 0
		toolTurn(t, "2026-10-05T09:01:00Z", read("box-a/plans/rollout.md")),
		ownerTurn(t, "2026-10-05T10:00:00Z", "where does the glacier tokenizer rollout plan stand today "+promptMarker), // turn 1
		toolTurn(t, "2026-10-05T10:01:00Z", read("box-a/plans/rollout.md"),
			use("Bash", map[string]any{"command": "cat " + filepath.Join(docs, "box-a", "README.md")}),
			use("Write", map[string]any{"file_path": filepath.Join(docs, "box-a", "briefs", "shard-plan.md")})),
		jsonLine(t, map[string]any{"type": "assistant", "timestamp": "2026-10-05T10:02:00Z", "message": map[string]any{
			"content": []map[string]any{{"type": "text", "text": "The rollout plan sets eight shards."}}}}),
		jsonLine(t, map[string]any{"type": "user", "isSidechain": true, "origin": map[string]string{"kind": "human"},
			"message": map[string]any{"content": "a subagent turn that is no owner prompt at all here"}}),
		ownerTurn(t, "2026-10-05T10:10:00Z", "<command-name>/clear</command-name>"),                                                                              // turn 2
		ownerTurn(t, "2026-10-05T10:20:00Z", "SELF-CHECK (owner automation, every 30 min). Count live workers and continue."),                                    // turn 3
		ownerTurn(t, "2026-10-05T10:30:00Z", "ok go on"),                                                                                                         // turn 4
		ownerTurn(t, "2026-10-05T10:40:00Z", `<pasted_content id="2">a long paste of somebody else's words that the owner did not type</pasted_content id="2">`), // turn 5
		ownerTurn(t, "2026-10-05T11:00:00Z", "tell me about the sourdough starter hydration notes in box b"),                                                     // turn 6
		toolTurn(t, "2026-10-05T11:01:00Z", read("box-b/baking.md")),
		jsonLine(t, map[string]any{"type": "attachment", "timestamp": "2026-10-05T11:10:00Z", "attachment": map[string]any{
			"type": "queued_command", "prompt": "check the nimbus flag status in staging before the canary", "origin": map[string]string{"kind": "human"}}}), // turn 7
		toolTurn(t, "2026-10-05T11:11:00Z", use("Read", map[string]any{"file_path": filepath.Join(home, ".claude", "projects", "-proj", "memory", "nimbus-flag.md")})),
		ownerTurn(t, "2026-10-05T12:00:00Z", "what is the state of glacier in box a again please"), // turn 8
		toolTurn(t, "2026-10-05T12:01:00Z", ls, ls, ls, ls, ls, ls, ls, ls, ls, ls, read("box-a/STATE.md")),
		ownerTurn(t, "2026-10-05T13:00:00Z", "look at the new shard report and summarise its findings now"), // turn 9
		toolTurn(t, "2026-10-05T13:01:00Z", read("box-a/new-report.md")),
	}
	transcript := filepath.Join(home, ".claude", "projects", "-proj", "aaaa1111-0000-4000-8000-000000000000.jsonl")
	write(t, transcript, strings.Join(lines, "\n")+"\n")
	return ix, transcript
}

type judgeInput struct {
	Items []struct {
		ID         string `json:"id"`
		Prompt     string `json:"prompt"`
		Candidates []struct {
			Path string `json:"path"`
		} `json:"candidates"`
	} `json:"items"`
}

// fakeJudge answers each item with its first candidate, or none for the sourdough prompt, and records what it saw.
func fakeJudge(t *testing.T, seen map[string][]string) *llm.Fake {
	var mu sync.Mutex
	return &llm.Fake{CostUSD: 0.25, Respond: func(c llm.Call) llm.Result {
		var in judgeInput
		if err := json.Unmarshal([]byte(c.Input), &in); err != nil {
			t.Errorf("judge input is not JSON: %v", err)
		}
		type answer struct {
			ID    string   `json:"id"`
			Files []string `json:"files"`
		}
		var out struct {
			Answers []answer `json:"answers"`
		}
		for _, it := range in.Items {
			var paths []string
			for _, c := range it.Candidates {
				paths = append(paths, c.Path)
			}
			mu.Lock()
			seen[it.ID] = paths
			mu.Unlock()
			a := answer{ID: it.ID, Files: []string{}}
			if !strings.Contains(it.Prompt, "sourdough") && len(paths) > 0 {
				a.Files = paths[:1]
			}
			out.Answers = append(out.Answers, a)
		}
		raw, _ := json.Marshal(out)
		return llm.Result{JSON: raw}
	}}
}

func buildHuman(t *testing.T, ix *index.Index, runner llm.Runner) (HumanSet, error) {
	t.Helper()
	ledger, err := llm.OpenLedger(filepath.Join(t.TempDir(), "llm.db"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ledger.Close() })
	return BuildHuman(context.Background(), ix, HumanBuildOptions{From: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Now: testNow,
		Max: 50, JudgeMax: 100, Runner: runner, Ledger: ledger})
}

// TestHumanSetKeepsTypedPromptsTheJudgeAnswered catches a set that admits turns the owner did not type (an Orca brief,
// a slash command, an automation note, a two-word reply, a bare paste), offers the judge files the session wrote,
// generic files, files read after the 10-call window or born after the prompt, keeps a 'none', drops a memory read
// instead of crediting its filed note, or stores prompt text.
func TestHumanSetKeepsTypedPromptsTheJudgeAnswered(t *testing.T) {
	ix, _ := humanFixture(t)
	seen := map[string][]string{}
	set, err := buildHuman(t, ix, fakeJudge(t, seen))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, it := range set.Items {
		got[it.ID] = it.Targets
	}
	want := map[string][]string{
		"human-devbox-aaaa1111-1": {"work/box-a/plans/rollout.md"},
		"human-devbox-aaaa1111-7": {"notes/decision-nimbus-flag.md"},
	}
	if len(got) != len(want) {
		t.Fatalf("items %v, want %v", got, want)
	}
	for id, targets := range want {
		if !slices.Equal(got[id], targets) {
			t.Errorf("%s targets %v, want %v", id, got[id], targets)
		}
	}
	if len(seen) != 3 || !slices.Equal(seen["human-devbox-aaaa1111-1"], []string{"work/box-a/plans/rollout.md"}) {
		t.Errorf("judge saw %v; want turns 1, 6 and 7, turn 1 offering only the file it read", seen)
	}
	m := set.Manifest
	if m.Typed != 5 || m.WithCandidates != 3 || m.Judged != 3 || m.None != 1 || m.Kept != 2 || m.Calls != 1 || m.CostUSD != 0.25 {
		t.Errorf("manifest %+v", m)
	}
	err = filepath.WalkDir(Dir(ix.Root), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), promptMarker) {
				t.Errorf("%s stores prompt text", p)
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// recordingRanker points at the nimbus note for any prompt that names it and remembers every prompt it was asked.
type recordingRanker struct{ prompts []string }

func (r *recordingRanker) Pointers(prompt, _ string, _ func(string) bool) []string {
	r.prompts = append(r.prompts, prompt)
	if strings.Contains(prompt, "nimbus") {
		return []string{"decision-nimbus-flag"}
	}
	return nil
}

// TestHumanEvalRereadsPromptsAndMergesTheOtherMachine catches a run that re-reads the wrong owner prompt for a turn
// (the builder and the runner counting turns differently: a queued command, a sidechain line), loses this machine's
// rows from the report the other machine merges, or reports this machine alone when the other machine's rows exist.
func TestHumanEvalRereadsPromptsAndMergesTheOtherMachine(t *testing.T) {
	ix, _ := humanFixture(t)
	set, err := buildHuman(t, ix, fakeJudge(t, map[string][]string{}))
	if err != nil {
		t.Fatal(err)
	}
	laptop := Report{Eval: "human-laptop", Ran: testNow.Add(-24 * time.Hour), Rows: []HumanRow{
		{ID: "human-laptop-bbbb2222-3", Machine: "laptop", Split: "dev", Top3: true, Calls: 1, L0Calls: 1},
		{ID: "human-laptop-bbbb2222-9", Machine: "laptop", Split: "test"},
	}}
	if _, err := laptop.Write(ix.Root); err != nil {
		t.Fatal(err)
	}
	ranker := &recordingRanker{}
	env := NewEnv(ix, Sets{}, testNow)
	env.Ranker = ranker
	run, err := env.Human(set, true)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(ranker.prompts, func(p string) bool { return strings.Contains(p, "nimbus flag status") }) {
		t.Errorf("turn 7 was re-read as some other prompt: %q", ranker.prompts)
	}
	top3 := run.Merged.Metrics[0]
	if top3.All.N != 4 || top3.All.Value != 0.5 {
		t.Errorf("merged top-3 %+v, want 2 of 4 (one hit per machine)", top3.All)
	}
	if run.Machines["devbox"] != 2 || run.Machines["laptop"] != 2 || !strings.Contains(run.Headline(), "laptop 2") {
		t.Errorf("machines %v, headline %q", run.Machines, run.Headline())
	}
	var own struct {
		Rows []HumanRow `json:"rows"`
	}
	raw, err := os.ReadFile(filepath.Join(ReportsDir(ix.Root, testNow), "human-devbox.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &own); err != nil || len(own.Rows) != 2 {
		t.Errorf("human-devbox.json rows %d (%v), want this machine's 2", len(own.Rows), err)
	}
}

// TestHumanSplitKeepsOneAnswerFileInOneHalf catches a split keyed by session or item, which puts prompts answered by
// the same file in both halves: ranking tuned on DEV would then be judged on TEST prompts it was tuned for.
func TestHumanSplitKeepsOneAnswerFileInOneHalf(t *testing.T) {
	first := ""
	for i := range 8 {
		it := HumanItem{ID: "human-devbox-0000000" + strconv.Itoa(i) + "-1", Transcript: "/t/" + strconv.Itoa(i) + ".jsonl",
			Targets: []string{"work/box-a/STATE.md", "work/box-z/plan-" + strconv.Itoa(i) + ".md"}}
		if s := humanSplit(it); first == "" {
			first = s
		} else if s != first {
			t.Fatalf("item %d in %s, item 0 in %s: one answer file split across halves", i, s, first)
		}
	}
}
