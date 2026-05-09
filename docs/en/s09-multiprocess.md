---
title: "s09 · Multi-process architecture"
chapter: 9
slug: s09-multiprocess
est_read_min: 14
---

# s09 · Multi-process architecture

> What this teaches: split the agent into **three independent processes** — CLI / Gateway / Scheduler — coordinating through a shared **Kanban SQLite DB** (jobs + crontab). Gateway never touches the LLM, Scheduler never speaks HTTP, CLI is single-purpose. This is hermes's real production shape, and the watershed between "laptop toy" and "production agent".

---

## Problem

The s01–s08 agent ran in **one process**. Several concrete blockers if you try to ship it:

1. **One inbound channel**: you can only start it from a CLI prompt. What about Telegram/Discord webhooks?
2. **No scheduling**: where does a cron job live? When the agent exits, it's gone.
3. **Crash = total loss**: a single panic takes everything down — the LLM call, the HTTP server, the cron loop.
4. **No horizontal scaling**: under load, can you run more workers? Not without rewiring everything.

**Direction**: split responsibilities into independent processes, each single-purpose, coordinating through a shared database.

```
inbound (Telegram/Discord/Slack)         scheduled (cron)
              │                                │
              ▼                                ▼
        ┌──────────┐                     ┌───────────┐
        │ Gateway  │                     │ Scheduler │
        │ (HTTP)   │   write             │  (poll)   │
        └────┬─────┘                     └─────┬─────┘
             │                                  │
             └────────────┬─────────────────────┘
                          ▼
               ┌────────────────────┐
               │  Kanban SQLite DB  │
               │  jobs / crontab    │
               └─────────┬──────────┘
                         │ read+claim
                         ▼
                ┌──────────────────┐
                │   Agent loop     │  (invoked by Scheduler)
                │   (LLM + tools)  │
                └──────────────────┘
```

CLI is the fourth process: either `s09 cli "prompt"` (direct agent, skip kanban) or `s09 send "prompt"` (drop in kanban so the scheduler runs it).

## Solution

Four pieces:

1. **Kanban**: SQLite + WAL, two tables — `jobs` (id/source/payload/status/result) and `crontab` (id/spec/prompt/last_fired_at).
2. **`ClaimNextPending`**: BEGIN IMMEDIATE transaction with `SELECT pending → UPDATE running` so two scheduler processes can't double-claim.
3. **Gateway**: HTTP server. `POST /msg` writes to kanban; **never calls the LLM**.
4. **Scheduler**: polls on an interval, claims a job, runs the agent, writes the result back. The `JobRunner` interface lets us inject `EchoRunner` (no API key needed) for CI/test.

## How It Works

```ascii-anim frames=3
┌──────────────────────────────────────────────────────────────────┐
│  Gateway process (HTTP)                                          │
│   POST /msg {"source":"...", "prompt":"..."}                     │
│      │                                                           │
│      └─► kanban.EnqueueJob("gateway", prompt)                    │
│                                                                  │
│  Kanban DB                                                       │
│   jobs( pending#7, gateway, "ping me" )                          │
│   jobs( running#5, cron,    "every 1m health" )                  │
│   jobs( done#3,    cli,     "..." )                              │
│                                                                  │
│  Scheduler process (poll loop, every 2s)                         │
│   tick:                                                          │
│     1) for each due crontab entry:                               │
│           kanban.EnqueueJob("cron", entry.prompt)                │
│           kanban.TouchCronEntry(id)                              │
│     2) loop:                                                     │
│           j := kanban.ClaimNextPending()                         │
│           if !j: break                                           │
│           result := JobRunner.Run(j)                             │
│           kanban.FinishJob(j.id, result, err)                    │
│                                                                  │
│  CLI process (one-shot)                                          │
│   s09 cli "prompt"     → agent loop direct (no kanban)           │
│   s09 send "prompt"    → kanban.EnqueueJob("cli-send")           │
│   s09 jobs             → kanban.ListJobs (read-only across procs)│
└──────────────────────────────────────────────────────────────────┘
```

`ClaimNextPending`'s atomicity is the critical bit (excerpt from [`kanban.go`](https://github.com/Ding-Ye/learn-hermes-agent/blob/main/agents/s09-multiprocess/kanban.go)):

```go
func (k *Kanban) ClaimNextPending(ctx context.Context) (*Job, bool, error) {
    tx, _ := k.db.BeginTx(ctx, nil)
    defer tx.Rollback()
    row := tx.QueryRowContext(ctx,
        `SELECT id, source, payload, created_at FROM jobs
         WHERE status = ? ORDER BY created_at ASC LIMIT 1`, JobPending)
    var j Job; var createdAt string
    if err := row.Scan(&j.ID, &j.Source, &j.Payload, &createdAt); err != nil {
        if err == sql.ErrNoRows { return nil, false, nil }
        return nil, false, err
    }
    res, _ := tx.ExecContext(ctx,
        `UPDATE jobs SET status = ?, started_at = ?
         WHERE id = ? AND status = ?`,
        JobRunning, time.Now().UTC(), j.ID, JobPending)
    n, _ := res.RowsAffected()
    if n == 0 { return nil, false, nil }   // someone else won the race
    tx.Commit()
    return &j, true, nil
}
```

**Four non-obvious points**:

1. **`UPDATE ... WHERE status = pending`** is the whole game. If two schedulers see the same row, the second's UPDATE affects 0 rows (status is already `running`) and it gives up that job and looks for another. BEGIN IMMEDIATE alone serialises but doesn't detect contention.

2. **WAL keeps reads from blocking writes**: `s09 jobs` (CLI listing jobs) is read-only while the scheduler writes. WAL gives readers a snapshot view; they don't stall. Production dashboards / metrics / Telegram status queries all depend on this.

3. **`every <duration>` rather than 5-field cron**: a teaching simplification — `time.ParseDuration` handles `1m30s` etc. and we don't pull in a cron parser. Upstream uses `robfig/cron/v3`-style 5-field expressions.

4. **Gateway never calls the LLM**: the HTTP handler inserts a row and returns 200 immediately. **This is the key decoupling** — a Telegram user's webhook (10s default timeout) can't get stuck waiting on a 30s LLM response.

## What Changed (vs. s08)

s08 was still single-process: `go run . "prompt"` ran the whole agent in one binary. s09 makes one binary serve **multiple roles**:

```diff
+ // single binary, three subcommand modes share one Kanban DB
+ s09 cli "<prompt>"        — direct agent (no kanban)
+ s09 gateway -addr :7079   — HTTP server, writes inbound to kanban
+ s09 scheduler -interval 2s — polls kanban, runs jobs via agent
+ s09 jobs                  — list jobs (read-only)
+ s09 send "<prompt>"       — enqueue
+ s09 cron add ...          — schedule
+
+ // shared persistence across all 3 processes
+ ~/.learn-hermes-agent/kanban.db   (jobs + crontab tables)
```

The agent loop itself (`loop.go`) is close to s01's — kept tiny so the multi-process lesson stays in focus.

## Try It

```bash
cd agents/s09-multiprocess
go build .

# Demo without an API key (echo runner)
./s09 gateway -addr :7079 &
./s09 scheduler -interval 1s -echo &

curl -X POST -d '{"source":"demo","prompt":"hello multiprocess"}' http://localhost:7079/msg
# {"job_id":1}

sleep 2
./s09 jobs
# #1     demo     done      ...   hello multiprocess
#        result: echo: hello multiprocess

./s09 cron add "every 30s" "what time is it?"
# cron entry 1 added

# Real LLM mode
kill %1 %2
export ANTHROPIC_API_KEY=sk-ant-...
./s09 gateway -addr :7079 &
./s09 scheduler -interval 1s &
curl -X POST -d '{"prompt":"compute 17 * 23 with bash"}' http://localhost:7079/msg
sleep 5
./s09 jobs

# Tests (includes in-process end-to-end gateway+scheduler with EchoRunner)
go test -v ./...
```

## Upstream Source Reading

hermes splits the same way: `hermes_cli/main.py` + `hermes_cli/kanban.py` + `hermes_cli/kanban_db.py`, plus the gateway in `gateway/`. Same shape, much larger — priority, retries, depends_on, worker pool, audit log.

```upstream:hermes_agent/kanban_db.py#L1-L60
# Excerpted + simplified from hermes_cli/kanban_db.py + kanban.py

import sqlite3, json, time, random
from datetime import datetime, timezone

class KanbanDB:
    """SQLite-backed task board. Tables: tasks (sessions/jobs to run),
    workers (heartbeats so dead workers' claims time out), runs (audit
    log), schedules (cron). WAL + BEGIN IMMEDIATE are mandatory because
    gateway/scheduler/CLI are independent processes."""

    SCHEMA = """
    CREATE TABLE IF NOT EXISTS tasks (
        id INTEGER PRIMARY KEY,
        kind TEXT,                -- 'session' | 'oneshot' | 'cron-fire'
        source TEXT,              -- cli | telegram | discord | slack | webhook
        priority INTEGER DEFAULT 0,
        payload JSON,
        depends_on JSON,          -- [task_id, ...]
        status TEXT,              -- pending|claimed|running|done|failed|retrying
        retries INTEGER DEFAULT 0,
        max_retries INTEGER DEFAULT 3,
        worker_id TEXT,
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
        host TEXT, pid INTEGER,
        last_heartbeat_at REAL
    );

    CREATE TABLE IF NOT EXISTS schedules (
        id INTEGER PRIMARY KEY,
        cron_expr TEXT,            -- 5-field cron (robfig-style)
        prompt TEXT,
        last_fire_at REAL,
        enabled INTEGER DEFAULT 1
    );
    """

    def claim_next(self, worker_id: str) -> dict | None:
        """Grab the highest-priority pending task. The retry-on-lock
        loop is the same idiom as hermes_state.SessionDB (s04 reading)."""
        for attempt in range(8):
            try:
                self.conn.execute("BEGIN IMMEDIATE")
                row = self.conn.execute("""
                    SELECT id, kind, source, payload FROM tasks
                    WHERE status = 'pending'
                       AND (depends_on IS NULL OR not any pending dep)
                    ORDER BY priority DESC, created_at ASC LIMIT 1
                """).fetchone()
                if not row:
                    self.conn.execute("COMMIT")
                    return None
                self.conn.execute("""
                    UPDATE tasks SET status='claimed', worker_id=?, claimed_at=?
                    WHERE id=? AND status='pending'
                """, (worker_id, time.time(), row[0]))
                self.conn.execute("COMMIT")
                return row_to_dict(row)
            except sqlite3.OperationalError as e:
                self.conn.execute("ROLLBACK")
                if "locked" in str(e):
                    time.sleep(random.uniform(0.020, 0.150))
                    continue
                raise
        return None
```

**Reading notes**:

- **Worker heartbeat table**: upstream has a `workers` table where each worker writes heartbeats; if no heartbeat in 30s, that worker's claimed tasks are reset. Our mini doesn't do this — single-scheduler doesn't need it. But it's mandatory in production; otherwise a crashed worker's job stays `running` forever.
- **`priority` + `depends_on`**: upstream tasks can carry priority and dependencies. "Run this ingest first, then summarize" is a tiny DAG via `depends_on`. Our mini is simple FIFO.
- **`retries / max_retries`**: automatic retry with backoff. Our mini marks failure once and stops. Production needs retries — transient LLM 5xx errors shouldn't require user-side retry.
- **5-field cron via robfig**: upstream uses real cron expressions. We use `every <duration>` — five lines vs sixty.
- **Multi-source field**: `cli/telegram/discord/slack/webhook` — same `tasks` table serves every inbound source. s10 (gateway adapters) is the bridge from the platform's webhook to `POST /msg`.
- **Audit log `runs` table**: every task run gets a row (start/end/output) for dashboard / billing / debugging. Our mini stuffs `result`/`error` directly into the `jobs` row — simple, but loses retry history.

**Read further**: start at `claim_next` in `kanban_db.py` (note the `depends_on` check), follow `kanban.py` for the worker heartbeat model, follow `gateway/__init__.py` for the platform-agnostic routing layer, and `gateway/platforms/telegram.py` for an actual platform adapter (the s10 content). That trace is the s09 → s10 real-source map.

---

**Next**: s10 grows the Gateway with **platform adapters** — actual webhook handlers for Telegram and Discord, normalising their message formats into `kanban.EnqueueJob`. One user comes in via Telegram, another via Discord; both conversations land in the same `jobs` table; each result is routed back to its own platform.
