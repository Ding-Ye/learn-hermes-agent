---
title: "s10 · Gateway platform adapters"
chapter: 10
slug: s10-platforms
est_read_min: 12
---

# s10 · Gateway platform adapters

> What this teaches: grow s09's Gateway with **platform adapters** — real Telegram and Discord webhook handlers. Each Platform translates its native wire format into a uniform `Inbound`, writes to the shared Kanban; on completion, `PlatformAwareRunner` routes the reply back to the originating platform. **By the end of this chapter, hermes's "single gateway for all platforms" story is complete.**

---

## Problem

s09's Gateway had a single `/msg` endpoint that required a fixed `{source, prompt}` JSON body. Telegram doesn't post in that shape. Neither does Discord. Each platform has its own webhook payload:

- **Telegram**: `{update_id, message: {from: {id, username}, chat: {id}, text}}`
- **Discord**: `{type:2, channel_id, member: {user: {id, username}}, data: {name: "ask", options: [{name: "prompt", value}]}}`
- **Slack**: a completely different events API plus signed-secret verification
- **WhatsApp / Signal**: yet more shapes

Forcing every IM platform to POST to `/msg` would mean writing a translation layer on each user side. **The agent side should do this work.**

## Solution

Three pieces:

1. **`Platform` interface**: `Name() / Inbound([]byte) / Outbound(channelID, text)`. Implement once per IM service.
2. **`/webhook/<name>` routing**: Gateway routes `/webhook/telegram` to `TelegramPlatform.Inbound`, `/webhook/discord` to `DiscordPlatform.Inbound`. Each parses, each produces an `Inbound`, all write to the same Kanban.
3. **`PlatformAwareRunner`**: after the Scheduler completes a job, look up the originating platform via `j.Source` and call its `Outbound(channel_id, result)`. Telegram-sourced jobs reply to Telegram; Discord-sourced jobs reply to Discord. One jobs table serves all platforms.

**Dry-run mode**: when the bot token is empty, `Outbound` only logs instead of hitting the real API. Lets the demo and CI tests run without real tokens.

## How It Works

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

`Platform` interface + one adapter's parse (excerpt from [`platform_telegram.go`](https://github.com/Ding-Ye/learn-hermes-agent/blob/main/agents/s10-platforms/platform_telegram.go)):

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

`PlatformAwareRunner` routes the reply back:

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

**Four non-obvious points**:

1. **Inbound JSON in the payload**: the scheduler doesn't need extra state to know "which platform am I currently servicing". All context (channel_id, user_id, text) lives in the payload JSON — keeps the scheduler stateless.
2. **Outbound failure is best-effort**: Telegram API may 5xx, but the job is already marked `done`. Failing-to-deliver is not the same as agent failure. Production should retry outbound, but never re-mark the job as failed.
3. **Discord uses interactions, not messages**: the model is fundamentally different (slash command + signed secret). We skip the ed25519 verification — production must verify, otherwise anyone can forge webhooks.
4. **`yeding#42` composite user_id**: usernames aren't unique across platforms (different `yeding` on Telegram and Discord are different people). So `username#numeric_id`. **`source` field already isolates platforms**; the numeric id disambiguates within a platform.

## What Changed (vs. s09)

s09's gateway had one endpoint; s10 grows to multi-endpoint + platform routing:

```diff
  // s09: gateway.go
- mux.HandleFunc("/msg", g.handleMsg)
+ // s10: gateway.go
+ mux.HandleFunc("/webhook/", g.handleWebhook)   // /webhook/telegram, /webhook/discord, ...

+ // new types in s10
+ type Platform interface { Name; Inbound; Outbound }
+ type Inbound struct { ChannelID, UserID, Text }
+ type PlatformRegistry struct { platforms map[string]Platform }
+ type PlatformAwareRunner struct { Inner JobRunner; Reg *PlatformRegistry }
```

main.go gets two more flags (`-tg-token` `-discord-token`), the scheduler wraps the inner runner in `PlatformAwareRunner` — otherwise unchanged from s09.

## Try It

```bash
cd agents/s10-platforms
go build .

# Dry-run: no bot token needed
./s10 gateway -addr :7079 &
./s10 scheduler -interval 1s -echo &

# Simulate Telegram
curl -X POST http://localhost:7079/webhook/telegram \
  -H 'Content-Type: application/json' \
  -d '{"update_id":1,"message":{"from":{"id":42,"username":"yeding"},"chat":{"id":99},"text":"hi"}}'

# Simulate Discord
curl -X POST http://localhost:7079/webhook/discord \
  -H 'Content-Type: application/json' \
  -d '{"type":2,"channel_id":"987","member":{"user":{"id":"42","username":"yeding"}},
       "data":{"name":"ask","options":[{"name":"prompt","value":"hi"}]}}'

./s10 jobs
# stderr shows [telegram dry-run] and [discord dry-run] outbound lines

# Live mode
export TG_BOT_TOKEN=... DISCORD_BOT_TOKEN=... ANTHROPIC_API_KEY=...
./s10 gateway -addr :7079 &
./s10 scheduler -interval 1s &

# Tests (includes end-to-end webhook → kanban → scheduler → dry-run reply)
go test -v ./...
```

## Upstream Source Reading

hermes's platforms live under `gateway/platforms/`: `telegram.py` `discord.py` `slack.py` `whatsapp.py` `signal.py`, plus `gateway/platform_registry.py` for registration and routing.

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

**Reading notes**:

- **Signature verification**: Discord requires ed25519, Slack uses a signing secret, Telegram offers an optional `secret_token` header. Our mini skips all of these. Production must — otherwise spam will flood the gateway.
- **Typing indicator**: upstream's `send_outbound(..., typing_indicator=True)` makes the IM client display "..." while the agent is thinking, so a multi-second response doesn't look frozen. Our mini omits this — worse UX, half the code.
- **Long reply splitting**: Telegram caps at 4096 chars, Discord at 2000. Upstream auto-splits; our mini just sends the full string (short OK, long gets truncated by the platform).
- **`reply_to`**: upstream stores the original message id and quotes it on reply. Our mini doesn't.
- **`whatsapp` / `signal`**: high business complexity (end-to-end encryption, compliance); hermes wraps both. `gateway/platforms/signal.py` is a great extension exercise.
- **Hub-and-spoke deployment**: upstream supports a "one gateway, N companies, N bot instances" multi-tenant config — different tokens per bot. Our mini is single-tenant.

**Read further**: start at `gateway/__init__.py` for the top-level routing, follow `gateway/platform_registry.py` for registration and discovery, follow `gateway/platforms/telegram.py` for typing indicator + long-message splitting, and `gateway/platforms/discord.py` for ed25519 verification.

---

**Next**: s_full assembles all 10 chapters into a single working demo — a cron job runs a health check every minute and DMs the result via Telegram; a Discord user asks a question, the agent uses skills + memory + MCP tools to answer. Every mechanism active simultaneously.
