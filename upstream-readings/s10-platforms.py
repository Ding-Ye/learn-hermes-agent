# Upstream source reading for s10 · Gateway platform adapters
#
# Sources:
#   - NousResearch/hermes-agent · gateway/__init__.py
#   - NousResearch/hermes-agent · gateway/platform_registry.py
#   - NousResearch/hermes-agent · gateway/platforms/{telegram,discord,slack,whatsapp,signal}.py
# License: see https://github.com/NousResearch/hermes-agent/blob/main/LICENSE


import abc
import json
import asyncio
import nacl.signing  # used by Discord


# ============================================================================
# gateway/__init__.py  --  HTTP server + routing top
# ============================================================================

class Gateway:
    """One process, many platforms. Lives separately from the agent loop.

    Inbound flow:
       webhook → Platform.parse_inbound → kanban.enqueue
    Outbound flow:
       scheduler completes job → Platform.send_outbound

    Production also exposes:
       /healthz          health probe for k8s / systemd
       /metrics          Prometheus
       /admin/...        GET /admin/jobs, POST /admin/cron, etc. (auth-gated)
    """

    def __init__(self, kanban, registry: "PlatformRegistry"):
        self.kanban = kanban
        self.registry = registry

    async def handle_webhook(self, platform_name: str,
                              headers: dict, body: bytes) -> dict:
        """Per-platform handler — same shape across IM services."""
        platform = self.registry.route(platform_name)
        if platform is None:
            return {"status": 404, "error": "unknown platform"}
        inbound = await platform.parse_inbound(headers, body)
        if inbound is None:
            return {"status": 200}  # ack-drop, often a ping or unsupported type
        if inbound.get("_ping"):
            return platform.ping_ack()  # Discord type-1 ping requires PONG
        job_id = await self.kanban.enqueue(
            kind="session",
            source=platform.name,
            payload=inbound,
        )
        return {"status": 200, "job_id": job_id}


# ============================================================================
# gateway/platform_registry.py  --  same idea as our Go PlatformRegistry
# ============================================================================

class PlatformRegistry:
    def __init__(self):
        self._by_name: dict[str, Platform] = {}

    def register(self, p: "Platform"):
        if p.name in self._by_name:
            raise ValueError(f"platform {p.name} already registered")
        self._by_name[p.name] = p

    def route(self, name: str) -> "Platform | None":
        return self._by_name.get(name)

    def names(self) -> list[str]:
        return sorted(self._by_name)


# ============================================================================
# gateway/platforms/  --  the IM-specific adapters
# ============================================================================

class Platform(abc.ABC):
    """Production additions on top of our Go Platform interface:

      - signature verification (Discord ed25519, Slack signing secret,
        Telegram secret_token header)
      - presence/typing indicators
      - long-message splitting (Telegram 4096, Discord 2000)
      - reply quoting (reply_to message id)
      - rate-limit + back-off awareness
    """

    name: str

    @abc.abstractmethod
    async def parse_inbound(self, headers: dict, body: bytes) -> dict | None: ...

    @abc.abstractmethod
    async def send_outbound(self, channel_id: str, text: str, *,
                             reply_to: str | None = None,
                             typing_indicator: bool = False,
                             attachments: list | None = None) -> None: ...


class TelegramPlatform(Platform):
    name = "telegram"

    def __init__(self, bot_token: str, secret_token: str = ""):
        self._token = bot_token
        self._secret = secret_token  # optional X-Telegram-Bot-Api-Secret-Token

    async def parse_inbound(self, headers, body):
        if self._secret:
            if headers.get("X-Telegram-Bot-Api-Secret-Token") != self._secret:
                return None  # spam / forgery — ack-drop
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

    async def send_outbound(self, channel_id, text, *,
                             reply_to=None, typing_indicator=False, attachments=None):
        if typing_indicator:
            await self._sendChatAction(channel_id, "typing")
        # Split into 4096-char chunks if needed
        for chunk in _split_chunks(text, 4096):
            await self._sendMessage(channel_id, chunk, reply_to=reply_to)


class DiscordPlatform(Platform):
    name = "discord"

    def __init__(self, bot_token: str, application_public_key: str):
        self._token = bot_token
        self._verify_key = nacl.signing.VerifyKey(bytes.fromhex(application_public_key))

    async def parse_inbound(self, headers, body):
        # ★ Discord interactions endpoint REQUIRES ed25519 verification.
        # Without this, anyone with the URL can forge interactions.
        sig = headers.get("X-Signature-Ed25519")
        ts = headers.get("X-Signature-Timestamp")
        try:
            self._verify_key.verify(ts.encode() + body, bytes.fromhex(sig))
        except Exception:
            return None  # ack-drop forged webhook

        ix = json.loads(body)
        if ix.get("type") == 1:
            return {"_ping": True}  # Discord wants a PONG response

        # type 2 = application command (slash)
        opts = {o["name"]: o["value"] for o in ix["data"].get("options", [])}
        return {
            "channel_id": ix["channel_id"],
            "user_id":    f"{ix['member']['user']['username']}#{ix['member']['user']['id']}",
            "text":       opts.get("prompt", ""),
        }


# ============================================================================
# A reading map for going further
# ============================================================================
#
# - gateway/__init__.py            HTTP routes + signature verification
# - gateway/platform_registry.py   per-name dispatch
# - gateway/platforms/telegram.py  long-message split, sendChatAction typing
# - gateway/platforms/discord.py   ed25519 verification, slash commands,
#                                  follow-up vs immediate reply handling
# - gateway/platforms/slack.py     events API, signing secret, threading
# - gateway/platforms/whatsapp.py  WhatsApp Business Cloud API, templates
# - gateway/platforms/signal.py    end-to-end encrypted, signal-cli wrapper
#
# Sessions in this course that revisit these files:
#   s_full — the integration demo wires gateway+scheduler+platforms
#            with skills + memory + MCP all in flight
```
