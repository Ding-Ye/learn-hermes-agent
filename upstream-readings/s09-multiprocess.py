# Upstream source reading for s09 · Multi-process architecture
#
# Sources:
#   - NousResearch/hermes-agent · hermes_cli/kanban_db.py (the spine)
#   - NousResearch/hermes-agent · hermes_cli/kanban.py    (Python wrappers + worker heartbeats)
#   - NousResearch/hermes-agent · gateway/__init__.py     (HTTP/IM router)
# License: see https://github.com/NousResearch/hermes-agent/blob/main/LICENSE


import sqlite3
import time
import random
import json


# ============================================================================
# hermes_cli/kanban_db.py  --  shared task board across processes
# ============================================================================

class KanbanDB:
    """SQLite-backed coordination. Three processes participate:

      gateway   — receives inbound messages, writes 'pending' tasks
      scheduler — claims tasks, dispatches to workers, writes results
      cli       — read-only views (status, history) + ad-hoc enqueue

    WAL mode is mandatory: it's how readers (CLI status views, dashboard
    HTTP queries) coexist with the writer scheduler without the readers
    blocking. busy_timeout=5000 then BEGIN IMMEDIATE + jittered retry
    handles the rare write-vs-write contention.
    """

    SCHEMA = """
    PRAGMA journal_mode = WAL;
    PRAGMA busy_timeout = 5000;

    CREATE TABLE IF NOT EXISTS tasks (
        id INTEGER PRIMARY KEY,
        kind TEXT,                  -- 'session' | 'oneshot' | 'cron-fire'
        source TEXT,                -- 'cli' | 'telegram' | 'discord' | 'slack' | 'webhook'
        priority INTEGER DEFAULT 0,
        payload JSON,
        depends_on JSON,            -- [task_id, ...] DAG support
        status TEXT,                -- pending | claimed | running | done | failed | retrying
        retries INTEGER DEFAULT 0,
        max_retries INTEGER DEFAULT 3,
        worker_id TEXT,             -- which worker holds the claim
        claimed_at REAL,
        created_at REAL,
        completed_at REAL,
        result JSON,
        error TEXT
    );
    CREATE INDEX IF NOT EXISTS idx_tasks_pending
        ON tasks(status, priority DESC, created_at) WHERE status = 'pending';

    CREATE TABLE IF NOT EXISTS workers (
        id TEXT PRIMARY KEY,
        host TEXT,
        pid INTEGER,
        last_heartbeat_at REAL
    );

    CREATE TABLE IF NOT EXISTS schedules (
        id INTEGER PRIMARY KEY,
        cron_expr TEXT,             -- 5-field cron (robfig-style)
        prompt TEXT,
        last_fire_at REAL,
        enabled INTEGER DEFAULT 1
    );

    -- Audit log: every state transition for forensics + billing.
    CREATE TABLE IF NOT EXISTS runs (
        id INTEGER PRIMARY KEY,
        task_id INTEGER REFERENCES tasks(id),
        worker_id TEXT,
        started_at REAL,
        ended_at REAL,
        outcome TEXT,
        tokens_in INTEGER,
        tokens_out INTEGER,
        cost_usd REAL
    );
    """

    # --------------------------------------------------------- claim_next

    def claim_next(self, worker_id: str) -> dict | None:
        """Atomic claim. Two schedulers racing for the same row: only
        one's UPDATE WHERE status='pending' affects a row; the other
        gets affected_rows == 0 and skips. Same shape as our Go
        ClaimNextPending — just with retries-on-lock around it."""
        for attempt in range(8):
            try:
                self.conn.execute("BEGIN IMMEDIATE")
                row = self.conn.execute("""
                    SELECT id, kind, source, payload FROM tasks
                    WHERE status = 'pending'
                       AND _deps_satisfied(depends_on)
                    ORDER BY priority DESC, created_at ASC LIMIT 1
                """).fetchone()
                if row is None:
                    self.conn.execute("COMMIT")
                    return None
                self.conn.execute("""
                    UPDATE tasks
                    SET status = 'claimed', worker_id = ?, claimed_at = ?
                    WHERE id = ? AND status = 'pending'
                """, (worker_id, time.time(), row[0]))
                self.conn.execute("COMMIT")
                return _row_to_task(row)
            except sqlite3.OperationalError as e:
                self.conn.execute("ROLLBACK")
                if "locked" not in str(e):
                    raise
                time.sleep(random.uniform(0.020, 0.150))
        return None  # gave up after 8 attempts

    # ----------------------------------------------------- worker liveness

    def reap_dead_workers(self, threshold_seconds: float = 30.0):
        """Workers crash. Their `claimed_at` tasks would otherwise stay
        running forever. Anyone (commonly the scheduler itself, but a
        sidecar reaper is fine) checks heartbeats and resets stale
        claims back to pending so another worker picks them up."""
        cutoff = time.time() - threshold_seconds
        self.conn.execute("BEGIN IMMEDIATE")
        dead = [
            r[0] for r in self.conn.execute(
                "SELECT id FROM workers WHERE last_heartbeat_at < ?", (cutoff,)
            ).fetchall()
        ]
        if dead:
            self.conn.execute(
                f"""UPDATE tasks SET status='pending', worker_id=NULL, claimed_at=NULL
                    WHERE worker_id IN ({','.join('?'*len(dead))}) AND status='claimed'""",
                dead,
            )
            self.conn.execute(
                f"DELETE FROM workers WHERE id IN ({','.join('?'*len(dead))})",
                dead,
            )
        self.conn.execute("COMMIT")


# ============================================================================
# gateway/__init__.py  --  the HTTP+IM router
# ============================================================================
#
# Hermes's gateway is one process exposing webhooks for many platforms.
# Each platform module (gateway/platforms/telegram.py, discord.py,
# slack.py, whatsapp.py, signal.py) translates the platform's wire
# format into a uniform KanbanDB.enqueue call. Replies come back via
# the same module — a "from_telegram" task's result message is sent
# back to Telegram, never to Discord.

class Gateway:
    def __init__(self, kanban: KanbanDB, platform_registry):
        self.kanban = kanban
        self.platforms = platform_registry  # name -> Platform handler

    async def handle_inbound(self, source: str, raw_payload: dict) -> int:
        """Single ingress point. Platforms call this after parsing
        their wire format into a normalised dict."""
        prompt = raw_payload["text"]
        user_id = raw_payload["user_id"]
        return self.kanban.enqueue(
            kind="session",
            source=source,
            payload={"prompt": prompt, "user_id": user_id, "channel_id": raw_payload.get("channel_id")},
        )

    async def handle_outbound(self, task: dict, result: dict):
        """Scheduler/worker calls this when a task finishes. We dispatch
        the result back through the platform module that originated it."""
        platform = self.platforms.get(task["source"])
        if platform is None:
            return  # 'cli' / 'cron-fire' have no outbound channel
        await platform.send(task["payload"]["channel_id"], result["text"])


# ============================================================================
# A reading map for going further
# ============================================================================
#
# - hermes_cli/kanban_db.py        SQLite schema + claim_next + reap_dead_workers
# - hermes_cli/kanban.py           Python wrapper: workers, heartbeats, retries
# - hermes_cli/main.py             how cli/gateway/scheduler subcommands wire up
# - gateway/__init__.py            inbound/outbound router
# - gateway/platforms/telegram.py  one platform adapter (the s10 content)
# - gateway/platforms/discord.py
#
# Sessions in this course that revisit these files:
#   s10 (Gateway platform adapters) — adds a real Telegram / Discord adapter
#   s_full (End-to-end)              — wires everything from a real prompt to
#                                        a Telegram reply through the kanban
```
