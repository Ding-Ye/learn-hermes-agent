# s01 · 最小 agent loop / Minimum agent loop

最小可用的 agent loop：一个 provider 抽象、一个 bash 工具、一个 stop_reason 驱动的循环。
The smallest agent loop that earns the name: one provider abstraction, one bash tool, one stop_reason-driven loop.

## 运行 / Run

```bash
cd agents/s01-loop

# 默认 Anthropic Claude
export ANTHROPIC_API_KEY=sk-ant-...
go run . "what files are in this directory?"
go run . -v "compute 17 * 23 by running an expression in bash"
go run . -model claude-haiku-4-5-20251001 "echo hello"

# 切换到 DeepSeek（OpenAI-compatible 翻译层自动处理）
export DEEPSEEK_API_KEY=sk-...
go run . -provider deepseek -v "compute 17 * 23 with bash"

# 切换到阿里通义 Qwen
export DASHSCOPE_API_KEY=sk-...
go run . -provider qwen -v "..."

# 自托管 vLLM/SGLang
go run . -provider local -base-url http://localhost:8000/v1 -model your-model "..."

# 跑单元测试（包含 10 个 wire-format 翻译用例）
go test -v ./...
```

8 个 provider profile 内置：anthropic / openai / deepseek / moonshot / qwen / groq / openrouter / local。详见 [`docs/zh/multi-model.md`](../../docs/zh/multi-model.md)。

需要 Go ≥ 1.21。s01 只用 stdlib（含 OpenAI-compat 翻译层），没有外部依赖。
Requires Go ≥ 1.21. s01 uses stdlib only — including the OpenAI-compatible translation layer.

## 文件结构 / Files

| File | Role |
|---|---|
| `main.go` | CLI 入口，`-provider` flag + 8 个 profile 派发 |
| `provider.go` | `Provider` 接口 + `AnthropicProvider`（裸 HTTP，原生协议）|
| `provider_openai.go` | `OpenAIProvider` + Anthropic↔OpenAI 双向 wire-format 翻译 |
| `provider_openai_test.go` | 10 个翻译单测（含 DeepSeek 数组 content 边界） |
| `tools.go` | `Tool` 接口 + `BashTool` |
| `loop.go` | `Loop.Run` —— `messages` → LLM → `stop_reason` 派发 |
| `testdata/expected.txt` | 一个示例 prompt 的预期输出形态 |
| `testdata/wire-format-real.md` | **真实** Anthropic + DeepSeek 双协议端到端 JSON 例子 |

## 教学要点 / What this teaches

- **agent loop 的形状**：`while stop_reason != "end_turn": call_llm; dispatch_tools`
- **协议层面看消息**：用裸 HTTP 而不是 SDK，让你看到 messages / content blocks / tool_use / tool_result 的真实 JSON
- **provider 抽象的价值**：后续 session 换 streaming、换 mock、换其他模型都不动 loop

完整讲解见 [`docs/zh/s01-minimum-loop.md`](../../docs/zh/s01-minimum-loop.md) / [`docs/en/s01-minimum-loop.md`](../../docs/en/s01-minimum-loop.md)。
