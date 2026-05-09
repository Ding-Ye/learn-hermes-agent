---
title: "s06 · Plugin system + Curator"
chapter: 6
slug: s06-plugins-curator
est_read_min: 14
---

# s06 · Plugin system + Curator

> What this teaches: define a `Plugin` interface + `PluginManager` bus so multiple subscribers can listen to lifecycle events. Then write a **CuratorPlugin** — which is the truth behind hermes's "self-improving memory" tagline: **track `last_activity_at`, auto-archive idle memories**, NOT auto-generate new ones.

---

## Problem

In s05 the Loop calls `MemoryProvider.OnSessionStart` / `OnSessionEnd` directly. That works — until a second thing wants to know about session start/end. Observability wants to emit metrics, curator wants to scan for idle items, billing wants to track tokens, enterprise wants audit logs… they all hook the same set of events.

If you keep growing the Loop:

```go
if l.Memory != nil       { l.Memory.OnSessionStart(...) }
if l.Curator != nil      { l.Curator.OnSessionStart(...) }
if l.Telemetry != nil    { l.Telemetry.OnSessionStart(...) }
if l.AuditLog != nil     { l.AuditLog.OnSessionStart(...) }
if l.CostAccountant ...
```

It quickly becomes a wall of `if`s. The Loop also starts to know about every concrete extension. **Flip it around**: have the Loop talk to one object — `PluginManager` — and the manager fans events out to any number of plugins. Loop doesn't know how many plugins exist; plugins don't know about each other.

This is hermes's plugin architecture. Memory provider, curator, observability, billing, dashboards — all plugins.

## Solution

Four pieces:

1. **`Plugin` interface** (5 methods): `Name`, `Init(ctx, host)`, `OnSessionStart`, `OnSessionEnd`, `Close`.
2. **`Host` struct** — the "safe surface" plugins receive at Init time: `Registry` / `Memory` / `Logger`. Deliberately a narrow capability surface, not the whole agent state.
3. **`PluginManager`** bus: `Register` / `Init` / `DispatchSessionStart` / `DispatchSessionEnd` / `Close`. **One plugin's error must not stop others** — the manager logs and continues.
4. **Two example plugins**:
   - `LoggingPlugin` — smallest possible plugin; logs each lifecycle event.
   - `CuratorPlugin` — reads `last_activity_at`, calls `Memory.ListStale` + `Memory.Archive`, archives memories untouched longer than N.

The memory schema gains two columns: `last_activity_at` (bumped on every save/search) and `archived_at` (curator writes). Search filters `archived_at IS NULL`.

## How It Works

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

CuratorPlugin's OnSessionStart (excerpt):

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

PluginManager's fault-tolerant dispatch (excerpt):

```go
func (pm *PluginManager) DispatchSessionStart(ctx context.Context, sid string) {
    pm.mu.Lock(); defer pm.mu.Unlock()
    for _, p := range pm.plugins {
        if err := p.OnSessionStart(ctx, sid); err != nil {
            pm.logger.Errorf("plugin %s OnSessionStart: %v", p.Name(), err)
            // do NOT return — keep dispatching to remaining plugins
        }
    }
}
```

**Four non-obvious points**:

1. **Search hits auto-bump `last_activity_at`** — using a memory is "extending its life". A key hermes design choice: feed usage frequency directly into archive decisions, **no separate access log needed**. `SQLiteMemory.Search` calls `touchMany` over all hit ids internally.

2. **`archived_at` doesn't delete the row** — archiving only hides it from the search view (`WHERE archived_at IS NULL`); data is still there. This is what enables `rollback`/`unarchive`-style UNDO operations in upstream (we don't ship the CLI subcommands, but the data foundation is in place).

3. **Plugin manager errors get `logger.Errorf`'d, not propagated**. **A single buggy plugin must not take down the agent** — it's a hard hermes invariant, codified in our `TestPluginManager_PluginErrorDoesNotStopOthers`.

4. **Host is a capability surface**, not *agent.go*. Plugins only see Registry / Memory / Logger, not the Loop / Provider / Session internals. This makes future fine-grained capabilities — "this plugin can write memory but not read", "this one sees registry but cannot register new tools" — feasible to add by extending Host.

## What Changed (vs. s05)

```diff
  type Loop struct {
      Provider Provider
      Registry *Registry
      Store    *Store
-     Memory   MemoryProvider
+     Plugins  *PluginManager     // new in s06
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

Memory schema extension:

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

The s05 `MemoryProvider` interface gains three curator-facing methods: `Touch` / `Archive` / `ListStale`.

## Try It

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s06-plugins-curator

# Demo: shrink the stale threshold to 1 second so the curator acts
# right away.
go run . -v -curator-stale-after 1s "remember my favorite color is blue"
sleep 2
go run . -v -curator-stale-after 1s "what's my favorite color?"
# stderr from run 2 will include:
#   [s06] [plugin:curator] archived 1 stale memories: [1]
# Then memory_search returns empty — the "blue" row is no longer
# visible.

# Disable the curator for comparison
go run . -v -no-curator "what's my favorite color?"

# Unit tests
go test -v ./...
```

## Upstream Source Reading

hermes's real plugin system lives in `plugins/` plus `agent/plugin_manager.py`. Plugins are Python classes with a `manifest.toml`:

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

**Reading notes**:

- **manifest.toml + dynamic load**: upstream plugins are dynamically discovered — `plugins/<name>/manifest.toml` declares capabilities, PluginManager.from_config uses `importlib`. Go's plugin system is awkward, so our Go version uses compile-time registration. Simpler and more explicit, but loses the "drop a folder, get a plugin" experience.
- **More lifecycle events**: upstream has `on_turn_begin` / `on_turn_end` / `on_tool_call` / `on_error` and others. Our mini has two. Adding more is mechanical — one extra `pm.Dispatch...` in the Loop, one extra empty method on each plugin.
- **2-stage stale → archive**: upstream uses three buckets (active / stale / archived), letting the middle state surface a "this memory is about to be archived; pin it?" prompt. Our mini has two (active / archived).
- **`_compact_fts()`**: upstream physically compacts the FTS5 table after archival; we don't (archived rows still take FTS index space, but are SELECT-filtered). Worth doing past a certain volume.
- **Dispatch via `asyncio.gather`** — upstream plugins run in parallel (interleaved at `await`s); our Go version is sequential. Switching to goroutines + WaitGroup is one diff; we keep sequential for predictable order.
- **Plugin from cron tick**: upstream's Scheduler is itself a plugin and sends ticks to the Curator — s09 (multi-process) shows it.

**Read further**: start at the manifest loader in `plugins/__init__.py`, follow into `agent/plugin_manager.py` for dispatch, into `plugins/curator/__init__.py` for one real plugin, into `plugins/hermes_memory_fts5/__init__.py` for the memory provider as a plugin. That trace is the s06 → s05 (memory plugin) → s09 (scheduler plugin) real-source map.

---

**Next**: s07 wires Model Context Protocol (MCP) servers into the Registry — yet another tool source. MCP runs out-of-process via stdio or HTTP, tools change at runtime — the `Generation` counter (planted in s02) and the plugin system (just built in s06) finally come together.
