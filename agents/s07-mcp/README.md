# s07 · MCP integration

把 Model Context Protocol（JSON-RPC over stdio）服务器接到 s02 Registry：远端工具被本地化成 `mcp_<server>_<tool>`，loop 不知道自己在跨进程调用。同一个 binary 双模式（`-server` 跑 demo 服务器），自带端到端测试。

Wires Model Context Protocol (JSON-RPC over stdio) servers into the s02 Registry: remote tools are surfaced locally as `mcp_<server>_<tool>` and the loop has no idea the call crosses a process boundary. Same binary, two modes (`-server` runs the demo server) — fully self-contained end-to-end test.

## 运行 / Run

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s07-mcp

# 1) 跑测试（自己 build 自己当 server，端到端 JSON-RPC roundtrip）
go test -v ./...

# 2) 编译，然后让 agent 连一个 demo MCP server（同一 binary 的 -server 模式）
go build .
./s07 -v -mcp "echo=./s07 -server" "use mcp_echo_reverse to reverse the string 'hermes'"
```

## 文件 / Files

| File | Role |
|---|---|
| `mcp.go` | **本节核心**：JSON-RPC 类型 / `MCPClient` / `MCPTool` / `RegisterMCPTools` |
| `echo_server.go` | 同进程 demo MCP server，handles `initialize` + `tools/list` + `tools/call` |
| `registry.go` | s02 registry + 新增 `DeregisterToolset`（断连时清理一整组） |
| `mcp_test.go` | 3 个用例：端到端 stdio roundtrip / 注册 / 按 toolset 批量取消 |
| `main.go` | 双模式入口：`-server` 短路成 server；否则解析 `-mcp` flag、连接、跑 agent |
| 其他 | 复用 s02 形态 |

## 教学要点

- **MCP = JSON-RPC 2.0 over stdio**：每行一个 JSON 对象（请求/响应），`id` 关联 request/response，notification 是无 id 的事件
- **`mcp_<server>_<tool>` 命名约定**给 LLM 暴露："工具名告诉你它来自哪里"
- **`Generation` 计数器** (s02 埋的)在这里第一次有真用：MCP server 重连/换工具集时 bump，loop 重建 schemas
- **`DeregisterToolset` 模式**：断连时一次干掉整组 tools，避免逐个 deregister 的事务性问题
- **进程隔离**：MCP server 崩溃只影响它自己的工具集，agent 继续。这是和 plugin 系统的本质区别——plugin 跑在 agent 进程内

## Try It with the demo server

```bash
go build .
./s07 -v -mcp "echo=./s07 -server" "reverse the word hermes for me, use the mcp tool"
# stderr: [mcp] echo connected; 2 tools registered
# stderr: [registry] 4 tools (gen=4): [bash mcp_echo_echo mcp_echo_reverse read_file]
# turn 0: assistant calls mcp_echo_reverse with input='hermes'
# turn 0: result = 'semreh'
# turn 1: assistant: "The reverse is semreh."
```

完整讲解见 [`docs/zh/s07-mcp.md`](../../docs/zh/s07-mcp.md) / [`docs/en/s07-mcp.md`](../../docs/en/s07-mcp.md)。
