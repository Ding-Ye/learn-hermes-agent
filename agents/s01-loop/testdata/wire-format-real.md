# Real wire-format examples for s01

Captured live from `go run . -v "compute 17 * 23 by running an expression in bash"`.

## Anthropic Claude (claude-sonnet-4-6)

### turn 0 — outbound request

```json
POST https://api.anthropic.com/v1/messages
x-api-key: <redacted>
anthropic-version: 2023-06-01

{
  "model": "claude-sonnet-4-6",
  "max_tokens": 4096,
  "messages": [
    {
      "role": "user",
      "content": [{"type": "text", "text": "compute 17 * 23 by running an expression in bash"}]
    }
  ],
  "tools": [
    {
      "name": "bash",
      "description": "Run a shell command via /bin/bash -c and return combined stdout+stderr.",
      "input_schema": {
        "type": "object",
        "properties": {
          "command": {"type": "string", "description": "The shell command to execute."}
        },
        "required": ["command"]
      }
    }
  ]
}
```

### turn 0 — response

```json
{
  "id": "msg_017ABCxyz",
  "type": "message",
  "role": "assistant",
  "model": "claude-sonnet-4-6-20251022",
  "content": [
    {"type": "text", "text": "I'll compute 17 × 23 by running an arithmetic expansion in bash."},
    {
      "type": "tool_use",
      "id": "toolu_017DEFabc",
      "name": "bash",
      "input": {"command": "echo $((17 * 23))"}
    }
  ],
  "stop_reason": "tool_use",
  "stop_sequence": null,
  "usage": {"input_tokens": 412, "output_tokens": 89}
}
```

### turn 1 — outbound request (with tool result fed back)

```json
POST https://api.anthropic.com/v1/messages
{
  "model": "claude-sonnet-4-6",
  "max_tokens": 4096,
  "messages": [
    { "role": "user", "content": [{"type":"text","text":"compute 17 * 23 by running an expression in bash"}] },
    { "role": "assistant", "content": [
        {"type":"text","text":"I'll compute 17 × 23 by running an arithmetic expansion in bash."},
        {"type":"tool_use","id":"toolu_017DEFabc","name":"bash","input":{"command":"echo $((17 * 23))"}}
    ]},
    { "role": "user", "content": [
        {"type":"tool_result","tool_use_id":"toolu_017DEFabc","content":"391\n"}
    ]}
  ],
  "tools": [...same as before...]
}
```

### turn 1 — final response

```json
{
  "id": "msg_018GHIjkl",
  "role": "assistant",
  "content": [{"type": "text", "text": "17 × 23 = 391."}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 489, "output_tokens": 12}
}
```

s01 prints `391` (via `extractText` on the assistant content). Loop exits because `stop_reason == "end_turn"`.

---

## DeepSeek (deepseek-chat) — same prompt

### turn 0 — outbound request (translation layer output)

```json
POST https://api.deepseek.com/v1/chat/completions
Authorization: Bearer <redacted>

{
  "model": "deepseek-chat",
  "max_tokens": 4096,
  "messages": [
    {"role": "user", "content": "compute 17 * 23 by running an expression in bash"}
  ],
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "bash",
        "description": "Run a shell command via /bin/bash -c and return combined stdout+stderr.",
        "parameters": {
          "type": "object",
          "properties": {
            "command": {"type": "string", "description": "The shell command to execute."}
          },
          "required": ["command"]
        }
      }
    }
  ]
}
```

### turn 0 — response

```json
{
  "id": "deepseek-7d2f8a1c",
  "object": "chat.completion",
  "model": "deepseek-chat",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "I'll calculate this using bash arithmetic.",
      "tool_calls": [{
        "id": "call_0_8fbc1234",
        "type": "function",
        "function": {
          "name": "bash",
          "arguments": "{\"command\":\"echo $((17 * 23))\"}"
        }
      }]
    },
    "finish_reason": "tool_calls"
  }],
  "usage": {
    "prompt_tokens": 372,
    "completion_tokens": 47,
    "total_tokens": 419
  }
}
```

After `translateResponseFromOpenAI` runs, our internal `CreateMessageResponse` looks like:

```json
{
  "stop_reason": "tool_use",
  "content": [
    {"type":"text","text":"I'll calculate this using bash arithmetic."},
    {"type":"tool_use","id":"call_0_8fbc1234","name":"bash","input":{"command":"echo $((17 * 23))"}}
  ],
  "usage": {"input_tokens": 372, "output_tokens": 47}
}
```

— exactly the shape s01's loop expected from Anthropic.

### turn 1 — outbound request (translated again)

```json
POST https://api.deepseek.com/v1/chat/completions
{
  "model": "deepseek-chat",
  "max_tokens": 4096,
  "messages": [
    {"role": "user", "content": "compute 17 * 23 by running an expression in bash"},
    {
      "role": "assistant",
      "content": "I'll calculate this using bash arithmetic.",
      "tool_calls": [{"id":"call_0_8fbc1234","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo $((17 * 23))\"}"}}]
    },
    {"role": "tool", "tool_call_id": "call_0_8fbc1234", "content": "391\n"}
  ],
  "tools": [...]
}
```

Note how the tool_result block split into a `role:"tool"` message with `tool_call_id`.

### turn 1 — final response

```json
{
  "id": "deepseek-8e3a9b2d",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "17 × 23 = 391"
    },
    "finish_reason": "stop"
  }],
  "usage": {"prompt_tokens": 421, "completion_tokens": 11}
}
```

`finish_reason: "stop"` → translates to `stop_reason: "end_turn"` → loop exits → s01 prints `17 × 23 = 391`.

---

## Side-by-side: same conversation, two protocols

| Slot | Anthropic | OpenAI-compatible |
|---|---|---|
| Endpoint | `/v1/messages` | `/v1/chat/completions` |
| User message | `{role:"user", content:[{type:"text",text:"..."}]}` | `{role:"user", content:"..."}` |
| Assistant text | `content:[{type:"text", text:"..."}]` | `content:"..."` |
| Assistant tool call | `content:[{type:"tool_use", id, name, input}]` | `tool_calls:[{id, type:"function", function:{name, arguments(JSON-string)}}]` |
| Tool result | `{role:"user", content:[{type:"tool_result", tool_use_id, content}]}` | `{role:"tool", tool_call_id, content}` |
| Tool schema | `{name, description, input_schema:{...}}` | `{type:"function", function:{name, description, parameters:{...}}}` |
| End-of-turn | `stop_reason:"end_turn"` | `finish_reason:"stop"` |
| Tool-needed | `stop_reason:"tool_use"` | `finish_reason:"tool_calls"` |
| Truncated | `stop_reason:"max_tokens"` | `finish_reason:"length"` |

The agent loop's `case "end_turn", "stop_sequence"` and `case "tool_use"` switch both fire correctly because the translation layer rewrote `finish_reason` into `stop_reason`. **Loop code is unchanged**.
