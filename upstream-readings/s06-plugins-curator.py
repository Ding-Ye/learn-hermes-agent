# Upstream source reading for s06 · Plugin system + Curator
#
# Sources:
#   - NousResearch/hermes-agent · plugins/__init__.py        (manifest loader)
#   - NousResearch/hermes-agent · agent/plugin_manager.py    (dispatch bus)
#   - NousResearch/hermes-agent · plugins/curator/           (one real plugin)
#   - NousResearch/hermes-agent · hermes_cli/curator.py      (the CLI face)
# License: see https://github.com/NousResearch/hermes-agent/blob/main/LICENSE


# ============================================================================
# agent/plugin_manager.py  --  the bus
# ============================================================================

from abc import ABC, abstractmethod
import importlib
import logging
import asyncio
from pathlib import Path

logger = logging.getLogger(__name__)


class Plugin(ABC):
    """Hermes plugin contract.

    Every plugin lives under plugins/<name>/ with:
      - manifest.toml declaring capabilities, version, dependencies
      - __init__.py (or any module) exporting a Plugin subclass
    """
    name: str

    @abstractmethod
    async def init(self, host: "Host") -> None: ...

    async def on_session_start(self, session_id: str) -> None: ...
    async def on_session_end(self, session_id: str) -> None: ...
    async def on_turn_begin(self, session_id: str, turn: int) -> None: ...
    async def on_turn_end(self, session_id: str, turn: int) -> None: ...
    async def on_tool_call(self, name: str, args: dict) -> None: ...
    async def on_error(self, err: Exception) -> None: ...

    async def close(self) -> None: ...


class PluginManager:
    """Owns the plugins, fans events out to them, never lets a single
    plugin take down the agent.
    """

    def __init__(self, plugins: list[Plugin], logger):
        self._plugins = plugins
        self._logger = logger

    @classmethod
    def from_config(cls, config_path: Path):
        plugins = []
        for entry in load_manifests(config_path):
            # entry: {"module": "plugins.curator", "class": "CuratorPlugin",
            #         "kwargs": {...}}
            mod = importlib.import_module(entry["module"])
            plugin_cls = getattr(mod, entry["class"])
            plugins.append(plugin_cls(**entry.get("kwargs", {})))
        return cls(plugins, logger)

    async def init_all(self, host):
        for p in self._plugins:
            try:
                await p.init(host)
            except Exception:
                # log + keep going; do NOT crash the agent on plugin init
                logger.exception("plugin %s init failed", p.name)

    async def dispatch(self, event: str, **kwargs):
        """Parallel dispatch. asyncio.gather with return_exceptions=True
        means one plugin raising never cancels the others.
        """
        coros = [getattr(p, event)(**kwargs) for p in self._plugins]
        results = await asyncio.gather(*coros, return_exceptions=True)
        for p, r in zip(self._plugins, results):
            if isinstance(r, Exception):
                logger.exception("plugin %s %s failed: %s", p.name, event, r)

    async def close_all(self):
        for p in self._plugins:
            try:
                await p.close()
            except Exception:
                logger.exception("plugin %s close failed", p.name)


# ============================================================================
# plugins/curator/__init__.py  --  one concrete plugin
# ============================================================================

from datetime import datetime, timezone


class CuratorPlugin(Plugin):
    """The actual hermes_cli/curator.py logic surfaced as a plugin.

    Triggers:
      - on_session_start: cheap pass — mark stale, archive overdue.
      - on_cron_tick (delivered by Scheduler plugin in s09): full sweep
        with FTS5 compaction.
    """
    name = "curator"

    def __init__(self, stale_after_days=90, archive_after_days=180, limit=200):
        self.stale_after_days = stale_after_days
        self.archive_after_days = archive_after_days
        self.limit = limit
        self._host = None

    async def init(self, host):
        self._host = host

    async def on_session_start(self, session_id):
        # 1) Mark items stale (between stale_after and archive_after).
        await self._mark_stale()
        # 2) Archive items older than archive_after_days.
        await self._archive_old()
        # 3) Drop FTS index entries for archived rows. Frees disk and
        #    keeps query plans tight. Skipped on session_start in
        #    practice — too expensive to run every session; reserved
        #    for the cron tick.

    async def _mark_stale(self):
        # ... uses self._host.memory.list_active(...) + .mark_stale(...)
        ...

    async def _archive_old(self):
        # ... uses self._host.memory.archive(...) + records audit log
        ...


# ============================================================================
# hermes_cli/curator.py  --  the CLI face users actually invoke
# ============================================================================
#
# `hermes curator status`  → counts of active/stale/archived
# `hermes curator run`     → trigger the plugin's logic immediately
#                              (--dry-run shows what would change)
# `hermes curator prune`   → force-archive bottom N% by idle days
# `hermes curator pin <id>` → never auto-archive this row
# `hermes curator backup`  → snapshot skills+memories
# `hermes curator rollback`→ restore from snapshot
#
# The two layers — a plugin doing the work + a CLI for ergonomics —
# is a recurring hermes pattern. Plugins handle automation, CLI handles
# user exception cases.


def _idle_days(record: dict) -> int | None:
    """Days since the row's last activity. The single decision input
    every curator threshold compares against."""
    ts = record.get("last_activity_at") or record.get("created_at")
    dt = datetime.fromisoformat(str(ts))
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=timezone.utc)
    return max(0, (datetime.now(timezone.utc) - dt).days)


# ============================================================================
# A reading map for going further
# ============================================================================
#
# - plugins/__init__.py            manifest discovery, Plugin ABC
# - agent/plugin_manager.py        dispatch bus
# - plugins/curator/__init__.py    Curator as a plugin (the s06 work)
# - hermes_cli/curator.py          the user-facing CLI for manual ops
# - plugins/hermes_memory_fts5/    memory provider as a plugin (s05)
# - plugins/scheduler/             cron-tick provider as a plugin (s09)
# - plugins/observability/         metrics + traces as a plugin
#
# Sessions in this course that revisit these files:
#   s05 (Memory)         — the FTS5 memory IS a plugin in production
#   s09 (Multi-process)  — Scheduler plugin sends ticks to the Curator
