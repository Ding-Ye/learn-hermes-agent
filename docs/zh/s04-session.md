---
title: "s04 · Session 持久化"
chapter: 4
slug: s04-session
est_read_min: 13
---

# s04 · Session 持久化

> 教什么：把 conversation 状态（messages 列表 + token 计数 + 元数据）落到磁盘，让 agent 跨进程恢复。每 turn 后立即原子写——hermes 的 "Ctrl-C survival" 哲学。提供 `-resume` `-branch` `-reset` `-list` CLI 子命令。

---

## Problem / 问题

s01-s03 的 agent 跑一次就死了——每次 `go run` 都是从空白消息列表开始。这有两个具体痛点：

1. **没法继续聊**。问完 "what is 17 * 23?"，下次想问 "now add 100"，模型不知道前文，得把整段重发。
2. **跑到一半挂了什么都没了**。Ctrl-C 一按、SSH 断了、模型 API 报错——之前跑完几十步、烧了几千 tokens 的工作全丢。

这两个问题都指向**持久化**，但具体怎么做有几个非显然的决策：
- **存在哪**？文件还是数据库？
- **存什么**？只存 messages 列表，还是连 token 计数、模型 ID、ParentID 一起存？
- **什么时候写**？每 turn 后写、每 N turn 写、还是退出时写？
- **怎么写**？直接 `WriteFile` 行不行，断电会怎样？

s04 给出最小可用答案；s05 在此基础上加 FTS5 全文搜索；上游 hermes 用 SQLite 把这些都做成 production-ready。

## Solution / 解决方案

四件事：

1. **`Session` struct**：`ID` `CreatedAt` `UpdatedAt` `Model` `ParentID` `Messages` `Usage`，序列化成 JSON。
2. **`Store`** 是个目录抽象：`Save(s)` / `Load(id)` / `List()` / `Delete(id)`。一个 session 一个文件 `<id>.json`。
3. **每 turn 后立即 `Store.Save()`**——hermes 的 "Ctrl-C survival" 哲学：进程被打断也只丢部分 tool 结果，下次 resume 自动重做。
4. **原子写**：`os.WriteFile(tmp) → os.Rename(tmp, final)`。断电只可能保留"完整旧版"或"完整新版"，绝不会留半截文件。

CLI：`-resume <id>` `-branch <id>` `-reset <id>` `-list`，配合 prompt（除了 `-reset`/`-list`）。

## How It Works / 工作原理

```ascii-anim frames=3
┌──────────────────────────────────────────────────────────────────┐
│ ~/.learn-hermes-agent/sessions/                                  │
│   s04-20260509-135133-a1b2c3.json                                │
│   s04-20260509-135533-d4e5f6.json   (parent: …a1b2c3)            │
│                                                                  │
│   每个 .json 内容：                                                │
│   { "id": "...", "model": "...",                                 │
│     "parent_id": "..." (可选, 标记 branch),                      │
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
│     ────►  store.Save(sess)   ★ 每 turn 写盘                     │
│     switch resp.StopReason:                                      │
│       end_turn:                  return                          │
│       tool_use:                                                  │
│         results = runTools(...)                                  │
│         sess.AppendUserToolResults(results)                      │
│     ────►  store.Save(sess)   ★ tool 结果也写盘                  │
└──────────────────────────────────────────────────────────────────┘
```

原子写（核心 6 行，节选自 [`session.go`](https://github.com/Ding-Ye/learn-hermes-agent/blob/main/agents/s04-session/session.go)）：

```go
func (st *Store) Save(s *Session) error {
    data, _ := json.MarshalIndent(s, "", "  ")
    final := st.pathFor(s.ID)
    tmp := final + ".tmp"
    if err := os.WriteFile(tmp, data, 0o644); err != nil { return err }
    return os.Rename(tmp, final)   // POSIX rename is atomic
}
```

Branch（核心 8 行）：

```go
func (s *Session) Branch() *Session {
    fork := &Session{
        ID:        newSessionID(time.Now().UTC()),
        ParentID:  s.ID,
        Model:     s.Model,
        Messages:  append([]Message(nil), s.Messages...), // 切片要 deep copy
        // Usage 留零：每个 fork 自己一本账
    }
    return fork
}
```

**四个非显然之处**：

1. **每 turn 写盘**——而不是退出时一次性写。代价是几次额外 `WriteFile`（每次几十 KB），收益是进程被任意打断（Ctrl-C / OOM / network drop / kernel panic）也只丢"上一个 LLM 调用之后到下一次 Save 之间"的工作。LLM 调用本身就要几秒，I/O 反而是零头。

2. **`os.Rename` 是 atomic 的**（POSIX 保证），但 `os.WriteFile` **不是**——后者直接打开 final 路径覆盖写，断电可能留半截文件。所以模式必须是 "write tmp → rename"。Go 在 Windows 上 rename 也是 atomic（自 Go 1.5+）。

3. **Branch 切片要 deep copy**：`append([]Message(nil), s.Messages...)` 而不是 `s.Messages` 直接赋值——否则 fork 上 append 一条消息，parent 的切片也跟着变（共享底层数组）。**`Message.Content` 也是切片，但我们没 deep copy**——意味着如果 fork 修改某条消息的 content blocks，parent 也会看见。教学上够用，生产上要再深一层。

4. **Branch 把 Usage 清零**：每个 fork 是独立账本。要看血统总和就在 report 时往 ParentID 上爬。这是个**值得**的简化——不清零的话，"这个 fork 烧了多少 token" 的指标就要做减法。

## What Changed / 与 s03 的变化

```diff
  type Loop struct {
      Provider Provider
      Registry *Registry
+     Store    *Store          // s04 新增
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

**接口变化**：Loop.Run 的签名从"接 prompt 字符串、返回最终文本"变成"接 Session 指针、in-place 修改它"。最终文本由 `LastAssistantText(sess)` 在 main.go 里抽取。

s04 把 s03 的 skills 暂时拿掉了——单节聚焦在持久化，不让 skill 加载噪声分散注意力。生产 agent 这两块当然都要。

## Try It / 动手试一试

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s04-session

# 新 session
go run . -v "what is 17 * 23?"
# stderr: [session] s04-20260509-135133-a1b2c3 saved (turns=2 in=312 out=89)

# 看一眼 session 文件长什么样
cat ~/.learn-hermes-agent/sessions/s04-20260509-135133-a1b2c3.json

# resume：续聊
go run . -resume s04-20260509-135133-a1b2c3 "now add 100"

# branch：拉一条平行支线
go run . -branch s04-20260509-135133-a1b2c3 "actually subtract 50 from the first answer"

# 列表
go run . -list

# 删除
go run . -reset s04-20260509-135133-a1b2c3

# 测试
go test -v ./...
```

期望输出形态（resume 那次）：

```
[session] id=s04-20260509-135133-a1b2c3 parent=- msgs=4 turns=2 in/out=312/89
[turn 0] assistant: 391 + 100 = 491.
491
```

注意 stderr 显示 `msgs=4 turns=2`——resume 装载到内存的不仅是 4 条 messages，还有"已经做过 2 轮 LLM 调用、烧了 312/89 tokens"的状态。

## Upstream Source Reading / 上游源码阅读

hermes 把 conversation 状态存在 **SQLite** 里（`hermes_state.SessionDB`），不是一文件一 session 的 JSON。模式：

```upstream:hermes_agent/hermes_state.py#L1-L70
import sqlite3
import time
import random
from pathlib import Path

class SessionDB:
    """SQLite-backed session store. WAL mode for concurrent reads
    (gateway processes from telegram/discord/cli all read the same db).
    FTS5 virtual table on messages enables search across all sessions —
    that's how `/resume <title>` and the agent's own past-conversation
    recall work.
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
        """Atomic write with jittered retry on lock contention.
        Multiple gateway processes may write concurrently — BEGIN
        IMMEDIATE acquires the write lock immediately rather than
        waiting until the first DML, which lets us back off cleanly
        instead of deadlocking with another writer."""
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
        gets the freshest context, not the deprecated original.
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

**对照阅读要点**：

- **SQLite vs JSON 文件**：上游一个 db 文件存所有 session（含跨平台来源 cli/telegram/discord），WAL 模式让 gateway 多进程并发读 OK。我们 mini 版每 session 一个 JSON 文件——单进程读写没问题，多进程并发就会撞车。s05 引入 FTS5，s09 引入 multi-process，那时 SQLite 的并发模型才真的派上用场。
- **`parent_session_id` 不是 branch，是 compression chain**！这是 hermes 的"长上下文"解决方案：上下文超 N 万 tokens 时，把旧 turns 压成摘要、新建一个 session 接续、`parent_session_id` 指回去。`get_compression_tip` 沿链找最新 tip——用户 `/resume` 的是 title，hermes 自动跳到最新版本。**我们的 ParentID 是 branch（用户主动 fork）**，不是 compression。两个概念名字撞了，要区分。
- **FTS5 messages_fts**：上游 messages 表配了 FTS5 虚拟表，`/resume <title>` 是 title 模糊匹配，agent 的"past-conversation recall" 也走它。我们没做。s05 会引入 FTS5（在 memory provider 里）。
- **`BEGIN IMMEDIATE` + jitter 重试**：多 gateway 进程并发写时，`BEGIN IMMEDIATE` 立刻拿写锁（vs default 的 deferred lock），失败时随机抖动 20-150ms 重试。我们的 `os.Rename` 是单写者方案，无并发问题但也不 scale。
- **多源追踪**：`source` 字段标记会话来自 cli/telegram/discord/slack——这是 s10（gateway 平台适配器）的前置铺垫。同一个用户从不同 IM 平台来的会话能在同一个 db 里区分管理。
- **成本追踪**：`estimated_cost_usd` `actual_cost_usd` `cost_status`——hermes 实时算每个 session 的钱。本地 personal use 看着没必要，企业部署是必需。

**想读更多**：从 `hermes_state.py` 的 `SessionDB.create_session` 入手，跟 `append_message` 看消息怎么入 FTS5，跟 `get_compression_tip` 看压缩链；再去 `hermes_cli/main.py` 的 `_resolve_session_by_name_or_id` 看 title→id 的解析路径。这条线就是 s04 → s05（FTS5 memory）→ s09（multi-process gateway）→ s10（多源 source）的真实代码地图。

---

**下一节预告**：s05 引入 **MemoryProvider 接口** + 一个 **FTS5 SQLite 实现**。Session 持久化（s04）解决"我现在的对话记得住"，memory（s05）解决"agent 想起之前一周的对话"——是 hermes "self-curating memory" 的真正核心。
