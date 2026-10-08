package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/view"
)

func init() {
	register("view", "build or refresh a read-only folder for Obsidian: the brain's text plus work/'s markdown, no bulk: [--dir ~/brain-view]", cmdView)
}

func viewDir() string { return filepath.Join(config.Home(), "brain-view") }

func cmdView(args []string) int {
	fs := flag.NewFlagSet("view", flag.ContinueOnError)
	dir := fs.String("dir", viewDir(), "where the view goes (outside the brain)")
	if _, err := parseAll(fs, args); err != nil {
		return 2
	}
	st, err := view.Build(config.BrainRoot(), *dir)
	if err != nil {
		return fail("view: %v", err)
	}
	fmt.Printf("view %s: %d note/hub/memory copies (%d rewritten) · %d links made · %d kept · %d stale removed (open it as an Obsidian vault; read only)\n",
		*dir, st.Copies, st.Rendered, st.Linked, st.Kept, st.Removed)
	return 0
}

// refreshView keeps an existing view current after a sync; with no view yet it does nothing.
func refreshView() {
	if _, err := os.Stat(viewDir()); err != nil {
		return
	}
	if st, err := view.Build(config.BrainRoot(), viewDir()); err != nil {
		fmt.Fprintf(os.Stderr, "brain: view refresh: %v\n", err)
	} else if st.Linked+st.Removed > 0 {
		fmt.Printf("view: %d links made, %d removed\n", st.Linked, st.Removed)
	}
}
