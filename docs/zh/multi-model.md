---
title: "多模型接入指南"
slug: multi-model
est_read_min: 10
---

# 多模型接入指南

> 本课程的所有 session 都支持 **Anthropic 原生协议**和 **OpenAI-compatible 协议**两套接入方式。后者覆盖了几乎所有主流大模型 API：DeepSeek、Moonshot/Kimi、Qwen（通义千问）、Groq、OpenRouter、自托管 vLLM/SGLang，以及 OpenAI 自己。本指南讲清如何切换、底层翻译怎么做、以及每家模型的实战注意点。

---

## 设计：一份 loop，两套 wire format

我们的 agent loop **内部**始终用 Anthropic 风格的 `Message` / `ContentBlock`（含 `tool_use` / `tool_result` 块、`stop_reason` 字段）。这是因为：

1. Anthropic 的 block 模型表达能力更强（同一个 message 里能有 text + tool_use + image），方便上层直接处理多模态
2. s07 的 MCP 设计、s05 的 memory 接口都基于 Anthropic 协议写
3. 翻译只在 provider 边界做一次，loop / registry / plugin 系统全部不动

具体实现见 `agents/s01-loop/provider_openai.go`：

```ascii-anim frames=2
                  ┌──────────────────────────────────────┐
                  │  Anthropic-style internal types       │
                  │   Message{Role, Content[]ContentBlock}│
                  │   ContentBlock{type=text|tool_use|   │
                  │                tool_result, ...}     │
                  │   stop_reason: end_turn|tool_use|... │
                  └────────────┬─────────────────────────┘
                               │
                  ┌────────────┴───────────┐
                  ▼                        ▼
       ┌──────────────────┐    ┌──────────────────────┐
       │ AnthropicProvider│    │  OpenAIProvider       │
       │  (native)        │    │  + translation layer  │
       │                  │    │                       │
       │  POST /v1/       │    │  Anthropic→OpenAI:    │
       │   messages       │    │   - tool_use →        │
       │                  │    │     tool_calls        │
       │                  │    │   - tool_result →     │
       │                  │    │     role:"tool" msg   │
       │                  │    │   - input_schema →    │
       │                  │    │     parameters        │
       │                  │    │                       │
       │                  │    │  POST /v1/chat/       │
       │                  │    │   completions         │
       │                  │    │                       │
       │                  │    │  OpenAI→Anthropic:    │
       │                  │    │   - tool_calls →      │
       │                  │    │     tool_use blocks   │
       │                  │    │   - finish_reason:    │
       │                  │    │     stop→end_turn     │
       │                  │    │     tool_calls→tool_use│
       │                  │    │     length→max_tokens │
       └────────────────────┘    └──────────────────────┘
```

## 内置 Provider profile（s01）

s01 的 main.go 已经集成 `-provider` flag，**8 种 profile** 开箱即用：

| `-provider` | API endpoint | 默认 model | 需要的 env var |
|---|---|---|---|
| `anthropic` (默认) | api.anthropic.com | claude-sonnet-4-6 | `ANTHROPIC_API_KEY` |
| `openai` | api.openai.com/v1 | gpt-4o-mini | `OPENAI_API_KEY` |
| `deepseek` | api.deepseek.com/v1 | deepseek-chat | `DEEPSEEK_API_KEY` |
| `moonshot` | api.moonshot.cn/v1 | moonshot-v1-8k | `MOONSHOT_API_KEY` |
| `qwen` | dashscope.aliyuncs.com/compatible-mode/v1 | qwen-plus | `DASHSCOPE_API_KEY` |
| `groq` | api.groq.com/openai/v1 | llama-3.3-70b-versatile | `GROQ_API_KEY` |
| `openrouter` | openrouter.ai/api/v1 | openai/gpt-4o-mini | `OPENROUTER_API_KEY` |
| `local` | http://localhost:8000/v1 | local-model | `OPENAI_API_KEY` |

## 实战：用 DeepSeek 跑 s01

```bash
cd agents/s01-loop
export DEEPSEEK_API_KEY=sk-...

# 默认就是 deepseek-chat
go run . -provider deepseek -v "compute 17 * 23 by running an expression in bash"

# 切到 deepseek-reasoner（带思维链的更强模型）
go run . -provider deepseek -model deepseek-reasoner -v "..."
```

`-v` 会打印 wire format 细节：`provider=deepseek model=deepseek-chat url=https://api.deepseek.com/v1`。

## 实战：用 Qwen（通义千问）

阿里 DashScope 提供 OpenAI-compatible endpoint：

```bash
export DASHSCOPE_API_KEY=sk-...
go run . -provider qwen -v "你好，请用 bash 算 17 × 23"

# 切到长上下文版
go run . -provider qwen -model qwen-plus-latest "..."

# 用 qwen3 模型
go run . -provider qwen -model qwen-plus -v "..."
```

## 实战：自托管模型（vLLM / SGLang）

如果你在本地或私有云跑 vLLM / SGLang，它们都暴露 OpenAI-compatible endpoint：

```bash
# 假设你的 vLLM 在 :8000 跑 Llama-3.3-70b
export OPENAI_API_KEY=any-token-vllm-doesnt-check

go run . -provider local \
  -base-url http://your-server:8000/v1 \
  -model meta-llama/Llama-3.3-70B-Instruct-Turbo \
  -v "hello"
```

**注意**：自托管模型 tool calling 支持参差。Llama-3.x、Qwen3、DeepSeek 都支持；Gemma、Phi 一般不行。详见你部署的 inference 服务文档。

## 真实 wire format 对比

同一个 prompt（"compute 17 \* 23 with bash"）在 Anthropic 和 DeepSeek 的请求-响应循环：

### Anthropic 原生协议

**请求** `POST https://api.anthropic.com/v1/messages`：

```json
{
  "model": "claude-sonnet-4-6",
  "max_tokens": 4096,
  "messages": [
    { "role": "user",
      "content": [{"type": "text", "text": "compute 17 * 23 with bash"}] }
  ],
  "tools": [
    { "name": "bash",
      "description": "Run a shell command via /bin/bash -c ...",
      "input_schema": {
        "type": "object",
        "properties": {
          "command": {"type": "string", "description": "The shell command..."}
        },
        "required": ["command"]
      } }
  ]
}
```

**响应**：

```json
{
  "id": "msg_01ABC...",
  "model": "claude-sonnet-4-6",
  "role": "assistant",
  "content": [
    {"type": "text", "text": "I'll compute 17 * 23 using bash."},
    {"type": "tool_use",
     "id": "toolu_01XYZ...",
     "name": "bash",
     "input": {"command": "echo $((17 * 23))"}}
  ],
  "stop_reason": "tool_use",
  "usage": {"input_tokens": 412, "output_tokens": 89}
}
```

### DeepSeek (OpenAI-compatible) 协议

**请求** `POST https://api.deepseek.com/v1/chat/completions`（由翻译层产出）：

```json
{
  "model": "deepseek-chat",
  "max_tokens": 4096,
  "messages": [
    { "role": "user", "content": "compute 17 * 23 with bash" }
  ],
  "tools": [
    { "type": "function",
      "function": {
        "name": "bash",
        "description": "Run a shell command via /bin/bash -c ...",
        "parameters": {
          "type": "object",
          "properties": {
            "command": {"type": "string", "description": "The shell command..."}
          },
          "required": ["command"]
        }
      } }
  ]
}
```

**响应**：

```json
{
  "id": "12abc...",
  "model": "deepseek-chat",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "I'll compute 17 * 23 using bash.",
      "tool_calls": [{
        "id": "call_0_a1b2c3...",
        "type": "function",
        "function": {
          "name": "bash",
          "arguments": "{\"command\":\"echo $((17 * 23))\"}"
        }
      }]
    },
    "finish_reason": "tool_calls"
  }],
  "usage": {"prompt_tokens": 408, "completion_tokens": 86}
}
```

**翻译规则一一对应**：

| Anthropic block | OpenAI 等价物 |
|---|---|
| `messages[].content[]: {type:"text"}` | `messages[].content` (string) |
| `messages[].content[]: {type:"tool_use"}` | `messages[].tool_calls[]` 加 `role:"assistant"` 配 `content:null` |
| `messages[].content[]: {type:"tool_result"}` | `messages[].role:"tool"` 配 `tool_call_id` |
| `tools[]: {name, description, input_schema}` | `tools[]: {type:"function", function:{name, description, parameters}}` |
| `stop_reason:"end_turn"` | `finish_reason:"stop"` |
| `stop_reason:"tool_use"` | `finish_reason:"tool_calls"` |
| `stop_reason:"max_tokens"` | `finish_reason:"length"` |

翻译代码见 `provider_openai.go` 的 `translateRequestToOpenAI` 和 `translateResponseFromOpenAI`，有 10 个单测覆盖各种 corner case（含 DeepSeek 偶尔返回 content 是数组形式而非字符串的情况）。

## 给 s02-s10 添加多 provider 支持

s02-s10 默认还是 Anthropic-only（教学聚焦不同的机制），但 `provider_openai.go` 已就位。**1 行改造即可启用**：

```diff
  // 在每个 session 的 main.go 里：
- provider := NewAnthropicProvider(apiKey, *model)
+ provider := NewOpenAIProvider(
+     os.Getenv("DEEPSEEK_API_KEY"),
+     "https://api.deepseek.com/v1",
+     "deepseek-chat",
+ )
```

或者从 s01 复制完整的 `providerProfiles` map + `-provider` flag 解析逻辑。

## 选哪家：实战经验

| 场景 | 推荐 |
|---|---|
| 中文为主 + 国内网络 | DeepSeek（性价比极高）/ Qwen（生态完善） |
| 工具调用质量要求高 | Anthropic Claude / OpenAI gpt-4o |
| 推理任务（数学、代码） | DeepSeek-reasoner / o1 / Qwen-QwQ |
| 极速响应（小模型） | Groq + Llama 3.3 / Moonshot kimi-latest |
| 数据隐私 / 合规 | 自托管 vLLM + 私有模型 |
| 多模型对比 / 路由 | OpenRouter（一个 key，N 家模型） |
| 完全本地（无外网） | vLLM/SGLang + 7-13B 量化模型 |

## 已知坑点

1. **DeepSeek 偶尔在 content 字段返回数组而非字符串**——我们的翻译层 `contentToString` 处理了这种情况
2. **Qwen 的 max_tokens 跟 Anthropic 含义不一样**——qwen-plus 默认 6K，传太大可能直接报错
3. **Groq 的免费额度有 RPM 限制**——demo 跑得快但不能压测
4. **OpenRouter 不同模型路由可能不支持 tools**——切前看下文档
5. **本地 vLLM 的 tool calling 需要正确的 chat template**——某些版本默认 template 不支持 function calling，启动时加 `--enable-auto-tool-choice`

## 调试技巧

加 `-v` flag 后看 stderr 的 `[s01] provider=... model=... url=...` 行验证选对了。如果 wire format 有问题，最快方式是：

```bash
# 用 mitmproxy 或 charles 抓包
HTTP_PROXY=http://localhost:8080 go run . -provider deepseek "..."
```

或者直接 dump 翻译产物：在 `provider_openai.go` 的 `CreateMessage` 里临时加 `fmt.Println(string(body))` 看请求 JSON。

---

**接着读**：
- 回 [s01](./s01-minimum-loop.md) 看协议如何驱动 loop
- 看 [s02 Tool Registry](./s02-tool-registry.md) 理解为什么 tool schema 跨 provider 也得稳定（prompt cache 友好）
