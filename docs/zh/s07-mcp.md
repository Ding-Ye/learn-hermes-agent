---
title: "s07 · MCP 集成"
chapter: 7
slug: s07-mcp
est_read_min: 13
---

# s07 · MCP 集成

> 教什么：实现一个 **JSON-RPC 2.0 over stdio** 的 MCP 客户端，连接 MCP server，把远端工具包成 `Tool` 注册到 s02 Registry。`Generation` 计数器（s02 埋的）和 `DeregisterToolset`（s07 加的）让动态工具集第一次有真用。同 binary 双模式（`-server` 跑 demo MCP server）让 demo 完全自包含。

---

## Problem / 问题

s02-s06 的工具来源都在 agent 进程内：builtin Go 函数、skill 文件、plugin 注册。但社区生态里大量**有用的工具**——GitHub 集成、文件系统沙箱、数据库客户端、浏览器自动化——存在于独立的 **MCP server** 里，跑在 agent 进程**之外**。

接进来要解决三个具体问题：
1. **协议**：怎么和外部进程通信？JSON-RPC over stdio（subprocess）or HTTP/SSE。
2. **抽象**：远端工具进来后该长什么样？理想是和 builtin 一模一样——loop 不该知道某个 `bash` 是本地的、某个 `mcp_github_search` 是 RPC 出去的。
3. **生命周期**：MCP server 可能崩、可能断、可能重连后工具集变了。Registry 怎么处理动态变化？

s07 给最小可用答案。stdio 一个 transport（HTTP/SSE 是练习），三个 RPC 方法（`initialize` / `tools/list` / `tools/call`），加一个 `DeregisterToolset` 让断连时一次清掉一整组。

## Solution / 解决方案

四件事：

1. **`MCPClient`**：单连接、并发安全的 JSON-RPC 客户端。`call(method, params)` 发请求、按 id 路由响应、超时取消。读 goroutine 监听 stdout，请求方等 channel。
2. **`MCPTool`**：实现 `Tool` interface 的适配器，`Schema()` 来自远端的 `inputSchema`，`Execute()` 调 `client.CallTool`。本地名字 `mcp_<server>_<remote-name>`——跟 hermes 约定一致。
3. **`RegisterMCPTools`**：连接、`tools/list`、批量 `Register(toolset="mcp-<server>")`。这一步 bump `Generation`。
4. **`Registry.DeregisterToolset`**：断连时一次性删掉这个 server 注册的所有工具，再 bump `Generation`。

**双模式 binary**：同一个 `s07` 加 `-server` 标志就跑 demo MCP server（暴露 `echo` 和 `reverse`）。这让端到端测试不需要任何外部依赖——`go test` 自己 build 自己 + 自己 spawn 自己。

## How It Works / 工作原理

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
│                            │  call/listToFns/...  │             │
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

JSON-RPC roundtrip（节选自 [`mcp.go`](https://github.com/Ding-Ye/learn-hermes-agent/blob/main/agents/s07-mcp/mcp.go)）：

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

读 goroutine（持续 demux 响应）：

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

**四个非显然之处**：

1. **每行一个 JSON 对象** —— stdio MCP 用 newline-delimited JSON。`bufio.Scanner` 默认按行切，正好匹配。注意 buffer 要够大（我们设 4MB），否则一个大工具结果就崩。

2. **`id == 0` 是 notification** —— JSON-RPC 2.0 协议规定。MCP 用它来发 `notifications/tools/list_changed`（动态刷新）。我们暂时丢弃，但读 loop 已经识别这个边界 case。

3. **`MCPClient.done` channel** 让阻塞的调用方在 server 死掉时立即解锁——没有它，subprocess 崩了之后 caller 永远 hang 在 `<-ch` 上。读 loop 退出时关 done，所有 pending call 一起 unblock。

4. **`mcp_<server>_<tool>` 命名**给 LLM 暴露完整路径——它能看到一个工具来自哪个 server。这是个**有意的可见性**：production agent 跑十几个 MCP server，模型选错 server 是常见 bug，命名能减少。

## What Changed / 与 s06 的变化

s07 简化回到了 s02 的 loop 形态（去掉 plugin manager / memory provider，专注协议）。新增的核心是 `mcp.go` 和 `echo_server.go`：

```diff
+ // mcp.go: JSON-RPC types + MCPClient + MCPTool + RegisterMCPTools
+ // echo_server.go: 同 binary 的 -server 模式，handles 3 个 RPC 方法

  type Loop struct {
      Provider Provider
      Registry *Registry
      MaxTurns int
      Verbose  bool
  }
  // loop.go 没变 —— 这是 s07 的核心论据：MCP 工具进来 *不动 loop*
```

`Registry` 新增 `DeregisterToolset`：

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

## Try It / 动手试一试

```bash
cd agents/s07-mcp

# 1) 端到端测试（自己 build 自己当 server）
go test -v ./...

# 2) 真跑 agent，连一个 demo MCP server（同 binary 的 -server 模式）
go build .
export ANTHROPIC_API_KEY=sk-ant-...
./s07 -v -mcp "echo=./s07 -server" "use mcp_echo_reverse to reverse the string 'hermes'"
```

期望输出：

```
[mcp] echo connected; 2 tools registered
[registry] 4 tools (gen=4): [bash mcp_echo_echo mcp_echo_reverse read_file]
[turn 0] -> mcp_echo_reverse map[text:hermes]
[turn 0] <- semreh
[turn 1] assistant: 'hermes' reversed is 'semreh'.
'hermes' reversed is 'semreh'.
```

## Upstream Source Reading / 上游源码阅读

hermes 的 MCP 实现在 `tools/mcp_tool.py`，主要差别：
- 支持 stdio + HTTP transport（HTTP 还有 SSE 和 streamable variants）
- 处理 `notifications/tools/list_changed` —— server 主动告诉 client "我的工具集变了"，client refresh
- OAuth 失败自动重试（HTTP transport）
- 把每个 server 装进独立的 asyncio Task，崩溃隔离

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
            # notification: no id
            if "id" not in msg:
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
            # server is telling us its toolset changed; refresh the registry.
            await self._on_tools_changed()
        # ... resources/list_changed, prompts/list_changed, etc.

    async def _on_tools_changed(self):
        # 1) tools/list to fetch the new catalogue
        new_tools = await self.call("tools/list")
        # 2) deregister this server's existing tools (toolset DeregisterToolset)
        registry.deregister_toolset(self.toolset)
        # 3) re-register the new ones; generation bumps; loop sees them next turn
        for tdef in new_tools["tools"]:
            registry.register(...)
```

**对照阅读要点**：

- **stdio + HTTP/SSE**：上游两种 transport 都支持。stdio 简单（subprocess + pipe），HTTP/SSE 适合 cloud-hosted MCP server（无主机访问）。我们 mini 只 stdio。
- **`notifications/tools/list_changed`**：上游真的处理动态刷新——server 一通知，client deregister 整组、重新 list、re-register。s06 的 plugin lifecycle 在这里和 s07 的 deregister-toolset 合流。我们 mini 没接这个 notification（在 readLoop 直接 `continue`），但 `DeregisterToolset` 已经准备好了。
- **OAuth 自动重试**：HTTP MCP server 经常用 OAuth；上游捕获 401、走 refresh token、重发请求。CLI 里的 `hermes mcp auth` 命令就是给这个流程做的。
- **每 server 一个 asyncio Task**：上游的并发模型用 asyncio.Task 隔离 server 故障——一个 server 崩了不影响别的。我们 Go 版本天然有 goroutine + recover，但目前没显式 recover wrapper（生产代码该加）。
- **超时**：上游每个 call 有 30s timeout。我们用 `context.Context` 让 caller 自己控制。

**想读更多**：从 `tools/mcp_tool.py` 入手，跟 `MCPClient.call` 看 RPC 框架，跟 `_dispatch_notification` 看动态刷新，跟 HTTP transport（`HTTPMCPTransport`）看 SSE 支持，跟 `hermes_cli/mcp_*` 看 CLI 子命令（add/remove/list/auth）。这条线 → s06 (plugin) → s09 (multi-process: gateway 各 source 共享 mcp_tool 注册表)。

---

**下一节预告**：s08 把 BashTool 抽成 **Terminal Backend 工厂**——同一个 `terminal` 工具，背后可以是本地 shell、Docker container、SSH、Modal lambda…… "tool 接口稳定、execution 环境替换" 的另一个例子。
