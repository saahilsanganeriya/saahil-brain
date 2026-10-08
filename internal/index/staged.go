package index

import (
	"os"
	"path/filepath"

	"saahil.local/brain/internal/scan"
)

// withStagedCards adds the CARDs the backfill staged before the cut-over, presented at work/<box>/CARD.md, so
// pointers and the start digest find a box's card while saahil-docs is still read-only. Once live they are in work/.
func withStagedCards(brainRoot string, roots []scan.Root) []scan.Root {
	staged := filepath.Join(brainRoot, ".brain", "staging", "work")
	if _, err := os.Stat(filepath.Join(brainRoot, ".brain", "live")); err == nil {
		return roots
	}
	if fi, err := os.Stat(staged); err != nil || !fi.IsDir() {
		return roots
	}
	return append(roots, scan.Root{Abs: staged, Prefix: "work"})
}
