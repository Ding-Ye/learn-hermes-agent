---
title: "Multi-model integration guide"
slug: multi-model
est_read_min: 10
---

# Multi-model integration guide

> Every session in this course supports both **Anthropic's native protocol** and the **OpenAI-compatible protocol**. The latter covers nearly every popular LLM API: DeepSeek, Moonshot/Kimi, Qwen (Tongyi), Groq, OpenRouter, self-hosted vLLM/SGLang, and OpenAI itself. This guide explains how to switch, how the translation layer works, and gotchas for each provider.

---

## Design: one loop, two wire formats

The agent loop's **internal** types are always Anthropic-style — `Message` / `ContentBlock` with `tool_use` / `tool_result` blocks, `stop_reason` field. Three reasons:

1. The Anthropic block model is more expressive (one message can contain text + tool_use + image), keeping multi-modal handling simple at the upper layers
2. s07's MCP design and s05's memory interface are written against the Anthropic protocol
3. Translation happens once at the provider boundary; loop / registry / plugin system never need to change

The implementation lives in `agents/s01-loop/provider_openai.go`:

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

## Built-in provider profiles (s01)

s01's main.go ships an integrated `-provider` flag with **8 profiles** out of the box:

| `-provider` | API endpoint | Default model | Required env var |
|---|---|---|---|
| `anthropic` (default) | api.anthropic.com | claude-sonnet-4-6 | `ANTHROPIC_API_KEY` |
| `openai` | api.openai.com/v1 | gpt-4o-mini | `OPENAI_API_KEY` |
| `deepseek` | api.deepseek.com/v1 | deepseek-chat | `DEEPSEEK_API_KEY` |
| `moonshot` | api.moonshot.cn/v1 | moonshot-v1-8k | `MOONSHOT_API_KEY` |
| `qwen` | dashscope.aliyuncs.com/compatible-mode/v1 | qwen-plus | `DASHSCOPE_API_KEY` |
| `groq` | api.groq.com/openai/v1 | llama-3.3-70b-versatile | `GROQ_API_KEY` |
| `openrouter` | openrouter.ai/api/v1 | openai/gpt-4o-mini | `OPENROUTER_API_KEY` |
| `local` | http://localhost:8000/v1 | local-model | `OPENAI_API_KEY` |

## Hands-on: run s01 against DeepSeek

```bash
cd agents/s01-loop
export DEEPSEEK_API_KEY=sk-...

# Default model is deepseek-chat
go run . -provider deepseek -v "compute 17 * 23 by running an expression in bash"

# Switch to deepseek-reasoner (the chain-of-thought heavy variant)
go run . -provider deepseek -model deepseek-reasoner -v "..."
```

The `-v` flag prints the wire format details: `provider=deepseek model=deepseek-chat url=https://api.deepseek.com/v1`.

## Hands-on: Qwen (Tongyi) via DashScope

Alibaba's DashScope provides an OpenAI-compatible endpoint:

```bash
export DASHSCOPE_API_KEY=sk-...
go run . -provider qwen -v "Hello, please run a bash command to compute 17 * 23"

# Long-context version
go run . -provider qwen -model qwen-plus-latest "..."

# Use qwen3
go run . -provider qwen -model qwen-plus -v "..."
```

## Hands-on: self-hosted (vLLM / SGLang)

If you run vLLM or SGLang on-prem, they expose OpenAI-compatible endpoints:

```bash
# Assume your vLLM is on :8000 hosting Llama-3.3-70b
export OPENAI_API_KEY=any-token-vllm-doesnt-check

go run . -provider local \
  -base-url http://your-server:8000/v1 \
  -model meta-llama/Llama-3.3-70B-Instruct-Turbo \
  -v "hello"
```

**Note**: tool-calling support across self-hosted models varies. Llama-3.x, Qwen3, DeepSeek work; Gemma and Phi typically don't. Check your inference server's docs.

## Real wire format comparison

Same prompt ("compute 17 \* 23 with bash") on Anthropic vs DeepSeek:

### Anthropic native protocol

**Request** `POST https://api.anthropic.com/v1/messages`:

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

**Response**:

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

### DeepSeek (OpenAI-compatible) protocol

**Request** `POST https://api.deepseek.com/v1/chat/completions` (produced by the translation layer):

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

**Response**:

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

**Translation rule one-to-one**:

| Anthropic block | OpenAI equivalent |
|---|---|
| `messages[].content[]: {type:"text"}` | `messages[].content` (string) |
| `messages[].content[]: {type:"tool_use"}` | `messages[].tool_calls[]` on `role:"assistant"` with `content:null` |
| `messages[].content[]: {type:"tool_result"}` | `messages[].role:"tool"` with `tool_call_id` |
| `tools[]: {name, description, input_schema}` | `tools[]: {type:"function", function:{name, description, parameters}}` |
| `stop_reason:"end_turn"` | `finish_reason:"stop"` |
| `stop_reason:"tool_use"` | `finish_reason:"tool_calls"` |
| `stop_reason:"max_tokens"` | `finish_reason:"length"` |

Translation code lives in `provider_openai.go` as `translateRequestToOpenAI` and `translateResponseFromOpenAI`, with 10 unit tests covering edge cases (including the case where DeepSeek occasionally returns `content` as an array of blocks instead of a plain string).

## Adding multi-provider to s02–s10

s02–s10 default to Anthropic only (to keep each chapter focused on its specific mechanism), but `provider_openai.go` is in place. **A 1-line tweak enables it**:

```diff
  // in each session's main.go:
- provider := NewAnthropicProvider(apiKey, *model)
+ provider := NewOpenAIProvider(
+     os.Getenv("DEEPSEEK_API_KEY"),
+     "https://api.deepseek.com/v1",
+     "deepseek-chat",
+ )
```

Or copy the full `providerProfiles` map + `-provider` flag handling from s01.

## Which one to pick: practical guidance

| Scenario | Recommended |
|---|---|
| Chinese-first + China network | DeepSeek (best price/performance) / Qwen (mature ecosystem) |
| High-quality tool calling | Anthropic Claude / OpenAI gpt-4o |
| Reasoning tasks (math, code) | DeepSeek-reasoner / o1 / Qwen-QwQ |
| Ultra-fast small models | Groq + Llama 3.3 / Moonshot kimi-latest |
| Privacy / compliance | Self-hosted vLLM + private model |
| Multi-model A/B / routing | OpenRouter (one key, many models) |
| Fully offline | vLLM/SGLang + 7-13B quantized |

## Known gotchas

1. **DeepSeek occasionally returns `content` as an array instead of a string** — our `contentToString` translator handles this.
2. **Qwen's `max_tokens` semantics differ** — qwen-plus defaults to 6K; passing too large a value can hard-fail.
3. **Groq's free tier has tight RPM limits** — demos run fast but you can't load-test.
4. **OpenRouter routes some models without tool support** — check the model's docs before switching.
5. **Local vLLM tool calling needs the right chat template** — some defaults don't enable function calling; start with `--enable-auto-tool-choice`.

## Debugging tips

Add `-v` and watch stderr for `[s01] provider=... model=... url=...` to verify selection. If the wire format misbehaves, fastest path is:

```bash
# Use mitmproxy or Charles
HTTP_PROXY=http://localhost:8080 go run . -provider deepseek "..."
```

Or dump translated requests directly: in `provider_openai.go`'s `CreateMessage`, temporarily add `fmt.Println(string(body))` to print the wire JSON.

---

**Read next**:
- Back to [s01](./s01-minimum-loop.md) to see the protocol drive the loop
- [s02 Tool Registry](./s02-tool-registry.md) to understand why tool schemas need to stay byte-stable across providers (prompt-cache friendliness)
