---
title: "s07 · MCP integration"
chapter: 7
slug: s07-mcp
est_read_min: 13
---

# s07 · MCP integration

> What this teaches: implement a **JSON-RPC 2.0 over stdio** MCP client, connect to an MCP server, and surface remote tools through the s02 Registry as ordinary `Tool`s. The `Generation` counter (planted in s02) and `DeregisterToolset` (added in s07) finally have real work to do. The same binary doubles as the demo MCP server (`-server` flag) so the end-to-end test needs no external dependencies.

---

## Problem

s02–s06 sourced tools from inside the agent process: builtin Go functions, skill files, plugin registrations. But the community ecosystem has heaps of useful tools — GitHub integration, sandboxed file systems, database clients, browser automation — that live in standalone **MCP servers** running **outside** the agent process.

Three concrete problems to solve when wiring them in:

1. **Protocol**: how does the agent talk to an external process? JSON-RPC over stdio (subprocess) or HTTP/SSE.
2. **Abstraction**: once a remote tool arrives, what shape does it take? Ideally identical to a builtin — the loop should never know whether `bash` runs in-process or `mcp_github_search` round-trips out.
3. **Lifecycle**: an MCP server can crash, disconnect, or reconnect with a different tool set. How does the Registry handle dynamic changes?

s07 ships the smallest answer. One transport (stdio; HTTP/SSE is an exercise), three RPC methods (`initialize` / `tools/list` / `tools/call`), and a `DeregisterToolset` for clean disconnect.

## Solution

Four pieces:

1. **`MCPClient`**: a concurrency-safe JSON-RPC client over a single connection. `call(method, params)` writes a request, the read goroutine routes responses by id, contexts cancel waiters.
2. **`MCPTool`**: an adapter that implements `Tool`. `Schema()` reuses the remote `inputSchema`; `Execute()` calls `client.CallTool`. Local name is `mcp_<server>_<remote-name>` — same convention hermes uses.
3. **`RegisterMCPTools`**: connect, `tools/list`, batch `Register(toolset="mcp-<server>")`. Bumps `Generation`.
4. **`Registry.DeregisterToolset`**: drop every tool in a given toolset at once on disconnect. Bumps `Generation`.

**Dual-mode binary**: the same `s07` runs the demo MCP server when started with `-server` (it exposes `echo` and `reverse`). End-to-end tests build the binary, spawn it as a subprocess, and connect — no external deps.

## How It Works

```ascii-anim frames=2
┌─────────────────────────────────────────────────────────────────┐
│  Agent (s07 binary, agent mode)                                 │
│  ┌────────────────────────────────────────────────┐             │
│  │  Registry                                      │             │
│  │   bash             builtin                     │             │
│  │   read_file        builtin                     │             │
│  │   mcp_echo_echo    mcp-echo  ─────┐            │             │
│  │   mcp_echo_reverse mcp-echo  ─────┤            │             │
│  └────────────────────────────────────┼───────────┘             │
│                                       │                         │
│                                       ▼ stdio JSON-RPC          │
│                            ┌──────────────────────┐             │
│                            │ MCPClient            │             │
│                            │  pending[id] -> chan │             │
│                            │  call/list/...       │             │
│                            └──────────┬───────────┘             │
│                                       │                         │
│                          subprocess.stdin / stdout              │
│                                       ▼                         │
│  ┌────────────────────────────────────────────────┐             │
│  │  Demo MCP Server (s07 binary, -server mode)    │             │
│  │   handles: initialize / tools/list / tools/call │             │
│  │   tools:   echo, reverse                        │             │
│  └────────────────────────────────────────────────┘             │
└─────────────────────────────────────────────────────────────────┘
```

JSON-RPC roundtrip (excerpt from [`mcp.go`](https://github.com/Ding-Ye/learn-hermes-agent/blob/main/agents/s07-mcp/mcp.go)):

```go
func (c *MCPClient) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
    id := atomic.AddUint64(&c.nextID, 1)
    pb, _ := json.Marshal(params)
    req := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: pb}
    line, _ := json.Marshal(req)
    ch := make(chan *rpcResponse, 1)
    c.mu.Lock(); c.pending[id] = ch; c.mu.Unlock()
    if _, err := c.w.Write(append(line, '\n')); err != nil { ... }
    select {
    case <-ctx.Done():       return nil, ctx.Err()
    case <-c.done:           return nil, fmt.Errorf("server closed: %v", c.readErr)
    case resp := <-ch:
        if resp.Error != nil { return nil, resp.Error }
        return resp.Result, nil
    }
}
```

The read goroutine (continuously demuxes responses):

```go
func (c *MCPClient) readLoop(r io.Reader) {
    scanner := bufio.NewScanner(r)
    for scanner.Scan() {
        var resp rpcResponse
        if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil { continue }
        if resp.ID == 0 { continue }   // notification, no waiter
        c.mu.Lock()
        ch, ok := c.pending[resp.ID]
        delete(c.pending, resp.ID)
        c.mu.Unlock()
        if ok { ch <- &resp }
    }
    c.doneOnce.Do(func() { close(c.done) })
}
```

**Four non-obvious points**:

1. **One JSON object per line** — stdio MCP uses newline-delimited JSON. `bufio.Scanner` defaults to splitting on newlines, which fits. Make the buffer big (we use 4 MB) — one large tool result will otherwise crash the scan.

2. **`id == 0` means notification** — per JSON-RPC 2.0. MCP uses it for `notifications/tools/list_changed` (dynamic refresh). We drop them for now, but the read loop already identifies the edge case.

3. **`MCPClient.done` channel** unblocks pending callers when the server dies. Without it, after a subprocess crash the caller hangs forever on `<-ch`. Closing `done` from the read loop wakes every pending caller at once.

4. **`mcp_<server>_<tool>` naming** is intentional visibility — the model sees which server a tool belongs to. Production agents run many MCP servers, and "wrong server picked" is a common bug; the name reduces ambiguity.

## What Changed (vs. s06)

s07 simplifies back toward s02's loop shape (no plugin manager, no memory provider) to keep the focus on protocol. The new code is `mcp.go` and `echo_server.go`:

```diff
+ // mcp.go: JSON-RPC types + MCPClient + MCPTool + RegisterMCPTools
+ // echo_server.go: same-binary -server mode, handles 3 RPC methods

  type Loop struct {
      Provider Provider
      Registry *Registry
      MaxTurns int
      Verbose  bool
  }
  // loop.go is unchanged — that's s07's core argument: MCP tools land
  // in the Registry without touching the loop.
```

`Registry` gains `DeregisterToolset`:

```go
func (r *Registry) DeregisterToolset(toolset string) int {
    r.mu.Lock(); defer r.mu.Unlock()
    removed := 0
    for name, e := range r.tools {
        if e.Toolset == toolset {
            delete(r.tools, name); removed++
        }
    }
    if removed > 0 { r.generation++ }
    return removed
}
```

## Try It

```bash
cd agents/s07-mcp

# 1) End-to-end test (builds itself, spawns itself in -server mode)
go test -v ./...

# 2) Run the agent against a demo MCP server (same binary, -server mode)
go build .
export ANTHROPIC_API_KEY=sk-ant-...
./s07 -v -mcp "echo=./s07 -server" "use mcp_echo_reverse to reverse the string 'hermes'"
```

Expected output:

```
[mcp] echo connected; 2 tools registered
[registry] 4 tools (gen=4): [bash mcp_echo_echo mcp_echo_reverse read_file]
[turn 0] -> mcp_echo_reverse map[text:hermes]
[turn 0] <- semreh
[turn 1] assistant: 'hermes' reversed is 'semreh'.
'hermes' reversed is 'semreh'.
```

## Upstream Source Reading

hermes's MCP implementation lives in `tools/mcp_tool.py`. Major upstream-only features:
- Both stdio and HTTP transports (HTTP also has SSE and streamable variants)
- Handles `notifications/tools/list_changed` — the server proactively tells the client "my toolset changed", client refreshes
- OAuth recovery on auth failures (HTTP transport)
- Each server runs as its own asyncio Task — failure isolation

```upstream:hermes_agent/mcp_tool.py#L1-L60
# Excerpted + simplified from tools/mcp_tool.py

import asyncio
import json
from typing import Any

class MCPClient:
    """MCP client supporting both stdio and HTTP transports. Hermes runs
    each server as a long-lived asyncio Task; OAuth refresh, server
    crashes, and tools/list_changed events are all handled transparently
    by background routines."""

    def __init__(self, transport):
        self.transport = transport
        self._next_id = 0
        self._pending: dict[int, asyncio.Future] = {}
        self._task = asyncio.create_task(self._read_loop())

    async def _read_loop(self):
        async for line in self.transport.lines():
            try:
                msg = json.loads(line)
            except json.JSONDecodeError:
                continue
            if "id" not in msg:                            # notification
                await self._dispatch_notification(msg)
                continue
            fut = self._pending.pop(msg["id"], None)
            if fut and not fut.done():
                if "error" in msg:
                    fut.set_exception(MCPError(msg["error"]))
                else:
                    fut.set_result(msg.get("result"))

    async def call(self, method: str, params: dict | None = None) -> Any:
        self._next_id += 1
        id_ = self._next_id
        fut = asyncio.get_running_loop().create_future()
        self._pending[id_] = fut
        await self.transport.write({"jsonrpc": "2.0", "id": id_,
                                    "method": method, "params": params or {}})
        return await asyncio.wait_for(fut, timeout=30)

    async def _dispatch_notification(self, msg):
        method = msg.get("method")
        if method == "notifications/tools/list_changed":
            await self._on_tools_changed()

    async def _on_tools_changed(self):
        # 1) re-fetch the catalogue
        new_tools = await self.call("tools/list")
        # 2) drop this server's existing tools (registry.deregister_toolset)
        registry.deregister_toolset(self.toolset)
        # 3) re-register; generation bumps; loop sees the change next turn
        for tdef in new_tools["tools"]:
            registry.register(...)
```

**Reading notes**:

- **stdio + HTTP/SSE**: upstream supports both. stdio is simple (subprocess + pipes); HTTP/SSE is right for cloud-hosted MCP servers without host access. Our mini does stdio only.
- **`notifications/tools/list_changed`**: upstream truly does dynamic refresh — server sends the notification, client deregisters the whole toolset, re-lists, re-registers. s06's plugin lifecycle and s07's deregister-toolset converge here. Our mini drops the notification (`continue` in the read loop), but `DeregisterToolset` is already in place.
- **OAuth recovery**: HTTP MCP servers often use OAuth; upstream catches 401s, runs the refresh-token flow, retries. The `hermes mcp auth` CLI command is for this flow.
- **One asyncio Task per server**: upstream's concurrency model isolates failures using `asyncio.Task` — one server crashing doesn't take down the others. Our Go version naturally has goroutines + recover, but we don't ship an explicit recover wrapper (production should).
- **Per-call timeout**: upstream defaults to 30s per call. We let the caller pass a `context.Context` with whatever deadline they want.

**Read further**: start at `tools/mcp_tool.py` `MCPClient.call` for the RPC framing, follow `_dispatch_notification` for dynamic refresh, follow the HTTP transport (`HTTPMCPTransport`) for SSE support, and finally the `hermes_cli/mcp_*` family for the CLI subcommands (add/remove/list/auth). That trace → s06 (plugin) → s09 (multi-process gateway shares the mcp_tool registry).

---

**Next**: s08 abstracts BashTool into a **Terminal Backend factory** — one `terminal` tool, but the implementation behind it can be a local shell, a Docker container, an SSH session, a Modal lambda, etc. Another instance of "stable tool interface, swappable execution environment".
