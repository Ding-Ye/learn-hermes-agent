---
title: "s09 · Multi-process 架构"
chapter: 9
slug: s09-multiprocess
est_read_min: 14
---

# s09 · Multi-process 架构

> 教什么：把 agent 拆成 **CLI / Gateway / Scheduler 三个独立进程**，通过一个共享 **Kanban SQLite DB** 协调（jobs 表 + crontab 表）。Gateway 不调 LLM、Scheduler 不收 HTTP、CLI 单一职责——hermes 真实部署形态。**这一节是从"笔记本玩具"到"production agent"的真正分水岭。**

---

## Problem / 问题

s01-s08 的 agent 是**单进程**。这有几个具体问题阻碍它从 demo 上线：

1. **入口单一**：你只能从 CLI 拿一个 prompt 启动它。Telegram/Discord 进来的消息怎么办？
2. **无法定时**：cron job 要活在哪里？Agent 退出了它就没了。
3. **崩溃即死**：单进程 panic 把所有东西一起带走——LLM 调用、HTTP 服务、cron 调度全完蛋。
4. **不能水平扩展**：负载高时多开几个 worker 吗？没法。

**解决方向**：把责任拆给独立进程，每个职责单一，通过共享数据库协调。

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
                │   Agent loop     │  (called by Scheduler)
                │   (LLM + tools)  │
                └──────────────────┘
```

CLI 是第四个进程：可以是 `s09 cli "prompt"`（直接调 agent，跳过 kanban），也可以是 `s09 send "prompt"`（投到 kanban 让 scheduler 跑）。

## Solution / 解决方案

四件事：

1. **Kanban**：SQLite + WAL，两张表 `jobs`（id/source/payload/status/result）和 `crontab`（id/spec/prompt/last_fired_at）。
2. **`ClaimNextPending`**：BEGIN IMMEDIATE 事务里 `SELECT pending → UPDATE running`——防止两 scheduler 进程抢同一 job。
3. **Gateway**：HTTP 服务，`POST /msg` 把消息塞 kanban，**绝不调 LLM**。
4. **Scheduler**：定时轮询，claim 一个 job、跑 agent、写回 result。`JobRunner` 接口让我们能注入 `EchoRunner`（不要 API key）做 CI/test。

## How It Works / 工作原理

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

`ClaimNextPending` 的原子性是核心（节选自 [`kanban.go`](https://github.com/Ding-Ye/learn-hermes-agent/blob/main/agents/s09-multiprocess/kanban.go)）：

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

**四个非显然之处**：

1. **`UPDATE ... WHERE status = pending`**——条件 UPDATE 是关键。两 scheduler 同时拿到同一行后，第二个的 UPDATE 影响 0 行（因为 status 已经是 running），它就放弃这个 job 找下一个。光靠 BEGIN IMMEDIATE 会序列化但不会检测竞争。

2. **WAL 让读不阻塞写**：`s09 jobs`（CLI 列出 jobs）是只读，scheduler 在写。WAL 模式下读者看的是 snapshot，不会卡。生产里 dashboard / metrics / Telegram 状态查询都依赖这个。

3. **`every <duration>` 而非 5 字段 cron**：教学省略——`cron` 库不算 stdlib，引入 `robfig/cron/v3` 没有教学价值。`time.ParseDuration` 处理 `1m30s` 这种已经够用。

4. **Gateway 不调 LLM**：HTTP handler 只插行进 kanban 立刻 200 返回。**这是关键解耦**——Telegram 用户不会因为 LLM 慢卡住 webhook（webhook 默认 10s 超时，LLM 回答 30s 是常态）。

## What Changed / 与 s08 的变化

s08 还是单进程：`go run . "prompt"` 把整个 agent 放在一个 binary 里。s09 第一次让一份 binary 有**多种角色**：

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

agent loop 本身（`loop.go`）是 s01 的近亲——专注教 multi-process 不让 agent 自身分散注意力。

## Try It / 动手试一试

```bash
cd agents/s09-multiprocess
go build .

# 不需要 API key 的 demo（用 echo runner）
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

# 真 LLM 模式
kill %1 %2
export ANTHROPIC_API_KEY=sk-ant-...
./s09 gateway -addr :7079 &
./s09 scheduler -interval 1s &
curl -X POST -d '{"prompt":"compute 17 * 23 with bash"}' http://localhost:7079/msg
sleep 5
./s09 jobs

# 单元测试（包括 in-process 端到端 gateway+scheduler 用 EchoRunner）
go test -v ./...
```

## Upstream Source Reading / 上游源码阅读

hermes 的多进程在 `hermes_cli/main.py` + `hermes_cli/kanban.py` + `hermes_cli/kanban_db.py`，加 gateway 子模块在 `gateway/` 目录。形态相同，规模大很多——支持 priority、retries、depends_on、worker pool。

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
        worker_id TEXT,           -- which worker holds the claim
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

**对照阅读要点**：

- **Worker heartbeat 表**：上游有 `workers` 表，每个 worker 写心跳；超时（30s 没心跳）就重置它的 claimed task 给别人。我们 mini 没做——单 scheduler 用，不需要。但这是 production 必备，否则 worker 崩了它的 job 永远卡 running。
- **`priority` + `depends_on`**：上游 task 可以有优先级和依赖。"先跑这个 ingest，再跑 summarize" 这种 DAG 形态就靠 `depends_on`。我们 mini 是简单 FIFO。
- **`retries / max_retries`**：失败自动重试，配指数退避。我们 mini 失败就标 `failed` 不回头。生产需要——LLM API 偶发 5xx 不能让用户 retry。
- **5-field cron via robfig**：上游用 `robfig/cron/v3`（Python 端）解析真 cron 表达式。我们用 `every <duration>`——简化 5 行变 60 行。
- **多 source 字段**：`cli/telegram/discord/slack/webhook`——同一张 jobs 表服务所有 inbound 来源。s10（gateway 平台适配器）就是把 telegram/discord 的 webhook 转成 `POST /msg`。
- **Audit log `runs` 表**：每次 task run 留一条记录（开始/结束/输出），供 dashboard / billing / debugging 用。我们 mini 把 result/error 直接塞 jobs 行——简单但丢了重试历史。

**想读更多**：从 `kanban_db.py` 的 `claim_next` 入手（注意 `depends_on` 检查），跟 `kanban.py` 看 worker heartbeat 模型，跟 `gateway/__init__.py` 看 platform-agnostic 路由层，跟 `gateway/platforms/telegram.py` 看具体平台 adapter（s10 的内容）。这条线是 s09 → s10 真实代码地图。

---

**下一节预告**：s10 把 Gateway 长出 **平台 adapter**——给 Telegram 和 Discord 写两个真实的 webhook handler，把它们的消息格式标准化成 `kanban.EnqueueJob`。一个用户从 Telegram 来、另一个从 Discord 来，两人的对话进入同一个 jobs 表、各自的 result 走回各自的平台。
