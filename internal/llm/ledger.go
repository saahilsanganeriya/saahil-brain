package llm

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// The table is owned by internal/store (FORMAT.md); creating it here keeps the dream working before that lands.
const llmCallsDDL = `CREATE TABLE IF NOT EXISTS llm_calls(
	key TEXT PRIMARY KEY, workflow TEXT NOT NULL, step TEXT NOT NULL, model TEXT NOT NULL,
	cost_usd REAL NOT NULL DEFAULT 0, tokens_in INTEGER NOT NULL DEFAULT 0, tokens_out INTEGER NOT NULL DEFAULT 0,
	started TEXT NOT NULL, ended TEXT NOT NULL DEFAULT '', status TEXT NOT NULL)`

// Ledger records every call in llm_calls and keeps each finished result as a file, so a key seen before is answered
// from disk for $0. Results live under .data/ (never in git); the prompt text is never stored, only its hash.
type Ledger struct {
	db         *sql.DB
	resultsDir string
}

func OpenLedger(dbPath, resultsDir string) (*Ledger, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(llmCallsDDL); err != nil {
		db.Close()
		return nil, err
	}
	return &Ledger{db: db, resultsDir: resultsDir}, nil
}

func (l *Ledger) Close() error { return l.db.Close() }

// Key identifies a call by everything that decides its answer; the run id is deliberately absent so a replay or a
// rerun with the same material maps to the same key.
func Key(workflow, step, model, skill, input string) string {
	h := sha256.New()
	for _, part := range []string{workflow, step, model, sha(skill), sha(input)} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Done returns the stored result of a finished key; ok is false when the key never finished.
func (l *Ledger) Done(key string) (Result, bool, error) {
	var status string
	err := l.db.QueryRow(`SELECT status FROM llm_calls WHERE key = ?`, key).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && status != "done") {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, err
	}
	b, err := os.ReadFile(l.resultPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, err
	}
	var res Result
	return res, true, json.Unmarshal(b, &res)
}

func (l *Ledger) Begin(key, workflow, step, model string, now time.Time) error {
	_, err := l.db.Exec(`INSERT INTO llm_calls(key, workflow, step, model, started, status) VALUES(?,?,?,?,?, 'running')
		ON CONFLICT(key) DO UPDATE SET started = excluded.started, ended = '', status = 'running'`,
		key, workflow, step, model, now.UTC().Format(time.RFC3339))
	return err
}

// Finish adds this attempt's cost to the row (a failed attempt that paid still counts toward the daily cap) and, on
// success, stores the result before marking the key done.
func (l *Ledger) Finish(key string, res Result, status string, now time.Time) error {
	if status == "done" {
		b, err := json.Marshal(res)
		if err != nil {
			return err
		}
		if err := writeAtomic(l.resultPath(key), b); err != nil {
			return err
		}
	}
	_, err := l.db.Exec(`UPDATE llm_calls SET cost_usd = cost_usd + ?, tokens_in = tokens_in + ?,
		tokens_out = tokens_out + ?, ended = ?, status = ? WHERE key = ?`,
		res.CostUSD, res.TokensIn, res.TokensOut, now.UTC().Format(time.RFC3339), status, key)
	return err
}

// SpentSince sums every call started at or after since, failed ones included.
func (l *Ledger) SpentSince(since time.Time) (float64, error) {
	var spent float64
	err := l.db.QueryRow(`SELECT COALESCE(SUM(cost_usd), 0) FROM llm_calls WHERE started >= ?`,
		since.UTC().Format(time.RFC3339)).Scan(&spent)
	return spent, err
}

func (l *Ledger) resultPath(key string) string { return filepath.Join(l.resultsDir, key+".json") }

func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
