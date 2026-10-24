package dream

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A gate that walks the folders the index never reads lists every run output there: on 2026-10-24 it walked 10 M
// entries of work/ (three 15-min timeouts lost a dream) and queued 8,870 big .md run outputs for summaries no search
// can show.
func TestGateListsOnlyDocsTheIndexReads(t *testing.T) {
	acts, _ := newBrain(t, 0)
	big := strings.Repeat("a line of a long document\n", bigDocBytes/20)
	box := filepath.Join(acts.Cfg.BrainRoot, "work", "sample-box")
	write(t, filepath.Join(box, "plans", "PLAN.md"), big)
	for _, bulk := range []string{"artifacts/run-1", "node_modules/pkg", "cache-runs"} {
		write(t, filepath.Join(box, bulk, "REPORT.md"), big)
	}

	res, err := acts.Gate(context.Background(), GateInput{RunID: "r", Now: time.Now().UTC(), Force: true})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range res.BigDocs {
		got = append(got, d.Rel)
	}
	if res.Counts.Docs != 1 || len(got) != 1 || got[0] != filepath.Join("work", "sample-box", "plans", "PLAN.md") {
		t.Fatalf("gate docs: %d listed, big %v; want only the plan (artifacts/, node_modules/ and cache* are never indexed)",
			res.Counts.Docs, got)
	}
}
