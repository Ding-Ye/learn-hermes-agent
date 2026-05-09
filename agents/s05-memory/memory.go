package main

import (
	"context"
	"time"
)

// Memory is one persisted fact/recall unit. Generic enough that different
// providers can implement it on top of SQLite, Postgres, vector DBs, etc.
//
// `Score` is populated on Search returns (FTS rank, vector similarity, etc.)
// and ignored on Save.
type Memory struct {
	ID        int64     `json:"id"`
	Content   string    `json:"content"`
	Tags      []string  `json:"tags,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Score     float64   `json:"score,omitempty"`
}

// MemoryProvider is the contract every backend implements. Three groups:
//
//   - Search/Save are tool-facing — exposed to the LLM as memory_search /
//     memory_save tools, the agent invokes them deliberately.
//   - OnSessionStart/OnSessionEnd are lifecycle hooks the loop calls. They
//     enable the three-phase pattern hermes uses: prefetch ahead, sync
//     during, queue for next turn. We use them in s05 as no-ops/light
//     updates; s06's plugin system will route real prefetch work here.
//   - Close releases backing resources (db handle, etc.).
//
// hermes calls this an "ABC Provider" (abstract base class). The point is
// that the agent loop stays oblivious to whether memory lives in SQLite,
// Postgres, or a vector store — only the interface matters.
type MemoryProvider interface {
	Search(ctx context.Context, query string, limit int) ([]Memory, error)
	Save(ctx context.Context, m Memory) (int64, error)

	OnSessionStart(ctx context.Context, sessionID string) error
	OnSessionEnd(ctx context.Context, sessionID string) error

	Close() error
}
