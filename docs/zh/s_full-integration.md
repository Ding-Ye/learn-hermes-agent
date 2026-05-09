---
title: "s_full · 端到端集成"
chapter: 11
slug: s_full-integration
est_read_min: 15
---

# s_full · 端到端集成

> 把 s01-s10 的所有机制拼起来。一个 cron job 每 5 分钟跑一次健康检查，把结果通过 Telegram 发回；一个用户从 Discord 发问题进来，agent 用 skills + memory + MCP 工具答复。十章的所有机制同时生效。这一节没有新代码——全部走的是前面已经构建好的接口。

---

## 全景架构图

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

## 一次完整请求的执行轨迹

跟着一条 Discord `/ask` 消息从 webhook 到回复，看每一步触发了哪节课的机制：

| Step | 谁干的 | 涉及哪节 | 做了什么 |
|---|---|---|---|
| 1 | Discord 服务器 | — | 用户输入 `/ask "remember my favorite color is blue"`，Discord POST 到 `/webhook/discord` |
| 2 | Gateway HTTP 路由 | s09 | 收到请求，按路径 `/webhook/discord` 路由 |
| 3 | DiscordPlatform.Inbound | s10 | 解析 interaction JSON，拿出 `channel_id="987"` `text="remember..."` |
| 4 | kanban.EnqueueJob("discord", ...) | s09 | 把 Inbound 编 JSON 写进 jobs 表，状态 `pending` |
| 5 | 200 OK 立即返回 | s09 | Gateway **不调 LLM**，让 Discord webhook 不超时 |
| 6 | Scheduler tick (~2s 后) | s09 | 轮询，看到 pending job |
| 7 | ClaimNextPending atomic | s09 | BEGIN IMMEDIATE → SELECT pending → UPDATE running |
| 8 | PluginManager.DispatchSessionStart | s06 | LoggingPlugin 打日志，CuratorPlugin 扫一下 stale memories 归档 |
| 9 | Provider.CreateMessage | s01 | 真调 Anthropic API |
| 10 | LLM 决定 tool_use: memory_save | s02→s05 | 通过 Registry.Get 找到 memory_save tool |
| 11 | MemorySaveTool.Execute → SQLite INSERT | s05 | memories 表加一行，FTS5 触发器同步索引 |
| 12 | Provider.CreateMessage 第二轮 | s01 | LLM 看到 tool_result，决定 end_turn |
| 13 | sess.UpdateAt + atomic save | s04 | 会话状态写盘 |
| 14 | PlatformAwareRunner.Outbound | s10 | 按 j.Source="discord" 找 DiscordPlatform，POST 到 channels/987/messages |
| 15 | kanban.FinishJob | s09 | jobs 表 status=done，result 字段填好 |
| 16 | PluginManager.DispatchSessionEnd | s06 | CuratorPlugin OnSessionEnd（noop） |

**16 步里 9 节课的代码都被触发**——这就是 hermes 的"看似简单的 IM bot"背后真实做的事。

## 一个 cron job 的完整生命周期

```ascii-anim frames=2
┌───────────────────────────────────────────────────────┐
│  Setup: ./s10 cron add "every 5m" "check disk space"  │
│                                                       │
│  Scheduler tick:                                      │
│    1) DueCronEntries(now) →  返回 entry#1             │
│    2) kanban.EnqueueJob("cron", "check disk space")   │
│    3) kanban.TouchCronEntry(1)                        │
│    4) ClaimNextPending → 拿到刚 enqueue 的 job        │
│    5) AgentRunner.Run                                  │
│         turn 0: assistant 决定 → bash{"command":"df -h"}│
│         turn 0 result: <df 输出>                       │
│         turn 1: assistant 文字总结                      │
│    6) FinishJob done                                   │
│    7) PlatformAwareRunner: source="cron"               │
│       cron 没注册成 Platform，所以 Outbound 路径里     │
│       registry.Get("cron") → false → skip。result 留    │
│       在 jobs 表里给 dashboard 看，不主动 push 给谁。  │
└───────────────────────────────────────────────────────┘
```

如果想让 cron 结果 push 到 Telegram，给 Platform 起个名字 `cron-telegram`，注册时让 `Outbound` 调 Telegram sendMessage。一行映射改造，**机制不变**。

## 真正跑起来 demo

s_full 没有专属 binary——直接用 s10 + s06 已经够了。把它们组合的 shell 脚本：

```bash
# build everything
cd /Users/yeding/learn-hermes-agent
go build -o /tmp/lha-s06 ./agents/s06-plugins-curator
go build -o /tmp/lha-s10 ./agents/s10-platforms

# Window A: gateway with platform adapters (dry-run)
/tmp/lha-s10 gateway -addr :7079 &

# Window B: scheduler (echo mode for demo, no API key needed)
/tmp/lha-s10 scheduler -interval 1s -echo &

# Window C: send some traffic
curl -X POST http://localhost:7079/webhook/telegram \
  -d '{"update_id":1,"message":{"from":{"id":1,"username":"alice"},
                                 "chat":{"id":99},"text":"hello"}}'
curl -X POST http://localhost:7079/webhook/discord \
  -d '{"type":2,"channel_id":"987","member":{"user":{"id":"2","username":"bob"}},
       "data":{"name":"ask","options":[{"name":"prompt","value":"hi"}]}}'

# Inspect
/tmp/lha-s10 jobs

# Memory + curator demo (s06 binary)
/tmp/lha-s06 -v -curator-stale-after 1s "remember my favorite color is blue"
sleep 2
/tmp/lha-s06 -v -curator-stale-after 1s "what's my favorite color?"
# stderr 应该看到 [plugin:curator] archived 1 stale memories

# 真跑：导出 ANTHROPIC_API_KEY、TG_BOT_TOKEN、DISCORD_BOT_TOKEN，去掉 -echo
```

## 哪些 hermes 特性我们 mini 没做

回顾整个课程，明确列出我们 mini **不做**的事——这些都在 hermes-agent 上游真实存在，只是教学上不必走那么深：

| 维度 | 我们 mini | 上游 hermes |
|---|---|---|
| **Skill 物理形态** | flat `<name>.md` | `<dir>/SKILL.md` + references/scripts/ |
| **Skill 注册** | 每 skill 一个 tool | 一个 `skill_view` tool（progressive disclosure） |
| **Memory backend** | 仅 SQLite FTS5 | + Postgres pgvector / Qdrant 等 |
| **3-phase prefetch** | 只 explicit search | Prefetch → Sync → Queue 异步 overlap |
| **Session 持久化** | JSON 文件 | SQLite + WAL + FTS5 messages 搜索 + compression chain |
| **Plugin 加载** | 编译时注册 | 运行时 manifest.toml dynamic load |
| **Curator** | 单 stale 阶段 | active / stale / archived 三 bucket，pin/rollback/prune CLI |
| **MCP transport** | 仅 stdio | + HTTP / SSE / OAuth refresh |
| **MCP refresh** | 不 listen notification | 真处理 `tools/list_changed` |
| **Terminal backend** | local + per-cmd Docker | + long-running Docker / SSH / Modal / Daytona / Vercel + container reaper |
| **Multi-process** | 单 scheduler | worker pool + heartbeats + reaper |
| **Cron** | `every <duration>` | 5-field cron via robfig |
| **Job DAG** | FIFO | priority + depends_on + retries |
| **Audit log** | result 直接进 jobs 行 | 独立 runs 表，每次执行留记录 |
| **Platform adapters** | Telegram + Discord | + Slack + WhatsApp + Signal + IMessage |
| **Platform 安全** | 无校验 | ed25519 / signing-secret / secret_token |
| **Long messages** | 截断 | 自动按平台限制拆分 |
| **Typing indicator** | 无 | "...thinking" 状态 |
| **Multi-tenant** | 单 bot | 一 gateway N 公司 N bot |
| **Atropos / RL** | 不实现 | 全套 trajectory generation + 训练 (附录 A) |

这些都是合理的 hermes 设计——要么跨过 production threshold（多租户、安全校验、retries），要么是规模化优化（progressive disclosure、prefetch overlap）。教学上跳过让代码规模可读；生产上必须补。

## 接下来读什么

- 附录 A：[Atropos / RL 心智模型](./appendix-a-atropos-rl.md)——hermes 怎么用产生的 agent trajectory 训练自己
- 附录 B：[上游源码导读地图](./appendix-b-upstream-map.md)——hermes-agent Python 仓库的完整 reading order
- 直接 clone 源码：`git clone https://github.com/NousResearch/hermes-agent.git`，对照本课程 10 节看每个 mechanism 的真实实现
