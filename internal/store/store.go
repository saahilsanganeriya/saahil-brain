// Package store owns the brain's SQLite index (<brain>/.index/brain.db): schema, migrations and meta.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

// migrations are applied in order; the index of the last applied one is meta.schema_version. Append, never edit.
// An ADD COLUMN goes alone in its entry: rerun over a column that is already there (meta lost), it counts as done.
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS node(
		id TEXT PRIMARY KEY, kind TEXT NOT NULL, type TEXT NOT NULL DEFAULT '', path TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '', summary TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT '',
		hubs TEXT NOT NULL DEFAULT '', scope TEXT NOT NULL DEFAULT '', as_of TEXT NOT NULL DEFAULT '',
		updated TEXT NOT NULL DEFAULT '', size INTEGER NOT NULL DEFAULT 0, sha256 TEXT NOT NULL DEFAULT '',
		mtime INTEGER NOT NULL DEFAULT 0, canonical_of TEXT NOT NULL DEFAULT '', pagerank REAL NOT NULL DEFAULT 0);
	CREATE INDEX IF NOT EXISTS node_sha ON node(sha256) WHERE sha256 != '';
	CREATE INDEX IF NOT EXISTS node_kind ON node(kind);
	CREATE TABLE IF NOT EXISTS edge(src TEXT NOT NULL, dst TEXT NOT NULL, rel TEXT NOT NULL, why TEXT NOT NULL DEFAULT '',
		source TEXT NOT NULL);
	CREATE UNIQUE INDEX IF NOT EXISTS edge_key ON edge(src, rel, dst, source);
	CREATE INDEX IF NOT EXISTS edge_dst ON edge(dst, rel);
	CREATE INDEX IF NOT EXISTS edge_source ON edge(source);
	` + FTSSchema + `;
	CREATE TABLE IF NOT EXISTS section(doc TEXT NOT NULL, idx INTEGER NOT NULL, heading TEXT NOT NULL, line_start INTEGER NOT NULL,
		line_end INTEGER NOT NULL, bytes INTEGER NOT NULL, summary TEXT NOT NULL DEFAULT '', PRIMARY KEY(doc, idx));
	CREATE TABLE IF NOT EXISTS node_stats(id TEXT PRIMARY KEY, shown INTEGER NOT NULL DEFAULT 0, opened INTEGER NOT NULL DEFAULT 0,
		followed INTEGER NOT NULL DEFAULT 0, helpful INTEGER NOT NULL DEFAULT 0, harmful INTEGER NOT NULL DEFAULT 0,
		owner_confirmed INTEGER NOT NULL DEFAULT 0, last_shown_at TEXT NOT NULL DEFAULT '',
		last_helpful_at TEXT NOT NULL DEFAULT '');
	CREATE TABLE IF NOT EXISTS lookups(time TEXT NOT NULL, session TEXT NOT NULL, hook TEXT NOT NULL, candidates TEXT NOT NULL,
		chosen TEXT NOT NULL, abstained INTEGER NOT NULL, ms REAL NOT NULL);
	CREATE TABLE IF NOT EXISTS llm_calls(key TEXT PRIMARY KEY, workflow TEXT NOT NULL, step TEXT NOT NULL, model TEXT NOT NULL,
		cost_usd REAL NOT NULL DEFAULT 0, tokens_in INTEGER NOT NULL DEFAULT 0, tokens_out INTEGER NOT NULL DEFAULT 0,
		started TEXT NOT NULL DEFAULT '', ended TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT '');`,
	`CREATE INDEX IF NOT EXISTS node_path ON node(path);`,
	// usage feedback: pointers shown and never opened (hooks), and the dream's demotion of never-opened nodes
	`ALTER TABLE node_stats ADD COLUMN ignored INTEGER NOT NULL DEFAULT 0;`,
	`ALTER TABLE node_stats ADD COLUMN demoted_at TEXT NOT NULL DEFAULT '';`,
}

var SchemaVersion = strconv.Itoa(len(migrations))

// FTSSchema is also used by a full rebuild, which drops and recreates the table: deleting every row of an FTS5
// table costs more than building it.
const FTSSchema = `CREATE VIRTUAL TABLE IF NOT EXISTS fts USING fts5(id UNINDEXED, title, aliases, summary, body, tokenize='porter unicode61')`

type Store struct{ DB *sql.DB }

// Path is where a brain keeps its index.
func Path(brainRoot string) string { return filepath.Join(brainRoot, ".index", "brain.db") }

// Open creates or migrates the index at dbPath. WAL lets hooks read while an update writes.
func Open(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)" +
		"&_pragma=cache_size(-65536)&_pragma=temp_store(MEMORY)&_pragma=journal_size_limit(67108864)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	st := &Store{DB: db}
	if err := st.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate %s: %w", dbPath, err)
	}
	return st, nil
}

func (s *Store) Close() error { return s.DB.Close() }

// meta predates every migration because it records which ones ran.
func (s *Store) migrate() error {
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return err
	}
	have, _ := strconv.Atoi(s.Meta("schema_version"))
	for i := have; i < len(migrations); i++ {
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			tx.Rollback()
			return err
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO meta(key, value) VALUES ('schema_version', ?)`, strconv.Itoa(i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Checkpoint folds the WAL back into the database and truncates it. A full rebuild writes the whole index through
// the WAL; left there, every reader's page lookup probes a WAL of hundreds of megabytes.
func (s *Store) Checkpoint() error {
	_, err := s.DB.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}

// Compact rewrites the file when more than a quarter of it is free pages (after a full rebuild).
func (s *Store) Compact() error {
	var free, total int64
	if err := s.DB.QueryRow(`SELECT (SELECT freelist_count FROM pragma_freelist_count), (SELECT page_count FROM pragma_page_count)`).Scan(&free, &total); err != nil {
		return err
	}
	if free*4 <= total {
		return nil
	}
	_, err := s.DB.Exec(`VACUUM`)
	return err
}

// Meta returns the value for key, or "" when unset.
func (s *Store) Meta(key string) string {
	var v string
	_ = s.DB.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	return v
}
