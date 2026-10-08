package dream

import (
	"testing"

	"go.temporal.io/sdk/worker"
)

// TestDreamReplaysRecordedHistory catches a non-deterministic change to DreamWorkflow — reordered, added or removed
// activities, or a branch on wall-clock state — that would wedge every in-flight dream on the next worker deploy.
// The history was recorded from a run on the local dev server against a synthetic brain (testdata/).
func TestDreamReplaysRecordedHistory(t *testing.T) {
	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflow(DreamWorkflow)
	if err := replayer.ReplayWorkflowHistoryFromJSONFile(nil, "testdata/dream-history.json"); err != nil {
		t.Fatal(err)
	}
}
