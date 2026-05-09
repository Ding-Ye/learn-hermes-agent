---
title: "s01 · 最小 agent loop"
chapter: 1
slug: s01-minimum-loop
est_read_min: 10
---

# s01 · 最小 agent loop

> 教什么：用最少的代码画出 hermes / claude-code / 任何 agent harness 的共同骨架——一个 `Provider` 抽象、一个 `Tool` 抽象、一个由 `stop_reason` 驱动的 `while` 循环。

---

## Problem / 问题

如果你第一次写 agent，很容易写成这样：

```go
resp := llm.Call(prompt)
fmt.Println(resp.Text)
```

它工作，但 **不是** agent。它是一次问答。一旦你给模型一把工具（"你可以执行 bash"），模型回答里就会出现 *"我要执行 `ls -la`"* 这种东西——而不是真的执行。它没法执行，因为它只是文字。

agent 的本质不是 "更聪明的 LLM"，而是 **"LLM + 一个外部循环"**：循环负责把模型的工具意图变成真实动作，再把动作结果塞回模型，让下一次调用看到。

s01 要解决的问题：用最少的代码、最透明的形式，把这个循环的骨架立起来。

## Solution / 解决方案

agent loop 在概念上就三句话：

1. 把 `user prompt` 放进 `messages` 列表。
2. 反复调 LLM。每次调完看一眼 `stop_reason`：
   - 是 `end_turn` → 收工，返回 assistant 的文字。
   - 是 `tool_use` → 执行模型点名的工具，把结果作为 `tool_result` block **以 user 角色** 追加进 `messages`，再调一次 LLM。
3. 设个 `MaxTurns` 兜底，防止失控。

剩下的所有"agent 复杂性"（skills、memory、子 agent、cron……）都长在这个循环上，不会改变它的形状。

## How It Works / 工作原理

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

核心 30 行（节选自 [`agents/s01-loop/loop.go`](https://github.com/Ding-Ye/learn-hermes-agent/blob/main/agents/s01-loop/loop.go)）：

```go
for turn := 0; turn < l.MaxTurns; turn++ {
    resp, err := l.Provider.CreateMessage(ctx, CreateMessageRequest{
        Messages: messages,
        Tools:    schemas,
    })
    if err != nil { return "", err }

    // 1. assistant 这一轮总是要回到 messages 里 —— 哪怕里面是 tool_use
    messages = append(messages, Message{Role: "assistant", Content: resp.Content})

    switch resp.StopReason {
    case "end_turn", "stop_sequence":
        return extractText(resp.Content), nil
    case "tool_use":
        results, err := l.runTools(ctx, resp.Content, toolByName, turn)
        if err != nil { return "", err }
        // 2. tool_result 走 user 角色 —— 这是 Anthropic 协议的硬规定
        messages = append(messages, Message{Role: "user", Content: results})
    default:
        return "", fmt.Errorf("unexpected stop_reason %q", resp.StopReason)
    }
}
```

**三个非显然之处**：

1. **`tool_result` 必须以 `user` 角色追加**。直觉上"工具结果是 assistant 的"，但协议把它定义为"用户回话给 assistant 的反馈"。错了 API 会 400。
2. **assistant 含 `tool_use` 的那一轮 *必须* 进 messages**。删掉它再追加 `tool_result` 会让协议链断裂——`tool_result.tool_use_id` 找不到对应的 `tool_use`。
3. **`Provider` 是接口，不是 SDK 调用**。s01 用裸 `net/http` 是故意的——后续 session 你会看到 streaming、mock、其它模型接口都从这个 interface 进来，loop 不动一行。

## What Changed / 与上一节的变化

s01 是第 1 节，没有上一节。所以这里对比的是 **"一次 LLM 调用"**：

```diff
- // 一次性 Q&A
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

差距是质变——一次 Q&A 变成了能拿工具去做事的 agent。

## Try It / 动手试一试

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s01-loop

# 简单一句
go run . "what files are in this directory?"

# 看每一步（推荐第一次跑加 -v）
go run . -v "compute 17 * 23 by running an expression in bash"

# 换模型
go run . -model claude-haiku-4-5-20251001 "echo hello world"
```

期望输出形态（`-v` 模式）：

```
[turn 0] assistant: I'll use bash to compute 17 * 23.
[turn 0] -> bash map[command:echo $((17 * 23))]
[turn 0] <- 391
[turn 1] assistant: 17 * 23 = 391.
391
```

模型不是确定性的，每次输出会有差异——这本身是 s04（session 持久化）和 s_full（CI 录像回放）会重新讨论的话题。

## Upstream Source Reading / 上游源码阅读

hermes-agent 的等价 loop 在 `hermes_cli/main.py` 的 `_run_session` 附近。它复杂得多——包了 session 持久化、token 计数、`/interrupt` 处理、tool 错误恢复——但骨架是一样的。

```upstream:hermes_agent/loop.py#L1-L40
# 节选 + 简化自 hermes_cli/main.py 的 session 循环
async def _run_session(session, provider, tools):
    while True:
        # 1) 调用模型
        resp = await provider.create_message(
            session.messages,
            session.system_prompt,
            tools=tools,
        )

        # 2) 累计 token + 写盘
        session.record_usage(resp.usage)
        session.add_assistant(resp.content)
        await session.persist()

        # 3) stop_reason 派发
        if resp.stop_reason == "end_turn":
            return resp
        if resp.stop_reason == "tool_use":
            results = await execute_tool_calls(resp.content, tools)
            session.add_user_tool_results(results)
            continue

        # 4) hermes 把异常 stop_reason 当作可恢复事件而非 panic
        raise UnexpectedStop(resp.stop_reason)
```

**对照阅读要点**：

- **L4-L8**：和我们的 Go 版同形态——`messages` 列表 + provider 调用。`session.system_prompt` 是 hermes 多出的部分，s04 我们再加。
- **L11-L13**：hermes 每一轮都把 token 用量和消息**写盘**。我们的 mini 版还没有持久化——s04 会补上这块（`/resume`、`/branch`）。
- **L16-L21**：和我们一样靠 `stop_reason` 派发。`continue` 显式回循环开头。
- **L24**：hermes 用 `UnexpectedStop` 表达"模型返回了我不认识的 stop_reason"。这是一个 hermes 风格——大量异常被升格为可恢复事件，由上层 supervisor 决定重试还是放弃。我们的 mini 版直接 `error`，工程上不够好但教学上更直白。

**想读更多**：从 `hermes_cli/main.py` 的 `_run_session` 入手，跟着 `provider.create_message` 进 `agent/providers/anthropic.py`，再跟着 `execute_tool_calls` 进 `tools/registry.py`——这条线就是 s01 → s02（tool registry）→ s04（session 持久化）的真实代码地图。

---

**下一节预告**：s02 把"一个 bash 工具"扩展成 **统一的 tool registry**——后续 MCP 工具、skill 命令、子 agent 都从这个 registry 进来。
