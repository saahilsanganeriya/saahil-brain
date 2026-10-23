package skills

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/delta"
	"saahil.local/brain/templates"
)

var judgment = []string{"file", "distil", "judge", "link", "hubs", "summarise", "verify", "wrap"}

var jsonBlock = regexp.MustCompile("(?s)```json\n(.*?)```")

func seedHubs(t *testing.T) map[string]delta.Note {
	t.Helper()
	entries, err := fs.ReadDir(templates.FS, "hubs")
	if err != nil {
		t.Fatal(err)
	}
	hubs := map[string]delta.Note{}
	for _, e := range entries {
		b, _ := fs.ReadFile(templates.FS, "hubs/"+e.Name())
		n, err := delta.Parse(b)
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		hubs[n.ID] = n
	}
	return hubs
}

// TestSkillExamplesAndSeedHubsPassLint catches a skill whose example answer is not a valid delta or would be
// rejected by the applier (bad id, no hub, over a cap, a hub that is not seeded) — a model copies its examples — and
// a seed hub that breaks the hub rules (broader chain, caps), standing in for `brain lint` until it lands.
func TestSkillExamplesAndSeedHubsPassLint(t *testing.T) {
	hubs := seedHubs(t)
	resolve := func(kind, ref string) bool {
		if kind == "hub" {
			_, ok := hubs[ref]
			return ok
		}
		return true // examples cite synthetic paths and ids
	}
	for _, name := range judgment {
		b, err := fs.ReadFile(FS, name+".md")
		if err != nil {
			t.Fatalf("skill %s missing: %v", name, err)
		}
		text := string(b)
		for _, section := range []string{"## Purpose", "## Input", "## Output", "## Rules", "## Examples"} {
			if !strings.Contains(text, section) {
				t.Errorf("%s: no %q section", name, section)
			}
		}
		answers := 0
		for _, m := range jsonBlock.FindAllStringSubmatch(text, -1) {
			if !strings.Contains(m[1], `"ops"`) || strings.Contains(m[1], "…") {
				continue // an input example or the schema sketch
			}
			var d delta.Delta
			if err := json.Unmarshal([]byte(m[1]), &d); err != nil {
				t.Errorf("%s: example answer is not a delta: %v\n%s", name, err, m[1])
				continue
			}
			answers++
			for _, op := range d.Ops {
				if op.Op == "delete" {
					t.Errorf("%s: an example deletes", name)
				}
				if op.Note == nil {
					continue
				}
				if probs := delta.Check(*op.Note, delta.Render(*op.Note), resolve); len(probs) > 0 {
					t.Errorf("%s: example note %s: %v", name, op.Note.ID, probs)
				}
			}
		}
		if answers < 2 {
			t.Errorf("%s: %d example answers, want 2", name, answers)
		}
	}
	for id, h := range hubs {
		if probs := delta.Check(h, delta.Render(h), resolve); len(probs) > 0 {
			t.Errorf("seed hub %s: %v", id, probs)
		}
		depth, at := 0, h
		for len(at.Broader) > 0 && depth < 5 {
			at, depth = hubs[at.Broader[0]], depth+1
		}
		if at.ID != "index" || depth > 3 {
			t.Errorf("seed hub %s: broader chain does not reach index within 3 steps", id)
		}
	}
	for file, max := range map[string]int{"brain.md": 40, "brain-write.md": 35} {
		b, err := fs.ReadFile(FS, file)
		if err != nil || strings.Count(string(b), "\n") > max {
			t.Errorf("%s: missing or over %d lines (%v)", file, max, err)
		}
	}
	if b, _ := fs.ReadFile(templates.FS, "BRAIN.md"); strings.Count(string(b), "\n") > 80 {
		t.Errorf("BRAIN.md is over 80 lines")
	}
}

// noteWriters are the skills whose answers write a note's summary or body (hubs edits hub pages, link only aliases).
var noteWriters = []string{"file", "distil", "verify", "wrap"}

// TestLinkSkillSaysWhatAFullNoteGets catches the link skill naming the per-note link cap only in its output sketch: with
// no rule for a note that already holds the cap, it proposed a sixth related link on the notes it was handed and the
// applier refused 60 link ops in 16 dreams of 2026-10-23, one note 15 times.
func TestLinkSkillSaysWhatAFullNoteGets(t *testing.T) {
	b, err := fs.ReadFile(FS, "link.md")
	if err != nil {
		t.Fatal(err)
	}
	rule := fmt.Sprintf("already holds %d", config.LinksPerKindMax)
	if !strings.Contains(string(b), rule) {
		t.Errorf("link.md has no rule for a note that %s `about` or `related` links", rule)
	}
}

// TestNoteWritingSkillsStateTheCaps catches a skill that writes note text without stating the caps the applier
// refuses it for: verify never named them, and the applier refused 43% of its ops (summaries over 160 chars, lesson
// bodies over 300) while the step stayed ok.
func TestNoteWritingSkillsStateTheCaps(t *testing.T) {
	caps := []string{
		fmt.Sprintf("≤ %d char", config.SummaryMaxChars),
		fmt.Sprintf("≤ %d char", config.LessonMaxChars),
		fmt.Sprintf("≤ %d KB", config.NoteMaxBytes/1024),
	}
	for _, name := range noteWriters {
		b, err := fs.ReadFile(FS, name+".md")
		if err != nil {
			t.Fatalf("skill %s missing: %v", name, err)
		}
		for _, c := range caps {
			if !strings.Contains(string(b), c) {
				t.Errorf("%s: does not state the cap %q", name, c)
			}
		}
	}
}
