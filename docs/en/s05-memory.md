---
title: "s05 · Memory Provider + FTS5"
chapter: 5
slug: s05-memory
est_read_min: 14
---

# s05 · Memory Provider + FTS5

> What this teaches: define a **`MemoryProvider` interface**, ship a **SQLite + FTS5** implementation, expose `memory_search` / `memory_save` as builtin tools. The agent now recalls facts across sessions — the actual foundation under hermes's "self-curating memory".

---

## Problem

s04 made a *single* conversation resumable: `-resume <id>` brought back that conversation's transcript. But the agent really wants to do something different:

- "The user mentioned last week that they're using Postgres" — **cross-session fact recall**
- "What was that docker command I ran in this project last time?" — **keyword search across past turns**
- "Remember that I prefer concise answers" — **persistent user preferences**

That's *memory*, not *session*. Different beast: memory is a shared fact base for the user/agent, alive across all sessions; a session is the transcript of one conversation.

There's also one non-obvious design decision: **memory should be an interface, not a concrete store**. SQLite + FTS5 is fine today; tomorrow it might be Postgres + pgvector, then Pinecone. The loop should never know. That's exactly what hermes's `MemoryProvider` abstraction is for.

## Solution

Four pieces:

1. **`MemoryProvider` interface**: `Search` / `Save` exposed to the LLM as tools, `OnSessionStart` / `OnSessionEnd` lifecycle hooks (placeholders for hermes's three-phase prefetch→sync→queue), `Close` releases resources.
2. **`SQLiteMemory` implementation**: one SQLite file at `~/.learn-hermes-agent/memory.db`, the `memories` table paired with an **FTS5 virtual table + three triggers** that auto-sync on insert/update/delete. Pure-Go driver (`modernc.org/sqlite`) — no cgo.
3. **Two builtin tools**: `memory_search(query, limit)` / `memory_save(content, tags)`. The model decides when to call them.
4. **Loop gains lifecycle hooks**: `Run` calls `OnSessionStart` on entry, `defer OnSessionEnd`. The SQLite impl makes them no-ops here; s06's plugin system will route them.

## How It Works

```ascii-anim frames=2
┌─────────────────────────────────────────────────────────────────┐
│  ~/.learn-hermes-agent/memory.db  (one SQLite file, all sessions)│
│  ┌──────────────────────────────────────────────────────────┐   │
│  │ memories(id, content, tags, session_id, created_at)      │   │
│  │ memories_fts(content)   ─── FTS5 virtual, rowid=id       │   │
│  │   AFTER INSERT/DELETE/UPDATE triggers auto-sync          │   │
│  └──────────────────────────────────────────────────────────┘   │
│         ▲                        │                              │
│  Save   │                        │ Search                       │
│         │                        ▼                              │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  MemoryProvider interface                                 │   │
│  │  ├─ Search(ctx, query, limit) → []Memory                  │   │
│  │  ├─ Save(ctx, Memory) → id                                │   │
│  │  ├─ OnSessionStart / OnSessionEnd  (lifecycle hooks)      │   │
│  │  └─ Close                                                  │   │
│  └──────────────────────────────────────────────────────────┘   │
│         ▲                                                       │
│         │ via MemorySearchTool / MemorySaveTool                 │
│         │ registered into the s02 Registry                      │
│   builtin: bash, read_file, memory_search, memory_save          │
└─────────────────────────────────────────────────────────────────┘
```

The core schema (excerpt from [`memory_sqlite.go`](https://github.com/Ding-Ye/learn-hermes-agent/blob/main/agents/s05-memory/memory_sqlite.go)):

```sql
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;

CREATE TABLE memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    content TEXT NOT NULL,
    tags TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);

CREATE VIRTUAL TABLE memories_fts USING fts5(
    content,
    content='memories', content_rowid='id',
    tokenize='unicode61'
);

-- Three triggers keep the fts table in sync with memories — no app code needed.
CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts(rowid, content) VALUES (new.id, new.content);
END;
CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, content) VALUES('delete', old.id, old.content);
END;
CREATE TRIGGER memories_au AFTER UPDATE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, content) VALUES('delete', old.id, old.content);
    INSERT INTO memories_fts(rowid, content) VALUES (new.id, new.content);
END;
```

Search uses FTS5 `MATCH` + `ORDER BY rank`:

```go
rows, err := s.db.QueryContext(ctx, `
    SELECT m.id, m.content, m.tags, m.session_id, m.created_at, fts.rank
    FROM memories_fts AS fts
    JOIN memories AS m ON m.id = fts.rowid
    WHERE memories_fts MATCH ?
    ORDER BY fts.rank
    LIMIT ?`,
    q, limit)
```

**Four non-obvious points**:

1. **`content='memories', content_rowid='id'`** is FTS5's *external content* mode. The fts table doesn't store a copy of the content — only the inverted index; `MATCH` queries roundtrip through `rowid` to fetch from `memories`. **Half the disk** at the cost of needing the three AFTER triggers (`_ai` / `_ad` / `_au`) to keep things in sync.

2. **`fts.rank` ascending = BM25** — FTS5 puts the BM25 score (which is *negative*) on `rank`, **smaller is more relevant**. A row hitting both terms in `"docker postgres"` gets `-5`, a row hitting one gets `-2`, so the right order falls out for free.

3. **PRAGMA WAL + busy_timeout** are unnecessary single-process — but s09's multi-process gateway needs them. Configure once now, save a migration later. Upstream hermes gateway processes share the db this way.

4. **`unicode61` tokenizer** — the default `simple` tokenizer only knows ASCII and treats CJK as one giant token. `unicode61` segments by Unicode boundaries so Chinese/Japanese search at all (though it's not as good as a real CJK segmenter like jieba/IK).

## What Changed (vs. s04)

```diff
  type Loop struct {
      Provider Provider
      Registry *Registry
      Store    *Store
+     Memory   MemoryProvider     // new in s05
      MaxTurns int
      Verbose  bool
  }

  func (l *Loop) Run(ctx context.Context, sess *Session) error {
+     if l.Memory != nil {
+         _ = l.Memory.OnSessionStart(ctx, sess.ID)
+         defer l.Memory.OnSessionEnd(ctx, sess.ID)
+     }
      for turn := 0; turn < l.MaxTurns; turn++ { ... }
  }

  // main.go
+ mem, err := NewSQLiteMemory(memPath)
+ defer mem.Close()
+ registry.Register(NewMemorySearchTool(mem), ToolsetBuiltin)
+ registry.Register(NewMemorySaveTool(mem, sess.ID), ToolsetBuiltin)
```

Note: **the memory db is not part of a session** — one db serves all sessions, all users, and (later) all sources (cli/telegram/discord). That's why the default path is `~/.learn-hermes-agent/memory.db`, not `sessions/<id>/memory.db`.

## Try It

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s05-memory

# turn 1: ask the agent to remember something
go run . -v "remember that my favorite color is blue, save it as a memory tagged 'preference'"

# turn 2: new session (shares the same memory.db by default), recall
go run . -v "what's my favorite color? search your memory"

# Inspect the db
sqlite3 ~/.learn-hermes-agent/memory.db "SELECT id,content,tags,session_id FROM memories;"

# Tests
go test -v ./...
```

Expected second-run output:

```
[memory] db=/Users/yeding/.learn-hermes-agent/memory.db
[session] id=s05-... parent=- msgs=1
[registry] tools=[bash memory_save memory_search read_file]
[turn 0] -> memory_search map[query:favorite color]
[turn 0] <- {"results":[{"id":1,"content":"user's favorite color is blue","tags":["preference"],...}]}
[turn 1] assistant: Your favorite color is blue.
Your favorite color is blue.
```

## Upstream Source Reading

hermes's `MemoryProvider` lives in `agent/memory_provider.py` (an ABC) plus per-plugin implementations. The design is **three-phase (Prefetch → Sync → Queue)** — every turn, the provider prefetches relevant memories into context BEFORE the LLM call, syncs new memories AFTER, and **asynchronously queues** the next prefetch to overlap I/O with the LLM call.

```upstream:hermes_agent/memory_provider.py#L1-L60
# Excerpted + simplified from agent/memory_provider.py and an FTS5 plugin

from abc import ABC, abstractmethod
from typing import Iterable

class MemoryProvider(ABC):
    """Abstract base class. The agent loop never types against a concrete
    backend; only this interface. Plugins (s06) supply implementations:
    SQLite+FTS5, Postgres+pgvector, Qdrant, ..."""

    # Tool-facing
    @abstractmethod
    async def search(self, query: str, limit: int = 5) -> list["Memory"]: ...
    @abstractmethod
    async def save(self, m: "Memory") -> int: ...

    # 3-phase lifecycle hooks the loop calls.
    # Prefetch runs BEFORE the user's turn lands at the LLM:
    # the provider returns memories to be injected as context.
    @abstractmethod
    async def prefetch(self, q: "PrefetchQuery") -> list["Memory"]: ...

    # Sync runs AFTER each turn — write new memories the agent decided
    # to save during this turn.
    @abstractmethod
    async def sync(self, session_id: str, turn_blocks: list[dict]) -> None: ...

    # Queue starts the next prefetch ASYNCHRONOUSLY so I/O overlaps
    # with the LLM call. By the time the next turn needs the memories,
    # they're already in the cache.
    @abstractmethod
    async def queue_next(self, session_id: str) -> None: ...

    @abstractmethod
    async def close(self) -> None: ...


# --- one implementation: hermes_memory_fts5.py (community plugin) -----------

class FTS5MemoryProvider(MemoryProvider):
    """SQLite + FTS5 + WAL. Same shape as our Go SQLiteMemory but:
       - async (asyncio + aiosqlite)
       - prefetches recent memories on every session start
       - keeps a per-session tag namespace
       - integrates with the Curator for stale-memory pruning"""

    SCHEMA = """
    CREATE TABLE IF NOT EXISTS memories (
        id INTEGER PRIMARY KEY,
        content TEXT NOT NULL,
        tags TEXT NOT NULL DEFAULT '[]',  -- JSON, not CSV
        session_id TEXT NOT NULL,
        last_activity_at REAL,            -- ★ used by Curator (s06)
        created_at REAL,
        ...
    );
    CREATE VIRTUAL TABLE memories_fts USING fts5(
        content, tags,                    -- both columns indexed
        content='memories', content_rowid='id',
        tokenize='unicode61 remove_diacritics 2'
    );
    """

    async def prefetch(self, q):
        # "Recent memories from the same source/session, fanout to FTS5
        # for anything semantically close to the user's last message."
        recents = await self._fetch_recent(q.session_id, limit=q.recent)
        related = await self._fts_search(q.user_text, limit=q.related)
        return _merge_dedupe(recents + related)

    async def queue_next(self, session_id):
        # Fire-and-forget background task; results land in self._cache.
        asyncio.create_task(self._prefetch_into_cache(session_id))
```

**Reading notes**:

- **The 3-phase prefetch/sync/queue** is the heart of hermes memory. Our mini only does *the agent explicitly invokes a tool*. Simpler and easier to read, but it costs an I/O round trip (the user waits for `memory_search` before getting an answer). Real hermes hides that cost.

- **`tags`: JSON vs CSV** — upstream uses a JSON array (nestable, indexable); we use comma-separated. CSV makes `MATCH 'tags:"preference"'`-style filters awkward.

- **`last_activity_at`** is the **hook the Curator uses (s06)**: every search/save bumps it, the curator scans for "untouched in N days" and archives. "Hermes self-improving" = automatic stale-memory archiving, **not** automatic memory generation. We already corrected this myth in s03; s06 actually implements it.

- **`FTS5(content, tags)`** — upstream's FTS5 indexes both `content` AND `tags`, enabling `MATCH 'tags:preference AND blue'`. Ours indexes content only; tag filters require post-filtering.

- **`unicode61 remove_diacritics 2`** — upstream's tokenizer also strips diacritics (café → cafe), friendlier for accented-Latin search.

**Read further**: start from `MemoryProvider` ABC in `agent/memory_provider.py`, follow `plugins/hermes_memory_fts5/__init__.py` for the concrete impl, then `agent/memory_manager.py` for the prefetch/sync/queue orchestration. That trace → s06 (plugin system registers it) → s09 (multi-process shares the db).

---

**Next**: s06 introduces the plugin system. Memory providers, curator, observability all plug in via a single lifecycle bus. s05's `OnSessionStart` / `OnSessionEnd` hooks will be dispatched by the plugin manager — same interface, multiple listeners.
