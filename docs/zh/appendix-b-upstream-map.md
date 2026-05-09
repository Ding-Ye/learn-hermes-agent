---
title: "附录 B · 上游源码导读地图"
chapter: B
slug: appendix-b-upstream-map
est_read_min: 10
---

# 附录 B · 上游源码导读地图

> 这一节给你一份 [hermes-agent](https://github.com/NousResearch/hermes-agent) Python 仓库的完整地图——按 directory + key file + 哪节课对应解释，让你读完十节后能直接拿源码对照。

---

## 推荐阅读顺序

不是按字母序，是按**架构理解**的最佳路径：

1. `hermes_cli/main.py` — agent 入口（s01）
2. `agent/providers/anthropic.py` — Provider 抽象（s01）
3. `tools/registry.py` — Tool registry（s02）
4. `tools/skills_tool.py` + `agent/skill_preprocessing.py` — Skills（s03）
5. `hermes_state.py` — SessionDB（s04）
6. `agent/memory_provider.py` + `plugins/hermes_memory_fts5/` — Memory（s05）
7. `agent/plugin_manager.py` + `plugins/curator/` — Plugin + Curator（s06）
8. `tools/mcp_tool.py` — MCP（s07）
9. `tools/terminal_tool.py` — Terminal backends（s08）
10. `hermes_cli/kanban_db.py` + `hermes_cli/kanban.py` — Multi-process（s09）
11. `gateway/__init__.py` + `gateway/platforms/*.py` — Platform adapters（s10）
12. `environments/__init__.py` — Atropos integration（附录 A）

---

## 完整目录索引

### 顶层
- `README.md` — 项目介绍、安装、quickstart
- `pyproject.toml` — Python 包配置（依赖、scripts、构建配置）
- `LICENSE` — MIT
- `hermes_state.py` — SessionDB（**s04 重点阅读**）

### `hermes_cli/` — CLI 入口和命令
- `main.py` — 入口 + session loop（**s01/s04 重点**）
- `config.py` / `config_io.py` — 配置加载
- `commands.py` / `auth_commands.py` — 子命令分发
- `auth.py` / `copilot_auth.py` / `dingtalk_auth.py` — 各种登录流程
- `models.py` / `model_catalog.py` / `model_normalize.py` — 模型注册表
- `kanban.py` / `kanban_db.py` — Kanban DB（**s09 重点**）
- `kanban_diagnostics.py` / `kanban_specify.py` — kanban 诊断和 spec
- `checkpoints.py` — git-based filesystem snapshots（**注意**：和 s04 的 session 不同概念）
- `curator.py` — curator CLI 子命令（**s06 重点**）
- `skills_config.py` / `skills_hub.py` — skill 配置和 Skills Hub 集成
- `mcp_config.py` / `mcp_hub.py` — MCP 配置（**s07**）
- `tools_config.py` — 工具开关配置
- `plugins.py` / `plugins_cmd.py` — plugin 命令（**s06**）
- `cron.py` — cron 命令（**s09**）
- `voice.py` — 语音转录集成
- `webhook.py` — webhook 子命令（**s10**）
- `cli_output.py` / `curses_ui.py` / `colors.py` / `banner.py` — 输出 / TUI

### `agent/` — 核心 agent 实现
- `providers/anthropic.py` / `providers/openai.py` / `providers/openrouter.py` 等 — Provider 实现（**s01**）
- `memory_provider.py` — MemoryProvider ABC（**s05**）
- `memory_manager.py` — 多 provider orchestration（**s05**）
- `plugin_manager.py` — PluginManager（**s06**）
- `skill_preprocessing.py` — 模板替换 + inline shell（**s03**）

### `tools/` — 内置工具 + tool registry
- `registry.py` — ToolRegistry（**s02 重点**）
- `mcp_tool.py` — MCP client（**s07 重点**）
- `terminal_tool.py` — Environment factory（**s08 重点**）
- `terminal_tool/` 子目录（如果存在）— 各 backend 实现
- `skills_tool.py` — skill discovery + skill_view tool（**s03 重点**）
- `builtin/` — 各 builtin 工具（bash / read_file / web_fetch 等）

### `plugins/` — 插件
- `__init__.py` — manifest loader
- `curator/` — Curator plugin（**s06**）
- `hermes_memory_fts5/` — FTS5 memory provider（**s05**）
- 其它如 observability / scheduler / mcp 等

### `gateway/` — 多平台 gateway
- `__init__.py` — HTTP routing（**s10**）
- `platform_registry.py` — Platform 注册（**s10**）
- `platforms/telegram.py` / `discord.py` / `slack.py` / `whatsapp.py` / `signal.py`（**s10**）

### `environments/` — Atropos integration
- `__init__.py` — `HermesAgentBaseEnv`（**附录 A**）
- `coding/` / `skill_creation/` / `multi_turn_tool/` — 具体 environment

### `web/` — 可选 Web UI
- 不在我们课程范围

### `ui-tui/` — 可选 TUI
- 不在我们课程范围

### `docs/` — 用户面向文档
- `quickstart.md` / `architecture.md` / 等

### `tests/` — 测试套件
- 按目录组织：`tests/agent/` `tests/tools/` 等

---

## 与十节课的对照（速查表）

| 课程章节 | 上游 Python | 关键看哪几行 |
|---|---|---|
| s01 Agent loop | `hermes_cli/main.py` | `_run_session` 函数 |
| s01 Provider | `agent/providers/anthropic.py` | `AnthropicProvider.create_message` |
| s02 Registry | `tools/registry.py` | `ToolRegistry.register` + `_can_replace` |
| s03 Skills loader | `tools/skills_tool.py` | `_find_all_skills` + `iter_skill_index_files` |
| s03 Skill expansion | `agent/skill_preprocessing.py` | `preprocess_skill_content` |
| s04 SessionDB | `hermes_state.py` | `SessionDB.SCHEMA` + `_execute_write` |
| s04 Compression chain | `hermes_state.py` | `get_compression_tip` |
| s05 Memory ABC | `agent/memory_provider.py` | `MemoryProvider.prefetch/sync/queue_next` |
| s05 FTS5 impl | `plugins/hermes_memory_fts5/__init__.py` | `FTS5MemoryProvider.SCHEMA` |
| s05 3-phase orchestration | `agent/memory_manager.py` | `MemoryManager.on_turn_begin/end` |
| s06 Plugin bus | `agent/plugin_manager.py` | `PluginManager.dispatch` |
| s06 Curator | `plugins/curator/__init__.py` | `CuratorPlugin.on_session_start` |
| s06 Curator CLI | `hermes_cli/curator.py` | `_idle_days` |
| s07 MCP client | `tools/mcp_tool.py` | `MCPClient.call` + `_dispatch_notification` |
| s08 Terminal factory | `tools/terminal_tool.py` | `_create_environment` |
| s08 DockerEnv | `tools/terminal_tool.py` (DockerEnvironment 类) | `_run_subprocess` |
| s09 Kanban claim | `hermes_cli/kanban_db.py` | `KanbanDB.claim_next` |
| s09 Worker heartbeat | `hermes_cli/kanban.py` | `reap_dead_workers` |
| s10 Telegram | `gateway/platforms/telegram.py` | `parse_inbound` + `send_outbound` |
| s10 Discord | `gateway/platforms/discord.py` | ed25519 verification |
| 附录 A | `environments/__init__.py` | `HermesAgentBaseEnv` |

---

## 怎么 clone + 跑一次

```bash
git clone https://github.com/NousResearch/hermes-agent.git
cd hermes-agent

# 推荐用 uv 或 pdm 管理依赖
uv sync   # 或 pdm install

# 看 README 配 ANTHROPIC_API_KEY 等
cp .env.example .env
$EDITOR .env

# 跑起来
hermes  # 进入 TUI 或 cli mode
```

具体启动命令、配置项很多——以 hermes-agent README 为准。

## 学完之后的扩展练习

如果想深入：

1. **加一个新 platform**：照 s10 的 `Platform` interface，实现 SlackPlatform 或 WeChatPlatform
2. **加一个新 memory backend**：照 s05 的 `MemoryProvider` interface，写 PostgresMemory 或 RedisMemory
3. **加一个 MCP server**：写一个 stdio MCP server 暴露你常用的内部 API，agent 一接入就有了
4. **写一个 plugin**：跟 s06 的 `Plugin` interface，做一个 metrics plugin（每 turn 记 token 数到 prometheus）
5. **真接 Atropos 训练**：跑 hermes-agent 跑 100 个 coding task，trajectory 喂 Atropos coding env，DPO 一个小模型

---

**全课程结束**。回到 [README](../../README.md) 看 quickstart，或重读 [s01](./s01-minimum-loop.md) 加深印象。
