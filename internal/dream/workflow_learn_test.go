package dream

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"

	"saahil.local/brain/internal/delta"
	"saahil.local/brain/internal/llm"
	"saahil.local/brain/internal/store"
)

const learnSession = "sessions/devbox/2026/10/2026-10-08-%s-sample.md"

// newLearnBrain holds one session note per entry of corrections (its short id → its correction count), each with a
// transcript that has one failing then passing test run, and one prompt-hook pointer shown to every session. The fake
// judges every session a success with the pointer helpful, and reflects with one lesson citing the session note.
func newLearnBrain(t *testing.T, corrections map[string]int) (*Activities, *llm.Fake) {
	t.Helper()
	acts, _ := newBrain(t, 0.01)
	acts.Cfg.SkillsDir = ""
	brain := acts.Cfg.BrainRoot
	write(t, filepath.Join(brain, "hubs", "ci-depot.md"),
		"---\nid: ci-depot\ntype: hub\nstatus: current\nsummary: CI on Depot\nbroader: [index]\n---\n")
	for _, id := range []string{"lesson-shown", "lesson-unshown"} {
		write(t, filepath.Join(brain, "notes", id+".md"), "---\nid: "+id+"\ntype: lesson\nstatus: current\n"+
			"summary: a lesson\nhubs: [ci-depot]\nevidence: [hubs/ci-depot.md]\n---\nbody\n")
	}
	transcript := filepath.Join(brain, "t.jsonl")
	write(t, transcript, `{"message":{"content":[{"type":"tool_use","id":"a","input":{"command":"go test ./..."}}]}}
{"message":{"content":[{"type":"tool_result","tool_use_id":"a","is_error":true,"content":"FAIL"}]}}
{"message":{"content":[{"type":"tool_use","id":"b","input":{"command":"go test ./..."}}]}}
{"message":{"content":[{"type":"tool_result","tool_use_id":"b","content":"ok"}]}}
`)
	stats, err := delta.OpenStats(filepath.Join(brain, ".index", "brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer stats.Close()
	db, err := sql.Open("sqlite", filepath.Join(brain, ".index", "brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS lookups(time TEXT, session TEXT, hook TEXT, candidates TEXT,
		chosen TEXT, abstained INTEGER, ms REAL)`); err != nil {
		t.Fatal(err)
	}
	for short, n := range corrections {
		id := short + "-0000-0000-0000-000000000000"
		write(t, filepath.Join(brain, strings.Replace(learnSession, "%s", short, 1)), "---\ntype: session\nsession_id: "+id+
			"\ntranscript: "+transcript+"\ncorrections: "+strconv.Itoa(n)+"\n---\n## Owner prompts\n- no, not like that\n")
		if _, err := db.Exec(`INSERT INTO lookups VALUES('2026-10-08T00:00:00Z', ?, 'prompt', '[]', '["lesson-shown"]', 0, 1)`, id); err != nil {
			t.Fatal(err)
		}
	}
	fake := acts.LLM.(*llm.Fake)
	fake.Respond = func(c llm.Call) llm.Result {
		if strings.HasPrefix(c.Skill, "# Skill: judge") {
			return llm.Result{JSON: json.RawMessage(`{"outcome":"success","ops":[
				{"op":"counter","id":"lesson-shown","field":"helpful","delta":1,"why":"opened it, then re-ran the failing package"},
				{"op":"counter","id":"lesson-shown","field":"helpful","delta":1,"why":"a second verdict on one pointer"},
				{"op":"counter","id":"lesson-unshown","field":"harmful","delta":1,"why":"never shown to this session"}]}`)}
		}
		var in struct{ Material []struct{ Path string } }
		_ = json.Unmarshal([]byte(c.Input), &in)
		note := map[string]any{"id": "lesson-check-before-push", "type": "lesson", "status": "current",
			"summary": "When CI is red → run the failing package locally first, because it is faster", "scope": "global",
			"hubs": []string{"ci-depot"}, "evidence": []string{in.Material[0].Path + "#Owner prompts"},
			"sources": []string{in.Material[0].Path}, "as_of": "2026-10-08", "body": "run it locally first"}
		out, _ := json.Marshal(map[string]any{"ops": []any{map[string]any{"op": "add_note", "note": note}}})
		return llm.Result{JSON: out}
	}
	return acts, fake
}

func runLearn(t *testing.T, acts *Activities, session string) LearnResult {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(LearnSessionWorkflow)
	env.RegisterActivity(acts)
	env.ExecuteWorkflow(LearnSessionWorkflow, LearnInput{Session: session, Apply: true})
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow: completed=%v err=%v", env.IsWorkflowCompleted(), env.GetWorkflowError())
	}
	var out LearnResult
	if err := env.GetWorkflowResult(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestLearnOnlyReflectsOnCorrectedOrFailed catches a learn step that pays Opus for every session (DESIGN §6 gates
// reflect on a correction or failure), one that drops a corrected session's lesson or its evidence, and judge counter
// ops that never reach the shown pointer's counters, count one pointer twice, move a note the session was never
// shown, or lose their why.
func TestLearnOnlyReflectsOnCorrectedOrFailed(t *testing.T) {
	acts, fake := newLearnBrain(t, map[string]int{"0000aaaa": 0, "0000bbbb": 2})
	clean := runLearn(t, acts, strings.Replace(learnSession, "%s", "0000aaaa", 1))
	if clean.Reflected || clean.Calls != 1 || clean.Outcome != "success" || !clean.Signals.TestsFixed {
		t.Fatalf("clean session: %+v", clean)
	}
	corrected := runLearn(t, acts, strings.Replace(learnSession, "%s", "0000bbbb", 1))
	if !corrected.Reflected || corrected.Applied != 1 || fake.Calls() != 3 {
		t.Fatalf("corrected session: %+v (calls %d)", corrected, fake.Calls())
	}
	n, err := delta.Parse([]byte(corrected.Edits[0].Content))
	if err != nil || n.Type != "lesson" || !strings.HasPrefix(n.Evidence[0], corrected.Session) {
		t.Fatalf("lesson without the session as evidence: %+v %v", n, err)
	}
	stats, _ := delta.OpenStats(filepath.Join(acts.Cfg.BrainRoot, ".index", "brain.db"))
	defer stats.Close()
	if helpful, _ := stats.Get("lesson-shown", "helpful"); helpful != 2 {
		t.Fatalf("helpful counter %d, want one per judged session", helpful)
	}
	if harmful, _ := stats.Get("lesson-unshown", "harmful"); harmful != 0 {
		t.Fatalf("a note the session was never shown moved: harmful %d", harmful)
	}
	if len(clean.Counters) != 1 || clean.Counters[0].Why != "opened it, then re-ran the failing package" {
		t.Fatalf("counters reported %+v, want the one helpful op with its why", clean.Counters)
	}
}

// TestLearnSessionReplay catches a non-deterministic change to LearnSessionWorkflow (reordered or added activities,
// a branch on the clock) that would wedge in-flight learn runs on the next worker deploy. The history was recorded
// on a throwaway local dev server with the fake LLM, for a corrected session that judged, reflected and applied.
func TestLearnSessionReplay(t *testing.T) {
	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflow(LearnSessionWorkflow)
	if err := replayer.ReplayWorkflowHistoryFromJSONFile(nil, "testdata/learn-history.json"); err != nil {
		t.Fatal(err)
	}
}

// TestReflectReadsTheSessionsProducedDocs catches a reflect that sees only the session note: with no tools the call
// cannot open the reports and PR bodies a session wrote, and live calls took most of their facts from those.
func TestReflectReadsTheSessionsProducedDocs(t *testing.T) {
	acts, fake := newBrain(t, 0.01)
	brain := acts.Cfg.BrainRoot
	session := "sessions/devbox/2026/10/2026-10-08-0000abcd-sample.md"
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.md")
	write(t, elsewhere, "OUTSIDE\n")
	write(t, filepath.Join(brain, "..", "outside.md"), "OUTSIDE\n")
	note := "---\ntype: session\ncorrections: 1\nproduced: [work/box/report.md, work/box/run.sh, " + elsewhere +
		", ../outside.md, work/box/missing.md, work/box/big.md]\n---\nran tests\n"
	write(t, filepath.Join(brain, session), note)
	write(t, filepath.Join(brain, "work/box/report.md"), "FACT-IN-REPORT\n")
	write(t, filepath.Join(brain, "work/box/run.sh"), "echo SCRIPT\n")
	write(t, filepath.Join(brain, "work/box/big.md"), strings.Repeat("x", fileContentCap+10))
	var input string
	fake.Respond = func(c llm.Call) llm.Result {
		input = c.Input
		return llm.Result{JSON: json.RawMessage(`{"ops":[]}`)}
	}
	if _, err := acts.LearnReflect(context.Background(), LearnCallInput{Session: session}); err != nil {
		t.Fatal(err)
	}
	var payload struct{ Material []payloadFile }
	if err := json.Unmarshal([]byte(input), &payload); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range payload.Material {
		got = append(got, f.Path+" "+strconv.FormatBool(f.Truncated)+" "+strconv.Itoa(len(f.Content)))
	}
	want := []string{session + " false " + strconv.Itoa(len(note)), "work/box/report.md false 15", "work/box/big.md true " + strconv.Itoa(fileContentCap)}
	if !slices.Equal(got, want) {
		t.Fatalf("material %q, want %q", got, want)
	}
}

// TestReflectSeesWhatEarlierRunsLearnedFromTheSession catches a reflect that cannot see the notes earlier dreams took
// from the same live session: with no tools it proposed most of them again under new ids, every dream.
func TestReflectSeesWhatEarlierRunsLearnedFromTheSession(t *testing.T) {
	acts, fake := newBrain(t, 0.01)
	brain := acts.Cfg.BrainRoot
	session := "sessions/devbox/2026/10/2026-10-08-0000abcd-sample.md"
	write(t, filepath.Join(brain, session), "---\ntype: session\ncorrections: 1\n---\nran tests\n")
	learned := "---\nid: lesson-learned\ntype: lesson\nstatus: retired\nsources: [" + session + "]\n---\nalready learned\n"
	write(t, filepath.Join(brain, "notes/lesson-learned.md"), learned)
	write(t, filepath.Join(brain, "notes/lesson-other.md"), "---\nid: lesson-other\n---\nfrom another session\n")
	st, err := store.Open(filepath.Join(brain, ".index", "brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(`INSERT INTO node(id, kind, path, status) VALUES
		('lesson-learned', 'doc', 'notes/lesson-learned.md', 'retired'), ('lesson-other', 'doc', 'notes/lesson-other.md', 'current');
		INSERT INTO edge(src, dst, rel, source) VALUES ('lesson-learned', ?, 'sources', 'fm'),
		('lesson-other', 'sessions/devbox/2026/10/2026-10-08-0000ffff-other.md', 'evidence', 'fm')`, session); err != nil {
		t.Fatal(err)
	}
	st.Close()
	var input string
	fake.Respond = func(c llm.Call) llm.Result {
		input = c.Input
		return llm.Result{JSON: json.RawMessage(`{"ops":[]}`)}
	}
	if _, err := acts.LearnReflect(context.Background(), LearnCallInput{Session: session}); err != nil {
		t.Fatal(err)
	}
	var payload struct{ Proposed []Edit }
	if err := json.Unmarshal([]byte(input), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Proposed) != 1 || payload.Proposed[0].Path != "notes/lesson-learned.md" || payload.Proposed[0].Content != learned {
		t.Fatalf("proposed %+v, want only notes/lesson-learned.md with its content", payload.Proposed)
	}
}

// TestLearnHoldsASessionWholeWhenTheCapIsFull catches the edit cap cutting through one session's edits: on 2026-10-19T19
// learn reached the 50 files mid-session, wrote 11 notes that supersede others but not the status edits on the notes
// they supersede (the doctor's supersession check failed on both machines), lost 31 notes for good, and still paid to
// reflect on every later session. A session's edits land whole or wait, with every later session, for the next dream.
func TestLearnHoldsASessionWholeWhenTheCapIsFull(t *testing.T) {
	corrections := map[string]int{}
	for i := range 20 {
		corrections[fmt.Sprintf("0000%04d", i)] = 1
	}
	acts, fake := newLearnBrain(t, corrections)
	brain := acts.Cfg.BrainRoot
	for short := range corrections {
		write(t, filepath.Join(brain, "notes", "lesson-old-"+short+".md"), "---\nid: lesson-old-"+short+
			"\ntype: lesson\nstatus: current\nsummary: an older lesson\nscope: global\nhubs: [ci-depot]\n"+
			"evidence: [hubs/ci-depot.md]\nas_of: 2026-10-01\n---\nwhen this → do the old thing, because it once worked\n")
	}
	judge := fake.Respond
	var reflected atomic.Int32
	fake.Respond = func(c llm.Call) llm.Result {
		if !strings.HasPrefix(c.Skill, "# Skill: distil") {
			if strings.HasPrefix(c.Skill, "# Skill: judge") {
				return judge(c)
			}
			return llm.Result{JSON: json.RawMessage(`{"ops":[]}`)}
		}
		reflected.Add(1)
		var in struct{ Material []struct{ Path string } }
		_ = json.Unmarshal([]byte(c.Input), &in)
		session := in.Material[0].Path
		short := strings.Split(filepath.Base(session), "-")[3]
		note := func(id string) map[string]any {
			return map[string]any{"id": id, "type": "lesson", "status": "current", "summary": "a lesson from " + short,
				"scope": "global", "hubs": []string{"ci-depot"}, "evidence": []string{session + "#Owner prompts"},
				"sources": []string{session}, "as_of": "2026-10-08", "body": "when this → do that, because it broke"}
		}
		out, _ := json.Marshal(map[string]any{"ops": []any{
			map[string]any{"op": "add_note", "note": note("lesson-new-" + short)},
			map[string]any{"op": "supersede", "old": "lesson-old-" + short, "new": "lesson-new-" + short},
			map[string]any{"op": "add_note", "note": note("lesson-extra-" + short)}}})
		return llm.Result{JSON: out}
	}
	// each session changes 3 files: its 17th would get the cap's last 2
	whole := func() (landed int) {
		t.Helper()
		for short := range corrections {
			_, errNew := os.Stat(filepath.Join(brain, "notes", "lesson-new-"+short+".md"))
			_, errExtra := os.Stat(filepath.Join(brain, "notes", "lesson-extra-"+short+".md"))
			old, _ := os.ReadFile(filepath.Join(brain, "notes", "lesson-old-"+short+".md"))
			superseded := strings.Contains(string(old), "status: superseded")
			if (errNew == nil) != superseded || (errNew == nil) != (errExtra == nil) {
				t.Errorf("session %s landed in part: new=%v extra=%v old superseded=%v", short, errNew == nil,
					errExtra == nil, superseded)
			}
			if errNew == nil {
				landed++
			}
		}
		return landed
	}

	t0 := time.Now().UTC()
	first := runDream(t, acts, Input{Force: true}, t0)
	landed := whole()
	if landed != 16 || step(first, "learn").Edits != 48 {
		t.Fatalf("first dream landed %d sessions in %d edits, want 16 in 48 (the cap's 50 files)", landed,
			step(first, "learn").Edits)
	}
	for _, rej := range first.Rejected {
		if rej.Step == "learn" && (!rej.Held || rej.Lost || !strings.Contains(rej.Reason, "waits for the next dream")) {
			t.Errorf("learn's held edit is recorded as %+v, want held, not lost, waiting for the next dream", rej)
		}
	}
	if got := reflected.Load(); got != 17 {
		t.Errorf("reflected %d sessions, want 17: none after the first that did not fit", got)
	}

	second := runDream(t, acts, Input{Force: true}, t0.Add(13*time.Hour))
	if landed := whole(); landed != 20 || second.Material.Sessions != 4 {
		t.Fatalf("second dream: %d of 20 sessions landed from %d sessions read, want 20 from the 4 that waited",
			landed, second.Material.Sessions)
	}
}
