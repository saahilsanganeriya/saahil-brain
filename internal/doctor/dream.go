package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/dream"
)

// One failed dream can be a worker restart, and the next hourly run retries it; two in a row is a fault that repeats.
// A dream that completes with failed steps counts too: with the model endpoint unreachable every model step fails
// and the run still completes.
func init() {
	Register(Check{Name: "dream.last", Run: func(context.Context) (Status, string) {
		return dreamRuns(config.BrainRoot())
	}})
}

var activityWrapper = regexp.MustCompile(`activity error \([^)]*\): `)

func dreamRuns(root string) (Status, string) {
	files, _ := filepath.Glob(filepath.Join(root, ".brain", "dream-runs", "*.json"))
	sort.Sort(sort.Reverse(sort.StringSlice(files))) // run ids start with the UTC start time
	var failed []dream.Report
	var lastFault string
	for _, f := range files {
		var rep dream.Report
		if b, err := os.ReadFile(f); err != nil || json.Unmarshal(b, &rep) != nil || rep.Shadow {
			continue
		}
		why, faulty := runFault(rep)
		if !faulty {
			if len(failed) == 0 {
				return OK, fmt.Sprintf("last dream %s %s", rep.RunID, rep.Status)
			}
			break
		}
		if len(failed) == 0 {
			lastFault = why
		}
		failed = append(failed, rep)
	}
	if len(failed) == 0 {
		return Skip, "no dream has run on this brain"
	}
	reason, _, _ := strings.Cut(activityWrapper.ReplaceAllString(lastFault, ""), "\n")
	detail := fmt.Sprintf("%d in a row failed since %s, the last: %.240s", len(failed), failed[len(failed)-1].RunID, reason)
	if len(failed) == 1 {
		return Warn, detail
	}
	return Fail, detail
}

// runFault says why a run counts against the streak: a failed run's error starts with its step ("snapshot: git add:
// ..."); a completed run names its failed steps and the first one's reason.
func runFault(rep dream.Report) (string, bool) {
	if rep.Status == "failed" {
		return rep.Error, true
	}
	var names []string
	var first string
	for _, s := range rep.Steps {
		if s.Status == "failed" {
			if len(names) == 0 {
				first = s.Reason
			}
			names = append(names, s.Name)
		}
	}
	if len(names) == 0 {
		return "", false
	}
	return fmt.Sprintf("steps %s failed: %s", strings.Join(names, ", "), first), true
}
