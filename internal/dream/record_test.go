//go:build record

package dream

import (
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
)

// TestRecordMergeHistory and TestRecordCardsHistory are harnesses, not tests: each re-records a replay fixture from
// one dream over a synthetic brain on a throwaway dev server (a free loopback port, in memory), never the brain's own
// Temporal.
//
//	go test -tags record -run 'TestRecord(Merge|Cards)History' ./internal/dream
func TestRecordMergeHistory(t *testing.T) {
	acts, _ := newMergeBrain(t)
	rep := recordDream(t, acts, "dream/record-merge", "testdata/dream-merge-history.json")
	if s := step(rep, "curate.merge"); s.Status != "ok" || s.Edits == 0 {
		t.Fatalf("the recorded dream did not merge: %+v", s)
	}
}

func TestRecordCardsHistory(t *testing.T) {
	acts, _ := newCardsBrain(t, 0.05, map[string]time.Duration{"busy": 2 * time.Hour, "quiet": 20 * time.Minute})
	rep := recordDream(t, acts, "dream/record-cards", "testdata/dream-cards-history.json")
	if s := step(rep, cardsStepName); s.Status != "ok" || s.Edits != 1 {
		t.Fatalf("the recorded dream did not refresh a CARD: %+v", s)
	}
}

func recordDream(t *testing.T, acts *Activities, id, file string) Report {
	t.Helper()
	bin, err := exec.LookPath("temporal")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := testsuite.StartDevServer(t.Context(), testsuite.DevServerOptions{ExistingPath: bin,
		ClientOptions: &client.Options{Namespace: Namespace}, LogLevel: "error", Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	dreamWorker := worker.New(srv.Client(), TaskQueue, worker.Options{})
	dreamWorker.RegisterWorkflow(DreamWorkflow)
	dreamWorker.RegisterWorkflow(LearnSessionWorkflow)
	dreamWorker.RegisterActivity(acts)
	opus, haiku := worker.New(srv.Client(), QueueOpus, worker.Options{}), worker.New(srv.Client(), QueueHaiku, worker.Options{})
	for _, w := range []worker.Worker{opus, haiku} {
		w.RegisterActivityWithOptions(acts.SkillStep, activity.RegisterOptions{Name: "LLMStep"})
	}
	opus.RegisterActivityWithOptions(acts.MergeStep, activity.RegisterOptions{Name: "MergeStep"})
	haiku.RegisterActivityWithOptions(acts.BackfillCard, activity.RegisterOptions{Name: "BackfillCard"})
	for _, w := range []worker.Worker{dreamWorker, opus, haiku} {
		if err := w.Start(); err != nil {
			t.Fatal(err)
		}
		defer w.Stop()
	}
	run, err := srv.Client().ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{ID: id, TaskQueue: TaskQueue},
		DreamWorkflow, Input{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	var rep Report
	if err := run.Get(t.Context(), &rep); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "workflow", "show", "-w", id, "-n", Namespace, "--address", srv.FrontendHostPort(),
		"--output", "json").Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, out, 0o644); err != nil {
		t.Fatal(err)
	}
	return rep
}
