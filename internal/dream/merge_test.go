package dream

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/delta"
	"saahil.local/brain/internal/llm"
)

var mergeT0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// mergeModel is the fake model's side of curate.merge: what the find stage saw and how often each stage was asked.
type mergeModel struct {
	mu        sync.Mutex
	finds     int
	bodies    int
	findInput string
}

func (m *mergeModel) counts() (int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.finds, m.bodies
}

const mergePlanAnswer = `{"duplicates": [
  {"keep": "anti-pattern-probe-old", "supersede": ["anti-pattern-probe-new"], "why": "same rule"},
  {"keep": "lesson-cap-b", "supersede": ["lesson-cap-a"], "why": "same rule"}],
 "conflicts": [
  {"newer": "decision-flakes-only-rerun", "older": "lesson-rerun-on-depot", "resolution": "narrow", "why": "reruns are for flakes only"},
  {"newer": "decision-p", "older": "decision-q", "resolution": "supersede", "why": "opposite rules"}],
 "stale": [{"id": "lesson-use-old-tool", "replaced_by": "decision-new-tool", "why": "the old tool was replaced"}]}`

// newMergeBrain adds to newBrain current global notes holding one case of each kind curate.merge handles, plus a
// hub-scoped note it must not see. The fake's find answer keeps the OLDER probe note and calls a same-date pair a
// supersede; its bodies answer merges each group, over the 300-char lesson cap for lesson-cap-b.
func newMergeBrain(t *testing.T) (*Activities, *mergeModel) {
	t.Helper()
	acts, _ := newBrain(t, 0.01)
	acts.Cfg.SkillsDir = ""
	brain := acts.Cfg.BrainRoot
	write(t, filepath.Join(brain, "hubs", "ci-depot.md"),
		"---\nid: ci-depot\ntype: hub\nstatus: current\nsummary: CI on Depot\nbroader: [index]\n---\n")
	slug := strings.ReplaceAll(filepath.Join(config.LaptopHome, "Documents", "work-repo"), "/", "-")
	importSource := ".import/devbox/" + slug + "/probe.md"
	write(t, filepath.Join(brain, importSource), "a probe memory\n")
	note := func(id, typ, scope, asOf, summary, body, source string) {
		src := ""
		if source != "" {
			src = "sources: [" + source + "]\n"
		}
		write(t, filepath.Join(brain, "notes", id+".md"), "---\nid: "+id+"\ntype: "+typ+"\nstatus: current\nsummary: "+
			summary+"\nscope: "+scope+"\nhubs: [ci-depot]\nevidence: [hubs/ci-depot.md]\n"+src+"as_of: "+asOf+"\n---\n"+body+"\n")
	}
	note("anti-pattern-probe-old", "anti-pattern", "global", "2026-09-30", "A write probe leaves litter in a prod bucket",
		"Wrong move: put a test object to check write access.", importSource)
	note("anti-pattern-probe-new", "anti-pattern", "global", "2026-10-02", "A write probe you cannot delete stays there",
		"Wrong move: write _probe.txt where DeleteObject is denied.", "")
	note("lesson-cap-a", "lesson", "global", "2026-10-01", "When a run fails, read its logs first", "Read the logs first.", "")
	note("lesson-cap-b", "lesson", "global", "2026-10-04", "When a run fails, open its log before rerunning",
		"Open the log before a rerun.", "")
	note("lesson-rerun-on-depot", "lesson", "global", "2026-09-01", "When CI is red, rerun the lane on Depot",
		"Rerun the lane on Depot.", "")
	note("decision-flakes-only-rerun", "decision", "global", "2026-10-07", "Rerun a Depot lane only for a known flake",
		"Owner: rerun only a known flake.", "")
	note("decision-p", "decision", "global", "2026-10-03", "Agents ask the owner and wait", "Ask and wait.", "")
	note("decision-q", "decision", "global", "2026-10-03", "The driver settles design gaps alone", "Settle alone.", "")
	note("lesson-use-old-tool", "lesson", "global", "2026-09-10", "When stacking PRs, use the old stack tool",
		"Use the old tool.", "")
	note("decision-new-tool", "decision", "global", "2026-10-01", "Stacked PRs use gh stack; the old tool was replaced",
		"gh stack replaced the old tool.", "")
	note("finding-hub-only", "finding", "hub", "2026-10-05", "A hub-scoped finding", "Not global.", "")

	m := &mergeModel{}
	fake := acts.LLM.(*llm.Fake)
	fake.Respond = func(c llm.Call) llm.Result {
		if !strings.HasPrefix(c.Skill, "# Skill: merge") {
			return llm.Result{JSON: json.RawMessage(`{"edits":[]}`)}
		}
		var in struct {
			Stage  string
			Groups []struct{ Keep string }
		}
		_ = json.Unmarshal([]byte(c.Input), &in)
		m.mu.Lock()
		defer m.mu.Unlock()
		if in.Stage == "find" {
			m.finds++
			m.findInput = c.Input
			return llm.Result{JSON: json.RawMessage(mergePlanAnswer)}
		}
		m.bodies++
		var bodies []MergeBody
		for _, g := range in.Groups {
			body := "MERGED " + g.Keep + ": check put and delete rights before any write probe."
			if g.Keep == "lesson-cap-b" {
				body = strings.Repeat("x", config.LessonMaxChars+100)
			}
			bodies = append(bodies, MergeBody{Keep: g.Keep, Body: body})
		}
		out, _ := json.Marshal(map[string]any{"bodies": bodies})
		return llm.Result{JSON: out}
	}
	return acts, m
}

func readNote(t *testing.T, acts *Activities, id string) delta.Note {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(acts.Cfg.BrainRoot, "notes", id+".md"))
	if err != nil {
		t.Fatalf("%s is gone: %v", id, err)
	}
	n, err := delta.Parse(b)
	if err != nil {
		t.Fatalf("%s: %v", id, err)
	}
	return n
}

// TestCurateMergeMergesNarrowsAndAsks catches curate.merge losing or misplacing facts: a duplicate kept on the older
// note, a superseded note whose facts never reached the kept body (a merged body the applier refused must leave its
// whole group alone), a narrow not written, a same-date pair settled by the model's guess instead of going to the
// owner, a stale note left current, a delete, and a merge whose own writes wake the next dream's merge.
func TestCurateMergeMergesNarrowsAndAsks(t *testing.T) {
	acts, model := newMergeBrain(t)
	rep := runDream(t, acts, Input{Force: true}, mergeT0)
	if s := step(rep, "curate.merge"); s.Status != "ok" || s.Edits != 5 {
		t.Fatalf("curate.merge: %+v", s)
	}
	if !strings.Contains(model.findInput, `"project":"work-repo"`) || strings.Contains(model.findInput, "finding-hub-only") {
		t.Fatalf("find input is not the current global notes with their project: %s", model.findInput)
	}

	kept, gone := readNote(t, acts, "anti-pattern-probe-new"), readNote(t, acts, "anti-pattern-probe-old")
	if !strings.HasPrefix(kept.Body, "MERGED anti-pattern-probe-new") || !slices.Contains(kept.Supersedes, gone.ID) ||
		kept.Status != "current" || gone.Status != "superseded" {
		t.Fatalf("duplicate group not merged into the newest note: kept=%+v gone=%+v", kept, gone)
	}
	for _, id := range []string{"lesson-cap-a", "lesson-cap-b"} {
		if n := readNote(t, acts, id); n.Status != "current" || len(n.Supersedes) > 0 || strings.Contains(n.Body, "xxx") {
			t.Fatalf("a group whose merged body was refused was still changed: %+v", n)
		}
	}
	older := readNote(t, acts, "lesson-rerun-on-depot")
	if !strings.Contains(older.Body, "Narrowed 2026-10-08 by decision-flakes-only-rerun: reruns are for flakes only") ||
		len(older.Related) != 1 || older.Related[0].ID != "decision-flakes-only-rerun" || older.Status != "current" {
		t.Fatalf("narrow not written: %+v", older)
	}
	if readNote(t, acts, "decision-p").Status != "current" || readNote(t, acts, "decision-q").Status != "current" {
		t.Fatal("a same-date pair was settled without the owner")
	}
	queued, err := os.ReadFile(filepath.Join(acts.Cfg.BrainRoot, ".brain", "owner-questions.jsonl"))
	if err != nil || strings.Count(string(queued), "\n") != 1 || !strings.Contains(string(queued), `"ids":["decision-p","decision-q"]`) {
		t.Fatalf("owner question not queued once: %q %v", queued, err)
	}
	if n := readNote(t, acts, "lesson-use-old-tool"); n.Status != "superseded" ||
		!slices.Contains(readNote(t, acts, "decision-new-tool").Supersedes, n.ID) {
		t.Fatalf("stale note not superseded by its replacement: %+v", n)
	}
	if rep.Deletions != 0 || rep.DeletionsRejected != 0 {
		t.Fatalf("deletions=%d rejected=%d", rep.Deletions, rep.DeletionsRejected)
	}

	finds, bodies := model.counts()
	write(t, filepath.Join(acts.Cfg.BrainRoot, "memory", "feedback-later.md"), "a later capture\n")
	second := runDream(t, acts, Input{Force: true}, mergeT0.Add(time.Hour))
	if s := step(second, "curate.merge"); s.Status != "skipped" || !strings.Contains(s.Reason, "no global note changed") {
		t.Fatalf("the merge's own writes woke the next merge: %+v", s)
	}
	if f, b := model.counts(); f != finds || b != bodies {
		t.Fatalf("model asked again: finds %d→%d bodies %d→%d", finds, f, bodies, b)
	}
}

// TestMergeGateRunsOnChangeOrDaily catches a merge gate that never wakes for a changed global note, or that pays every
// dream when nothing changed: it runs on a change to the global set, and with no change only once 24 h have passed.
func TestMergeGateRunsOnChangeOrDaily(t *testing.T) {
	acts, _ := newMergeBrain(t)
	ctx := t.Context()
	if g, err := acts.MergeGate(ctx, mergeT0); err != nil || g.Skip != "" {
		t.Fatalf("first merge should run: %+v %v", g, err)
	}
	if _, err := acts.MergeDone(ctx, MergeDoneInput{RunID: "r1", Now: mergeT0, Complete: true}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		after  time.Duration
		change bool
		runs   bool
	}{{time.Hour, false, false}, {25 * time.Hour, false, true}, {2 * time.Hour, true, true}} {
		if c.change {
			write(t, filepath.Join(acts.Cfg.BrainRoot, "notes", "decision-p.md"), strings.Replace(
				string(must(os.ReadFile(filepath.Join(acts.Cfg.BrainRoot, "notes", "decision-p.md")))),
				"Agents ask the owner and wait", "Agents ask the owner and wait for the answer", 1))
		}
		g, err := acts.MergeGate(ctx, mergeT0.Add(c.after))
		if err != nil || (g.Skip == "") != c.runs {
			t.Fatalf("after %s (changed=%v): skip=%q err=%v, want runs=%v", c.after, c.change, g.Skip, err, c.runs)
		}
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
