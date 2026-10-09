package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"saahil.local/brain/internal/backfill"
	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/dream"
)

func init() {
	register("dream cards", "dry run of the dream's curate.cards: brain dream cards --dry-run lists the stale CARDs "+
		"a dream would refresh now", cmdDreamCards)
}

func cmdDreamCards(args []string) int {
	fs := flag.NewFlagSet("dream cards", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "list the stale CARDs with their age and newest file; no model, nothing written")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*dry {
		fmt.Fprintln(os.Stderr, "brain dream cards takes --dry-run only: the hourly dream refreshes CARDs (curate.cards)")
		return 2
	}
	return dreamCardsDry(os.Stdout, config.BrainRoot(), time.Now().UTC())
}

// dreamCardsDry runs the step's plan activity, which only reads the boxes and their CARDs.
func dreamCardsDry(w io.Writer, root string, now time.Time) int {
	acts := &dream.Activities{Cfg: dream.Config{BrainRoot: root}}
	plan, err := acts.CardsPlan(context.Background())
	if err != nil {
		return fail("curate.cards plan: %v", err)
	}
	fmt.Fprintf(w, "brain dream cards — dry run on %s; nothing is written\n", root)
	stale := len(plan.Refresh) + len(plan.Waiting)
	fmt.Fprintf(w, "%d boxes, %d stale (CARD ≥ 1 h behind the newest work file, or no CARD); a dream refreshes at most 4, "+
		"stalest first\n", plan.Boxes, stale)
	if stale == 0 {
		fmt.Fprintf(w, "nothing to refresh: %s\n", plan.Skip)
	}
	show := func(verdict string, c backfill.CardAge) {
		card, lag := "no CARD", ""
		if !c.Card.IsZero() {
			card = fmt.Sprintf("CARD %s old (%s, %s)", minutes(now.Sub(c.Card)), c.Status,
				c.Card.Format("2006-01-02T15:04Z"))
			lag = ", " + minutes(c.Box.NewestFileTime.Sub(c.Card)) + " after the CARD"
		}
		fmt.Fprintf(w, "  %-7s %-48s %s  newest %s %s%s\n", verdict, c.Box.Name, card, c.Box.NewestFile,
			c.Box.NewestFileTime.Format("2006-01-02T15:04Z"), lag)
	}
	for _, c := range plan.Refresh {
		show("refresh", c)
	}
	for _, c := range plan.Waiting {
		show("waits", c)
	}
	return 0
}

func minutes(d time.Duration) string {
	return strings.TrimSuffix(d.Round(time.Minute).String(), "0s")
}
