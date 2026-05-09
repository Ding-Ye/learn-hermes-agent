# s01 · 最小 agent loop / Minimum agent loop

最小可用的 agent loop：一个 provider 抽象、一个 bash 工具、一个 stop_reason 驱动的循环。
The smallest agent loop that earns the name: one provider abstraction, one bash tool, one stop_reason-driven loop.

## 运行 / Run

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s01-loop

# 最简单
go run . "what files are in this directory?"

# 看每一步
go run . -v "compute 17 * 23 by running an expression in bash"

# 换模型
go run . -model claude-haiku-4-5-20251001 "echo hello"
```

需要 Go ≥ 1.21。s01 只用 stdlib，没有外部依赖。
Requires Go ≥ 1.21. s01 uses stdlib only — no external dependencies.

## 文件结构 / Files

| File | Role |
|---|---|
| `main.go` | CLI 入口，flag 解析，调 `Loop.Run` |
| `provider.go` | `Provider` 接口 + `AnthropicProvider`（裸 HTTP）|
| `tools.go` | `Tool` 接口 + `BashTool` |
| `loop.go` | `Loop.Run` —— `messages` → LLM → `stop_reason` 派发 |
| `testdata/expected.txt` | 一个示例 prompt 的预期输出形态 |

## 教学要点 / What this teaches

- **agent loop 的形状**：`while stop_reason != "end_turn": call_llm; dispatch_tools`
- **协议层面看消息**：用裸 HTTP 而不是 SDK，让你看到 messages / content blocks / tool_use / tool_result 的真实 JSON
- **provider 抽象的价值**：后续 session 换 streaming、换 mock、换其他模型都不动 loop

完整讲解见 [`docs/zh/s01-minimum-loop.md`](../../docs/zh/s01-minimum-loop.md) / [`docs/en/s01-minimum-loop.md`](../../docs/en/s01-minimum-loop.md)。
