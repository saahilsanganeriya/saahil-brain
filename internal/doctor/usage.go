package doctor

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"saahil.local/brain/internal/config"
)

// The usage funnel over the last 7 days of lookups, reported and not judged yet: pull rate (sessions that used a brain
// tool or opened a pointer, of the sessions that prompted) and open rate (pointers opened or followed, of the
// distinct pointers each session was shown).
func init() {
	Register(Check{Name: "hooks usage", Run: func(context.Context) (Status, string) {
		root := config.BrainRoot()
		since, live := liveSince(root)
		if !live {
			return Skip, "shadow: nothing is shown until .brain/live exists"
		}
		if week := time.Now().Add(-7 * 24 * time.Hour); since.Before(week) {
			since = week
		}
		return withGraph(root, func(db *sql.DB) (Status, string, error) { return usageRates(db, since) })
	}})
}

// liveSince is when the brain went live: the time .brain/live holds, else its mtime. Earlier rows are shadow rows,
// which logged pointers no agent saw.
func liveSince(root string) (time.Time, bool) {
	p := filepath.Join(root, ".brain", "live")
	fi, err := os.Stat(p)
	if err != nil {
		return time.Time{}, false
	}
	if b, err := os.ReadFile(p); err == nil {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b))); err == nil {
			return t, true
		}
	}
	return fi.ModTime(), true
}

func usageRates(db *sql.DB, since time.Time) (Status, string, error) {
	from := since.UTC().Format(time.RFC3339)
	var sessions, pulled, shown, used, ignored int
	err := db.QueryRow(`
		WITH prompted AS (SELECT DISTINCT session FROM lookups WHERE hook = 'prompt' AND time >= ?1 AND session != ''),
		shown AS (SELECT DISTINCT l.session, j.value AS id FROM lookups l, json_each(l.chosen) j
			WHERE l.hook = 'prompt' AND l.time >= ?1),
		used AS (SELECT DISTINCT l.session, j.value AS id FROM lookups l, json_each(l.chosen) j
			WHERE l.hook = 'post' AND l.time >= ?1)
		SELECT (SELECT count(*) FROM prompted),
			(SELECT count(DISTINCT l.session) FROM lookups l JOIN prompted p ON p.session = l.session
				WHERE l.time >= ?1 AND l.hook IN ('pull', 'post')),
			(SELECT count(*) FROM shown),
			(SELECT count(*) FROM shown JOIN used USING (session, id)),
			(SELECT count(*) FROM lookups l, json_each(l.chosen) WHERE l.hook = 'end' AND l.time >= ?1)`, from).
		Scan(&sessions, &pulled, &shown, &used, &ignored)
	if err != nil {
		return Fail, "", err
	}
	if sessions == 0 {
		return Skip, "no prompts since " + since.UTC().Format("2006-01-02T15:04Z"), nil
	}
	detail := fmt.Sprintf("since %s: pull rate %d of %d sessions (%s) · open rate %d of %d pointers shown (%s) · %d ignored",
		since.UTC().Format("2006-01-02T15:04Z"), pulled, sessions, pct(pulled, sessions), used, shown, pct(used, shown), ignored)
	// pull and end rows began with a later build than the prompt and post rows: say from when they count
	var first sql.NullString
	if db.QueryRow(`SELECT min(time) FROM lookups WHERE hook IN ('pull', 'end')`).Scan(&first) == nil && first.Valid {
		if t, err := time.Parse(time.RFC3339Nano, first.String); err == nil && t.After(since) {
			detail += " (pulls and ignores counted from " + t.UTC().Format("2006-01-02T15:04Z") + ")"
		}
	}
	return Info, detail, nil
}

func pct(n, of int) string {
	if of == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(n)/float64(of))
}
