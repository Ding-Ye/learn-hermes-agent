---
title: "s04 · Session persistence"
chapter: 4
slug: s04-session
est_read_min: 13
---

# s04 · Session persistence

> What this teaches: persist conversation state (messages list + token totals + metadata) to disk so the agent survives across processes. Save atomically after every turn — hermes's "Ctrl-C survival" philosophy. CLI subcommands `-resume` `-branch` `-reset` `-list`.

---

## Problem

The s01–s03 agents die after one run. Every `go run` starts from an empty messages list. Two concrete pain points:

1. **No follow-ups.** After "what is 17 * 23?", the next "now add 100" sees nothing of the prior turn — the user has to repeat context.
2. **Mid-run crashes lose everything.** Ctrl-C, an SSH drop, a transient API failure — and dozens of tool calls and thousands of tokens of work are gone.

Both problems point to **persistence**, but several non-obvious choices follow:
- **Where to store?** Files vs database?
- **What to store?** Just the messages, or also token totals, model ID, ParentID, ...?
- **When to write?** Every turn? Every N turns? Only on exit?
- **How to write?** Plain `WriteFile` — what happens on power loss mid-write?

s04 ships the smallest answer that works; s05 layers FTS5 full-text search on top; upstream hermes pushes the same pattern into a production SQLite store.

## Solution

Four pieces:

1. **`Session` struct**: `ID` `CreatedAt` `UpdatedAt` `Model` `ParentID` `Messages` `Usage`, serialised as JSON.
2. **`Store`** is a directory abstraction: `Save(s)` / `Load(id)` / `List()` / `Delete(id)`. One file per session at `<id>.json`.
3. **`Store.Save()` after every turn** — hermes's Ctrl-C survival philosophy. If the process dies, the next `-resume` only loses tool results between the previous Save and the crash.
4. **Atomic writes**: `os.WriteFile(tmp) → os.Rename(tmp, final)`. After power loss, you have either the full old version or the full new version, never a half-written file.

CLI: `-resume <id>` `-branch <id>` `-reset <id>` `-list`, plus a prompt (except for `-reset`/`-list`).

## How It Works

```ascii-anim frames=3
┌──────────────────────────────────────────────────────────────────┐
│ ~/.learn-hermes-agent/sessions/                                  │
│   s04-20260509-135133-a1b2c3.json                                │
│   s04-20260509-135533-d4e5f6.json   (parent: …a1b2c3)            │
│                                                                  │
│   Each .json:                                                    │
│   { "id": "...", "model": "...",                                 │
│     "parent_id": "..." (set on branch),                          │
│     "messages": [ {role, content[]}, ... ],                      │
│     "usage": { turns, input_tokens, output_tokens, ... } }       │
└──────────────────────────────────────────────────────────────────┘
                          │ Save / Load
                          ▼
┌──────────────────────────────────────────────────────────────────┐
│ Loop.Run(ctx, sess):                                             │
│   for turn := 0; turn < MaxTurns; turn++:                        │
│     resp = provider.CreateMessage(sess.Messages, schemas)        │
│     sess.AppendAssistant(resp.Content)                           │
│     sess.Usage.Add(resp.Usage)                                   │
│     ────►  store.Save(sess)   ★ persist every turn               │
│     switch resp.StopReason:                                      │
│       end_turn:                  return                          │
│       tool_use:                                                  │
│         results = runTools(...)                                  │
│         sess.AppendUserToolResults(results)                      │
│     ────►  store.Save(sess)   ★ persist tool results too         │
└──────────────────────────────────────────────────────────────────┘
```

The atomic write (6 lines, excerpt from [`session.go`](https://github.com/Ding-Ye/learn-hermes-agent/blob/main/agents/s04-session/session.go)):

```go
func (st *Store) Save(s *Session) error {
    data, _ := json.MarshalIndent(s, "", "  ")
    final := st.pathFor(s.ID)
    tmp := final + ".tmp"
    if err := os.WriteFile(tmp, data, 0o644); err != nil { return err }
    return os.Rename(tmp, final)   // POSIX rename is atomic
}
```

Branch (8 lines):

```go
func (s *Session) Branch() *Session {
    fork := &Session{
        ID:        newSessionID(time.Now().UTC()),
        ParentID:  s.ID,
        Model:     s.Model,
        Messages:  append([]Message(nil), s.Messages...), // deep-copy slice
        // Usage left zero on purpose: every fork is its own balance sheet.
    }
    return fork
}
```

**Four non-obvious points**:

1. **Persist every turn** — not just at exit. The cost is a few extra `WriteFile`s (tens of KB each), the gain is that arbitrary interruption (Ctrl-C / OOM / dropped connection / kernel panic) only loses work between the last LLM call and the next Save. The LLM call itself takes seconds; I/O is rounding error.

2. **`os.Rename` is atomic** (POSIX guarantee), but `os.WriteFile` is **not** — it opens the final path and overwrites in place, leaving a half-written file on power loss. So the pattern *must* be "write tmp → rename". Go's rename is also atomic on Windows (since Go 1.5).

3. **Branch must deep-copy the messages slice**: `append([]Message(nil), s.Messages...)` rather than `s.Messages` directly — otherwise appending to the fork would mutate the parent (shared backing array). **`Message.Content` is also a slice but we don't deep-copy that** — meaning a mutation inside a content block on the fork would leak. Sufficient for teaching, not for production.

4. **Branch resets Usage to zero**: each fork is its own balance sheet. To see lineage totals, walk ParentID at report time. The simplification is worth it — without it, "how much did this fork cost" requires arithmetic.

## What Changed (vs. s03)

```diff
  type Loop struct {
      Provider Provider
      Registry *Registry
+     Store    *Store          // new in s04
      MaxTurns int
      Verbose  bool
  }

- func (l *Loop) Run(ctx context.Context, userPrompt string) (string, error) {
-     messages := []Message{ {Role: "user", ...} }
+ func (l *Loop) Run(ctx context.Context, sess *Session) error {
      for turn := 0; turn < l.MaxTurns; turn++ {
          resp, _ := l.Provider.CreateMessage(...)
+         sess.AppendAssistant(resp.Content)
+         sess.Usage.Add(resp.Usage)
+         if err := l.Store.Save(sess); err != nil { return err }
          ...
      }
  }
```

The signature change is real: `Loop.Run` no longer takes a string and returns the final text. It takes a `*Session` and mutates it in place. `LastAssistantText(sess)` extracts the user-visible text in main.go.

s04 drops s03's skills temporarily — single-chapter focus on persistence keeps the noise down. A production agent has both, of course.

## Try It

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s04-session

# Fresh session
go run . -v "what is 17 * 23?"
# stderr: [session] s04-20260509-135133-a1b2c3 saved (turns=2 in=312 out=89)

# Look at the persisted file
cat ~/.learn-hermes-agent/sessions/s04-20260509-135133-a1b2c3.json

# Resume: continue talking
go run . -resume s04-20260509-135133-a1b2c3 "now add 100"

# Branch: fork a parallel line
go run . -branch s04-20260509-135133-a1b2c3 "actually subtract 50 from the first answer"

# List
go run . -list

# Delete
go run . -reset s04-20260509-135133-a1b2c3

# Tests
go test -v ./...
```

Resume output:

```
[session] id=s04-20260509-135133-a1b2c3 parent=- msgs=4 turns=2 in/out=312/89
[turn 0] assistant: 391 + 100 = 491.
491
```

The stderr line carries `msgs=4 turns=2` — resume loaded not just 4 messages but the running token totals.

## Upstream Source Reading

hermes stores conversation state in **SQLite** (`hermes_state.SessionDB`), not one-file-per-session JSON. The schema:

```upstream:hermes_agent/hermes_state.py#L1-L70
import sqlite3
import time
import random
from pathlib import Path

class SessionDB:
    """SQLite-backed session store. WAL mode for concurrent reads (gateway
    processes for telegram/discord/cli all read the same db). FTS5 virtual
    table on messages enables search across sessions — that's how
    `/resume <title>` and the agent's own past-conversation recall work.
    """

    SCHEMA = """
    CREATE TABLE IF NOT EXISTS sessions (
        id TEXT PRIMARY KEY,
        source TEXT,                  -- cli | telegram | discord | slack
        user_id TEXT,
        model TEXT,
        model_config JSON,
        system_prompt TEXT,
        parent_session_id TEXT,       -- compression chain (NOT branching)
        started_at REAL, ended_at REAL, end_reason TEXT,
        message_count INTEGER,
        tool_call_count INTEGER,
        input_tokens INTEGER, output_tokens INTEGER,
        cache_read_tokens INTEGER, reasoning_tokens INTEGER,
        estimated_cost_usd REAL, actual_cost_usd REAL, cost_status TEXT,
        title TEXT UNIQUE,
        api_call_count INTEGER
    );
    CREATE TABLE IF NOT EXISTS messages (
        id INTEGER PRIMARY KEY,
        session_id TEXT REFERENCES sessions(id),
        role TEXT, content TEXT,
        created_at REAL
    );
    CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
        content, content='messages', content_rowid='id'
    );
    """

    def __init__(self, path: Path):
        self.conn = sqlite3.connect(path, isolation_level=None)
        self.conn.execute("PRAGMA journal_mode=WAL")
        self.conn.execute("PRAGMA busy_timeout=5000")
        self.conn.executescript(self.SCHEMA)

    def _execute_write(self, sql, params=()):
        """Atomic write with jittered retry on lock contention. Multiple
        gateway processes may write concurrently — BEGIN IMMEDIATE
        acquires the write lock right away rather than waiting for the
        first DML, which lets us back off cleanly instead of deadlocking
        with another writer."""
        for attempt in range(8):
            try:
                self.conn.execute("BEGIN IMMEDIATE")
                self.conn.execute(sql, params)
                self.conn.execute("COMMIT")
                return
            except sqlite3.OperationalError as e:
                self.conn.execute("ROLLBACK")
                if "locked" not in str(e):
                    raise
                time.sleep(random.uniform(0.020, 0.150))
        raise RuntimeError("could not acquire write lock after 8 attempts")

    def get_compression_tip(self, session_id: str) -> str:
        """Walk parent_session_id chain to the latest tip.

        When a session's context gets too long, hermes compresses old
        turns into a summary, creates a NEW session whose
        parent_session_id points back to the original, and continues
        from there. Resuming the original transparently jumps to the
        latest compressed tip — the user types `/resume <title>` and
        sees the freshest context, not the deprecated original.
        """
        cur = session_id
        for _ in range(64):  # cycle guard
            row = self.conn.execute(
                "SELECT id FROM sessions WHERE parent_session_id = ?",
                (cur,),
            ).fetchone()
            if not row: return cur
            cur = row[0]
        return cur
```

**Reading notes**:

- **SQLite vs JSON files**: upstream stores everything in one db file (across all platforms — cli/telegram/discord), WAL mode lets gateway processes read concurrently. Our mini's one-JSON-per-session is fine for a single writer but doesn't scale to multi-process. s05 introduces FTS5; s09 introduces multi-process — that's when SQLite's concurrency model earns its keep.
- **`parent_session_id` is NOT branching, it's the COMPRESSION CHAIN**! This is hermes's long-context solution: when a session crosses N tokens, compress old turns into a summary, create a new session pointing back via `parent_session_id`, continue from there. `get_compression_tip` walks the chain forward — the user types `/resume <title>` and hermes jumps to the freshest tip. **Our ParentID is branching (user-initiated fork)**. Same name, different concept; mind the gap.
- **FTS5 messages_fts**: upstream's `messages` table is paired with an FTS5 virtual table; `/resume <title>` is fuzzy title matching, and the agent's own past-conversation recall reads from the same FTS. Not in our mini. s05 introduces FTS5 (in the memory provider).
- **`BEGIN IMMEDIATE` + jitter retry**: with multiple gateway processes writing concurrently, `BEGIN IMMEDIATE` grabs the write lock right away (vs the default deferred lock), and a jittered 20–150ms retry handles the rest. Our `os.Rename` is single-writer — atomic but doesn't scale.
- **Multi-source tracking**: the `source` field marks where a session came from (cli/telegram/discord/slack). That's the prerequisite for s10 (gateway adapters) — same user across IM platforms is one db, distinguishable per-source.
- **Cost tracking**: `estimated_cost_usd` `actual_cost_usd` `cost_status`. Personal use can ignore it; production deployments need it.

**Read further**: start at `SessionDB.create_session` in `hermes_state.py`, follow `append_message` to see how messages enter FTS5, follow `get_compression_tip` for the chain logic. Then in `hermes_cli/main.py`, find `_resolve_session_by_name_or_id` for the title-to-id resolution. That trace is the s04 → s05 (FTS5 memory) → s09 (multi-process) → s10 (multi-source gateway) real-source map.

---

**Next**: s05 introduces a **MemoryProvider interface** plus an **FTS5 SQLite implementation**. Session persistence (s04) is "remember the current conversation"; memory (s05) is "the agent recalls last week's conversations" — the actual core of hermes's "self-curating memory".
