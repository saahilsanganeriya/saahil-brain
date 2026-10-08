package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/mcpserver"
)

func init() {
	register("mcp", "MCP stdio server: brain_search, brain_show, brain_sections, brain_neighbors, brain_note", cmdMCP)
	register("note", "drop a note into the brain's inbox for the dream: brain note [--hub h]… [--type t] [--title T] [--json] <text…>", cmdNote)
}

func cmdMCP([]string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := mcpserver.Serve(ctx, config.BrainRoot()); err != nil && ctx.Err() == nil {
		return fail("mcp: %v", err)
	}
	return 0
}

type hubList []string

func (h *hubList) String() string     { return strings.Join(*h, ",") }
func (h *hubList) Set(v string) error { *h = append(*h, v); return nil }

func cmdNote(args []string) int {
	fs := flag.NewFlagSet("note", flag.ContinueOnError)
	var hubs hubList
	fs.Var(&hubs, "hub", "hub id (repeatable)")
	typ := fs.String("type", "", "finding, decision, lesson… when known")
	title := fs.String("title", "", "one line (default: the text)")
	asJSON := fs.Bool("json", false, "print {\"path\": …}")
	words, err := parseAll(fs, args)
	if err != nil || (len(words) == 0 && *title == "") {
		return fail("note [--hub h]… [--type t] [--title T] [--json] <text…>")
	}
	p, err := mcpserver.Capture(config.BrainRoot(), mcpserver.Note{Title: *title, Body: strings.Join(words, " "),
		Hubs: hubs, Type: *typ, Source: "cli"}, time.Now())
	if err != nil {
		return fail("note: %v", err)
	}
	if *asJSON {
		out, _ := json.Marshal(map[string]string{"path": p})
		fmt.Println(string(out))
	} else {
		fmt.Println(p)
	}
	return 0
}
