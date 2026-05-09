# Upstream source reading for s02 · Tool Registry
#
# Source: NousResearch/hermes-agent · tools/registry.py
# License: see https://github.com/NousResearch/hermes-agent/blob/main/LICENSE
#
# Teaching excerpt only — the upstream file is ~650 lines covering many
# operational concerns (telemetry, deregister cascades, alias resolution,
# async bridging) that we omit here. The pieces below are the load-bearing
# core that maps directly onto agents/s02-tool-registry/registry.go.
#
# Annotations are inline. Line numbers in this file are LOCAL.


# --- excerpted from tools/registry.py (the type, the lock, the counter) -----

from typing import NamedTuple, Callable, Any
import threading
import inspect
import logging

logger = logging.getLogger(__name__)


class ToolEntry(NamedTuple):
    """One row in the registry. Immutable on purpose — re-registration
    creates a fresh ToolEntry, never mutates an existing one. Mirrors our
    Go ToolEntry struct.
    """
    handler: Callable
    schema: dict
    toolset: str       # "builtin" | "mcp-<server>" | "skill-<name>" | "plugin-<name>"
    generation: int    # snapshot of registry._generation at insertion time


class UnknownTool(Exception):
    pass


# --- excerpted from tools/registry.py (the registry itself) -----------------

class ToolRegistry:
    """All tool sources (builtin, plugin, MCP, skill) share this registry.
    The `toolset` field on each entry drives the shadow protection rules
    in `_can_replace`. Production hermes layers on toolset-availability
    checks, alias resolution, and async dispatch bridging on top of this
    same skeleton.
    """

    def __init__(self):
        self._tools: dict[str, ToolEntry] = {}
        # Per-toolset "is this toolset usable right now?" callbacks. Used
        # by MCP to mark its tools unavailable when its server is down.
        # We omit this in our Go mini until s07.
        self._toolset_checks: dict[str, Callable] = {}
        # Cosmetic aliasing: register_toolset_alias("mcp-github", "gh")
        # lets users invoke "gh" while the registry stores "mcp-github".
        # Also omitted in our mini.
        self._toolset_aliases: dict[str, str] = {}
        # Coarse-grained lock — registers are infrequent enough that
        # contention isn't a concern, and an RLock lets dispatch nest
        # in case a tool wants to inspect the registry mid-call.
        self._lock = threading.RLock()
        self._generation = 0  # bumped on every successful register/deregister

    # ------------------------------------------------------------------- API

    def register(self, name: str, handler: Callable, schema: dict, toolset: str) -> bool:
        """Add or replace a tool. Returns True on success.

        Hermes's design choice: shadow conflicts are LOGGED and IGNORED,
        not raised. The registry never crashes the agent over a config
        mishap. Our Go mini errors out instead — easier to reason about
        in tests, but you should treat hermes's behaviour as the right
        production default.
        """
        with self._lock:
            existing = self._tools.get(name)
            if existing and not self._can_replace(existing.toolset, toolset):
                logger.error(
                    "registry: refusing to shadow %r (%s) with %s",
                    name, existing.toolset, toolset,
                )
                return False
            self._generation += 1
            self._tools[name] = ToolEntry(
                handler=handler,
                schema=schema,
                toolset=toolset,
                generation=self._generation,
            )
            return True

    def deregister(self, name: str) -> bool:
        """Remove a tool by name. Used by MCP when a server disconnects:
        the MCP plugin enumerates its previously-registered tools and
        deregisters them, bumping generation so the next loop turn
        rebuilds its schemas without them.
        """
        with self._lock:
            if name not in self._tools:
                return False
            del self._tools[name]
            self._generation += 1
            return True

    def get_entry(self, name: str) -> ToolEntry | None:
        with self._lock:
            return self._tools.get(name)

    def get_definitions(self) -> list[dict]:
        """Return tool schemas in NAME-SORTED order. Stability matters:
        Anthropic's prompt cache matches the tools array byte-for-byte.
        """
        with self._lock:
            names = sorted(self._tools)
            return [
                {**self._tools[n].schema, "name": n}
                for n in names
            ]

    @property
    def generation(self) -> int:
        with self._lock:
            return self._generation

    # ------------------------------------------------------------ internals

    @staticmethod
    def _can_replace(existing: str, incoming: str) -> bool:
        """The shadow-protection rule. Same as our Go canReplace, plus
        room for the toolset-availability and alias logic that hermes
        layers in.
        """
        if existing == incoming:
            return True   # idempotent refresh — same source replacing itself
        if existing == "builtin" or incoming == "builtin":
            return False  # builtins are sacred in both directions
        if existing.startswith("mcp-") and incoming.startswith("mcp-"):
            return True   # one MCP server replacing another's tool is fine
        return False

    def dispatch(self, name: str, args: dict) -> Any:
        """Execute a tool's handler. Sync handlers run directly; async
        handlers are bridged via `_run_async` so callers don't have to
        know which kind they got.
        """
        entry = self.get_entry(name)
        if entry is None:
            raise UnknownTool(name)
        result = entry.handler(**args)
        if inspect.isawaitable(result):
            result = self._run_async(result)
        return result


# --- a guide to reading further --------------------------------------------
#
# - tools/registry.py        — full registry (this file's source)
# - tools/mcp_tool.py        — uses deregister + register on MCP refresh
# - plugins/__init__.py      — plugin-sourced tool registration (s06)
# - hermes_cli/skills.py     — skill files exposed as tools (s03)
#
# Each is revisited later in this course; this registry is the contact
# point that ties them all together.
