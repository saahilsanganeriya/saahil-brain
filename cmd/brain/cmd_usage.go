package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/hooks"
	"saahil.local/brain/internal/store"
)

func init() {
	register("usage replay", "re-count the pointer funnel from this machine's transcripts, read-only: [--since DATE|RFC3339] [--projects DIR]", cmdUsageReplay)
}

// cmdUsageReplay prints the pointer funnel of every transcript modified since --since (default: today, UTC) under the
// post hook's old rule (Read/Grep/Glob only) and its current one. It opens the index read-only and never migrates it.
func cmdUsageReplay(args []string) int {
	fs := flag.NewFlagSet("usage replay", flag.ContinueOnError)
	sinceFlag := fs.String("since", time.Now().UTC().Format("2006-01-02"), "transcripts modified at or after: YYYY-MM-DD or RFC3339")
	projects := fs.String("projects", filepath.Join(config.ClaudeDir(), "projects"), "Claude Code's transcript folder")
	if _, err := parseAll(fs, args); err != nil {
		return 2
	}
	since, err := time.Parse(time.RFC3339, *sinceFlag)
	if err != nil {
		if since, err = time.Parse("2006-01-02", *sinceFlag); err != nil {
			return fail("--since: %v", err)
		}
	}
	root := config.BrainRoot()
	db, err := sql.Open("sqlite", "file:"+store.Path(root)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return fail("index: %v", err)
	}
	defer db.Close()
	pathOf := func(id string) string {
		var p string
		if db.QueryRow(`SELECT path FROM node WHERE id = ?`, id).Scan(&p) == nil {
			return p
		}
		if strings.Contains(id, "/") {
			return id
		}
		return "notes/" + id + ".md"
	}
	files, err := filepath.Glob(filepath.Join(*projects, "*", "*.jsonl"))
	if err != nil {
		return fail("%v", err)
	}
	env := hooks.DefaultEnv()
	var f hooks.Funnel
	for _, path := range files {
		if fi, err := os.Stat(path); err != nil || fi.ModTime().Before(since) {
			continue
		}
		r, err := os.Open(path)
		if err != nil {
			return fail("%v", err)
		}
		err = hooks.Replay(env, r, pathOf, &f)
		r.Close()
		if err != nil {
			return fail("%s: %v", path, err)
		}
	}
	fmt.Printf("usage replay on %s: %d transcripts in %s modified since %s (read-only)\n\n", config.ThisMachine(),
		f.Transcripts, *projects, since.UTC().Format("2006-01-02T15:04Z"))
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "\tbefore (Read/Grep/Glob)\tafter (this hook)")
	fmt.Fprintf(tw, "sessions shown a pointer\t%d\t%d\n", f.Sessions, f.Sessions)
	fmt.Fprintf(tw, "pointers shown (session, id)\t%d\t%d\n", f.Shown, f.Shown)
	fmt.Fprintf(tw, "opened\t%d\t%d\n", f.OpenedFileTools, f.Opened)
	fmt.Fprintf(tw, "followed, never opened\tsame rule\t%d\n", f.Followed)
	fmt.Fprintf(tw, "ignored\tnot counted\t%d\n", f.Ignored)
	fmt.Fprintf(tw, "sessions that pulled\tnot counted\t%d of %d\n", f.Pulled, f.Transcripts)
	tw.Flush()
	return 0
}
