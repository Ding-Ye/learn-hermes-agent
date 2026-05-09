---
title: "s_full · End-to-end integration"
chapter: 11
slug: s_full-integration
est_read_min: 15
---

# s_full · End-to-end integration

> Wire s01–s10 together. A cron job runs a health check every 5 minutes and DMs the result to Telegram; a user asks something on Discord, the agent replies using skills + memory + MCP tools. Every mechanism active simultaneously. There's no new code in this chapter — everything flows through the interfaces s01–s10 already built.

---

## The whole-system diagram

```ascii-anim frames=2
┌──────────────────────────────────────────────────────────────────────┐
│  Inbound                                                             │
│   ┌────────────┐   ┌────────────┐   ┌────────────┐   ┌────────────┐  │
│   │  Telegram  │   │  Discord   │   │   Slack    │   │   CLI      │  │
│   │  webhook   │   │ interaction│   │  events    │   │  one-shot  │  │
│   └─────┬──────┘   └──────┬─────┘   └──────┬─────┘   └──────┬─────┘  │
│         │ POST /webhook/* │                │                │        │
│         └────────┬────────┴────────┬───────┘                │        │
│                  ▼                                          │        │
│          ┌──────────────────────────┐                      │        │
│          │       GATEWAY (s09+s10)  │                      │        │
│          │  Platform adapters →     │                      │        │
│          │  uniform Inbound →       │                      │        │
│          │  kanban.EnqueueJob       │                      │        │
│          └─────────────┬────────────┘                      │        │
│                        │                                    │        │
│   ┌────────────────────▼────────────────────┐              │        │
│   │   KANBAN  (SQLite + WAL)                │              │        │
│   │   jobs(id, source, payload, status,...) │◄─────────────┘        │
│   │   crontab(spec, prompt, last_fired_at)  │  cli direct mode      │
│   │   memories(content, last_activity_at,    │                       │
│   │            archived_at)                  │                       │
│   └────────────────────┬────────────────────┘                       │
│                        │ ClaimNextPending                            │
│                        ▼                                             │
│          ┌──────────────────────────┐                                │
│          │    SCHEDULER (s09)       │                                │
│          │  loop tick: due cron →   │                                │
│          │  enqueue; claim job →    │                                │
│          │  PlatformAwareRunner     │                                │
│          └─────────────┬────────────┘                                │
│                        │                                             │
│                        ▼                                             │
│          ┌──────────────────────────────────────┐                    │
│          │   AGENT LOOP  (s01)                  │                    │
│          │   Provider.CreateMessage(messages,   │                    │
│          │     tools=Registry.Definitions())    │                    │
│          │   stop_reason: tool_use → run; loop  │                    │
│          └─────────────┬────────────────────────┘                    │
│                        │                                             │
│                        ▼                                             │
│   ┌────────────────────────────────────────────────────────┐         │
│   │   REGISTRY  (s02 — toolset shadow protection)          │         │
│   │   builtin:    bash / read_file / terminal     (s08)    │         │
│   │   builtin:    memory_search / memory_save     (s05)    │         │
│   │   skill-greet      ... ◄── from skills/*.md   (s03)    │         │
│   │   skill-summarize  ...                                  │         │
│   │   mcp-github_*     ... ◄── from MCP server    (s07)    │         │
│   │   mcp-fs_*         ...                                  │         │
│   └─────────────────────────────────────────────────────────┘        │
│                                                                      │
│   PluginManager  (s06)                                               │
│     ├─ LoggingPlugin        ── observability                         │
│     ├─ CuratorPlugin        ── archive stale memories                │
│     └─ (others)                                                      │
│                                                                      │
│   Lifecycle hooks: OnSessionStart / OnSessionEnd                     │
│   fired by Loop → fanned out to plugins                              │
│                                                                      │
│  Outbound                                                            │
│   PlatformAwareRunner → Platform.Outbound(channel_id, result)        │
│      ├─ Telegram: sendMessage                                        │
│      ├─ Discord:  channel POST                                        │
│      └─ CLI:      stdout                                             │
└──────────────────────────────────────────────────────────────────────┘
```

## End-to-end trace of one Discord request

Follow a Discord `/ask` message from webhook to reply; see which chapter's mechanism each step exercises:

| Step | Actor | Chapter | What it does |
|---|---|---|---|
| 1 | Discord servers | — | User types `/ask "remember my favorite color is blue"`; Discord POSTs to `/webhook/discord` |
| 2 | Gateway HTTP router | s09 | Routes by path `/webhook/discord` |
| 3 | DiscordPlatform.Inbound | s10 | Parses interaction JSON, extracts `channel_id="987"`, `text="remember..."` |
| 4 | kanban.EnqueueJob("discord", ...) | s09 | JSON-encodes Inbound, inserts row, `status=pending` |
| 5 | 200 OK returned immediately | s09 | Gateway **does not call the LLM** — keeps the webhook from timing out |
| 6 | Scheduler tick (~2s later) | s09 | Polling sees the pending job |
| 7 | ClaimNextPending (atomic) | s09 | BEGIN IMMEDIATE → SELECT pending → UPDATE running |
| 8 | PluginManager.DispatchSessionStart | s06 | LoggingPlugin logs; CuratorPlugin scans for stale memories and archives |
| 9 | Provider.CreateMessage | s01 | Real Anthropic API call |
| 10 | LLM picks tool_use: memory_save | s02→s05 | Registry.Get → MemorySaveTool |
| 11 | MemorySaveTool.Execute → SQLite INSERT | s05 | New row in `memories`, FTS5 trigger maintains index |
| 12 | Provider.CreateMessage second turn | s01 | LLM sees tool_result, decides end_turn |
| 13 | sess.UpdatedAt + atomic save | s04 | Session state written to disk |
| 14 | PlatformAwareRunner.Outbound | s10 | Looks up DiscordPlatform by source, POSTs to `channels/987/messages` |
| 15 | kanban.FinishJob | s09 | Jobs table row status=done, result populated |
| 16 | PluginManager.DispatchSessionEnd | s06 | CuratorPlugin OnSessionEnd (noop in our mini) |

**16 steps touch 9 chapters of code.** That's what a "deceptively simple IM bot" actually does.

## Lifecycle of one cron job

```ascii-anim frames=2
┌───────────────────────────────────────────────────────┐
│  Setup: ./s10 cron add "every 5m" "check disk space"  │
│                                                       │
│  Scheduler tick:                                      │
│    1) DueCronEntries(now) → returns entry #1          │
│    2) kanban.EnqueueJob("cron", "check disk space")   │
│    3) kanban.TouchCronEntry(1)                        │
│    4) ClaimNextPending → picks up the just-enqueued   │
│    5) AgentRunner.Run                                  │
│         turn 0: assistant decides → bash{"df -h"}     │
│         turn 0 result: <df output>                    │
│         turn 1: assistant text summary                │
│    6) FinishJob done                                  │
│    7) PlatformAwareRunner: source="cron"              │
│       cron isn't registered as a Platform, so          │
│       registry.Get("cron") → false → skipped. The      │
│       result lives in the jobs table for the           │
│       dashboard to display, not pushed anywhere.       │
└───────────────────────────────────────────────────────┘
```

To push cron results to Telegram, register a Platform named `cron-telegram` whose Outbound calls Telegram sendMessage. One mapping change — the underlying mechanics don't move.

## A real demo (no new binary needed)

s_full has no dedicated binary; just script the existing s10 + s06 binaries together:

```bash
# Build the two binaries we'll demo with
cd /Users/yeding/learn-hermes-agent
go build -o /tmp/lha-s06 ./agents/s06-plugins-curator
go build -o /tmp/lha-s10 ./agents/s10-platforms

# Window A: gateway with platform adapters (dry-run; no real tokens)
/tmp/lha-s10 gateway -addr :7079 &

# Window B: scheduler (echo mode = no API key needed)
/tmp/lha-s10 scheduler -interval 1s -echo &

# Window C: send some traffic
curl -X POST http://localhost:7079/webhook/telegram \
  -d '{"update_id":1,"message":{"from":{"id":1,"username":"alice"},
                                 "chat":{"id":99},"text":"hello"}}'
curl -X POST http://localhost:7079/webhook/discord \
  -d '{"type":2,"channel_id":"987","member":{"user":{"id":"2","username":"bob"}},
       "data":{"name":"ask","options":[{"name":"prompt","value":"hi"}]}}'

# Inspect kanban
/tmp/lha-s10 jobs

# Memory + curator demo (s06 binary)
/tmp/lha-s06 -v -curator-stale-after 1s "remember my favorite color is blue"
sleep 2
/tmp/lha-s06 -v -curator-stale-after 1s "what's my favorite color?"
# stderr will show [plugin:curator] archived 1 stale memories

# Live mode: export ANTHROPIC_API_KEY, TG_BOT_TOKEN, DISCORD_BOT_TOKEN; drop -echo
```

## What our mini deliberately omits

For honesty, here is the comprehensive list of features hermes-agent ships and we don't:

| Dimension | Our mini | Upstream hermes |
|---|---|---|
| **Skill physical shape** | flat `<name>.md` | `<dir>/SKILL.md` + references/scripts/ |
| **Skill registration** | one tool per skill | single `skill_view` tool (progressive disclosure) |
| **Memory backend** | SQLite FTS5 only | + Postgres pgvector / Qdrant etc. |
| **3-phase prefetch** | explicit search only | Prefetch → Sync → Queue async overlap |
| **Session persistence** | JSON files | SQLite + WAL + FTS5 messages search + compression chain |
| **Plugin loading** | compile-time | runtime manifest.toml dynamic load |
| **Curator** | single stale stage | active/stale/archived three-bucket + pin/rollback/prune CLI |
| **MCP transport** | stdio only | + HTTP / SSE / OAuth refresh |
| **MCP refresh** | does not listen | actually handles `tools/list_changed` |
| **Terminal backend** | local + per-cmd Docker | + long-running Docker / SSH / Modal / Daytona / Vercel + container reaper |
| **Multi-process** | single scheduler | worker pool + heartbeats + reaper |
| **Cron** | `every <duration>` | 5-field cron via robfig |
| **Job DAG** | FIFO | priority + depends_on + retries |
| **Audit log** | result on jobs row | separate runs table per execution |
| **Platform adapters** | Telegram + Discord | + Slack + WhatsApp + Signal + IMessage |
| **Platform security** | no verification | ed25519 / signing secrets / secret_token |
| **Long messages** | truncated | auto-split per platform limit |
| **Typing indicator** | none | "...thinking" status |
| **Multi-tenant** | one bot | one gateway, many companies, many bots |
| **Atropos / RL** | not implemented | full trajectory generation + training (Appendix A) |

These are reasonable hermes design choices — they're either past production thresholds (multi-tenant, security, retries), or scale-time optimisations (progressive disclosure, prefetch overlap). Skipping them keeps the curriculum small and readable; production work needs them.

## What to read next

- Appendix A: [Atropos / RL mental model](./appendix-a-atropos-rl.md) — how hermes uses the agent trajectories it generates to train itself
- Appendix B: [Upstream source-reading map](./appendix-b-upstream-map.md) — full reading order through hermes-agent's Python source
- Direct: `git clone https://github.com/NousResearch/hermes-agent.git` and walk it alongside this course's 10 chapters to see each mechanism's real implementation.
