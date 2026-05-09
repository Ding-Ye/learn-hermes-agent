# s05 · Memory Provider + FTS5 / Memory Provider with FTS5

`MemoryProvider` 接口 + 一个 SQLite + FTS5 实现 + 两个 builtin 工具（`memory_search` / `memory_save`）。Agent 现在能跨 session 召回事实。

`MemoryProvider` interface + a SQLite + FTS5 implementation + two builtin tools (`memory_search` / `memory_save`). The agent can now recall facts across sessions.

## 运行 / Run

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s05-memory

# turn 1: 让 agent 记住一件事
go run . -v "remember that my favorite color is blue, save it as a memory tagged 'preference'"
# turn 2 (新 session, 默认共享同一个 memory.db): 让 agent 召回
go run . -v "what's my favorite color? search your memory"

# memory.db 默认在 ~/.learn-hermes-agent/memory.db；用 -memory-db 自定义
go run . -memory-db /tmp/test.db "remember that I prefer Postgres over MySQL"
```

需要 Go ≥ 1.21。引入了 `modernc.org/sqlite`（pure-Go，无 cgo）以提供 FTS5。

## 测试 / Tests

```bash
go test -v ./...
```

7 个用例覆盖：save+exact-word search、BM25 排序（"docker postgres" 命中含两个词的）、empty query 返回空、limit 生效、tags 往返、tool schema 形态、save_tool→search roundtrip。

## 文件 / Files

| File | Role |
|---|---|
| `main.go` | 装配 mem provider + 4 个 builtin 工具 + loop |
| `memory.go` | **本节核心**：`Memory` struct + `MemoryProvider` interface（接口比实现重要）|
| `memory_sqlite.go` | SQLite + FTS5 实现，带 INSERT/DELETE/UPDATE 触发器同步 fts5 表 |
| `memory_tool.go` | `MemorySearchTool` + `MemorySaveTool`（暴露给 LLM）|
| `loop.go` | 加 `OnSessionStart` / `OnSessionEnd` lifecycle 钩子 |
| `provider.go` / `tools.go` / `registry.go` / `session.go` | 复用 s04 |

## 教学要点

- **接口比实现重要**：`MemoryProvider` 让上层不知道下面是 SQLite、Postgres、还是向量数据库
- **FTS5 触发器**：`AFTER INSERT/DELETE/UPDATE ON memories` 自动同步外部内容 FTS5 表，应用代码不用关心
- **BM25 ranking**：FTS5 的 `rank` 字段（升序）是 BM25 分数；多词查询会优先返回包含全部词的行
- **3-phase lifecycle**：`OnSessionStart` / `OnSessionEnd` 是 hermes prefetch→sync→queue 模式的占位钩子；s05 留空，s06 plugin 系统会真正接管

完整讲解见 [`docs/zh/s05-memory.md`](../../docs/zh/s05-memory.md) / [`docs/en/s05-memory.md`](../../docs/en/s05-memory.md)。
