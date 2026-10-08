package hooks

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"saahil.local/brain/internal/rank"
)

const ocrPrompt = "what was the ocr recall on the image census?"

// rows counts this session's lookups rows of one hook with the given candidates and chosen JSON.
func (f fixture) rows(t *testing.T, session, hook, cands, chosen string) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(`SELECT count(*) FROM lookups WHERE session = ? AND hook = ? AND candidates = ? AND chosen = ?`,
		session, hook, cands, chosen).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f fixture) post(t *testing.T, session, tool string, input any) {
	t.Helper()
	raw, _ := json.Marshal(input)
	if err := Post(f.env, Input{SessionID: session, Cwd: f.cwd, ToolName: tool, ToolInput: raw}); err != nil {
		t.Fatal(err)
	}
}

// TestReadsByBashAndBrainToolsCountAsOpened catches the usage funnel missing how agents really read: a pointed-at note
// read with cat, sed -n, head or rg (by absolute path, ~, a shell variable or after a cd) or opened with brain_show /
// `brain show` must count opened once per session, and log the post row the dream's learn step reads; an in-place
// sed, a redirect into the file, a heredoc body, a search of its folder or a read of an unshown note must not. Brain
// search, show, sections and neighbors mark the session as pulling from the brain, once.
func TestReadsByBashAndBrainToolsCountAsOpened(t *testing.T) {
	f := newFixture(t, true, nil)
	p := filepath.Join(f.env.Root, "notes", "finding-ocr-recall.md")
	for i, c := range []struct {
		tool         string
		input        any
		opened, pull bool
	}{
		{"Bash", map[string]string{"command": "cat " + p}, true, false},
		{"Bash", map[string]string{"command": "sed -n '1,40p' ~/brain/notes/finding-ocr-recall.md"}, true, false},
		{"Bash", map[string]string{"command": "B=" + f.env.Root + "; head -50 \"$B/notes/finding-ocr-recall.md\" | tail -5"}, true, false},
		{"Bash", map[string]string{"command": "cd " + f.env.Root + "/notes && rg -n recall finding-ocr-recall.md 2>/dev/null"}, true, false},
		{"Bash", map[string]string{"command": "/usr/local/bin/brain show finding-ocr-recall --json"}, true, true},
		{"mcp__saahil-brain__brain_show", map[string]string{"id": "finding-ocr-recall"}, true, true},
		{"mcp__saahil-brain__brain_sections", map[string]string{"id": "notes/finding-ocr-recall.md"}, true, true},
		{"mcp__saahil-brain__brain_search", map[string]string{"query": "ocr recall"}, false, true},
		{"Bash", map[string]string{"command": "brain search ocr recall | head"}, false, true},
		{"Bash", map[string]string{"command": "sed -i 's/0.91/0.92/' " + p}, false, false},
		{"Bash", map[string]string{"command": "cat > " + p + " <<'EOF'\nnew text\nEOF"}, false, false},
		{"Bash", map[string]string{"command": "python3 - <<'EOF'\ncat " + p + "\nEOF"}, false, false},
		{"Bash", map[string]string{"command": "rg recall " + f.env.Root + "/notes"}, false, false},
		{"Bash", map[string]string{"command": "cat " + filepath.Join(f.env.Root, "notes", "finding-tabular-nulls.md")}, false, false},
		{"Bash", map[string]string{"command": "echo " + p}, false, false},
	} {
		session := fmt.Sprintf("s%d", i)
		f.prompt(t, session, ocrPrompt)
		f.post(t, session, c.tool, c.input)
		f.post(t, session, c.tool, c.input)
		opened := f.rows(t, session, "post", `["opened"]`, `["finding-ocr-recall"]`)
		var pulled int
		f.db.QueryRow(`SELECT count(*) FROM lookups WHERE session = ? AND hook = 'pull'`, session).Scan(&pulled)
		if want := map[bool]int{true: 1}[c.opened]; opened != want {
			t.Errorf("%s %v: %d opened rows, want %d", c.tool, c.input, opened, want)
		}
		if want := map[bool]int{true: 1}[c.pull]; pulled != want {
			t.Errorf("%s %v: %d pull rows, want %d", c.tool, c.input, pulled, want)
		}
	}
	var opened int
	f.db.QueryRow(`SELECT opened FROM node_stats WHERE id = 'finding-ocr-recall'`).Scan(&opened)
	if opened != 7 {
		t.Errorf("node_stats.opened = %d, want one per session that read it (7)", opened)
	}
}

// TestEndCountsIgnoredOncePerSession catches a funnel that never learns what was ignored: at PostCompact and again at
// SessionEnd, each pointer shown and never opened counts ignored exactly once, and one the agent read counts nothing.
func TestEndCountsIgnoredOncePerSession(t *testing.T) {
	f := newFixture(t, true, nil)
	f.prompt(t, "s1", ocrPrompt)
	f.prompt(t, "s1", "what does decision-paddle-first say about scans?")
	st, _ := f.env.loadState("s1")
	if len(st.Shown) < 2 {
		t.Fatalf("fixture showed %d pointers, need two", len(st.Shown))
	}
	f.post(t, "s1", "Read", map[string]string{"file_path": st.Shown["finding-ocr-recall"]})
	for _, event := range []string{"PostCompact", "SessionEnd"} {
		if err := End(f.env, Input{SessionID: "s1", Event: event}); err != nil {
			t.Fatal(err)
		}
	}
	var want []string
	for id := range st.Shown {
		if id != "finding-ocr-recall" {
			want = append(want, id)
		}
	}
	sort.Strings(want)
	for _, id := range append(want, "finding-ocr-recall") {
		var ignored int
		f.db.QueryRow(`SELECT ignored FROM node_stats WHERE id = ?`, id).Scan(&ignored)
		if exp := map[bool]int{true: 1}[id != "finding-ocr-recall"]; ignored != exp {
			t.Errorf("%s ignored %d, want %d", id, ignored, exp)
		}
	}
	ids, _ := json.Marshal(want)
	if n := f.rows(t, "s1", "end", `["ignored"]`, string(ids)); n != 1 {
		t.Errorf("%d end rows for %s, want 1", n, ids)
	}
}

// TestDemotedNoteScoresItsPrior catches the dream's demotion never reaching the pointers: the same prompt must score
// a note the dream marked demoted (shown ≥ 10 times, never opened) exactly rank.DemotedPrior lower.
func TestDemotedNoteScoresItsPrior(t *testing.T) {
	f := newFixture(t, true, nil)
	// a borderline pointer drops under the threshold once demoted; keep it chosen to read its score
	threshold := Threshold
	Threshold = 0
	t.Cleanup(func() { Threshold = threshold })
	read := func(p string) []byte { b, _ := os.ReadFile(filepath.Join(f.env.Root, p)); return b }
	nm, err := BuildNames(f.db, read)
	if err != nil {
		t.Fatal(err)
	}
	score := func() float64 {
		lk, err := Pointers(f.db, ocrPrompt, Seeds{}, nm)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range lk.Chosen {
			if p.ID == "finding-ocr-recall" {
				return p.Score
			}
		}
		t.Fatalf("finding-ocr-recall not chosen: %+v", lk.Chosen)
		return 0
	}
	before := score()
	if _, err := f.db.Exec(`INSERT INTO node_stats(id, shown, demoted_at) VALUES ('finding-ocr-recall', 10, '2026-10-08T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if got := score() - before; math.Abs(got-rank.DemotedPrior) > 0.0015 {
		t.Fatalf("demoted note moved %.3f, want %.3f", got, rank.DemotedPrior)
	}
}
