package store

import (
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
