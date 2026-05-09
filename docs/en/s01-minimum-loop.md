---
title: "s01 · Minimum agent loop"
chapter: 1
slug: s01-minimum-loop
est_read_min: 10
---

# s01 · Minimum agent loop

> What this teaches: with the smallest amount of code, sketch the skeleton shared by hermes / claude-code / every agent harness — one `Provider` abstraction, one `Tool` abstraction, one `while` loop driven by `stop_reason`.

---

## Problem

If you write your first agent the obvious way, it looks like this:

```go
resp := llm.Call(prompt)
fmt.Println(resp.Text)
```

It works, but it is **not** an agent. It is one round of Q&A. The moment you hand the model a tool ("you can run bash"), the model's reply becomes things like *"I will run `ls -la`"* — text describing a tool call, not the tool actually running. The model can't run anything. It can only emit text.

The point of an agent is not "a smarter LLM" — it is **"an LLM plus an external loop"**. The loop turns the model's tool intent into real actions, then feeds the action results back into the model so the next call can see them.

s01 solves exactly this: stand up the loop's skeleton in the smallest, most transparent form possible.

## Solution

The agent loop is three sentences:

1. Put the user's prompt into a `messages` list.
2. Repeatedly call the LLM. After each call, look at `stop_reason`:
   - `end_turn` → done, return the assistant's text.
   - `tool_use` → execute the tools the model named, append the results as `tool_result` blocks **with role `user`**, call again.
3. Cap turns with `MaxTurns` so a runaway can't burn forever.

Every later agent feature (skills, memory, sub-agents, cron…) hangs off this loop. None of them changes its shape.

## How It Works

```ascii-anim frames=2
┌────────────────────────────────────────────────────────────┐
│  messages = [ user(prompt) ]                               │
│                                                            │
│   while turn < MaxTurns:                                   │
│       ┌─────────────────────────────────────┐              │
│       │  resp = provider.CreateMessage(...) │              │
│       └────────────────┬────────────────────┘              │
│                        │                                   │
│                        ▼                                   │
│         append assistant turn → messages                   │
│                        │                                   │
│             ┌──────────┴──────────┐                        │
│             ▼                     ▼                        │
│      stop_reason ==          stop_reason ==               │
│        "end_turn"              "tool_use"                  │
│             │                     │                        │
│             ▼                     ▼                        │
│      return text       run each tool_use,                  │
│                        append tool_result blocks           │
│                        as a user message → loop            │
└────────────────────────────────────────────────────────────┘
```

The 30-line core (excerpt from [`agents/s01-loop/loop.go`](https://github.com/Ding-Ye/learn-hermes-agent/blob/main/agents/s01-loop/loop.go)):

```go
for turn := 0; turn < l.MaxTurns; turn++ {
    resp, err := l.Provider.CreateMessage(ctx, CreateMessageRequest{
        Messages: messages,
        Tools:    schemas,
    })
    if err != nil { return "", err }

    // 1. The assistant turn ALWAYS goes back into messages — even when its
    //    content is tool_use blocks.
    messages = append(messages, Message{Role: "assistant", Content: resp.Content})

    switch resp.StopReason {
    case "end_turn", "stop_sequence":
        return extractText(resp.Content), nil
    case "tool_use":
        results, err := l.runTools(ctx, resp.Content, toolByName, turn)
        if err != nil { return "", err }
        // 2. tool_result is appended with role=user — wire-protocol rule.
        messages = append(messages, Message{Role: "user", Content: results})
    default:
        return "", fmt.Errorf("unexpected stop_reason %q", resp.StopReason)
    }
}
```

**Three non-obvious bits**:

1. **`tool_result` MUST have role `user`**. It feels like "tool results belong to the assistant" but the protocol defines them as "the user replying to the assistant with a tool's output". Get this wrong and the API returns 400.
2. **The assistant turn containing `tool_use` MUST be appended to `messages`**. Skipping it breaks the chain — the next `tool_result.tool_use_id` will reference an id that isn't in history.
3. **`Provider` is an interface, not an SDK wrapper**. s01 uses raw `net/http` on purpose — later sessions slot streaming, mocks, other models, all behind the same interface, without touching the loop.

## What Changed

s01 is chapter 1, so the comparison is against **a single Q&A call**:

```diff
- // single-turn Q&A
- resp := llm.Call(prompt)
- return resp.Text
+ // agent loop
+ for turn := 0; turn < MaxTurns; turn++ {
+     resp := provider.CreateMessage(messages, tools)
+     messages = append(messages, assistant(resp))
+     if resp.StopReason == "end_turn" { return text(resp) }
+     if resp.StopReason == "tool_use" {
+         messages = append(messages, user(runTools(resp)))
+     }
+ }
```

The gap is qualitative — one round of Q&A becomes an agent that can actually use tools to *do* things.

## Try It

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s01-loop

# Simplest run
go run . "what files are in this directory?"

# Trace every step (recommended on first run)
go run . -v "compute 17 * 23 by running an expression in bash"

# Different model
go run . -model claude-haiku-4-5-20251001 "echo hello world"
```

Expected shape with `-v`:

```
[turn 0] assistant: I'll use bash to compute 17 * 23.
[turn 0] -> bash map[command:echo $((17 * 23))]
[turn 0] <- 391
[turn 1] assistant: 17 * 23 = 391.
391
```

LLMs aren't deterministic — exact wording will vary. Pinning down outputs is the topic of s04 (session persistence) and s_full (CI replay fixtures).

## Upstream Source Reading

The equivalent loop in hermes-agent lives near `_run_session` in `hermes_cli/main.py`. It is much more complex — session persistence, token accounting, `/interrupt` handling, tool error recovery — but the bones are the same.

```upstream:hermes_agent/loop.py#L1-L40
# Excerpted + simplified from hermes_cli/main.py's session loop
async def _run_session(session, provider, tools):
    while True:
        # 1) Call the model
        resp = await provider.create_message(
            session.messages,
            session.system_prompt,
            tools=tools,
        )

        # 2) Token accounting + persistence
        session.record_usage(resp.usage)
        session.add_assistant(resp.content)
        await session.persist()

        # 3) Dispatch on stop_reason
        if resp.stop_reason == "end_turn":
            return resp
        if resp.stop_reason == "tool_use":
            results = await execute_tool_calls(resp.content, tools)
            session.add_user_tool_results(results)
            continue

        # 4) hermes treats unknown stop_reason as recoverable, not fatal
        raise UnexpectedStop(resp.stop_reason)
```

**Reading notes**:

- **L4–L8**: Same shape as our Go version — `messages` + provider call. `session.system_prompt` is hermes-only; we add it in s04.
- **L11–L13**: hermes persists token usage and messages **every turn**. Our mini still has no persistence — s04 introduces `/resume` and `/branch`.
- **L16–L21**: Same `stop_reason` dispatch. The `continue` is explicit "go back to the top".
- **L24**: hermes raises `UnexpectedStop` to mean "the model returned a stop_reason I don't recognize". This is a hermes idiom — most exceptions are *recoverable events* that a supervisor can retry/abort, not panics. Our mini errors out directly: less robust in production, but more legible for teaching.

**Read further**: start at `_run_session` in `hermes_cli/main.py`, follow `provider.create_message` into `agent/providers/anthropic.py`, then `execute_tool_calls` into `tools/registry.py`. That trace is the real-source map of s01 → s02 (tool registry) → s04 (session persistence).

---

**Next**: s02 turns "one bash tool" into a **unified tool registry** — the same registry that later admits MCP tools, skill commands, and sub-agents.
