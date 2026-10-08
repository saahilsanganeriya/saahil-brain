package evals

import (
	"crypto/sha256"
	"slices"
)

// Splits are the halves every eval set is cut into: tune ranking on DEV only, judge it on TEST.
var Splits = []string{"dev", "test"}

// SplitOf puts an item in DEV or TEST by the first byte of sha256(key), so the split never depends on set order or
// size. Navigation, repeat-mistake and A/B items are keyed by their session, QA items by their id.
func SplitOf(key string) string {
	if sha256.Sum256([]byte(key))[0]&1 == 0 {
		return "dev"
	}
	return "test"
}

// Only keeps the items of one split (dev or test), for a quicker run while tuning.
func (e *Env) Only(split string) {
	keep := func(key string) bool { return SplitOf(key) != split }
	e.sets.Navigation = slices.DeleteFunc(slices.Clone(e.sets.Navigation), func(it NavItem) bool { return keep(it.Session) })
	e.sets.Mistakes = slices.DeleteFunc(slices.Clone(e.sets.Mistakes), func(it MistakeItem) bool { return keep(it.Session) })
	e.sets.QA = slices.DeleteFunc(slices.Clone(e.sets.QA), func(it QAItem) bool { return keep(it.ID) })
}

// perSplit computes the metrics over every row, then again over each split's rows alone.
func perSplit[R any](rows []R, splitOf func(R) string, build func([]R) []Metric) []Metric {
	out := build(rows)
	for _, s := range Splits {
		var sub []R
		for _, r := range rows {
			if splitOf(r) == s {
				sub = append(sub, r)
			}
		}
		for _, m := range build(sub) {
			m.Split = s
			out = append(out, m)
		}
	}
	return out
}
