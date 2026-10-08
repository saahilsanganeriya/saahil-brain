package evals

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"saahil.local/brain/internal/index"
)

const glacierSession = "sessions/devbox/2026/10/2026-10-05-aaaa1111-glacier.md"

var glacierStart = time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)

// navRow runs the navigation eval on one item of the fixture's 10-05 session (its first prompt asks for the glacier
// tokenizer state; cwd /tmp/work) and returns its row.
func navRow(t *testing.T, ix *index.Index, targets ...string) NavRow {
	t.Helper()
	env := NewEnv(ix, Sets{Navigation: []NavItem{{ID: "nav-x", Session: glacierSession, Started: glacierStart,
		CWD: "/tmp/work", Targets: targets}}}, testNow)
	rep, err := env.Navigation()
	if err != nil {
		t.Fatal(err)
	}
	row := rep.Rows.([]NavRow)[0]
	if row.Error != "" {
		t.Fatal(row.Error)
	}
	return row
}

func reindex(t *testing.T, ix *index.Index) {
	t.Helper()
	if _, err := ix.Update(context.Background(), false); err != nil {
		t.Fatal(err)
	}
}

// TestL0MemoryIndexReachesAFiledMemoryInOneCall catches an L0-aware navigator that does not open what the as-of
// MEMORY.md links (a target memory whose filed note has a line there is one call away, though no search finds it),
// and one that renders MEMORY.md from today's notes: a note filed from a memory written after the session started
// had no line then.
func TestL0MemoryIndexReachesAFiledMemoryInOneCall(t *testing.T) {
	ix := fixture(t)
	write(t, filepath.Join(ix.Root, "notes", "decision-nimbus-flag.md"), "---\nid: decision-nimbus-flag\ntype: decision\n"+
		"status: current\nscope: global\nsummary: Nimbus flag stays off in staging until the canary passes\nhubs: [box-a]\n"+
		"evidence: [.import/devbox/-proj/nimbus-flag.md]\nsources: [.import/devbox/-proj/nimbus-flag.md]\n---\nFiled.\n")
	reindex(t, ix)
	const memory = "memory-import/-proj/nimbus-flag.md"
	// the note itself is filed today; only its source dates it
	stubFileTimes(t, map[string]time.Time{"notes/decision-nimbus-flag.md": testNow})
	row := navRow(t, ix, memory)
	if row.Calls != 0 || row.L0Calls != 1 || row.Level != "L0" {
		t.Fatalf("memory linked from the as-of MEMORY.md: calls %d, L0-aware calls %d at %q, want 0, 1 at L0",
			row.Calls, row.L0Calls, row.Level)
	}

	stubFileTimes(t, map[string]time.Time{"notes/decision-nimbus-flag.md": testNow, "-proj/nimbus-flag.md": glacierStart.Add(time.Hour)})
	if row := navRow(t, ix, memory); row.Level == "L0" {
		t.Errorf("reached through a MEMORY.md line for a memory written after the session started")
	}
}

// TestL0DigestQuotesOnlyFilesUnchangedSinceTheStart catches a start digest replayed from today's files: the work's
// STATE.md links the plan (one call), but a STATE.md edited after the session started, or a CARD.md born after it,
// held other text then and must give nothing.
func TestL0DigestQuotesOnlyFilesUnchangedSinceTheStart(t *testing.T) {
	ix := fixture(t)
	docs := filepath.Join(filepath.Dir(ix.Root), "Documents", "work-repo", "saahil-docs")
	write(t, filepath.Join(docs, "box-a", "CARD.md"), "---\nid: card-box-a\ntype: card\n---\n# Box A\n\nKey: [run 7](artifacts/run-7.json)\n")
	// an earlier session in the same cwd worked in box-a: that is how the start hook finds the session's work
	write(t, filepath.Join(ix.Root, "sessions", "devbox", "2026", "09", "2026-09-12-dddd4444-box.md"),
		strings.Replace(sessionFile("2026-09-12T10:00:00Z", "work/box-a/STATE.md", "", "box a"), "cwd: /tmp/work\n",
			"cwd: /tmp/work\nworks: [box-a]\n", 1))
	reindex(t, ix)
	const plan, run = "work/box-a/plans/rollout.md", "work/box-a/artifacts/run-7.json"

	if row := navRow(t, ix, plan); row.L0Calls != 1 || row.Level != "L0" || row.Calls != 2 {
		t.Fatalf("plan linked from the work's STATE.md: L0-aware calls %d at %q (navigator %d), want 1 at L0 (navigator 2)",
			row.L0Calls, row.Level, row.Calls)
	}
	if row := navRow(t, ix, run); row.Level != "L0" {
		t.Fatalf("control: run-7.json linked from a CARD.md older than the session is at %q, want L0", row.Level)
	}

	later := glacierStart.Add(time.Hour)
	stubFileTimes(t, map[string]time.Time{"box-a/STATE.md": later, "box-a/CARD.md": later})
	if row := navRow(t, ix, plan); row.Level == "L0" {
		t.Errorf("reached the plan through a STATE.md edited after the session started")
	}
	if row := navRow(t, ix, run); row.Level == "L0" {
		t.Errorf("reached run-7.json through a CARD.md born after the session started")
	}
}
