# s06 · Plugin 系统 + Curator / Plugin system + Curator

`Plugin` interface + `PluginManager` 总线 + 两个示例 plugin（`LoggingPlugin` 和 `CuratorPlugin`）。**Curator 自动归档闲置 memories**——hermes "self-improving" 的真相。

`Plugin` interface + `PluginManager` bus + two example plugins (`LoggingPlugin` and `CuratorPlugin`). **Curator auto-archives stale memories** — the truth behind hermes's "self-improving" tagline.

## 运行 / Run

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s06-plugins-curator

# 默认 curator 阈值 168h（一周）；演示时缩到 1s
go run . -v -curator-stale-after 1s "remember my favorite color is blue"
sleep 2
go run . -v -curator-stale-after 1s "what's my favorite color?"
# 第二跑，curator 在 OnSessionStart 时把第一跑的 memory 归档了；
# memory_search 找不到了。

# 关掉 curator，对照实验
go run . -v -no-curator "what's my favorite color?"
```

需要 Go ≥ 1.21；引入 `modernc.org/sqlite`（pure-Go）。

## 测试 / Tests

```bash
go test -v ./...
```

5 个用例：plugin 顺序派发、单 plugin 出错不影响其他、memory `Touch`/`Archive`/搜索过滤 archived 行、search hit 自动 bump `last_activity_at`、curator 端到端归档闲置但保留新鲜。

## 文件 / Files

| File | Role |
|---|---|
| `plugin.go` | **本节核心**：`Plugin` interface / `Host` / `PluginManager` |
| `plugin_logging.go` | 最小 plugin 范式 |
| `plugin_curator.go` | **CuratorPlugin** — 归档闲置 memory |
| `memory.go` | Memory 加 `LastActivityAt` / `ArchivedAt` |
| `memory_sqlite.go` | schema 加 `last_activity_at` `archived_at` 列 + Touch / Archive / ListStale |
| `loop.go` | Loop 改为通过 `PluginManager` 派发 lifecycle |
| `main.go` | 装配：mem + registry + plugins + loop |
| 其他 | 复用 s05 |

## 教学要点

- **接口小，组合多**：Plugin 只 5 个方法，复杂行为靠"多个 plugin 各管一段"
- **Lifecycle 派发隔离故障**：`DispatchSessionStart` 容忍单 plugin 出错，继续派发——不让一个坏 plugin 拖垮 agent
- **Curator 是 "自改进" 的真相**：追踪 `last_activity_at`，N 时长后归档。**不是**自动生成新 memory
- **`Touch on search hit`** 把使用即续命：你越用一条 memory，curator 越不会动它

完整讲解见 [`docs/zh/s06-plugins-curator.md`](../../docs/zh/s06-plugins-curator.md) / [`docs/en/s06-plugins-curator.md`](../../docs/en/s06-plugins-curator.md)。
