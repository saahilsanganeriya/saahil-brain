package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/metrics"
	"saahil.local/brain/internal/rpc"
	"saahil.local/brain/internal/store"
)

func init() {
	register("metrics", "the last 24 h on this machine (dreams, model spend, hooks, MCP/CLI tool calls, newest evals) and the change since the previous day: [--write] [--json]", cmdMetrics)
}

func cmdMetrics(args []string) int {
	fs := flag.NewFlagSet("metrics", flag.ContinueOnError)
	write := fs.Bool("write", false, "save the snapshot as .brain/metrics/<machine>-<day>.json")
	asJSON := fs.Bool("json", false, "print the snapshot as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	root := config.BrainRoot()
	db, err := sql.Open("sqlite", "file:"+store.Path(root)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return fail("index: %v", err)
	}
	defer db.Close()
	snap, err := metrics.Take(root, db, string(config.ThisMachine()), rpc.Version, time.Now())
	if err != nil {
		return fail("metrics: %v", err)
	}
	prev, hasPrev := metrics.Previous(root, snap)
	if *asJSON {
		raw, _ := json.MarshalIndent(snap, "", "  ")
		fmt.Println(string(raw))
	} else {
		fmt.Print(metrics.Table(snap, prev, hasPrev))
	}
	if *write {
		path, err := metrics.Write(root, snap)
		if err != nil {
			return fail("write: %v", err)
		}
		fmt.Println("snapshot:", path)
	}
	return 0
}
