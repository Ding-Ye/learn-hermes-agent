# Upstream source reading for s07 · MCP integration
#
# Source: NousResearch/hermes-agent · tools/mcp_tool.py
# License: see https://github.com/NousResearch/hermes-agent/blob/main/LICENSE
#
# Teaching excerpt focused on the JSON-RPC client + dynamic refresh path.
# Our s07 mini covers the synchronous request/response shape; what
# upstream layers on top is annotated below.


import asyncio
import json
import logging
from typing import Any, Optional

logger = logging.getLogger(__name__)


# ============================================================================
# tools/mcp_tool.py  --  the client core
# ============================================================================

class MCPError(Exception):
    pass


class MCPClient:
    """MCP client over an arbitrary transport. Two transports ship with
    hermes:

      - StdioMCPTransport: subprocess + stdin/stdout pipes
      - HTTPMCPTransport:  HTTP + optional SSE for streaming

    Each connected server lives in its own asyncio.Task — a crash in
    one server does not affect the others.
    """

    def __init__(self, transport, toolset: str):
        self.transport = transport
        self.toolset = toolset                         # "mcp-<server>"
        self._next_id = 0
        self._pending: dict[int, asyncio.Future] = {}
        self._task = asyncio.create_task(self._read_loop())
        self._closed = False

    # ----------------------------------------------------------- read loop

    async def _read_loop(self):
        try:
            async for line in self.transport.lines():
                try:
                    msg = json.loads(line)
                except json.JSONDecodeError:
                    continue

                # JSON-RPC 2.0: messages without "id" are notifications.
                if "id" not in msg:
                    await self._dispatch_notification(msg)
                    continue

                fut = self._pending.pop(msg["id"], None)
                if fut is None or fut.done():
                    continue
                if "error" in msg:
                    fut.set_exception(MCPError(msg["error"]))
                else:
                    fut.set_result(msg.get("result"))
        finally:
            # Wake up any callers still waiting — they'd otherwise hang
            # forever if the server died mid-call.
            for fut in self._pending.values():
                if not fut.done():
                    fut.set_exception(MCPError("server closed"))
            self._pending.clear()

    # ---------------------------------------------------------- request API

    async def call(self, method: str, params: Optional[dict] = None,
                   timeout: float = 30.0) -> Any:
        if self._closed:
            raise MCPError("client closed")
        self._next_id += 1
        id_ = self._next_id
        fut = asyncio.get_running_loop().create_future()
        self._pending[id_] = fut
        try:
            await self.transport.write({
                "jsonrpc": "2.0",
                "id": id_,
                "method": method,
                "params": params or {},
            })
            return await asyncio.wait_for(fut, timeout=timeout)
        except asyncio.TimeoutError:
            self._pending.pop(id_, None)
            raise

    # --------------------------------------------------- notifications path

    async def _dispatch_notification(self, msg: dict):
        method = msg.get("method")
        if method == "notifications/tools/list_changed":
            await self._on_tools_changed()
        elif method == "notifications/resources/list_changed":
            await self._on_resources_changed()
        # ... prompts/list_changed, etc.

    async def _on_tools_changed(self):
        """The server told us its tool catalogue changed. Hermes:

           1. fetches the new catalogue
           2. atomically drops the existing toolset and re-registers
              from the new list (registry.deregister_toolset bumps
              Generation, then each register bumps it again)
           3. the next agent turn sees the new tools — no restart needed.
        """
        try:
            new = await self.call("tools/list")
        except Exception:
            logger.exception("tools/list_changed: failed to refresh")
            return
        registry.deregister_toolset(self.toolset)
        for tdef in new.get("tools", []):
            registry.register(
                name=f"mcp_{self.toolset.removeprefix('mcp-')}_{tdef['name']}",
                toolset=self.toolset,
                schema=tdef.get("inputSchema", {}),
                handler=_make_call_handler(self, tdef["name"]),
            )


# ============================================================================
# tools/mcp_tool.py (continued)  --  HTTP transport with OAuth recovery
# ============================================================================
#
# Sketched, not full source. The HTTP path is materially more complex:
# OAuth refresh tokens, SSE streaming for long-running tools, retry on
# 502/503, request signing for some providers.

class HTTPMCPTransport:
    async def write(self, payload: dict):
        try:
            async with self._session.post(self.endpoint, json=payload) as resp:
                if resp.status == 401:
                    await self._refresh_oauth()
                    return await self.write(payload)
                resp.raise_for_status()
        except Exception:
            logger.exception("HTTP MCP write failed")
            raise

    async def _refresh_oauth(self):
        # Standard OAuth 2.0 token refresh. The MCP spec defines an
        # extension here; hermes_cli/mcp_auth.py handles the user-facing
        # half (browser redirect, code exchange).
        ...


# ============================================================================
# hermes_cli/mcp_*  --  user-facing CLI surface
# ============================================================================
#
#   hermes mcp add <name> <command...>   register a stdio server in config
#   hermes mcp add <name> --http <url>   register an HTTP server
#   hermes mcp list                      show configured servers + status
#   hermes mcp remove <name>             unregister
#   hermes mcp auth <name>               run OAuth flow for HTTP server
#   hermes mcp test <name>               connect, list_tools, exit
#
# The CLI is a thin wrapper around config.yaml's `mcp_servers` section
# plus the MCPClient. Persistence lives in YAML, not in a database —
# different from sessions/memories.


# ============================================================================
# A reading map
# ============================================================================
#
# - tools/mcp_tool.py            client + transports + dynamic refresh
# - tools/mcp_config.py          parse mcp_servers from config.yaml
# - hermes_cli/mcp_*             CLI subcommands
# - plugins/mcp/__init__.py      MCP-as-plugin (links tools/list_changed
#                                 to PluginManager events)
#
# Sessions in this course that revisit these files:
#   s06 (Plugin)        — MCPClient surfaces as a plugin
#   s09 (Multi-process) — gateway processes share one mcp_servers config
