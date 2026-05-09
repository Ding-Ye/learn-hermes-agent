package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteMemory adds two columns vs s05:
//   last_activity_at — bumped on Save and Search hits; the curator's input
//   archived_at      — non-empty hides the row; the curator's output
//
// The FTS5 trigger setup is the same; the search SELECT now filters
// archived_at IS NULL so curated rows really disappear from results
// without losing history (we keep the row in memories, just stop
// indexing it).

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
    created_at TEXT NOT NULL,
    last_activity_at TEXT NOT NULL,
    archived_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_memories_active_lastact
    ON memories(last_activity_at) WHERE archived_at IS NULL;

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
	now := time.Now().UTC()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	if m.LastActivityAt.IsZero() {
		m.LastActivityAt = now
	}
	tags := strings.Join(m.Tags, ",")
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO memories(content, tags, session_id, created_at, last_activity_at)
		VALUES (?, ?, ?, ?, ?)`,
		m.Content, tags, m.SessionID,
		m.CreatedAt.Format(time.RFC3339Nano),
		m.LastActivityAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, fmt.Errorf("insert memory: %w", err)
	}
	return res.LastInsertId()
}

func (s *SQLiteMemory) Search(ctx context.Context, query string, limit int) ([]Memory, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return []Memory{}, nil
	}
	if limit <= 0 {
		limit = 5
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.content, m.tags, m.session_id,
		       m.created_at, m.last_activity_at, fts.rank
		FROM memories_fts AS fts
		JOIN memories AS m ON m.id = fts.rowid
		WHERE memories_fts MATCH ?
		  AND m.archived_at IS NULL
		ORDER BY fts.rank
		LIMIT ?`, q, limit)
	if err != nil {
		return nil, fmt.Errorf("fts query: %w", err)
	}
	defer rows.Close()

	var out []Memory
	var hitIDs []int64
	for rows.Next() {
		var (
			mem            Memory
			tagsStr        string
			createdAt      string
			lastActivityAt string
		)
		if err := rows.Scan(&mem.ID, &mem.Content, &tagsStr, &mem.SessionID, &createdAt, &lastActivityAt, &mem.Score); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		if tagsStr != "" {
			mem.Tags = strings.Split(tagsStr, ",")
		}
		if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
			mem.CreatedAt = t
		}
		if t, err := time.Parse(time.RFC3339Nano, lastActivityAt); err == nil {
			mem.LastActivityAt = t
		}
		out = append(out, mem)
		hitIDs = append(hitIDs, mem.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// A successful search counts as activity — bump last_activity_at on
	// every hit so the curator doesn't archive recently-recalled memories.
	if len(hitIDs) > 0 {
		_ = s.touchMany(ctx, hitIDs)
	}
	return out, nil
}

func (s *SQLiteMemory) Touch(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE memories SET last_activity_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

func (s *SQLiteMemory) touchMany(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	placeholders := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, now)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	q := fmt.Sprintf(`UPDATE memories SET last_activity_at = ? WHERE id IN (%s)`,
		strings.Join(placeholders, ","))
	_, err := s.db.ExecContext(ctx, q, args...)
	return err
}

func (s *SQLiteMemory) Archive(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	placeholders := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, now)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	q := fmt.Sprintf(`UPDATE memories SET archived_at = ? WHERE id IN (%s)`,
		strings.Join(placeholders, ","))
	_, err := s.db.ExecContext(ctx, q, args...)
	return err
}

// ListStale returns active memories with last_activity_at older than now-d,
// up to limit. Used by the curator to decide what to archive.
func (s *SQLiteMemory) ListStale(ctx context.Context, olderThan time.Duration, limit int) ([]Memory, error) {
	if limit <= 0 {
		limit = 100
	}
	cutoff := time.Now().UTC().Add(-olderThan).Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, content, tags, session_id, created_at, last_activity_at
		FROM memories
		WHERE archived_at IS NULL AND last_activity_at < ?
		ORDER BY last_activity_at ASC
		LIMIT ?`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var (
			mem            Memory
			tagsStr        string
			createdAt      string
			lastActivityAt string
		)
		if err := rows.Scan(&mem.ID, &mem.Content, &tagsStr, &mem.SessionID, &createdAt, &lastActivityAt); err != nil {
			return nil, err
		}
		if tagsStr != "" {
			mem.Tags = strings.Split(tagsStr, ",")
		}
		if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
			mem.CreatedAt = t
		}
		if t, err := time.Parse(time.RFC3339Nano, lastActivityAt); err == nil {
			mem.LastActivityAt = t
		}
		out = append(out, mem)
	}
	return out, rows.Err()
}

func (s *SQLiteMemory) OnSessionStart(ctx context.Context, sessionID string) error { return nil }
func (s *SQLiteMemory) OnSessionEnd(ctx context.Context, sessionID string) error   { return nil }

func (s *SQLiteMemory) Close() error { return s.db.Close() }
