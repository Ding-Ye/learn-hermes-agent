---
title: "s06 · Plugin 系统 + Curator"
chapter: 6
slug: s06-plugins-curator
est_read_min: 14
---

# s06 · Plugin 系统 + Curator

> 教什么：定义 `Plugin` interface + `PluginManager` 总线，让 lifecycle 事件被多个监听者订阅。再写一个 **CuratorPlugin**——它是 hermes "self-improving memory" 的真相：**追踪 `last_activity_at`，自动归档闲置 memory**，不是自动生成新 memory。

---

## Problem / 问题

s05 让 Loop 直接调 `MemoryProvider.OnSessionStart` / `OnSessionEnd`。这就够用——直到第二个东西也想知道 session 开始/结束。observability 要打 metric、curator 要扫闲置、计费要算 token、企业部署要写审计日志……每一个都钩在同一组事件上。

如果继续往 Loop 里加 if 分支：

```go
if l.Memory != nil       { l.Memory.OnSessionStart(...) }
if l.Curator != nil      { l.Curator.OnSessionStart(...) }
if l.Telemetry != nil    { l.Telemetry.OnSessionStart(...) }
if l.AuditLog != nil     { l.AuditLog.OnSessionStart(...) }
if l.CostAccountant ...
```

很快就变成 if-墙。Loop 也开始知道每一个具体扩展存在。**反方向**：让 Loop 只跟一个对象说话——`PluginManager`——manager 把事件 fan out 到任意数量的 plugin。Loop 不知道有几个 plugin，plugin 也不知道彼此存在。

这是 hermes 的 plugin 架构。memory provider、curator、observability、计费、dashboard 全是 plugin。

## Solution / 解决方案

四件事：

1. **`Plugin` interface**（5 个方法）：`Name`、`Init(ctx, host)`、`OnSessionStart`、`OnSessionEnd`、`Close`。
2. **`Host` 结构**——plugins 在 Init 时拿到的"安全表面"：`Registry` / `Memory` / `Logger`。这是有意 narrow 的 capability surface，不是把整个 agent 状态暴露给 plugin。
3. **`PluginManager`** 总线：`Register` / `Init` / `DispatchSessionStart` / `DispatchSessionEnd` / `Close`。**单个 plugin 错误不会停掉别人**——manager 记录、继续派发。
4. **两个示例 plugin**：
   - `LoggingPlugin`——最小范式，只在每个 lifecycle 打日志。
   - `CuratorPlugin`——读 `last_activity_at`，调 `Memory.ListStale` + `Memory.Archive`，把超过 N 时长没动的 memory 归档。

memory schema 同时扩了两列：`last_activity_at`（每次 save/search 都 bump）、`archived_at`（curator 写）。Search 自动过滤 `archived_at IS NULL`。

## How It Works / 工作原理

```ascii-anim frames=2
┌──────────────────────────────────────────────────────────────────┐
│  main.go                                                         │
│   ├─ pm := NewPluginManager(logger)                              │
│   ├─ pm.Register(NewLoggingPlugin())                             │
│   ├─ pm.Register(NewCuratorPlugin(staleAfter, limit))            │
│   ├─ pm.Init(ctx, &Host{Registry, Memory, Logger})               │
│   └─ loop.Plugins = pm                                           │
│                                                                  │
│  loop.Run(ctx, sess):                                            │
│    pm.DispatchSessionStart(ctx, sess.ID)                         │
│      ├─► LoggingPlugin.OnSessionStart  (just logs)               │
│      └─► CuratorPlugin.OnSessionStart                            │
│            ├─ stale = Memory.ListStale(ctx, StaleAfter, Limit)   │
│            └─ Memory.Archive(ctx, [stale.IDs])                   │
│    ... LLM turns happen ...                                       │
│    pm.DispatchSessionEnd(ctx, sess.ID)                           │
│      ├─► LoggingPlugin.OnSessionEnd                              │
│      └─► CuratorPlugin.OnSessionEnd  (no-op)                     │
└──────────────────────────────────────────────────────────────────┘
```

CuratorPlugin 的 OnSessionStart（节选）：

```go
func (c *CuratorPlugin) OnSessionStart(ctx context.Context, sid string) error {
    stale, err := c.host.Memory.ListStale(ctx, c.StaleAfter, c.Limit)
    if err != nil { return err }
    if len(stale) == 0 {
        c.host.Logger.Infof("[plugin:curator] nothing to archive")
        return nil
    }
    ids := make([]int64, 0, len(stale))
    for _, m := range stale { ids = append(ids, m.ID) }
    if err := c.host.Memory.Archive(ctx, ids); err != nil { return err }
    c.host.Logger.Infof("[plugin:curator] archived %d stale memories: %v", len(ids), ids)
    return nil
}
```

PluginManager 的 dispatch 容错（节选）：

```go
func (pm *PluginManager) DispatchSessionStart(ctx context.Context, sid string) {
    pm.mu.Lock(); defer pm.mu.Unlock()
    for _, p := range pm.plugins {
        if err := p.OnSessionStart(ctx, sid); err != nil {
            pm.logger.Errorf("plugin %s OnSessionStart: %v", p.Name(), err)
            // 不 return —— 继续派发给后面的 plugin
        }
    }
}
```

**四个非显然之处**：

1. **Search hit 自动 bump `last_activity_at`**：用一条 memory 等于"续命"。这是 hermes 的关键设计——把使用频次反映到归档决策里，**不需要单独的 access log**。`SQLiteMemory.Search` 内部对所有命中的 id 调 `touchMany`。

2. **`archived_at` 不删行**：归档只是把行从 search 视图里隐藏（`WHERE archived_at IS NULL`），数据还在。这让 hermes 能做 `rollback` / `unarchive` 这种 UNDO 操作（我们 mini 没做这两个 CLI 子命令，但数据基础在）。

3. **Plugin manager 错误**用 `logger.Errorf` 记录而不是 panic。**单个 plugin 出 bug 不能拖垮 agent**——这是 hermes 的硬约束，写进了 plugin 测试里（见 `TestPluginManager_PluginErrorDoesNotStopOthers`）。

4. **Host 是 capability surface**，不是 *agent.go*。Plugin 只能拿到 Registry / Memory / Logger，不能直接 reach into Loop / Provider / Session。这让未来给 plugin 加 "能写 memory 但不能读"、"能读 registry 但不能注册新 tool" 这类细粒度权限是可行的——只要扩 Host。

## What Changed / 与 s05 的变化

```diff
  type Loop struct {
      Provider Provider
      Registry *Registry
      Store    *Store
-     Memory   MemoryProvider
+     Plugins  *PluginManager     // s06 新
      MaxTurns int
      Verbose  bool
  }

  func (l *Loop) Run(ctx context.Context, sess *Session) error {
-     if l.Memory != nil {
-         _ = l.Memory.OnSessionStart(ctx, sess.ID)
-         defer l.Memory.OnSessionEnd(ctx, sess.ID)
-     }
+     if l.Plugins != nil {
+         l.Plugins.DispatchSessionStart(ctx, sess.ID)
+         defer l.Plugins.DispatchSessionEnd(ctx, sess.ID)
+     }
      ...
  }
```

memory schema 扩展：

```diff
  CREATE TABLE memories (
      id INTEGER PRIMARY KEY AUTOINCREMENT,
      content TEXT NOT NULL,
      tags TEXT NOT NULL DEFAULT '',
      session_id TEXT NOT NULL DEFAULT '',
      created_at TEXT NOT NULL,
+     last_activity_at TEXT NOT NULL,
+     archived_at TEXT
  );
+ CREATE INDEX idx_memories_active_lastact
+     ON memories(last_activity_at) WHERE archived_at IS NULL;
```

s05 的 `MemoryProvider` 接口加了 3 个 curator-facing 方法：`Touch` / `Archive` / `ListStale`。

## Try It / 动手试一试

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s06-plugins-curator

# 演示：把 stale 阈值缩到 1 秒，让 curator 立刻干活
go run . -v -curator-stale-after 1s "remember my favorite color is blue"
sleep 2
go run . -v -curator-stale-after 1s "what's my favorite color?"
# 第二跑的 stderr 里能看到：
#   [s06] [plugin:curator] archived 1 stale memories: [1]
# 然后 memory_search 返回空——blue 那条已经不在 search 视野了

# 关掉 curator 对照
go run . -v -no-curator "what's my favorite color?"

# 单元测试
go test -v ./...
```

## Upstream Source Reading / 上游源码阅读

hermes 真实的 plugin 系统在 `plugins/` 目录 + `agent/plugin_manager.py`。Plugin 是 Python class，配 `manifest.toml`：

```upstream:hermes_agent/plugin_manager.py#L1-L60
# Excerpted + simplified from agent/plugin_manager.py + plugins/__init__.py

from abc import ABC, abstractmethod
import importlib
import logging
import asyncio
from pathlib import Path

logger = logging.getLogger(__name__)


class Plugin(ABC):
    """Hermes plugin contract. Every plugin lives in its own directory
    under plugins/<name>/ with a manifest.toml describing capabilities
    and an __init__.py exporting a Plugin subclass.
    """
    name: str

    @abstractmethod
    async def init(self, host: "Host") -> None: ...

    # Lifecycle. Hermes has more events than our mini (turn_begin,
    # turn_end, tool_call, tool_result, error) but the shape is the
    # same — fan out to all plugins, never block on one.
    async def on_session_start(self, session_id: str) -> None: ...
    async def on_session_end(self, session_id: str) -> None: ...
    async def on_turn_begin(self, session_id: str, turn: int) -> None: ...
    async def on_turn_end(self, session_id: str, turn: int) -> None: ...

    async def close(self) -> None: ...


class PluginManager:
    """Loads plugins by manifest, fans lifecycle events through them.
    Errors are LOGGED, never propagated — a misbehaving plugin must
    not take down the agent. Same hard rule we enforce in our Go
    PluginManager (see TestPluginManager_PluginErrorDoesNotStopOthers).
    """

    def __init__(self, plugins: list[Plugin], logger):
        self._plugins = plugins
        self._logger = logger

    @classmethod
    def from_config(cls, config_path: Path):
        plugins = []
        for entry in load_manifests(config_path):
            mod = importlib.import_module(entry["module"])
            plugin_cls = getattr(mod, entry["class"])
            plugins.append(plugin_cls(**entry.get("kwargs", {})))
        return cls(plugins, logger)

    async def init_all(self, host):
        for p in self._plugins:
            try:
                await p.init(host)
            except Exception:
                logger.exception("plugin %s init failed", p.name)

    async def dispatch(self, event: str, **kwargs):
        # asyncio.gather with return_exceptions=True so one plugin
        # raising doesn't cancel the rest. Errors get logged then
        # discarded — semantically identical to our Go for-loop.
        coros = [getattr(p, event)(**kwargs) for p in self._plugins]
        results = await asyncio.gather(*coros, return_exceptions=True)
        for p, r in zip(self._plugins, results):
            if isinstance(r, Exception):
                logger.exception("plugin %s %s failed: %s", p.name, event, r)


# === plugins/curator/__init__.py (excerpted) =================================

class CuratorPlugin(Plugin):
    """The actual hermes_cli/curator.py logic surfaced as a plugin.
    Triggers:
      - on_session_start: scan stale skills + memories
      - cron tick (delivered by Scheduler plugin in s09): periodic prune
    """
    name = "curator"

    def __init__(self, stale_after_days=90, archive_after_days=180, limit=200):
        self.stale_after_days = stale_after_days
        self.archive_after_days = archive_after_days
        self.limit = limit

    async def on_session_start(self, session_id):
        # 1) Mark items stale (between stale_after and archive_after)
        await self._mark_stale()
        # 2) Archive items older than archive_after_days
        await self._archive_old()
        # 3) Drop FTS index entries for archived rows (frees disk)
        await self._compact_fts()
```

**对照阅读要点**：

- **manifest.toml + dynamic load**：上游 plugin 是动态发现的，`plugins/<name>/manifest.toml` 写 capabilities，PluginManager.from_config 用 importlib 加载。我们 Go 版没有 dynamic plugin（Go plugin 系统差强人意），用编译时注册——更简单、更明确，但失去 "drop a folder, get a plugin" 的体验。
- **更多 lifecycle 事件**：上游有 `on_turn_begin` / `on_turn_end` / `on_tool_call` / `on_error` 等。我们 mini 只两个。加更多事件是机械的——每个事件在 Loop 里多一次 `pm.Dispatch...`，每个 plugin 多一个空方法。
- **2-stage stale → archive**：上游用三 bucket（active / stale / archived），中间状态可以提示 "你这条 memory 快被归档了，要不要 pin 它"。我们 mini 只两 bucket（active / archived）。
- **`_compact_fts()`**：上游归档后做 FTS5 表的物理 compaction，我们 mini 不做（archived rows 还占 FTS 索引空间，但被 SELECT 过滤）。一定数量后值得做。
- **dispatch 用 `asyncio.gather`**——上游 plugin 并行执行（每个 `await` 之间自然交错），我们 Go 是顺序循环。换成 goroutine + WaitGroup 是一行改动；保持顺序是有意为之，让顺序可预测。
- **plugin from cron tick**：上游 Scheduler 也是个 plugin，定时给 Curator 发 tick——s09（multi-process）会展示。

**想读更多**：从 `plugins/__init__.py` 的 manifest 加载入手，跟到 `agent/plugin_manager.py` 看 dispatch，跟到 `plugins/curator/__init__.py` 看一个真实 plugin 实现，跟到 `plugins/hermes_memory_fts5/__init__.py` 看 memory provider 怎么用 plugin 接口加进来。这条线就是 s06 → s05 (memory plugin) → s09 (scheduler plugin) 的真实代码地图。

---

**下一节预告**：s07 把 MCP（Model Context Protocol）服务器接到 Registry——又一个 tool 来源。MCP 通过 stdio 或 HTTP 跑在 agent 进程之外，工具集动态变化——`Generation` 计数器（s02 埋的）和 plugin 系统（s06 刚做的）会在这里同时派上用场。
