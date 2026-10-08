package rank

import (
	"database/sql"
	"time"
)

// Demotion is the usage funnel's ranking prior (DESIGN §6): a node shown as a pointer DemoteAfterShown times and never
// opened or followed scores DemotedPrior lower in L1 until an agent opens it. Its status and file never change.
const (
	DemoteAfterShown = 10
	DemotedPrior     = -0.15
)

// Demote marks newly demoted nodes with now and clears the mark of every demoted node opened or followed since.
func Demote(db *sql.DB, now time.Time) (demoted, restored int, err error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE node_stats SET demoted_at = ? WHERE demoted_at = '' AND shown >= ? AND opened = 0 AND followed = 0`,
		now.UTC().Format(time.RFC3339), DemoteAfterShown)
	if err != nil {
		return 0, 0, err
	}
	n, _ := res.RowsAffected()
	res, err = tx.Exec(`UPDATE node_stats SET demoted_at = '' WHERE demoted_at != '' AND (opened > 0 OR followed > 0)`)
	if err != nil {
		return 0, 0, err
	}
	m, _ := res.RowsAffected()
	return int(n), int(m), tx.Commit()
}

// UsagePrior is the L1 prior each node earns from its usage; only demoted nodes have one.
func UsagePrior(db *sql.DB) (map[string]float64, error) {
	rows, err := db.Query(`SELECT id FROM node_stats WHERE demoted_at != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	prior := map[string]float64{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		prior[id] = DemotedPrior
	}
	return prior, rows.Err()
}
