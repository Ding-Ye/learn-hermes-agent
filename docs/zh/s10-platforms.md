---
title: "s10 · Gateway 平台适配器"
chapter: 10
slug: s10-platforms
est_read_min: 12
---

# s10 · Gateway 平台适配器

> 教什么：在 s09 的 Gateway 上加 **平台 adapter**——Telegram 和 Discord 两个真实 webhook handler。每个 Platform 把自己的 wire format 翻译成统一 `Inbound`，写进同一个 Kanban；Scheduler 完成后通过 `PlatformAwareRunner` 把回复送回原平台。**到这里 hermes "from a single gateway, all platforms" 的故事完整了。**

---

## Problem / 问题

s09 的 Gateway 只有一个 `/msg` 端点，body 必须是固定的 `{source, prompt}` JSON。Telegram 不会按这格式 POST，Discord 也不会——它们各自有自己的 webhook payload 形态：

- **Telegram**：`{update_id, message: {from: {id, username}, chat: {id}, text}}`
- **Discord**：`{type:2, channel_id, member: {user: {id, username}}, data: {name: "ask", options: [{name: "prompt", value}]}}`
- **Slack**：完全不同的 events API + signed-secret 校验
- **WhatsApp / Signal**：又各自一套

如果让所有 IM 平台都用 `/msg`，每接一个新平台都要在用户那头写一个翻译层。**应该是 agent 这头干这事**。

## Solution / 解决方案

三件事：

1. **`Platform` interface**：`Name() / Inbound([]byte) / Outbound(channelID, text)`。每个 IM 平台实现一份。
2. **`/webhook/<name>` 路由**：Gateway 把 `/webhook/telegram` 路由到 `TelegramPlatform.Inbound`，把 `/webhook/discord` 路由到 `DiscordPlatform.Inbound`。各自解析、各自产 `Inbound`、统一写 kanban。
3. **`PlatformAwareRunner`**：Scheduler 完成 job 后，按 `j.Source` 找到原 platform，调它的 `Outbound(channel_id, result)` 把回复送回去。Telegram 来的回 Telegram，Discord 来的回 Discord——一个 jobs 表服务所有平台。

**Dry-run 模式**：bot token 留空时，`Outbound` 只打日志不真的调 API。让 demo 和 CI 测试都能跑而不需要真 token。

## How It Works / 工作原理

```ascii-anim frames=3
┌──────────────────────────────────────────────────────────────────┐
│  Telegram → POST /webhook/telegram   {"update_id":..., "message": ...}
│      │                                                           │
│      ▼                                                           │
│  TelegramPlatform.Inbound() → Inbound{ChannelID, UserID, Text}   │
│      │                                                           │
│      ▼                                                           │
│  kanban.EnqueueJob("telegram", Inbound-as-JSON)                  │
│                                                                  │
│  Discord → POST /webhook/discord     {"type":2, "channel_id":..., │
│                                       "data": {"options": ...}}  │
│      │                                                           │
│      ▼                                                           │
│  DiscordPlatform.Inbound() → Inbound{ChannelID, UserID, Text}    │
│      │                                                           │
│      ▼                                                           │
│  kanban.EnqueueJob("discord", Inbound-as-JSON)                   │
│                                                                  │
│  Scheduler claims job → PlatformAwareRunner.Run                  │
│    1) inbound := json.Unmarshal(j.Payload)                       │
│    2) result := AgentRunner.Run({Payload: inbound.Text})         │
│    3) platform := registry.Get(j.Source)                         │
│    4) platform.Outbound(ctx, inbound.ChannelID, result)          │
│       ├─ Telegram: POST .../sendMessage                          │
│       └─ Discord:  POST .../channels/{id}/messages               │
└──────────────────────────────────────────────────────────────────┘
```

`Platform` interface 核心 + 一个 adapter 的解析（节选自 [`platform_telegram.go`](https://github.com/Ding-Ye/learn-hermes-agent/blob/main/agents/s10-platforms/platform_telegram.go)）：

```go
type Platform interface {
    Name() string
    Inbound(rawBody []byte) (*Inbound, error)
    Outbound(ctx context.Context, channelID, text string) error
}

func (t *TelegramPlatform) Inbound(raw []byte) (*Inbound, error) {
    var update struct {
        Message *struct {
            From *struct{ ID int64; Username string }
            Chat *struct{ ID int64 }
            Text string
        }
    }
    json.Unmarshal(raw, &update)
    return &Inbound{
        ChannelID: strconv.FormatInt(update.Message.Chat.ID, 10),
        UserID:    fmt.Sprintf("%s#%d", update.Message.From.Username, update.Message.From.ID),
        Text:      update.Message.Text,
    }, nil
}
```

`PlatformAwareRunner` 把回复送回原平台：

```go
func (r *PlatformAwareRunner) Run(ctx context.Context, j Job) (string, error) {
    var inbound Inbound
    json.Unmarshal([]byte(j.Payload), &inbound)
    innerJob := j; innerJob.Payload = inbound.Text
    result, runErr := r.Inner.Run(ctx, innerJob)

    if platform, ok := r.Reg.Get(j.Source); ok && inbound.ChannelID != "" {
        _ = platform.Outbound(ctx, inbound.ChannelID, result)  // best-effort
    }
    return result, runErr
}
```

**四个非显然之处**：

1. **Inbound 整体存进 payload**：scheduler 不需要"我现在在哪个平台"的额外状态。所有上下文（channel_id、user_id、text）都在 payload JSON 里——把 scheduler 写成 stateless 工人。
2. **Outbound 失败 best-effort**：Telegram API 偶尔 5xx，但 jobs 表已经标 done。下游用户没收到不是 agent 的错误。production 应该把 outbound 失败也 retry，但不能让它把任务标 failed。
3. **Discord 用 interactions 而不是 message**：交互模型完全不同（slash command + signed secret）。我们简化没做 ed25519 校验——production 必须做，否则任何人能伪造 webhook。
4. **`yeding#42` 复合 user_id**：username 跨平台不唯一（Telegram 一个 yeding，Discord 一个 yeding 是不同人）。所以 `username#numeric_id` 形态。**source 字段进 jobs 已经隔离了平台**，但同一平台内部两人重名靠数字 ID。

## What Changed / 与 s09 的变化

s09 的 Gateway 一个端点；s10 长成多端点 + 平台路由：

```diff
  // s09: gateway.go
- mux.HandleFunc("/msg", g.handleMsg)
+ // s10: gateway.go
+ mux.HandleFunc("/webhook/", g.handleWebhook)   // /webhook/telegram, /webhook/discord, ...

+ // s10 new types
+ type Platform interface { Name; Inbound; Outbound }
+ type Inbound struct { ChannelID, UserID, Text }
+ type PlatformRegistry struct { platforms map[string]Platform }
+ type PlatformAwareRunner struct { Inner JobRunner; Reg *PlatformRegistry }
```

main.go 多两个 flag（`-tg-token` `-discord-token`），scheduler 多包一层 `PlatformAwareRunner` —— 否则跟 s09 一样。

## Try It / 动手试一试

```bash
cd agents/s10-platforms
go build .

# 干跑：不要 bot token
./s10 gateway -addr :7079 &
./s10 scheduler -interval 1s -echo &

# 模拟 Telegram
curl -X POST http://localhost:7079/webhook/telegram \
  -H 'Content-Type: application/json' \
  -d '{"update_id":1,"message":{"from":{"id":42,"username":"yeding"},"chat":{"id":99},"text":"hi"}}'

# 模拟 Discord
curl -X POST http://localhost:7079/webhook/discord \
  -H 'Content-Type: application/json' \
  -d '{"type":2,"channel_id":"987","member":{"user":{"id":"42","username":"yeding"}},
       "data":{"name":"ask","options":[{"name":"prompt","value":"hi"}]}}'

./s10 jobs
# stderr 里能看到 [telegram dry-run] / [discord dry-run] outbound 日志

# 真跑
export TG_BOT_TOKEN=... DISCORD_BOT_TOKEN=... ANTHROPIC_API_KEY=...
./s10 gateway -addr :7079 &
./s10 scheduler -interval 1s &

# 测试（含端到端：webhook → kanban → scheduler → dry-run reply）
go test -v ./...
```

## Upstream Source Reading / 上游源码阅读

hermes 的 platform 在 `gateway/platforms/` 下：`telegram.py` `discord.py` `slack.py` `whatsapp.py` `signal.py`，加上 `gateway/platform_registry.py` 做注册和路由。

```upstream:hermes_agent/gateway_platforms.py#L1-L70
# Excerpted + simplified from gateway/platforms/{telegram,discord}.py
# and gateway/platform_registry.py

import abc
import json
from typing import Any
import asyncio
import nacl.signing  # discord interactions verification


class Platform(abc.ABC):
    """Same shape as our Go Platform: parse inbound, render outbound.

    Production hermes adds:
      - signature verification (Discord ed25519, Slack signing secret)
      - rate-limit awareness (Telegram 30 msg/sec per chat)
      - multi-part message handling (long replies split, files attached)
      - presence updates (typing... while the agent thinks)
      - editing of the original message instead of follow-up reply
    """

    name: str

    @abc.abstractmethod
    async def parse_inbound(self, headers: dict, body: bytes) -> dict | None:
        """Returns the normalised inbound or None to ack-drop the webhook."""

    @abc.abstractmethod
    async def send_outbound(self, channel_id: str, text: str, *,
                             reply_to: str | None = None,
                             typing_indicator: bool = False) -> None: ...


class TelegramPlatform(Platform):
    name = "telegram"

    async def parse_inbound(self, headers, body):
        update = json.loads(body)
        msg = update.get("message")
        if not msg or not msg.get("text"):
            return None
        return {
            "channel_id": str(msg["chat"]["id"]),
            "user_id":    f"{msg['from'].get('username','')}#{msg['from']['id']}",
            "text":       msg["text"],
            "reply_to":   str(msg.get("message_id", "")),
        }


class DiscordPlatform(Platform):
    name = "discord"

    def __init__(self, public_key: str, *args, **kw):
        super().__init__(*args, **kw)
        self._verify_key = nacl.signing.VerifyKey(bytes.fromhex(public_key))

    async def parse_inbound(self, headers, body):
        # ★ Discord requires this — we MUST drop unsigned webhooks.
        sig = headers.get("X-Signature-Ed25519")
        ts = headers.get("X-Signature-Timestamp")
        try:
            self._verify_key.verify(ts.encode() + body, bytes.fromhex(sig))
        except Exception:
            return None  # signature failure -> ack but ignore

        ix = json.loads(body)
        if ix.get("type") == 1:
            return {"_ping": True}     # acknowledge ping
        # type 2 (slash) is what we care about
        opts = {o["name"]: o["value"] for o in ix["data"].get("options", [])}
        return {
            "channel_id": ix["channel_id"],
            "user_id":    f"{ix['member']['user']['username']}#{ix['member']['user']['id']}",
            "text":       opts.get("prompt", ""),
        }


class PlatformRegistry:
    def __init__(self):
        self._platforms: dict[str, Platform] = {}
    def register(self, p: Platform):
        self._platforms[p.name] = p
    def route(self, name: str) -> Platform | None:
        return self._platforms.get(name)
```

**对照阅读要点**：

- **签名校验**：Discord 强制 ed25519、Slack 用 signing secret、Telegram 可选 `secret_token` header。我们 mini 全跳过——production 必做，否则被 spam 灌爆。
- **Typing indicator**：上游 `send_outbound(..., typing_indicator=True)` 让用户在 IM 里看到"...对方正在输入"，agent 思考几秒不会显得卡死。我们 mini 没做，体验差但代码量减半。
- **多段 reply**：Telegram 4096 char 限制、Discord 2000 char 限制。上游自动拆分；我们 mini 把整段塞过去（短 reply OK，长 reply 会被截断）。
- **Reply-to**：上游记录 `reply_to: message_id`，回复时 quote 原消息。我们 mini 没存这个。
- **`whatsapp` / `signal`**：业务复杂度高（端到端加密、合规）；hermes 都包了 platform。看 `gateway/platforms/signal.py` 是个好的扩展练习。
- **Hub-and-spoke 部署**：上游有"一个 gateway 处理 N 个公司的 N 个 bot 实例"的多租户配置，每个 bot 一套 token。我们 mini 一组 token——单租户。

**想读更多**：从 `gateway/__init__.py` 看 HTTP 路由总入口，跟 `gateway/platform_registry.py` 看注册和发现，跟 `gateway/platforms/telegram.py` 看 typing indicator + 长消息拆分，跟 `gateway/platforms/discord.py` 看 ed25519 校验。

---

**下一节预告**：s_full 把全 10 章拼成一个完整 demo——一个 cron job 每分钟跑一次健康检查，结果通过 Telegram 发回；用户从 Discord 问问题，agent 用 skills + memory + MCP 工具答复。十章的所有机制同时生效。
