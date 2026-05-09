# s02 · Tool Registry

把 s01 的 `[]Tool` 切片升级成一个真正的 **`Registry`**：带 `Register / Deregister / Get / Definitions / Generation`，并用 toolset 标签防止 MCP/plugin 静默覆盖 builtin。

Upgrade s01's `[]Tool` slice into a real **`Registry`** with `Register / Deregister / Get / Definitions / Generation`, plus toolset labels that stop MCP/plugin from silently shadowing builtins.

## 运行 / Run

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s02-tool-registry

# 现在有 2 个工具，模型要选
go run . -v "show me the first 30 lines of agents/s02-tool-registry/main.go"
go run . -v "what's today's date in ISO 8601 format?"
go run . -v "count the number of Go files in this directory"
```

需要 Go ≥ 1.21。s02 仍只用 stdlib。
Go ≥ 1.21. Stdlib only.

## 测试 / Tests

```bash
go test ./...
```

`registry_test.go` 覆盖了 shadow 防护、mcp ↔ mcp 互换、idempotent 注册、`Definitions()` 顺序稳定、`Deregister` 计数器递增。

## 文件结构 / Files

| File | Role |
|---|---|
| `main.go` | CLI 入口，注册 2 个 builtin 工具 |
| `provider.go` | （从 s01 复制） |
| `tools.go` | `BashTool` + 新增 `ReadFileTool` |
| `registry.go` | **本节核心**：Registry / ToolEntry / canReplace |
| `loop.go` | s01 的 loop 改成读 Registry |
| `registry_test.go` | 单元测试 |

## 教学要点

- **toolset 是约定不是枚举**：`"builtin" / "mcp-X" / "skill-Y" / "plugin-Z"`，由 `canReplace` 解释。
- **shadow 规则**是注册表的核心存在意义——光是 `map[string]Tool` 不能防止恶意 MCP 偷换 `bash`。
- **`Generation` 计数器**为 s07 的 MCP 动态刷新铺路。
- **`Definitions` 必须 sorted**，否则 prompt cache 会因为顺序变化失效。

完整讲解见 [`docs/zh/s02-tool-registry.md`](../../docs/zh/s02-tool-registry.md) / [`docs/en/s02-tool-registry.md`](../../docs/en/s02-tool-registry.md)。
