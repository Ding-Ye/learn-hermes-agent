package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteMemory is a MemoryProvider backed by a single SQLite file with an
// FTS5 virtual table for full-text search. Pure-Go (modernc.org/sqlite)
// so we don't need cgo — the s05 binary cross-compiles cleanly to any
// platform Go targets.
//
// Schema:
//
//	memories(id, content, tags, session_id, created_at)
//	memories_fts(content)        -- external content FTS5, rowid = memories.id
//
// Saves go in a transaction so the row + its FTS entry are inserted
// atomically. Searches use FTS5 MATCH and order by `rank` (lower is better
// in BM25). Tags are stored as a comma-separated string for simplicity;
// real hermes uses JSON.
type SQLiteMemory struct {
	db *sql.DB
}

const sqliteSchema = `
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;

CREATE TABLE IF NOT EXISTS memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    content TEXT NOT NULL,
    tags TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
    content,
    content='memories', content_rowid='id',
    tokenize='unicode61'
);

CREATE TRIGGER IF NOT EXISTS memories_ai AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts(rowid, content) VALUES (new.id, new.content);
END;
CREATE TRIGGER IF NOT EXISTS memories_ad AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, content) VALUES('delete', old.id, old.content);
END;
CREATE TRIGGER IF NOT EXISTS memories_au AFTER UPDATE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, content) VALUES('delete', old.id, old.content);
    INSERT INTO memories_fts(rowid, content) VALUES (new.id, new.content);
END;
`

func NewSQLiteMemory(path string) (*SQLiteMemory, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// PRAGMAs and DDL combined — sqlite driver allows multi-statement Exec.
	if _, err := db.Exec(sqliteSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &SQLiteMemory{db: db}, nil
}

func (s *SQLiteMemory) Save(ctx context.Context, m Memory) (int64, error) {
	if strings.TrimSpace(m.Content) == "" {
		return 0, fmt.Errorf("Save: content is empty")
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	tags := strings.Join(m.Tags, ",")
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO memories(content, tags, session_id, created_at) VALUES (?, ?, ?, ?)`,
		m.Content, tags, m.SessionID, m.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("insert memory: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (s *SQLiteMemory) Search(ctx context.Context, query string, limit int) ([]Memory, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return []Memory{}, nil
	}
	if limit <= 0 {
		limit = 5
	}
	// memories_fts MATCH supports the FTS5 query syntax. We pass user input
	// straight through — adequate for teaching, but production would want
	// to escape special tokens (like leading '-' which means NOT).
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.content, m.tags, m.session_id, m.created_at, fts.rank
		FROM memories_fts AS fts
		JOIN memories AS m ON m.id = fts.rowid
		WHERE memories_fts MATCH ?
		ORDER BY fts.rank
		LIMIT ?`,
		q, limit)
	if err != nil {
		return nil, fmt.Errorf("fts query: %w", err)
	}
	defer rows.Close()

	var out []Memory
	for rows.Next() {
		var (
			mem       Memory
			tagsStr   string
			createdAt string
		)
		if err := rows.Scan(&mem.ID, &mem.Content, &tagsStr, &mem.SessionID, &createdAt, &mem.Score); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		if tagsStr != "" {
			mem.Tags = strings.Split(tagsStr, ",")
		}
		if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
			mem.CreatedAt = t
		}
		out = append(out, mem)
	}
	return out, rows.Err()
}

// OnSessionStart records a marker memory so a future search can find
// "everything from session X". Cheap and useful for the doc demo.
func (s *SQLiteMemory) OnSessionStart(ctx context.Context, sessionID string) error {
	return nil // intentionally lightweight; production hermes prefetches recent memories here
}

// OnSessionEnd is a no-op in our mini. Real hermes flushes pending writes,
// updates session metadata, and queues prefetches for the next session.
func (s *SQLiteMemory) OnSessionEnd(ctx context.Context, sessionID string) error {
	return nil
}

func (s *SQLiteMemory) Close() error { return s.db.Close() }
