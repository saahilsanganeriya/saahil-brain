package hooks

import "testing"

// TestNamedRelevanceScoresLikeTheTextQuery catches a named node scored apart from the text query: the ranker merges
// the two by node and weighs one relevance, so a node found by name and by text must carry the same BM25, and a named
// node that no term matches carries none.
func TestNamedRelevanceScoresLikeTheTextQuery(t *testing.T) {
	f := newFixture(t, true, nil)
	terms := []string{"paddleocr", "recall"}
	text, err := query(f.db, terms, nil)
	if err != nil {
		t.Fatal(err)
	}
	var textRel float64
	for _, c := range text {
		if c.id == "finding-ocr-recall" {
			textRel = c.rel
		}
	}
	if textRel == 0 {
		t.Fatalf("the text query did not rank finding-ocr-recall: %v", text)
	}
	named := []cand{{id: "finding-tabular-nulls"}, {id: "finding-ocr-recall"}}
	if err := namedRelevance(f.db, named, terms); err != nil {
		t.Fatal(err)
	}
	if named[1].rel != textRel {
		t.Errorf("named finding-ocr-recall rel %v, text query %v: one node, one relevance", named[1].rel, textRel)
	}
	if named[0].rel != 0 {
		t.Errorf("named finding-tabular-nulls rel %v; want 0: no term matches it", named[0].rel)
	}
}
