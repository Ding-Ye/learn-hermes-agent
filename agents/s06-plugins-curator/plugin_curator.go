package main

import (
	"context"
	"time"
)

// CuratorPlugin is the truth behind hermes's "self-improving memory"
// tagline: it does NOT generate new memories, it AUTOMATICALLY ARCHIVES
// stale ones. Implementation:
//
//  1. On every OnSessionStart, ask the memory provider for memories
//     whose last_activity_at is older than StaleAfter.
//  2. Archive them (set archived_at; FTS5 search excludes archived rows).
//  3. Print a short report so the user sees the action.
//
// Production hermes runs the curator on a wall-clock interval too
// (every N minutes via cron) and supports `pin` (never auto-archive),
// `prune` (force-archive worst offenders), and `rollback` (snapshot+
// restore the entire skills/memories tree). We keep the smallest
// teaching slice — archive-on-session-start — and call out the rest
// in the doc.
type CuratorPlugin struct {
	StaleAfter time.Duration // how old "stale" means
	Limit      int           // archive at most N per run

	host *Host
}

func NewCuratorPlugin(staleAfter time.Duration, limit int) *CuratorPlugin {
	if limit <= 0 {
		limit = 100
	}
	return &CuratorPlugin{StaleAfter: staleAfter, Limit: limit}
}

func (c *CuratorPlugin) Name() string { return "curator" }

func (c *CuratorPlugin) Init(ctx context.Context, host *Host) error {
	c.host = host
	host.Logger.Infof("[plugin:curator] init: stale_after=%s limit=%d", c.StaleAfter, c.Limit)
	return nil
}

func (c *CuratorPlugin) OnSessionStart(ctx context.Context, sid string) error {
	stale, err := c.host.Memory.ListStale(ctx, c.StaleAfter, c.Limit)
	if err != nil {
		return err
	}
	if len(stale) == 0 {
		c.host.Logger.Infof("[plugin:curator] nothing to archive")
		return nil
	}
	ids := make([]int64, 0, len(stale))
	for _, m := range stale {
		ids = append(ids, m.ID)
	}
	if err := c.host.Memory.Archive(ctx, ids); err != nil {
		return err
	}
	c.host.Logger.Infof("[plugin:curator] archived %d stale memories: %v", len(ids), ids)
	return nil
}

// OnSessionEnd is a no-op for the curator. Stale gardening on
// session-start is enough; doing it again on session-end would archive
// memories the agent JUST saved (their last_activity_at is "now", so
// they're not stale yet — but only in the literal sense of the
// inequality; cleaner to just not run twice).
func (c *CuratorPlugin) OnSessionEnd(ctx context.Context, sid string) error { return nil }

func (c *CuratorPlugin) Close() error { return nil }
