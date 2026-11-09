package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"saahil.local/brain/internal/dream"
)

func writeDreamRun(t *testing.T, root, id, status, errText string) {
	t.Helper()
	writeDreamReport(t, root, dream.Report{RunID: id, Status: status, Error: errText,
		Steps: []dream.StepRecord{{Name: "gate", Status: "ok"}, {Name: "snapshot", Status: status}}})
}

func writeDreamReport(t *testing.T, root string, rep dream.Report) {
	t.Helper()
	b, _ := json.Marshal(rep)
	dir := filepath.Join(root, ".brain", "dream-runs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rep.RunID+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDreamsCompletedWithFailedStepsTurnTheDoctorRed catches five dreams in a row that each "completed" with learn,
// merge, cards and summarise failed (every model call hung to its activity timeout: the model endpoint was
// unreachable) while dream.last said ok on both machines for two and a half days.
func TestDreamsCompletedWithFailedStepsTurnTheDoctorRed(t *testing.T) {
	root := t.TempDir()
	timeout := "activity error (type: LLMStep, scheduledEventID: 79, startedEventID: 80, identity: ): " +
		"activity StartToClose timeout (type: StartToClose)"
	hung := func(id string) dream.Report {
		return dream.Report{RunID: id, Status: "completed", Steps: []dream.StepRecord{
			{Name: "gate", Status: "ok"}, {Name: "learn.batched", Status: "failed", Reason: timeout},
			{Name: "curate.hubs", Status: "skipped", Reason: "no input"},
			{Name: "curate.merge", Status: "failed", Reason: timeout}, {Name: "commit", Status: "ok"}}}
	}
	writeDreamRun(t, root, "2026-11-06T220000Z-a", "completed", "")
	writeDreamReport(t, root, hung("2026-11-07T110000Z-b"))
	status, detail := dreamRuns(root)
	if status != Warn {
		t.Fatalf("one dream with failed steps: status %s (%s), want warn", status, detail)
	}
	writeDreamReport(t, root, hung("2026-11-07T230000Z-c"))
	status, detail = dreamRuns(root)
	if status != Fail {
		t.Fatalf("two dreams in a row with failed steps: status %s (%s), want fail", status, detail)
	}
	for _, want := range []string{"2 in a row", "2026-11-07T110000Z-b", "learn.batched, curate.merge", "StartToClose timeout"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail %q does not name %q", detail, want)
		}
	}
	if strings.Contains(detail, "scheduledEventID") || strings.Contains(detail, "curate.hubs") {
		t.Fatalf("detail %q keeps the activity wrapper or names a skipped step", detail)
	}
}

// TestDreamRunsFailingInARowTurnTheDoctorRed catches 14 hourly dreams failing at the same step while the doctor stayed
// green on both machines: no check read .brain/dream-runs/.
func TestDreamRunsFailingInARowTurnTheDoctorRed(t *testing.T) {
	root := t.TempDir()
	lockErr := "snapshot: activity error (type: Snapshot, scheduledEventID: 11, startedEventID: 12, identity: 1@host@): " +
		"git add: exit status 128: fatal: Unable to create '/Users/test-user/brain/.git/index.lock': File exists.\n\nAnother git process"
	writeDreamRun(t, root, "2026-10-28T230000Z-a", "completed", "")
	writeDreamRun(t, root, "2026-10-29T090000Z-b", "failed", lockErr)
	status, detail := dreamRuns(root)
	if status != Warn {
		t.Fatalf("one failed dream: status %s (%s), want warn", status, detail)
	}
	writeDreamRun(t, root, "2026-10-29T100000Z-c", "failed", lockErr)
	status, detail = dreamRuns(root)
	if status != Fail {
		t.Fatalf("two failed dreams in a row: status %s (%s), want fail", status, detail)
	}
	for _, want := range []string{"2 in a row", "2026-10-29T090000Z-b", "snapshot", "index.lock': File exists"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail %q does not name %q", detail, want)
		}
	}
	if strings.Contains(detail, "scheduledEventID") || strings.Contains(detail, "Another git process") {
		t.Fatalf("detail %q keeps the activity wrapper or the hint lines", detail)
	}
	writeDreamRun(t, root, "2026-10-29T110000Z-d", "completed", "")
	if status, detail = dreamRuns(root); status != OK {
		t.Fatalf("a completed dream after failures: status %s (%s), want ok", status, detail)
	}
}
