package rank

import (
	"context"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"saahil.local/brain/internal/graph"
	"saahil.local/brain/internal/scan"
	"saahil.local/brain/internal/store"
)

// TestPushMatchesPowerIteration catches a local-push PPR that drifts from the true personalized PageRank (wrong
// residual share, wrong teleport, lost mass): L1 error against power iteration must stay below 1e-3.
func TestPushMatchesPowerIteration(t *testing.T) {
	const n = 300
	rng := rand.New(rand.NewPCG(7, 11))
	adj := make([]map[int]bool, n)
	for i := range adj {
		adj[i] = map[int]bool{}
	}
	link := func(a, b int) {
		if a != b {
			adj[a][b], adj[b][a] = true, true
		}
	}
	for i := 0; i < n; i++ {
		link(i, (i+1)%n) // a ring keeps every node reachable, no dangling nodes
		for k := 0; k < 2; k++ {
			link(i, rng.IntN(n))
		}
	}
	key := strconv.Itoa
	neighbors := func(u string) []string {
		i, _ := strconv.Atoi(u)
		var out []string
		for v := range adj[i] {
			out = append(out, key(v))
		}
		return out
	}
	seeds := map[string]float64{"0": 0.7, "42": 0.3}

	got := Push(neighbors, seeds, 1e-9)

	alpha := 1 - Damping
	p := make([]float64, n)
	for it := 0; it < 500; it++ {
		next := make([]float64, n)
		next[0], next[42] = alpha*0.7, alpha*0.3
		for u := 0; u < n; u++ {
			share := (1 - alpha) * p[u] / float64(len(adj[u]))
			for v := range adj[u] {
				next[v] += share
			}
		}
		p = next
	}
	l1 := 0.0
	for i := 0; i < n; i++ {
		l1 += math.Abs(p[i] - got[key(i)])
	}
	if l1 >= 1e-3 {
		t.Fatalf("L1 error %.6f ≥ 1e-3", l1)
	}
}

// TestExcludesSuperseded catches search results that surface what must never be ranked: a superseded note, a note
// another note supersedes, a byte copy, an older version, a session note, a log file.
func TestExcludesSuperseded(t *testing.T) {
	brain := t.TempDir()
	files := map[string]string{
		"notes/finding-ocr-new.md":                           "---\nid: finding-ocr-new\ntype: finding\nstatus: current\nsupersedes: [finding-ocr-old]\n---\nOCR recall gate measured.\n",
		"notes/finding-ocr-old.md":                           "---\nid: finding-ocr-old\ntype: finding\nstatus: current\n---\nOCR recall gate guessed.\n",
		"notes/finding-ocr-retro.md":                         "---\nid: finding-ocr-retro\ntype: finding\nstatus: superseded\n---\nOCR recall gate retro.\n",
		"work/box/SPEC.md":                                   "# Spec\n\nOCR recall gate spec.\n",
		"work/box/SPEC.v1.md":                                "# Spec v1\n\nOCR recall gate first spec.\n",
		"work/snap/SPEC.md":                                  "# Spec\n\nOCR recall gate spec.\n",
		"work/box/log/2026-10-08.md":                         "OCR recall gate log line.\n",
		"sessions/devbox/2026/10/2026-10-08-abcd1234-ocr.md": "---\ntype: session\n---\nOCR recall gate session.\n",
		"memory-import/-proj/ocr-recall-gate.md":             "---\nname: ocr\n---\nOCR recall gate raw memory.\n",
	}
	old := filepath.Join(brain, "work/box/SPEC.v1.md")
	st := indexFiles(t, brain, files, func() {
		_ = os.Chtimes(old, timeAgo(2), timeAgo(2))
		_ = os.Chtimes(filepath.Join(brain, "work/box/SPEC.md"), timeAgo(1), timeAgo(1))
	})
	for _, deep := range []bool{false, true} {
		hits, err := Search(st.DB, "ocr recall gate", Opts{Limit: 20, Deep: deep})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, h := range hits {
			got[h.ID] = true
		}
		if len(got) != 2 || !got["finding-ocr-new"] || !got["work/box/SPEC.md"] {
			t.Fatalf("deep=%v hits = %v, want exactly finding-ocr-new and work/box/SPEC.md", deep, got)
		}
	}
}

// TestQueryWordsAndPathsReachTheDoc catches the two ways real queries missed their document on 2026-10-08: filler
// words AND-ed into the match ("What do I know about …" required "know" and "about" in the document; a third of the
// QA questions missed), and a document the query names by path (also as BOX/<path> after naming the box) ranked
// on its words alone.
func TestQueryWordsAndPathsReachTheDoc(t *testing.T) {
	brain := t.TempDir()
	st := indexFiles(t, brain, map[string]string{
		"notes/finding-glacier-stall.md": "---\nid: finding-glacier-stall\ntype: finding\nstatus: current\nsummary: Glacier shards stall when widened past eight\n---\nBody.\n",
		"work/box/plans/rollout.md":      "# Rollout\n\nShard widths for the next run.\n",
	}, nil)
	top := func(q string) string {
		hits, err := Search(st.DB, q, Opts{Limit: 3})
		if err != nil || len(hits) == 0 {
			t.Fatalf("%q: hits %v, err %v", q, hits, err)
		}
		return hits[0].ID
	}
	if got := top("What do I know about glacier shards that stall when widened for the next rollout?"); got != "finding-glacier-stall" {
		t.Errorf("question: top hit %s", got)
	}
	if got := top("Follow /Users/someone/Documents/work-repo/saahil-docs/box/plans/rollout.md for the glacier shards that stall"); got != "work/box/plans/rollout.md" {
		t.Errorf("named path: top hit %s", got)
	}
	if got := top("BOX is saahil-docs/box. Read BOX/plans/rollout.md about the glacier shards that stall"); got != "work/box/plans/rollout.md" {
		t.Errorf("box placeholder path: top hit %s", got)
	}
}

// TestQueryNamingAHubRanksItFirst catches a search for a hub by its own name that ranks a note above the hub: on
// 2026-10-08 `brain search "identity resolution"` put a decision note whose id and summary repeat the words above
// hub identity-resolution. A query equal to a hub's title, id or an alias puts that hub first; one word more does not.
func TestQueryNamingAHubRanksItFirst(t *testing.T) {
	brain := t.TempDir()
	st := indexFiles(t, brain, map[string]string{
		"hubs/glacier-shards.md": "---\nid: glacier-shards\ntype: hub\nstatus: current\nsummary: Sharding the glacier corpus\naliases: [shard ladder, gs]\n---\n# Glacier shards\n\nStart here.\n",
		"notes/decision-glacier-shards-width.md": "---\nid: decision-glacier-shards-width\ntype: decision\nstatus: current\n" +
			"summary: Glacier shards stay eight wide; glacier shards past eight stall the shard ladder\naliases: [glacier shards width]\n---\n# Glacier shards width\n\nGlacier shards, glacier shards.\n",
	}, nil)
	top := func(q string) string {
		hits, err := Search(st.DB, q, Opts{Limit: 3})
		if err != nil || len(hits) == 0 {
			t.Fatalf("%q: hits %v, err %v", q, hits, err)
		}
		return hits[0].ID
	}
	for _, q := range []string{"glacier shards", "Glacier-Shards", "shard ladder"} {
		if got := top(q); got != "glacier-shards" {
			t.Errorf("%q: top hit %s, want hub glacier-shards", q, got)
		}
	}
	if got := top("glacier shards width"); got != "decision-glacier-shards-width" {
		t.Errorf("a query naming the note, not the hub: top hit %s", got)
	}
}

// indexFiles writes files under brain, runs touch (file times) and indexes them into a fresh store.
func indexFiles(t *testing.T, brain string, files map[string]string, touch func()) *store.Store {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(brain, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if touch != nil {
		touch()
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := graph.Update(context.Background(), st.DB, []scan.Root{{Abs: brain}}, true); err != nil {
		t.Fatal(err)
	}
	return st
}

func timeAgo(hours int) time.Time { return time.Now().Add(-time.Duration(hours) * time.Hour) }
