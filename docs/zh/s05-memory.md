---
title: "s05 · Memory Provider + FTS5"
chapter: 5
slug: s05-memory
est_read_min: 14
---

# s05 · Memory Provider + FTS5

> 教什么：定义一个 **`MemoryProvider` 接口**，写一个 **SQLite + FTS5** 实现，挂上 `memory_search` / `memory_save` 两个 builtin 工具。Agent 现在能跨 session 召回事实——hermes "self-curating memory" 的真正基础。

---

## Problem / 问题

s04 让 *单个* 会话可恢复：你 resume 一个 session，看到的是这个对话本身的历史。但 agent 真正想做的是：

- "用户上周提过他在用 Postgres" — **跨 session 召回事实**
- "上次我在这个项目里跑 docker 的命令是什么？" — **基于关键词搜出过往 turn**
- "记住我喜欢简洁的回答" — **用户偏好长期持续**

这是 *memory*，不是 *session*。性质完全不同：memory 是用户/agent 共享的事实库，跨所有 session 存在；session 是这一段对话的 transcript。

设计上还有一个非显然的决策：**memory 应该是接口，不是具体存储**。今天 SQLite + FTS5 够用，明天换 Postgres + pgvector，后天换 Pinecone——loop 不应该感知到。这正是 hermes 的 `MemoryProvider` 抽象。

## Solution / 解决方案

四件事：

1. **`MemoryProvider` 接口**：`Search` / `Save` 暴露给 LLM 用的工具，`OnSessionStart` / `OnSessionEnd` 是 lifecycle 钩子（hermes 三阶段 prefetch→sync→queue 的占位），`Close` 释放资源。
2. **`SQLiteMemory` 实现**：单 SQLite 文件 `~/.learn-hermes-agent/memory.db`，`memories` 表配 **FTS5 虚拟表 + 三个触发器**自动同步，pure-Go 驱动 (`modernc.org/sqlite`) 不要 cgo。
3. **两个 builtin 工具**：`memory_search(query, limit)` / `memory_save(content, tags)`。模型决定何时调用。
4. **Loop 加 lifecycle 钩子**：`Run` 进入时 `OnSessionStart`，`defer OnSessionEnd`。s05 的 SQLite 实现这两个是 no-op；s06 plugin 会接管。

## How It Works / 工作原理

```ascii-anim frames=2
┌─────────────────────────────────────────────────────────────────┐
│  ~/.learn-hermes-agent/memory.db  (一个 SQLite 文件，跨所有 session)│
│  ┌──────────────────────────────────────────────────────────┐   │
│  │ memories(id, content, tags, session_id, created_at)      │   │
│  │ memories_fts(content)   ─── FTS5 virtual, rowid=id       │   │
│  │   AFTER INSERT/DELETE/UPDATE 触发器自动同步              │   │
│  └──────────────────────────────────────────────────────────┘   │
│         ▲                        │                              │
│  Save   │                        │ Search                       │
│         │                        ▼                              │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  MemoryProvider interface                                 │   │
│  │  ├─ Search(ctx, query, limit) → []Memory                  │   │
│  │  ├─ Save(ctx, Memory) → id                                │   │
│  │  ├─ OnSessionStart / OnSessionEnd  (lifecycle 钩子)       │   │
│  │  └─ Close                                                  │   │
│  └──────────────────────────────────────────────────────────┘   │
│         ▲                                                       │
│         │ 通过 MemorySearchTool / MemorySaveTool                │
│         │ 注册到 s02 Registry                                   │
│   builtin: bash, read_file, memory_search, memory_save          │
└─────────────────────────────────────────────────────────────────┘
```

核心 schema（节选自 [`memory_sqlite.go`](https://github.com/Ding-Ye/learn-hermes-agent/blob/main/agents/s05-memory/memory_sqlite.go)）：

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

-- 三触发器让 fts5 表跟 memories 自动同步，应用代码无需 INSERT 两次
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

Search 用 FTS5 `MATCH` + `ORDER BY rank`：

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

**四个非显然之处**：

1. **`content='memories', content_rowid='id'`** —— FTS5 的 *external content* 模式。fts 表不存内容拷贝，只存倒排索引；`MATCH` 查询通过 `rowid` 回表 `memories` 拿原始内容。**省一半磁盘**，代价是必须靠触发器手工同步——所以有 `_ai` `_ad` `_au` 三个 AFTER 触发器。

2. **`fts.rank` 升序排是 BM25**——FTS5 把 BM25 分数（值域 negative）放在 `rank` 列，**值越小越相关**。"docker postgres" 命中含两个词的行得 `-5`，命中只一个词的得 `-2`，自然排前。

3. **PRAGMA WAL + busy_timeout**：单进程下 WAL 不必要，但 s09 多进程一上来就要——上游 hermes gateway 多进程同时读 db。从一开始就配，省一次迁移。

4. **`unicode61` tokenizer**：默认的 simple tokenizer 只识 ASCII，把中文/日文当一坨。`unicode61` 按 Unicode 边界切，中文也能搜（虽然分词粒度不如真正的 IK 分词器）。

## What Changed / 与 s04 的变化

```diff
  type Loop struct {
      Provider Provider
      Registry *Registry
      Store    *Store
+     Memory   MemoryProvider     // s05 新增
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

注意：**memory db 不属于 session**——同一个 db 服务所有 session、所有用户、未来的多 source（cli/telegram/discord）。这就是为什么默认路径是 `~/.learn-hermes-agent/memory.db` 而不是 `sessions/<id>/memory.db`。

## Try It / 动手试一试

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s05-memory

# turn 1: 让 agent 记住一件事
go run . -v "remember that my favorite color is blue, save it as a memory tagged 'preference'"

# turn 2: 新 session（默认共享同一个 memory.db），让 agent 召回
go run . -v "what's my favorite color? search your memory"

# 看一下 db 里有什么
sqlite3 ~/.learn-hermes-agent/memory.db "SELECT id,content,tags,session_id FROM memories;"

# 测试
go test -v ./...
```

期望第二次跑（recall）的输出：

```
[memory] db=/Users/yeding/.learn-hermes-agent/memory.db
[session] id=s05-... parent=- msgs=1
[registry] tools=[bash memory_save memory_search read_file]
[turn 0] -> memory_search map[query:favorite color]
[turn 0] <- {"results":[{"id":1,"content":"user's favorite color is blue","tags":["preference"],...}]}
[turn 1] assistant: Your favorite color is blue.
Your favorite color is blue.
```

## Upstream Source Reading / 上游源码阅读

hermes `MemoryProvider` 的真实定义在 `agent/memory_provider.py`（抽象基类）+ 各 plugin 实现。设计是 **三阶段（Prefetch → Sync → Queue）**：每 turn 进来前预取相关记忆放进 system prompt，turn 结束后写新记忆，**异步排队**预取下一 turn——隐藏 I/O 延迟。

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

**对照阅读要点**：

- **三阶段 prefetch/sync/queue** 是 hermes memory 的精髓，我们 mini 只做 *agent 显式调用工具* 的版本。简单且容易理解，但浪费一次 I/O 延迟（用户等 search 完才看到回答）。生产里这部分的优化空间很大。

- **`tags` 字段：JSON vs CSV**：上游用 JSON 数组（可嵌套、可索引），我们用逗号分隔（简单、不可索引）。CSV 在 FTS5 里 `MATCH 'tags:"preference"'` 这种过滤就尴尬。

- **`last_activity_at`** 字段是 **s06 Curator 的钩子**：每次 search/save 都更新它，curator 后台扫描"超过 N 天没动"的 memory 归档掉。"hermes 自改进"=自动归档闲置记忆，**不是**自动生成新记忆。这个误传我们已经在 s03 docs 里澄清过；s06 会真做一遍。

- **`FTS5(content, tags)`** —— 上游 FTS5 同时索引 `content` AND `tags` 两列，所以 `MATCH 'tags:preference AND blue'` 这种限定查询能用。我们只索引 content，tags 是事后过滤。

- **`unicode61 remove_diacritics 2`** —— 上游 tokenizer 加了去音调符号（café → cafe），多语言用户友好。

**想读更多**：从 `agent/memory_provider.py` 的 ABC 入手，跟 `plugins/hermes_memory_fts5/__init__.py` 看具体 plugin 实现，跟 `agent/memory_manager.py` 看 prefetch/sync/queue 的 orchestration。这条线 → s06 plugin 系统注册 → s09 多进程 gateway 共用 db。

---

**下一节预告**：s06 把 plugin 系统做出来，让 memory provider、curator、observability 都通过同一个 plugin lifecycle 接进来。s05 的 `OnSessionStart` / `OnSessionEnd` 钩子在 s06 里会被 plugin 总线分发——同一接口，多个监听者。
