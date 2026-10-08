package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"saahil.local/brain/internal/index"
	"saahil.local/brain/internal/store"
)

func init() {
	register("index", "update the index (changed files only): brain index [--full] [--stats]", cmdIndex)
	register("search", "rank notes and docs: brain search <q> [--deep] [--json] [--limit N] [--type t,…] [--seed id,…]", cmdSearch)
	register("show", "a node and its edges: brain show <id|path>", cmdShow)
	register("sections", "a big doc's sections with line ranges: brain sections <id|path>", cmdSections)
	register("neighbors", "nodes within N hops (default 2): brain neighbors <id|path> [--depth N]", cmdNeighbors)
	register("pagerank", "recompute global PageRank without rescanning", cmdPageRank)
}

// parseAll lets flags sit before or after positional arguments (`brain search "q" --deep`).
func parseAll(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos, args = append(pos, fs.Arg(0)), fs.Args()[1:]
	}
}

func withIndex(run func(*index.Index) int) int {
	ix, err := index.Open(index.DefaultRoot())
	if err != nil {
		return fail("open index: %v", err)
	}
	defer ix.Close()
	return run(ix)
}

func cmdIndex(args []string) int {
	fs := flag.NewFlagSet("index", flag.ContinueOnError)
	full := fs.Bool("full", false, "rebuild from scratch")
	stats := fs.Bool("stats", false, "print counts (without --full: no scan)")
	if _, err := parseAll(fs, args); err != nil {
		return 2
	}
	return withIndex(func(ix *index.Index) int {
		if *stats && !*full {
			return printStats(ix)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		st, err := ix.Update(ctx, *full)
		if err != nil {
			return fail("index: %v", err)
		}
		fmt.Printf("indexed %d paths: %d changed, %d deleted, %d other files ignored; pagerank %d iterations; %s\n",
			st.Walked, st.Changed, st.Deleted, st.Ignored, st.PageRankIters, st.Took.Round(time.Millisecond))
		if *stats {
			return printStats(ix)
		}
		return 0
	})
}

func printStats(ix *index.Index) int {
	db := ix.DB()
	line := func(label, q string) {
		rows, err := db.Query(q)
		if err != nil {
			fmt.Printf("%-9s error: %v\n", label, err)
			return
		}
		defer rows.Close()
		var parts []string
		total := int64(0)
		for rows.Next() {
			var k string
			var n int64
			_ = rows.Scan(&k, &n)
			parts = append(parts, fmt.Sprintf("%s %d", k, n))
			total += n
		}
		fmt.Printf("%-9s %-8d %s\n", label, total, strings.Join(parts, " · "))
	}
	var size int64
	if fi, err := os.Stat(store.Path(ix.Root)); err == nil {
		size = fi.Size()
	}
	var schema, updated string
	_ = db.QueryRow(`SELECT coalesce((SELECT value FROM meta WHERE key = 'schema_version'), ''),
		coalesce((SELECT value FROM meta WHERE key = 'run.updated_at'), 'never')`).Scan(&schema, &updated)
	fmt.Printf("index     %s (schema %s, %.1f MB, updated %s)\n", store.Path(ix.Root), schema, float64(size)/1e6, updated)
	line("nodes", `SELECT kind, count(*) FROM node GROUP BY kind ORDER BY count(*) DESC`)
	line("edges", `SELECT rel, count(*) FROM edge GROUP BY rel ORDER BY count(*) DESC`)
	line("copies", `SELECT 'copies of', count(*) FROM node WHERE canonical_of != ''
		UNION ALL SELECT 'canonicals', count(DISTINCT canonical_of) FROM node WHERE canonical_of != ''`)
	line("versions", `SELECT 'older versions', count(DISTINCT src) FROM edge WHERE rel = 'version_of'
		UNION ALL SELECT 'current', count(DISTINCT dst) FROM edge WHERE rel = 'version_of'`)
	line("links", `SELECT replace(key, 'links.', ''), CAST(value AS INTEGER) FROM meta WHERE key LIKE 'links.%' ORDER BY key`)
	line("skipped", `SELECT type, count(*) FROM node WHERE kind = 'skip' GROUP BY type ORDER BY count(*) DESC`)
	line("partial", `SELECT 'docs read to 2.5 MB', count(*) FROM node WHERE kind = 'doc' AND size > 2500000
		UNION ALL SELECT 'data shaped from 64 KB', count(*) FROM node WHERE kind = 'artifact' AND type = 'artifact' AND size > 65536
		UNION ALL SELECT 'data too big to hash', count(*) FROM node WHERE kind = 'artifact' AND type = 'artifact' AND sha256 = ''`)
	line("roots", `SELECT CASE WHEN path LIKE 'work/%' THEN 'work' WHEN path LIKE 'memory-import/%' THEN 'memory-import'
		ELSE 'brain' END, count(*) FROM node WHERE kind IN ('doc', 'artifact') GROUP BY 1 ORDER BY 2 DESC`)
	return 0
}

func cmdSearch(args []string) int {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	deep := fs.Bool("deep", false, "add graph proximity (personalized PageRank)")
	asJSON := fs.Bool("json", false, "print JSON")
	limit := fs.Int("limit", 10, "results")
	types := fs.String("type", "", "comma-separated node types")
	seeds := fs.String("seed", "", "comma-separated seed ids (work box, hub, note)")
	pos, err := parseAll(fs, args)
	if err != nil || len(pos) == 0 {
		return fail("search <query> [--deep] [--json] [--limit N]")
	}
	return withIndex(func(ix *index.Index) int {
		hits, err := ix.Search(strings.Join(pos, " "), index.SearchOpts{Limit: *limit, Deep: *deep, Types: split(*types), Seeds: split(*seeds)})
		if err != nil {
			return fail("search: %v", err)
		}
		if *asJSON {
			_ = json.NewEncoder(os.Stdout).Encode(hits)
			return 0
		}
		for i, h := range hits {
			line := h.Title
			if h.Summary != "" && h.Summary != h.Title {
				line += " — " + h.Summary
			}
			fmt.Printf("%2d. %.3f  %s  [%s]\n    %.200s\n", i+1, h.Score, h.ID, h.Type, line)
		}
		return 0
	})
}

func split(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// withRef runs a command that takes exactly one node id or path.
func withRef(name string, args []string, run func(*index.Index, string) error) int {
	if len(args) != 1 {
		return fail("%s <id|path>", name)
	}
	return withIndex(func(ix *index.Index) int {
		if err := run(ix, args[0]); err != nil {
			return fail("%s %s: %v", name, args[0], err)
		}
		return 0
	})
}

func cmdShow(args []string) int {
	return withRef("show", args, func(ix *index.Index, ref string) error {
		n, err := ix.Show(ref)
		if err != nil {
			return err
		}
		fmt.Printf("%s\n  kind %s · type %s · %d bytes · pagerank %.3g\n  path %s\n  title %s\n", n.ID, n.Kind, n.Type, n.Size, n.PageRank, n.Path, n.Title)
		for _, kv := range [][2]string{{"summary", n.Summary}, {"status", n.Status}, {"hubs", n.Hubs}, {"scope", n.Scope},
			{"as_of", n.AsOf}, {"canonical_of", n.CanonicalOf}} {
			if kv[1] != "" {
				fmt.Printf("  %s %s\n", kv[0], kv[1])
			}
		}
		if secs, _ := ix.Sections(n.ID); len(secs) > 0 {
			fmt.Printf("  sections %d (brain sections %s)\n", len(secs), n.ID)
		}
		for _, e := range n.Out {
			fmt.Printf("  → %-10s %s %s\n", e.Rel, e.Dst, e.Why)
		}
		for _, e := range n.In {
			fmt.Printf("  ← %-10s %s %s\n", e.Rel, e.Src, e.Why)
		}
		return nil
	})
}

func cmdSections(args []string) int {
	return withRef("sections", args, func(ix *index.Index, ref string) error {
		secs, err := ix.Sections(ref)
		if err != nil || len(secs) == 0 {
			return cmp.Or(err, errors.New("no sections (only markdown over 50 KB is mapped)"))
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "#\tlines\tKB\theading")
		for _, s := range secs {
			fmt.Fprintf(tw, "%d\t%d-%d\t%.1f\t%s\n", s.Idx, s.LineStart, s.LineEnd, float64(s.Bytes)/1024, s.Heading)
		}
		return tw.Flush()
	})
}

func cmdNeighbors(args []string) int {
	fs := flag.NewFlagSet("neighbors", flag.ContinueOnError)
	depth := fs.Int("depth", 2, "hops")
	pos, err := parseAll(fs, args)
	if err != nil {
		return 2
	}
	return withRef("neighbors", pos, func(ix *index.Index, ref string) error {
		nbs, err := ix.Neighbors(ref, *depth)
		for _, nb := range nbs {
			arrow := "←"
			if nb.Outgoing {
				arrow = "→"
			}
			fmt.Printf("%d %s %-10s %s  %s\n", nb.Depth, arrow, nb.Rel, nb.ID, nb.Title)
		}
		return err
	})
}

func cmdPageRank([]string) int {
	return withIndex(func(ix *index.Index) int {
		start := time.Now()
		iters, err := ix.RecomputePageRank(context.Background())
		if err != nil {
			return fail("pagerank: %v", err)
		}
		fmt.Printf("pagerank: %d iterations in %s\n", iters, time.Since(start).Round(time.Millisecond))
		return 0
	})
}
