package evals

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"saahil.local/brain/internal/index"
)

// promptMarker appears only in owner prompts, so finding it in an eval file means prompt text was stored.
const promptMarker = "zebracornish"

var testNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sessionFile(started, touched, transcript string, prompts ...string) string {
	var b strings.Builder
	start, _ := time.Parse(time.RFC3339, started)
	b.WriteString("---\nid: session-x\ntype: session\nmachine: devbox\ncwd: /tmp/work\nstarted: " + started + "\n")
	b.WriteString("ended: " + start.Add(time.Hour).Format(time.RFC3339) + "\n")
	b.WriteString("transcript: " + transcript + "\ntouched: [" + touched + "]\nproduced: []\ncorrections: 1\n---\n\n## Owner prompts\n\n")
	for _, p := range prompts {
		b.WriteString("- 10-05 10:00Z " + p + "\n")
	}
	b.WriteString("\n## What it did\n\nFinal message: done.\n")
	return b.String()
}

func transcriptFile(t *testing.T, path string, prompts ...string) string {
	t.Helper()
	lines := []string{`{"type":"summary"}`}
	for _, p := range prompts {
		line, _ := json.Marshal(map[string]any{"type": "user", "origin": map[string]string{"kind": "human"}, "message": map[string]string{"content": p}})
		lines = append(lines, string(line), `{"type":"assistant","message":{"content":"ok"}}`)
	}
	write(t, path, strings.Join(lines, "\n")+"\n")
	return path
}

// stubFileTimes dates every fixture file 2026-09-01 (older than every fixture session) unless a key of newer ends
// its path: a test cannot set a real file's birth time. A later call replaces an earlier one.
func stubFileTimes(t *testing.T, newer map[string]time.Time) {
	t.Helper()
	old := fileTimes
	t.Cleanup(func() { fileTimes = old })
	fileTimes = func(abs string) (time.Time, time.Time) {
		for suffix, at := range newer {
			if strings.HasSuffix(abs, suffix) {
				return at, at
			}
		}
		at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
		return at, at
	}
}

// shardBrief is a brief file the dispatched fixture session's prompt was pasted from.
const shardBrief = "Update the glacier tokenizer shard plan for the next run and report the shard widths back."

// fixture is a synthetic brain: work docs (as the shadow saahil-docs), notes with a supersession, a lesson learned
// from a session, an imported memory and the note filed from it, and sessions on both sides of each date window.
func fixture(t *testing.T) *index.Index {
	t.Helper()
	stubFileTimes(t, nil)
	home := t.TempDir()
	t.Setenv("BRAIN_HOME", home)
	root := filepath.Join(home, "brain")
	t.Setenv("BRAIN_ROOT", root)
	docs := filepath.Join(home, "Documents", "work-repo", "saahil-docs")
	write(t, filepath.Join(docs, "box-a", "STATE.md"), "# Box A state\n\nThe glacier tokenizer rollout status. Plan: [plan](plans/rollout.md)\n")
	write(t, filepath.Join(docs, "box-a", "plans", "rollout.md"), "# Rollout plan\n\nShard widths for the next run.\n")
	write(t, filepath.Join(docs, "box-a", "README.md"), "# Box A\n\nglacier tokenizer readme\n")
	write(t, filepath.Join(docs, "box-a", "artifacts", "run-7.json"), `{"shards": 8}`+"\n")
	write(t, filepath.Join(docs, "box-a", "briefs", "shard-plan.md"), shardBrief+"\n")
	write(t, filepath.Join(docs, "box-b", "baking.md"), "# Notes\n\nsourdough starter hydration at seventy percent\n")
	write(t, filepath.Join(root, "notes", "decision-glacier-shards.md"), "---\nid: decision-glacier-shards\ntype: decision\nstatus: current\n"+
		"summary: Glacier tokenizer runs use eight shards per worker; wider shards stalled the queue\nhubs: [box-a]\nevidence: [work/box-a/STATE.md]\n---\nBody.\n")
	write(t, filepath.Join(root, "notes", "finding-quartz-old.md"), "---\nid: finding-quartz-old\ntype: finding\nstatus: superseded\n"+
		"summary: Quartz cache misses come from cold starts on the parser pods\nas_of: 2026-09-01\nhubs: [box-a]\nevidence: [work/box-a/STATE.md]\n---\nOld.\n")
	write(t, filepath.Join(root, "notes", "finding-quartz-new.md"), "---\nid: finding-quartz-new\ntype: finding\nstatus: current\n"+
		"summary: Quartz cache misses come from key churn after each deploy\nsupersedes: [finding-quartz-old]\nhubs: [box-a]\nevidence: [work/box-a/STATE.md]\n---\nNew.\n")
	write(t, filepath.Join(root, "notes", "lesson-glacier-retry.md"), "---\nid: lesson-glacier-retry\ntype: lesson\nstatus: current\n"+
		"summary: When a glacier tokenizer shard stalls, retry it alone before widening\nhubs: [box-a]\nevidence: [work/box-a/STATE.md]\n"+
		"sources: [sessions/devbox/2026/10/2026-10-05-aaaa1111-glacier.md]\n---\nSee [rollout](../work/box-a/plans/rollout.md)\n")
	write(t, filepath.Join(home, ".claude", "projects", "-proj", "memory", "nimbus-flag.md"), "---\nname: nimbus flag\n"+
		"description: The nimbus flag stays off in staging until the canary passes\ntype: feedback\n---\nKeep it off.\n")
	write(t, filepath.Join(root, "notes", "decision-nimbus-flag.md"), "---\nid: decision-nimbus-flag\ntype: decision\nstatus: current\n"+
		"summary: Nimbus flag stays off in staging until the canary passes\nhubs: [box-a]\nevidence: [.import/devbox/-proj/nimbus-flag.md]\n"+
		"sources: [.import/devbox/-proj/nimbus-flag.md]\n---\nFiled.\n")

	sessions := filepath.Join(root, "sessions", "devbox", "2026")
	write(t, filepath.Join(sessions, "09", "2026-09-10-bbbb2222-early.md"), sessionFile("2026-09-10T10:00:00Z",
		"work/box-a/plans/rollout.md", "", "glacier rollout "+promptMarker, "no, don't widen the shards"))
	brief := "Please carry out this task from my Orca coordinator. <pasted_content id=\"1\"> You are working inside Orca. " +
		"=== CLI COMMANDS === orca orchestration send ... === TASK === " + shardBrief + " " + promptMarker + " </pasted_content id=\"1\">"
	dispatched := transcriptFile(t, filepath.Join(home, "dispatched.jsonl"), brief)
	write(t, filepath.Join(sessions, "09", "2026-09-25-cccc3333-dispatch.md"), sessionFile("2026-09-25T10:00:00Z",
		"work/box-a/briefs/shard-plan.md, work/box-a/plans/rollout.md", dispatched, brief[:120], "no, stop widening them"))
	// fitted to its size cap, the note lists only the first prompt; the correction survives in the transcript
	typed := transcriptFile(t, filepath.Join(home, "typed.jsonl"), "where is the glacier tokenizer state "+promptMarker,
		"fine, continue", "no, don't widen the shards "+promptMarker)
	write(t, filepath.Join(sessions, "10", "2026-10-05-aaaa1111-glacier.md"), sessionFile("2026-10-05T10:00:00Z",
		"work/box-a/STATE.md, work/box-a/README.md, /tmp/repo/main.go, work/box-a, work/box-a/artifacts", typed,
		"where is the glacier tokenizer state "+promptMarker))

	ix, err := index.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ix.Close() })
	if _, err := ix.Update(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	return ix
}

func buildSets(t *testing.T, ix *index.Index) Sets {
	t.Helper()
	sets, err := Build(ix, BuildOptions{Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	return sets
}

func ids[T any](items []T, id func(T) string) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = id(it)
	}
	return out
}

// TestBuildSplitsByDateAndKeepsOnlyRealTargets catches sets that leak sessions from before their window into the
// evaluation, count generic files, folders, a bulk folder or unindexed repo code as navigation targets, count the
// brief a dispatch was pasted from (the session already holds its text; 21 of 74 DEV hits were only that, on
// 2026-10-08), replay a correction from the wrong prompt, or lose corrections a note dropped to fit its size cap
// (148 real ones were, on 2026-10-08).
func TestBuildSplitsByDateAndKeepsOnlyRealTargets(t *testing.T) {
	sets := buildSets(t, fixture(t))
	nav := ids(sets.Navigation, func(it NavItem) string { return it.ID })
	if want := []string{"nav-2026-09-25-cccc3333", "nav-2026-10-05-aaaa1111"}; !slices.Equal(nav, want) {
		t.Fatalf("navigation items %v, want %v (09-10 is before the 09-20 window)", nav, want)
	}
	if got := sets.Navigation[0].Targets; !slices.Equal(got, []string{"work/box-a/plans/rollout.md"}) {
		t.Errorf("targets %v: the brief the dispatch was pasted from must be dropped", got)
	}
	if got := sets.Navigation[1].Targets; !slices.Equal(got, []string{"work/box-a/STATE.md"}) {
		t.Errorf("targets %v: README, the folder, the bulk artifacts folder and repo code must be dropped", got)
	}
	if !sets.Navigation[0].Dispatched || sets.Navigation[1].Dispatched {
		t.Errorf("dispatched flags wrong: %+v", sets.Navigation)
	}
	if len(sets.Mistakes) != 1 || sets.Mistakes[0].Correction != 2 || sets.Mistakes[0].From != "transcript" ||
		sets.Mistakes[0].Session != "sessions/devbox/2026/10/2026-10-05-aaaa1111-glacier.md" {
		t.Fatalf("mistakes %+v: want only the 10-05 correction at prompt 2 (09-10 and 09-25 are before the cut)", sets.Mistakes)
	}
	if got := sets.Mistakes[0].Lessons; !slices.Equal(got, []string{"lesson-glacier-retry"}) {
		t.Errorf("lessons %v: the lesson citing the session as its source", got)
	}
}

// TestQASetCoversSupersessionMemoriesAndUnknowns catches a QA set that scores a memory as missed when the brain
// answers with the note filed from it, has no as-of question for a superseded fact, or asks an "unknown" the brain
// actually holds.
func TestQASetCoversSupersessionMemoriesAndUnknowns(t *testing.T) {
	sets := buildSets(t, fixture(t))
	byID := map[string]QAItem{}
	for _, it := range sets.QA {
		byID[it.ID] = it
	}
	mem, ok := byID["qa-memory--proj/nimbus-flag.md"]
	if !ok || !slices.Contains(mem.Accept, "decision-nimbus-flag") {
		t.Errorf("memory question %+v must accept the note filed from it", mem)
	}
	if asOf := byID["qa-asof-finding-quartz-old"]; asOf.Target != "finding-quartz-old" || !strings.Contains(asOf.Question, "2026-09-01") {
		t.Errorf("as-of question %+v", asOf)
	}
	if cur := byID["qa-current-finding-quartz-old"]; cur.Target != "finding-quartz-new" {
		t.Errorf("current question %+v must target the superseding note", cur)
	}
	for _, it := range sets.QA {
		if it.Kind == QAUnknown && strings.Contains(it.Question, "sourdough") {
			t.Errorf("unknown %q is in the brain (box-b/baking.md)", it.Question)
		}
	}
	if sets.Manifest.Skipped["qa: unknown topic found in the brain"] != 1 {
		t.Errorf("skipped %v", sets.Manifest.Skipped)
	}
}

// TestBuildIsRerunnable catches a build that appends to, or reorders, the previous run's sets.
func TestBuildIsRerunnable(t *testing.T) {
	ix := fixture(t)
	read := func() string {
		var b strings.Builder
		for _, f := range []string{"navigation.jsonl", "mistakes.jsonl", "qa.jsonl"} {
			data, err := os.ReadFile(filepath.Join(setsDir(ix.Root), f))
			if err != nil {
				t.Fatal(err)
			}
			b.Write(data)
		}
		return b.String()
	}
	buildSets(t, ix)
	first := read()
	buildSets(t, ix)
	if second := read(); second != first {
		t.Errorf("second build differs from the first")
	}
}

// TestTaskTextCutsDispatchPreamble catches dispatched-worker queries made of the Orca preamble (identical CLI
// boilerplate for every worker) instead of the task.
func TestTaskTextCutsDispatchPreamble(t *testing.T) {
	brief := "Please carry out this task. <pasted_content id=\"9\"> You are working inside Orca. === CLI COMMANDS === send " +
		"=== TASK === TASK W1 — rebuild the shard ledger\nTARGET: ledger.go </pasted_content id=\"9\">"
	if got := taskText(brief); got != "TASK W1 — rebuild the shard ledger\nTARGET: ledger.go" {
		t.Errorf("taskText = %q", got)
	}
	if got := taskText("where is the shard ledger"); got != "where is the shard ledger" {
		t.Errorf("a typed prompt must pass whole, got %q", got)
	}
}

// TestNavigatorCountsCallsAndHidesTheSessionsOwnLessons catches a navigator that credits a target reached only
// through a note learned from the evaluated session (knowledge the brain could not have had at its first prompt),
// and one that miscounts the call that first names the target.
func TestNavigatorCountsCallsAndHidesTheSessionsOwnLessons(t *testing.T) {
	ix := fixture(t)
	env := NewEnv(ix, Sets{}, testNow)
	rollout := env.targetsByPath([]string{"work/box-a/plans/rollout.md"})
	open := leakGuard{day: "2026-10-05", sourced: map[string]bool{}}
	if got := env.brainCalls("glacier tokenizer state", env.targetsByPath([]string{"work/box-a/STATE.md"}), open); got != 1 {
		t.Errorf("STATE.md is a search hit: calls = %d, want 1", got)
	}
	if got := env.brainCalls("stalls retry alone widening", rollout, open); got != 2 {
		t.Errorf("the lesson links to the plan: calls = %d, want 2", got)
	}
	guarded := env.guardFor("sessions/devbox/2026/10/2026-10-05-aaaa1111-glacier.md", time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC))
	if got := env.brainCalls("stalls retry alone widening", rollout, guarded); got == 2 {
		t.Errorf("reached the plan through the session's own lesson")
	}
}

// TestMistakesIsNotApplicableBeforeTheDreamLearns catches a repeat-mistake eval that reports 0% (a failure) when no
// lesson has been learned from any session yet, instead of n/a.
func TestMistakesIsNotApplicableBeforeTheDreamLearns(t *testing.T) {
	env := NewEnv(fixture(t), Sets{Mistakes: []MistakeItem{{ID: "m", Session: "sessions/x.md", Correction: 1, Lessons: []string{}}}}, testNow)
	rep, err := env.Mistakes()
	if err != nil {
		t.Fatal(err)
	}
	if m := rep.Metrics[0]; m.NA == "" || m.Pass != nil {
		t.Errorf("metric %+v: want n/a without a verdict", m)
	}
}

// TestEvalFilesKeepNoPromptText catches any set, report row or markdown that stores owner prompt text.
func TestEvalFilesKeepNoPromptText(t *testing.T) {
	ix := fixture(t)
	reports, err := RunAll(NewEnv(ix, Sets{}, testNow), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != len(Names) {
		t.Fatalf("%d reports", len(reports))
	}
	err = filepath.WalkDir(Dir(ix.Root), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if strings.Contains(string(data), promptMarker) {
			t.Errorf("%s holds prompt text", p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	nav := reports[0]
	if rows := nav.Rows.([]NavRow); len(rows) != 2 || rows[0].Query != "transcript" {
		t.Errorf("navigation rows %+v: the dispatched item must be replayed from its transcript", rows)
	}
}

// TestSplitsPartitionItemsBySession catches DEV and TEST metrics that share items or miss some (DEV + TEST must
// be every item), and a split that is not a fixed function of the session, which would let tuning on DEV see TEST.
func TestSplitsPartitionItemsBySession(t *testing.T) {
	env := NewEnv(fixture(t), Sets{}, testNow)
	reports, err := RunAll(env, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, rep := range reports {
		byName := map[string]map[string]int{}
		for _, m := range append(rep.Metrics, rep.SplitMetrics...) {
			if byName[m.Name] == nil {
				byName[m.Name] = map[string]int{}
			}
			byName[m.Name][m.Split] = m.All.N
		}
		for name, n := range byName {
			if n["dev"]+n["test"] != n[""] {
				t.Errorf("%s %q: dev n=%d + test n=%d != all n=%d", rep.Eval, name, n["dev"], n["test"], n[""])
			}
		}
	}
	for _, r := range reports[0].Rows.([]NavRow) {
		if r.Split != SplitOf(r.Session) {
			t.Errorf("%s: split %q, want %q (by session)", r.ID, r.Split, SplitOf(r.Session))
		}
	}
}

// TestRankingSeesTheBrainAsOfTheSessionStart catches a navigation eval that ranks the live index: a file born after
// the session started (no older copy, no earlier session read it) credited as a pointer (a note: the box the prompt
// names is the session's own, never pointed at) or a navigator hit, though a
// brain as of the first prompt could not hold it; one that takes a file re-born by an agent's Edit (a new inode) for a
// new one when an earlier session had read it; and one that keeps a file it cannot find where its path says as old
// (CARDs staged on 2026-10-08 under .brain/staging, presented as work/<box>/CARD.md, became pointers for 10-04 sessions).
func TestRankingSeesTheBrainAsOfTheSessionStart(t *testing.T) {
	ix := fixture(t)
	env := NewEnv(ix, Sets{}, testNow)
	start := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	const prompt = "where is the box-a glacier tokenizer state, and what did the glacier shards decision say"
	state := env.targetsByPath([]string{"work/box-a/STATE.md"})
	decision := env.targetsByPath([]string{"notes/decision-glacier-shards.md"})
	open := env.guardFor("sessions/devbox/2026/10/2026-10-05-aaaa1111-glacier.md", start)
	if !decision.any(env.Ranker.Pointers(prompt, "/tmp/work", open.hides)) || env.brainCalls(prompt, state, open) != 1 {
		t.Fatalf("control: the decision must be a pointer and STATE.md the first search's hit, both older than the session")
	}

	oct6 := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	stubFileTimes(t, map[string]time.Time{"box-a/STATE.md": oct6, "decision-glacier-shards.md": oct6, "box-a/plans/rollout.md": oct6, "box-a/README.md": {}})
	env = NewEnv(ix, Sets{}, testNow)
	g := env.guardFor("sessions/devbox/2026/10/2026-10-05-aaaa1111-glacier.md", start)
	if ptrs := env.Ranker.Pointers(prompt, "/tmp/work", g.hides); decision.any(ptrs) {
		t.Errorf("pointers %v include the decision, born after the session started", ptrs)
	}
	if got := env.brainCalls(prompt, state, g); got != 0 {
		t.Errorf("navigator reached STATE.md, born after the session started, in %d calls", got)
	}
	if g.hides("work/box-a/plans/rollout.md") {
		t.Errorf("rollout.md was re-born after the start, but the 09-25 session read it: it existed")
	}
	if !g.hides("work/box-a/README.md") {
		t.Errorf("README.md is not on disk and the index saw it after the start: it must be dated by that, not kept")
	}
}

// TestNavigationCountsANoteFiledFromATargetMemory catches a navigation eval that scores a pointer to the note filed
// from an imported memory as a miss when the session read the memory itself (QA already counts them as one answer).
func TestNavigationCountsANoteFiledFromATargetMemory(t *testing.T) {
	env := NewEnv(fixture(t), Sets{}, testNow)
	if !env.targetsByPath([]string{"memory-import/-proj/nimbus-flag.md"}).has("decision-nimbus-flag") {
		t.Errorf("decision-nimbus-flag, filed from the target memory, is not a hit")
	}
}

// TestCutoverManifestDatesRewrittenFiles: the cut-over's path rewrite and the sync's replacements gave files a birth
// and modification time of that day, so every older session's targets looked new and the navigation eval fell from
// 71% to 49%; the manifest taken before the move keeps the older date.
func TestCutoverManifestDatesRewrittenFiles(t *testing.T) {
	cutDay := time.Date(2026, 10, 8, 12, 43, 0, 0, time.UTC)
	stubFileTimes(t, map[string]time.Time{"plans/SPEC.md": cutDay, "plans/NEW.md": cutDay})
	x := &existence{root: t.TempDir(), touched: map[string]time.Time{}, times: map[string]fileDates{},
		cutover: map[string]time.Time{"work/box/plans/SPEC.md": time.Date(2026, 10, 6, 4, 42, 0, 0, time.UTC)}}
	session := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	if !x.before("work/box/plans/SPEC.md", session) {
		t.Fatal("a file listed before the cut-over with an older mtime existed at the session's start")
	}
	if x.before("work/box/plans/NEW.md", session) {
		t.Fatal("a file the manifest does not list keeps its own dates")
	}
}

// TestVettedTargetsStayVisible: the set builder checked each target existed before its session, with the file's
// original dates; the cut-over's rewrite replaced those files, so the run-time check hid three such targets.
func TestVettedTargetsStayVisible(t *testing.T) {
	cutDay := time.Date(2026, 10, 8, 13, 29, 0, 0, time.UTC)
	stubFileTimes(t, map[string]time.Time{"COMMON.md": cutDay, "OTHER.md": cutDay})
	x := &existence{root: t.TempDir(), touched: map[string]time.Time{}, times: map[string]fileDates{},
		paths: map[string]string{"work/box/specs/COMMON.md": "work/box/specs/COMMON.md", "work/box/specs/OTHER.md": "work/box/specs/OTHER.md"}}
	g := leakGuard{start: time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC), sourced: map[string]bool{}, exist: x,
		vetted: map[string]bool{"work/box/specs/COMMON.md": true}}
	if g.hides("work/box/specs/COMMON.md#scope") || !g.hides("work/box/specs/OTHER.md") {
		t.Fatal("a vetted target (and its sections) must stay visible; any other file keeps the existence check")
	}
}
