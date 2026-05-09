# Upstream source reading for s04 · Session persistence
#
# Sources:
#   - NousResearch/hermes-agent · hermes_state.py        (SessionDB)
#   - NousResearch/hermes-agent · hermes_cli/main.py     (resume/title resolution)
# License: see https://github.com/NousResearch/hermes-agent/blob/main/LICENSE
#
# Teaching excerpt focused on the parts that map onto agents/s04-session/.
# What's missing from our mini, on purpose, is called out at the end.


# ============================================================================
# hermes_state.py  --  SQLite-backed session store
# ============================================================================

import sqlite3
import time
import random
from pathlib import Path
from typing import Optional


class SessionDB:
    """SQLite-backed session store. WAL mode lets gateway processes for
    cli/telegram/discord/slack all read the same db concurrently. An FTS5
    virtual table over messages backs `/resume <title>` (fuzzy match) and
    the agent's past-conversation recall.

    Compared to our Go mini (one JSON file per session, single writer):
      + scales to many sessions (no readdir() on every list)
      + concurrent read across gateway processes
      + full-text search via FTS5
      + transactional updates of metadata + messages together
      - more setup, schema migrations to think about
    """

    SCHEMA = """
    CREATE TABLE IF NOT EXISTS sessions (
        id TEXT PRIMARY KEY,
        source TEXT,                  -- cli | telegram | discord | slack | ...
        user_id TEXT,
        model TEXT,
        model_config JSON,
        system_prompt TEXT,
        parent_session_id TEXT,       -- compression chain (NOT user branch)
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
        # isolation_level=None gives us manual transaction control.
        self.conn = sqlite3.connect(path, isolation_level=None)
        # WAL = Write-Ahead Logging. Readers don't block writer; one
        # writer at a time. Right trade-off when readers (telemetry,
        # gateway frontends) outnumber writers (the active session).
        self.conn.execute("PRAGMA journal_mode=WAL")
        self.conn.execute("PRAGMA busy_timeout=5000")
        self.conn.executescript(self.SCHEMA)

    # ---------------------------------------------------------- atomic write

    def _execute_write(self, sql: str, params=()):
        """Atomic write with jittered retry on contention.

        Multiple gateway processes can write concurrently. BEGIN
        IMMEDIATE acquires the write lock right away (vs the default
        BEGIN DEFERRED, which only takes the lock on first DML). On
        contention we ROLLBACK + sleep a jittered 20–150ms and retry,
        rather than deadlocking with the other writer.
        """
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

    # ----------------------------------------------------- compression chain

    def get_compression_tip(self, session_id: str) -> str:
        """Walk parent_session_id chain to the latest tip.

        When a session's context grows too large, hermes compresses old
        turns into a summary, creates a NEW session with its
        parent_session_id pointing at the original, and continues. The
        user resuming the original by title transparently jumps to the
        latest tip — old sessions never become "orphans the user can't
        easily get back to".
        """
        cur = session_id
        for _ in range(64):  # cycle guard, just in case
            row = self.conn.execute(
                "SELECT id FROM sessions WHERE parent_session_id = ?",
                (cur,),
            ).fetchone()
            if not row:
                return cur
            cur = row[0]
        return cur


# ============================================================================
# hermes_cli/main.py  --  the user-facing resume flow
# ============================================================================
#
# CRUCIAL: hermes's `/resume <X>` accepts BOTH a session id and a session
# TITLE. The resolution path is:
#   1) try id lookup (get_session)
#   2) if miss, try title resolution (resolve_session_by_title — fuzzy)
#   3) walk get_compression_tip to land on the freshest tip
# Our Go mini accepts ids only. Adding title resolution is one extra
# index lookup; we just don't do it.

def _resolve_session_by_name_or_id(name_or_id: str) -> Optional[str]:
    db = SessionDB()
    try:
        session = db.get_session(name_or_id)
        resolved_id: Optional[str] = None
        if session:
            resolved_id = session["id"]
        else:
            resolved_id = db.resolve_session_by_title(name_or_id)
        if resolved_id:
            resolved_id = db.get_compression_tip(resolved_id) or resolved_id
        return resolved_id
    finally:
        db.close()


# ============================================================================
# A reading map for going further
# ============================================================================
#
# What our mini omits, by design:
#
# - FTS5 search across messages          (s05 introduces it via memory)
# - Multi-process / WAL concurrency      (s09 introduces multi-process)
# - title-based resume                   (one extra index lookup; trivial)
# - compression chain                    (real long-context solution)
# - source tracking (telegram/etc)       (s10 sets up gateway adapters)
# - cost tracking                        (production-only; out of scope)
#
# Files to read for deeper understanding:
# - hermes_state.py             — SessionDB schema + write paths
# - hermes_cli/main.py          — _resolve_session_by_name_or_id
# - hermes_cli/checkpoints.py   — confusingly named: filesystem
#                                 snapshots via git, NOT conversation
#                                 sessions. Both keep state, very
#                                 different responsibilities.
