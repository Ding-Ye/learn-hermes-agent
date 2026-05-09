---
title: "Appendix B · Upstream source-reading map"
chapter: B
slug: appendix-b-upstream-map
est_read_min: 10
---

# Appendix B · Upstream source-reading map

> Complete map of the [hermes-agent](https://github.com/NousResearch/hermes-agent) Python repo — by directory, by key file, and by which course chapter explains it. Use this after finishing the 10 chapters to walk the upstream source with a guide.

---

## Recommended reading order

Not alphabetical — the best path for **architectural understanding**:

1. `hermes_cli/main.py` — agent entry point (s01)
2. `agent/providers/anthropic.py` — Provider abstraction (s01)
3. `tools/registry.py` — Tool registry (s02)
4. `tools/skills_tool.py` + `agent/skill_preprocessing.py` — Skills (s03)
5. `hermes_state.py` — SessionDB (s04)
6. `agent/memory_provider.py` + `plugins/hermes_memory_fts5/` — Memory (s05)
7. `agent/plugin_manager.py` + `plugins/curator/` — Plugin + Curator (s06)
8. `tools/mcp_tool.py` — MCP (s07)
9. `tools/terminal_tool.py` — Terminal backends (s08)
10. `hermes_cli/kanban_db.py` + `hermes_cli/kanban.py` — Multi-process (s09)
11. `gateway/__init__.py` + `gateway/platforms/*.py` — Platform adapters (s10)
12. `environments/__init__.py` — Atropos integration (Appendix A)

---

## Full directory index

### Top level
- `README.md` — overview, install, quickstart
- `pyproject.toml` — Python package config (deps, scripts, build)
- `LICENSE` — MIT
- `hermes_state.py` — SessionDB (**read for s04**)

### `hermes_cli/` — CLI entry + commands
- `main.py` — entry + session loop (**read for s01/s04**)
- `config.py` / `config_io.py` — config loading
- `commands.py` / `auth_commands.py` — subcommand dispatch
- `auth.py` / `copilot_auth.py` / `dingtalk_auth.py` — auth flows
- `models.py` / `model_catalog.py` / `model_normalize.py` — model registry
- `kanban.py` / `kanban_db.py` — Kanban DB (**read for s09**)
- `kanban_diagnostics.py` / `kanban_specify.py` — diagnostics + spec
- `checkpoints.py` — git-based filesystem snapshots (**note**: different concept from s04 sessions)
- `curator.py` — curator CLI (**read for s06**)
- `skills_config.py` / `skills_hub.py` — skill config + Skills Hub
- `mcp_config.py` / `mcp_hub.py` — MCP config (**s07**)
- `tools_config.py` — tool toggles
- `plugins.py` / `plugins_cmd.py` — plugin commands (**s06**)
- `cron.py` — cron commands (**s09**)
- `voice.py` — voice transcription
- `webhook.py` — webhook subcommands (**s10**)
- `cli_output.py` / `curses_ui.py` / `colors.py` / `banner.py` — output / TUI

### `agent/` — core agent
- `providers/anthropic.py` / `openai.py` / `openrouter.py` — Provider impls (**s01**)
- `memory_provider.py` — MemoryProvider ABC (**s05**)
- `memory_manager.py` — multi-provider orchestration (**s05**)
- `plugin_manager.py` — PluginManager (**s06**)
- `skill_preprocessing.py` — template + inline-shell expansion (**s03**)

### `tools/` — built-ins + registry
- `registry.py` — ToolRegistry (**read for s02**)
- `mcp_tool.py` — MCP client (**read for s07**)
- `terminal_tool.py` — Environment factory (**read for s08**)
- `terminal_tool/` subdirs (if present) — each backend impl
- `skills_tool.py` — skill discovery + `skill_view` tool (**read for s03**)
- `builtin/` — bash / read_file / web_fetch / etc.

### `plugins/` — plugins
- `__init__.py` — manifest loader
- `curator/` — Curator plugin (**s06**)
- `hermes_memory_fts5/` — FTS5 memory provider (**s05**)
- others: observability / scheduler / mcp / ...

### `gateway/` — multi-platform gateway
- `__init__.py` — HTTP routing (**s10**)
- `platform_registry.py` — Platform registration (**s10**)
- `platforms/telegram.py` / `discord.py` / `slack.py` / `whatsapp.py` / `signal.py` (**s10**)

### `environments/` — Atropos integration
- `__init__.py` — `HermesAgentBaseEnv` (**Appendix A**)
- `coding/` / `skill_creation/` / `multi_turn_tool/` — concrete envs

### `web/` — optional Web UI
- Out of scope for this course

### `ui-tui/` — optional TUI
- Out of scope

### `docs/` — user-facing docs
- `quickstart.md` / `architecture.md` / etc.

### `tests/` — test suite
- Organised by directory: `tests/agent/`, `tests/tools/`, etc.

---

## Quick-lookup table: chapter → upstream

| Course chapter | Upstream Python | Lines to focus on |
|---|---|---|
| s01 Agent loop | `hermes_cli/main.py` | `_run_session` |
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
| s08 DockerEnv | `tools/terminal_tool.py` (DockerEnvironment) | `_run_subprocess` |
| s09 Kanban claim | `hermes_cli/kanban_db.py` | `KanbanDB.claim_next` |
| s09 Worker heartbeat | `hermes_cli/kanban.py` | `reap_dead_workers` |
| s10 Telegram | `gateway/platforms/telegram.py` | `parse_inbound` + `send_outbound` |
| s10 Discord | `gateway/platforms/discord.py` | ed25519 verification |
| Appendix A | `environments/__init__.py` | `HermesAgentBaseEnv` |

---

## How to clone and run

```bash
git clone https://github.com/NousResearch/hermes-agent.git
cd hermes-agent

# Recommended dep manager: uv or pdm
uv sync   # or: pdm install

# Configure ANTHROPIC_API_KEY etc per the README
cp .env.example .env
$EDITOR .env

# Run
hermes  # enters TUI or cli mode
```

Specific launch flags and config keys change over time — defer to the upstream README.

## Extension exercises

If you want to go deeper:

1. **Add a new platform**: implement the s10 `Platform` interface as `SlackPlatform` or `WeChatPlatform`.
2. **Add a new memory backend**: implement the s05 `MemoryProvider` interface as `PostgresMemory` or `RedisMemory`.
3. **Add an MCP server**: write a stdio MCP server exposing your internal APIs; the agent gets them on connect.
4. **Write a plugin**: follow the s06 `Plugin` interface and ship a metrics plugin (per-turn token count → Prometheus).
5. **Plug into Atropos training**: run hermes-agent through 100 coding tasks, feed trajectories to Atropos's coding env, run DPO on a small model.

---

**End of course.** Go back to the [README](../../README.md) for the quickstart, or reread [s01](./s01-minimum-loop.md) to consolidate.
