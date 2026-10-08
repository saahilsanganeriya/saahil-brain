package dream

import (
	"testing"

	"go.temporal.io/sdk/worker"
)

// TestDreamReplaysRecordedHistory catches a non-deterministic change to DreamWorkflow — reordered, added or removed
// activities, or a branch on wall-clock state — that would wedge every in-flight dream on the next worker deploy.
// Both histories were recorded on a local dev server against a synthetic brain (testdata/): dream-history.json from
// before curate.merge (it must replay without the step), dream-merge-history.json through a merge that edited notes
// (record_test.go re-records it).
func TestDreamReplaysRecordedHistory(t *testing.T) {
	for _, file := range []string{"testdata/dream-history.json", "testdata/dream-merge-history.json"} {
		replayer := worker.NewWorkflowReplayer()
		replayer.RegisterWorkflow(DreamWorkflow)
		replayer.RegisterWorkflow(LearnSessionWorkflow)
		if err := replayer.ReplayWorkflowHistoryFromJSONFile(nil, file); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
	}
}
