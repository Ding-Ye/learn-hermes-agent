package main

import (
	"context"
	"time"
)

// Memory now carries LastActivityAt and ArchivedAt. The curator (s06) reads
// LastActivityAt to decide which memories are stale; ArchivedAt being
// non-zero hides a row from search.
type Memory struct {
	ID             int64     `json:"id"`
	Content        string    `json:"content"`
	Tags           []string  `json:"tags,omitempty"`
	SessionID      string    `json:"session_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	LastActivityAt time.Time `json:"last_activity_at"`
	ArchivedAt     time.Time `json:"archived_at,omitempty"` // zero when active
	Score          float64   `json:"score,omitempty"`
}

// MemoryProvider — same shape as s05 plus three curator-facing methods:
// Touch, Archive, and ListStale. The agent loop only sees Search/Save +
// lifecycle hooks; the curator plugin uses the lifecycle hooks AND the
// curator-facing helpers.
type MemoryProvider interface {
	Search(ctx context.Context, query string, limit int) ([]Memory, error)
	Save(ctx context.Context, m Memory) (int64, error)

	OnSessionStart(ctx context.Context, sessionID string) error
	OnSessionEnd(ctx context.Context, sessionID string) error

	// Curator-facing.
	Touch(ctx context.Context, id int64) error
	Archive(ctx context.Context, ids []int64) error
	ListStale(ctx context.Context, olderThan time.Duration, limit int) ([]Memory, error)

	Close() error
}
