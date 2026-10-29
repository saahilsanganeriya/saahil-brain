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
	for _, f := range files {
		var rep dream.Report
		if b, err := os.ReadFile(f); err != nil || json.Unmarshal(b, &rep) != nil || rep.Shadow {
			continue
		}
		if rep.Status != "failed" {
			if len(failed) == 0 {
				return OK, fmt.Sprintf("last dream %s %s", rep.RunID, rep.Status)
			}
			break
		}
		failed = append(failed, rep)
	}
	if len(failed) == 0 {
		return Skip, "no dream has run on this brain"
	}
	// a run's error starts with the step that failed: "snapshot: git add: ..."
	reason, _, _ := strings.Cut(activityWrapper.ReplaceAllString(failed[0].Error, ""), "\n")
	detail := fmt.Sprintf("%d in a row failed since %s, the last: %.240s", len(failed), failed[len(failed)-1].RunID, reason)
	if len(failed) == 1 {
		return Warn, detail
	}
	return Fail, detail
}
