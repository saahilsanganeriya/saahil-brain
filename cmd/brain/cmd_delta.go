package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/delta"
)

func init() {
	register("apply-delta", "apply a skill's delta to notes/ and hubs/: brain apply-delta <file.json> [--dry-run]", cmdApplyDelta)
}

// cmdApplyDelta exits 0 when every op applied, 1 when some were rejected (listed in the report), 2 on bad input.
func cmdApplyDelta(args []string) int {
	fs := flag.NewFlagSet("apply-delta", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "check and show the resulting files; write nothing")
	// flags may follow the file name
	var file string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return 2
		}
		if args = fs.Args(); len(args) > 0 {
			file, args = args[0], args[1:]
		}
	}
	if file == "" {
		return fail("usage: brain apply-delta <file.json> [--dry-run]")
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return fail("%v", err)
	}
	var d delta.Delta
	if err := json.Unmarshal(b, &d); err != nil {
		fmt.Fprintf(os.Stderr, "brain: %s is not a delta: %v\n", file, err)
		return 2
	}
	root := config.BrainRoot()
	opts := delta.Options{DryRun: *dryRun, Now: time.Now()}
	if !*dryRun {
		stats, err := delta.OpenStats(filepath.Join(root, ".index", "brain.db"))
		if err != nil {
			return fail("%v", err)
		}
		defer stats.Close()
		opts.Stats = stats
	}
	rep, err := delta.Apply(context.Background(), root, d, opts)
	if err != nil {
		return fail("%v", err)
	}
	out, _ := json.MarshalIndent(rep, "", "  ")
	fmt.Println(string(out))
	if len(rep.Rejected) > 0 {
		return 1
	}
	return 0
}
