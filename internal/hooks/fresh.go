package hooks

import (
	"database/sql"
	"time"
)

// A session's first prompt also sees what its own boxes got in the hours before it: the previous agent's outputs
// (run configs, evidence, reports, PR bodies) that no doc links yet and that the prompt names only by their content.
const (
	freshWindow = 2 * time.Hour
	freshMax    = 300 // newest first; the busiest box got 60-180 indexed files an hour
)

// freshFiles loads the docs and data files of the session's own boxes last written in the window before seeds.AsOf;
// fresh falls from 1 (written at AsOf) to 0 (at the window's start).
func freshFiles(db *sql.DB, seeds Seeds) ([]cand, error) {
	if seeds.AsOf.IsZero() {
		return nil, nil
	}
	from := seeds.AsOf.Add(-freshWindow)
	var out []cand
	for _, box := range seeds.Work {
		rows, err := db.Query(`SELECT `+candCols+`, n.mtime FROM node n `+rankable+` AND n.kind != 'section'
			AND n.path >= ? AND n.path < ? AND n.mtime >= ? AND n.mtime < ? ORDER BY n.mtime DESC LIMIT ?`,
			"work/"+box+"/", "work/"+box+"0", from.UnixNano(), seeds.AsOf.UnixNano(), freshMax)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var c cand
			var mtime int64
			if err := rows.Scan(&c.id, &c.kind, &c.typ, &c.path, &c.title, &c.summary, &c.hubs, &c.shown, &mtime); err != nil {
				rows.Close()
				return nil, err
			}
			if seeds.Hide != nil && seeds.Hide(c.id) {
				continue
			}
			c.fresh = float64(mtime-from.UnixNano()) / float64(freshWindow)
			out = append(out, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// replayAsOf is a replayed session's start as its guard tells it: the earliest last write among its boxes' files the
// guard hides, since a file written before the start is never hidden. Zero when it hides none: then nothing says how
// long before the start the boxes' newest files were written.
func replayAsOf(db *sql.DB, boxes []string, hide func(string) bool) time.Time {
	var asOf time.Time
	for _, box := range boxes {
		rows, err := db.Query(`SELECT id, mtime FROM node WHERE kind IN ('doc', 'artifact') AND path >= ? AND path < ?
			ORDER BY mtime`, "work/"+box+"/", "work/"+box+"0")
		if err != nil {
			continue
		}
		for rows.Next() {
			var id string
			var mtime int64
			if rows.Scan(&id, &mtime) == nil && hide(id) {
				if t := time.Unix(0, mtime); asOf.IsZero() || t.Before(asOf) {
					asOf = t
				}
				break
			}
		}
		rows.Close()
	}
	return asOf
}
