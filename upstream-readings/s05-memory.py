# Upstream source reading for s05 · Memory Provider + FTS5
#
# Sources:
#   - NousResearch/hermes-agent · agent/memory_provider.py    (ABC)
#   - NousResearch/hermes-agent · plugins/hermes_memory_fts5  (one impl)
#   - NousResearch/hermes-agent · agent/memory_manager.py     (orchestration)
# License: see https://github.com/NousResearch/hermes-agent/blob/main/LICENSE
#
# Teaching excerpt focused on the parts that map onto our s05 mini.
# What we deliberately skip is called out in the docs.


# ============================================================================
# agent/memory_provider.py  --  the ABC every backend implements
# ============================================================================

from abc import ABC, abstractmethod
from dataclasses import dataclass
from typing import Optional


@dataclass(frozen=True)
class Memory:
    id: int
    content: str
    tags: list[str]
    session_id: str
    created_at: float
    last_activity_at: float = 0.0  # ← used by the Curator (s06) to prune
    score: Optional[float] = None  # ← BM25/cosine, set on Search returns


@dataclass(frozen=True)
class PrefetchQuery:
    session_id: str
    user_text: str       # last user message — for FTS-based relevance
    recent: int = 5      # how many recent memories from this session
    related: int = 5     # how many "semantically close" memories


class MemoryProvider(ABC):
    """The agent loop never types against a concrete backend — only this
    interface. Plugins (s06) supply impls: SQLite+FTS5, Postgres+pgvector,
    Qdrant, etc.

    Three groups of methods:

      Tool-facing  : search / save     — bound to LLM tools
      Lifecycle    : prefetch / sync / queue_next — driven by the loop
      Resource     : close             — release backing handles
    """

    # ----- tool-facing ----------------------------------------------------

    @abstractmethod
    async def search(self, query: str, limit: int = 5) -> list[Memory]: ...

    @abstractmethod
    async def save(self, m: Memory) -> int: ...

    # ----- 3-phase lifecycle ---------------------------------------------

    @abstractmethod
    async def prefetch(self, q: PrefetchQuery) -> list[Memory]:
        """Called BEFORE the LLM call. Returns memories that should be
        injected as context for this turn. The agent loop merges them
        into the system prompt or as a leading user message — exact
        injection point is a separate decision in agent/memory_manager.py.
        """

    @abstractmethod
    async def sync(self, session_id: str, turn_blocks: list[dict]) -> None:
        """Called AFTER the LLM call. Writes new memories the agent decided
        to save during this turn (often via a tool_use of memory_save)."""

    @abstractmethod
    async def queue_next(self, session_id: str) -> None:
        """Asynchronously starts the next turn's prefetch. The point is to
        overlap I/O with the still-running LLM call — by the time the
        next turn lands, results are already cached."""

    # ----- resource ------------------------------------------------------

    @abstractmethod
    async def close(self) -> None: ...


# ============================================================================
# plugins/hermes_memory_fts5/__init__.py  --  one concrete implementation
# ============================================================================

class FTS5MemoryProvider(MemoryProvider):
    """SQLite + FTS5 + WAL. Same shape as our Go SQLiteMemory but:

      - async (asyncio + aiosqlite)
      - prefetches recent memories on session start
      - keeps a per-session tag namespace
      - integrates with the Curator for stale-memory pruning
    """

    SCHEMA = """
    CREATE TABLE IF NOT EXISTS memories (
        id INTEGER PRIMARY KEY,
        content TEXT NOT NULL,
        tags TEXT NOT NULL DEFAULT '[]',  -- JSON, not CSV
        session_id TEXT NOT NULL,
        last_activity_at REAL,            -- ★ Curator hook (s06)
        created_at REAL
    );
    CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
        content, tags,                    -- both columns indexed
        content='memories', content_rowid='id',
        tokenize='unicode61 remove_diacritics 2'
    );
    """

    async def prefetch(self, q: PrefetchQuery) -> list[Memory]:
        """Two-strategy fetch: recent + semantically related."""
        recents = await self._fetch_recent(q.session_id, limit=q.recent)
        related = await self._fts_search(q.user_text, limit=q.related)
        return _merge_dedupe(recents + related)

    async def sync(self, session_id, turn_blocks):
        """Scan the assistant turn for tool_use of memory_save. Each one
        becomes a new row — but bumping last_activity_at on referenced
        memories also happens here, so the Curator sees them as fresh."""
        for blk in turn_blocks:
            if blk.get("type") == "tool_use" and blk.get("name") == "memory_save":
                await self._insert(blk["input"], session_id)
            if blk.get("type") == "tool_use" and blk.get("name") == "memory_search":
                # Touch all returned ids so the Curator doesn't archive
                # something the agent just successfully recalled.
                for mem_id in blk.get("touched_ids", []):
                    await self._touch(mem_id)

    async def queue_next(self, session_id):
        # Fire-and-forget; result lands in self._cache for the next prefetch.
        asyncio.create_task(self._prefetch_into_cache(session_id))


# ============================================================================
# agent/memory_manager.py  --  orchestration that wires it together
# ============================================================================

class MemoryManager:
    """Routes hooks to whatever MemoryProvider the user has configured.
    Multiple providers can be active at once (e.g. local SQLite for
    facts, vector store for semantic) — the manager merges their
    prefetch results, dedupe, and passes a unified list to the loop.
    """

    def __init__(self, providers: list[MemoryProvider]):
        self._providers = providers

    async def on_turn_begin(self, session_id, user_text):
        q = PrefetchQuery(session_id=session_id, user_text=user_text)
        results = []
        for p in self._providers:
            results.extend(await p.prefetch(q))
        return _merge_dedupe(results)

    async def on_turn_end(self, session_id, turn_blocks):
        await asyncio.gather(*[p.sync(session_id, turn_blocks) for p in self._providers])
        # Kick off prefetches in parallel for the *next* turn.
        await asyncio.gather(*[p.queue_next(session_id) for p in self._providers])


# ============================================================================
# A reading map for going further
# ============================================================================
#
# - agent/memory_provider.py     — ABC + dataclasses (above)
# - agent/memory_manager.py      — orchestration of multiple providers
# - plugins/hermes_memory_fts5/  — SQLite FTS5 implementation
# - plugins/hermes_memory_*/     — alternative backends shipped as plugins
# - hermes_cli/curator.py        — uses last_activity_at to prune (s06)
#
# Sessions in this course that revisit these files:
#   s06 (Plugin + Curator) — plugin lifecycle, last_activity_at maintenance
#   s09 (Multi-process)     — gateway processes share memory.db via WAL
