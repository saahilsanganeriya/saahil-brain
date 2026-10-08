//go:build record

package dream

import (
	"io"
	"os"
	"os/exec"
	"testing"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
)

// TestRecordMergeHistory is a harness, not a test: it re-records testdata/dream-merge-history.json from one dream over
// newMergeBrain on a throwaway dev server (a free loopback port, in memory), never the brain's own Temporal.
//
//	go test -tags record -run TestRecordMergeHistory ./internal/dream
func TestRecordMergeHistory(t *testing.T) {
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
	acts, _ := newMergeBrain(t)
	dreamWorker := worker.New(srv.Client(), TaskQueue, worker.Options{})
	dreamWorker.RegisterWorkflow(DreamWorkflow)
	dreamWorker.RegisterWorkflow(LearnSessionWorkflow)
	dreamWorker.RegisterActivity(acts)
	opus, haiku := worker.New(srv.Client(), QueueOpus, worker.Options{}), worker.New(srv.Client(), QueueHaiku, worker.Options{})
	for _, w := range []worker.Worker{opus, haiku} {
		w.RegisterActivityWithOptions(acts.SkillStep, activity.RegisterOptions{Name: "LLMStep"})
	}
	opus.RegisterActivityWithOptions(acts.MergeStep, activity.RegisterOptions{Name: "MergeStep"})
	for _, w := range []worker.Worker{dreamWorker, opus, haiku} {
		if err := w.Start(); err != nil {
			t.Fatal(err)
		}
		defer w.Stop()
	}
	const id = "dream/record-merge"
	run, err := srv.Client().ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{ID: id, TaskQueue: TaskQueue},
		DreamWorkflow, Input{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	var rep Report
	if err := run.Get(t.Context(), &rep); err != nil {
		t.Fatal(err)
	}
	if s := step(rep, "curate.merge"); s.Status != "ok" || s.Edits == 0 {
		t.Fatalf("the recorded dream did not merge: %+v", s)
	}
	out, err := exec.Command(bin, "workflow", "show", "-w", id, "-n", Namespace, "--address", srv.FrontendHostPort(),
		"--output", "json").Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("testdata/dream-merge-history.json", out, 0o644); err != nil {
		t.Fatal(err)
	}
}
