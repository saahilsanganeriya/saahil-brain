package parse

import (
	"reflect"
	"strings"
	"testing"
)

// TestParseSectionsLineRanges catches off-by-one section ranges, a "## " inside a fenced code block read as a
// heading, and frontmatter lines dropped from the line count — any of which sends an agent to the wrong lines.
func TestParseSectionsLineRanges(t *testing.T) {
	src := strings.Join([]string{
		"---",                     // 1
		"type: spec",              // 2
		"hubs: [image-lane, ocr]", // 3
		"related: [{id: decision-x, why: same budget}]", // 4
		"---",       // 5
		"# Spec v2", // 6
		"",          // 7
		"Intro paragraph that summarises the spec.", // 8
		"## Scope",         // 9
		"```sh",            // 10
		"## not a heading", // 11
		"```",              // 12
		"### Detail",       // 13
		"## Plan",          // 14
		"see [ocr](../reports/ocr.md#recall) and [[finding-x]] and PR #3488, ABC-12", // 15
	}, "\n") + "\n"
	d := Markdown([]byte(src))

	if d.Title != "Spec v2" || d.Summary != "Intro paragraph that summarises the spec." || d.Type != "spec" {
		t.Fatalf("title=%q summary=%q type=%q", d.Title, d.Summary, d.Type)
	}
	type rng struct {
		Heading    string
		Start, End int
	}
	var got []rng
	for _, s := range d.Sections() {
		got = append(got, rng{s.Heading, s.LineStart, s.LineEnd})
	}
	want := []rng{{"Spec v2", 6, 8}, {"Scope", 9, 13}, {"Plan", 14, 15}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sections = %v, want %v", got, want)
	}
	if b := d.Sections()[2].Bytes; b != len("## Plan\n")+len(strings.Split(src, "\n")[14])+1 {
		t.Fatalf("Plan bytes = %d", b)
	}
	if !reflect.DeepEqual(d.Hubs, []string{"image-lane", "ocr"}) {
		t.Fatalf("hubs = %v", d.Hubs)
	}
	if len(d.Authored) != 3 || d.Authored[2] != (Rel{Rel: "related", Dst: "decision-x", Why: "same budget"}) {
		t.Fatalf("authored = %+v", d.Authored)
	}
	wantLinks := []Link{{Target: "../reports/ocr.md"}, {Target: "finding-x", Wiki: true}}
	if !reflect.DeepEqual(d.Links, wantLinks) {
		t.Fatalf("links = %+v", d.Links)
	}
	if !reflect.DeepEqual(d.Mentions, []string{"pr:example-org/work-repo#3488", "ticket:ABC-12"}) {
		t.Fatalf("mentions = %v", d.Mentions)
	}
}

// TestShapeOfJSON catches a shape reader that fails on a 64 KB prefix cut mid-value, or that reports nested keys
// as top-level ones.
func TestShapeOfJSON(t *testing.T) {
	cases := map[string]struct{ data, want string }{
		"object": {`{"runs": {"a": 1, "b": 2}, "total": 3, "cfg": [1, 2]}`, "json object: 3 keys: runs, total, cfg"},
		"array":  {`[{"id": 1, "label": "x"}, {"id": 2}]`, "json array: 2 items, first keys: id, label"},
		"cut":    {`{"meta": {"v": 1}, "rows": [1, 2, 3`, "json object: ≥2 keys (cut at 64 KB): meta, rows"},
	}
	for name, c := range cases {
		if got := Shape("x.json", []byte(c.data)); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
	if got := Shape("r.jsonl", []byte("{\"doc\": 1, \"spans\": []}\n{\"doc\": 2}\n")); got != "jsonl: first record keys: doc, spans" {
		t.Errorf("jsonl: %q", got)
	}
	if got := Shape("t.csv", []byte("id,name,score\n1,a,2\n")); got != "csv: 3 columns: id, name, score" {
		t.Errorf("csv: %q", got)
	}
}
