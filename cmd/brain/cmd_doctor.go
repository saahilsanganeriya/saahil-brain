package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/doctor"
	"saahil.local/brain/internal/rpc"
)

func init() {
	register("doctor", "health checks on this machine; green = no fail/warn (exit 1 otherwise)", cmdDoctor)
}

func cmdDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	results := doctor.RunAll(context.Background(), 20*time.Second)
	green := doctor.Green(results, true)
	if *asJSON {
		out, _ := json.MarshalIndent(map[string]any{"machine": config.ThisMachine(), "version": rpc.Version,
			"green": green, "checks": results}, "", "  ")
		fmt.Println(string(out))
	} else {
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, r := range results {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Status, r.Name, r.Detail)
		}
		tw.Flush()
		verdict := "GREEN"
		if !green {
			verdict = "NOT GREEN"
		}
		fmt.Printf("\nbrain doctor %s on %s (%s)\n", verdict, config.ThisMachine(), rpc.Version)
	}
	if !green {
		return 1
	}
	return 0
}
