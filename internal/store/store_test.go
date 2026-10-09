package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestSchemaHasFTS5 catches a SQLite build without FTS5 (search would silently have no BM25) and a schema that
// does not migrate to the current version on a fresh brain.
func TestSchemaHasFTS5(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), ".index", "brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var used int
	if err := st.DB.QueryRow(`SELECT sqlite_compileoption_used('ENABLE_FTS5')`).Scan(&used); err != nil || used != 1 {
		t.Fatalf("driver lacks FTS5: used=%d err=%v", used, err)
	}
	if _, err := st.DB.Exec(`CREATE VIRTUAL TABLE probe USING fts5(x)`); err != nil {
		t.Fatalf("cannot create an fts5 table: %v", err)
	}
	if _, err := st.DB.Exec(`INSERT INTO fts(rowid, id, title, aliases, summary, body) VALUES (1, 'n', 'OCR recall', '', '', '')`); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := st.DB.QueryRow(`SELECT id FROM fts WHERE fts MATCH 'recall' ORDER BY bm25(fts)`).Scan(&id); err != nil || id != "n" {
		t.Fatalf("fts match: id=%q err=%v", id, err)
	}
	if v := st.Meta("schema_version"); v != SchemaVersion {
		t.Fatalf("schema_version = %q, want %q", v, SchemaVersion)
	}
	var mode string
	_ = st.DB.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}

// TestOpenAdoptsTablesOthersCreated catches a migration that fails with "table llm_calls already exists" when the
// dream's cost ledger opened brain.db first (it happened on the devbox), and one that drops the ledger's rows.
func TestOpenAdoptsTablesOthersCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brain.db")
	pre, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TABLE node`, `DELETE FROM meta`, `INSERT INTO llm_calls(key, workflow, step, model) VALUES ('k1', 'dream', 'file', 'm')`} {
		if _, err := pre.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	pre.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("reopen over a foreign llm_calls: %v", err)
	}
	defer st.Close()
	var n int
	if err := st.DB.QueryRow(`SELECT (SELECT count(*) FROM llm_calls) + (SELECT count(*) FROM node)`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("ledger rows + nodes = %d (err %v), want 1", n, err)
	}
}

// TestNodeStatsGainsFeedbackColumnsKeepingRows catches a migration that loses the counters a live index already
// holds, or leaves an old index without the ignored count and the dream's demotion mark the hooks now write.
func TestNodeStatsGainsFeedbackColumnsKeepingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brain.db")
	pre, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// rebuild node_stats as the first migration made it, and mark the index as that old
	for _, q := range []string{`DROP TABLE node_stats`, `CREATE TABLE node_stats(id TEXT PRIMARY KEY, shown INTEGER NOT NULL DEFAULT 0,
		opened INTEGER NOT NULL DEFAULT 0, followed INTEGER NOT NULL DEFAULT 0, helpful INTEGER NOT NULL DEFAULT 0,
		harmful INTEGER NOT NULL DEFAULT 0, owner_confirmed INTEGER NOT NULL DEFAULT 0, last_shown_at TEXT NOT NULL DEFAULT '',
		last_helpful_at TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO node_stats(id, shown, opened) VALUES ('lesson-kept', 12, 3)`,
		`UPDATE meta SET value = '2' WHERE key = 'schema_version'`} {
		if _, err := pre.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	pre.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("migrate an index with counters: %v", err)
	}
	defer st.Close()
	var shown, opened, ignored int
	var demoted string
	err = st.DB.QueryRow(`SELECT shown, opened, ignored, demoted_at FROM node_stats WHERE id = 'lesson-kept'`).
		Scan(&shown, &opened, &ignored, &demoted)
	if err != nil || shown != 12 || opened != 3 || ignored != 0 || demoted != "" {
		t.Fatalf("after migration: shown %d opened %d ignored %d demoted %q (err %v)", shown, opened, ignored, demoted, err)
	}
}

// TestWriteSurvivesAWriterBetweenReadAndWrite catches the 2026-10-09 00:00 dream's failed reindex: a deferred
// transaction that reads, then meets a hook's commit, cannot upgrade its stale snapshot and fails at once
// ("database is locked") without the busy timeout ever applying.
func TestWriteSurvivesAWriterBetweenReadAndWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".index", "brain.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.DB.Exec(`CREATE TABLE probe(x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	hook, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer hook.Close()

	tx, err := st.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM probe`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	_, _ = hook.Exec(`INSERT INTO probe VALUES (1)`) // waits its turn (here: fails fast) while the update holds the lock
	if _, err := tx.Exec(`INSERT INTO probe VALUES (2)`); err != nil {
		t.Fatalf("the update's write after another writer: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
