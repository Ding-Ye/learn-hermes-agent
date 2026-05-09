---
title: "s02 · Tool Registry"
chapter: 2
slug: s02-tool-registry
est_read_min: 12
---

# s02 · Tool Registry

> 教什么：把 s01 的 `[]Tool` 切片升级成 **`Registry`**——一个带类型的工具命名空间，能在运行期增删、防止 MCP/plugin 静默覆盖 builtin、并把 `Definitions` 输出按名字排序保稳。这是 hermes-agent 把所有工具来源（builtin / MCP / skill / plugin）塞进同一个名字空间的核心设计。

---

## Problem / 问题

s01 的 loop 拿了一个 `[]Tool` 切片，编译期就锁死。这在两节课的玩具里没问题，但你想往真 agent 里加东西时，立刻就不够：

- **运行期才知道有什么工具**。MCP 服务器是 agent 启动后再连上的（s07），skill 是用户随时拷一个新的 `.md` 进 `skills/` 就生效（s03）。`[]Tool` 装不下"启动后才出现"的工具。
- **同名怎么办？** 一个恶意的 MCP server 把它的工具叫 `bash`，跟你的 builtin `bash` 冲突——你期望谁赢？默默用后注册的会出大事。
- **schema 顺序不稳**。`map` 迭代是随机的，每次发给模型的 `tools` 数组顺序不一样，**直接打爆 prompt cache**。
- **要查"我现在有什么工具"**。给用户列、给监控打点、给 `_generation` 做缓存键……都要一个统一入口。

`map[string]Tool` 解决前一半，但解决不了 shadow 防护和 generation 追踪。所以本节做一个真正的注册表。

## Solution / 解决方案

引入 **`Registry`**，三件事：

1. **每个 tool 携带一个 toolset 标签**：`"builtin"` / `"mcp-<server>"` / `"skill-<name>"` / `"plugin-<name>"`。这个标签是约定，不是枚举，由 `canReplace` 解释。
2. **`Register(t, toolset)` 检查 shadow 规则**：builtin 不可被覆盖；mcp 之间允许互换；同 toolset 重注册是 idempotent；其它跨 toolset 一律拒绝。
3. **`Generation` 单调计数器**：每次成功的 register/deregister 都 +1。`Definitions()` 总是返回排序后的 schema，`loop` 想做缓存就以 `(generation, schemas)` 当 key。

这一层抽象长出去就是 hermes 的 `tools/registry.py`：所有工具来源（包括 s07 的 MCP 动态刷新）都从这一个接口进来，loop 不需要知道某个工具是 builtin 还是 mcp。

## How It Works / 工作原理

```ascii-anim frames=2
┌──────────────────────────────────────────────────────────┐
│   builtin: BashTool, ReadFileTool                        │
│   skill:   greet, summarize          (s03)               │
│   mcp:     mcp-github_search, ...    (s07)               │
│         │                                                │
│         ▼                                                │
│  ┌──────────────────────────────────────────┐            │
│  │   Registry                               │            │
│  │  ┌──────────────────────────────────┐   │            │
│  │  │ tools:  map[name]ToolEntry       │   │            │
│  │  │   ToolEntry{Tool, Toolset, Gen} │   │            │
│  │  │ generation: int (monotonic)     │   │            │
│  │  └──────────────────────────────────┘   │            │
│  │   Register / Deregister  ──► canReplace │            │
│  │   Get / Definitions / Names              │            │
│  └──────────────────────────────────────────┘            │
│         │                                                │
│         ▼                                                │
│   Loop.Run: schemas = registry.Definitions()             │
│             ... resp = provider.CreateMessage(...)       │
│             registry.Get(toolUse.Name) → execute         │
└──────────────────────────────────────────────────────────┘
```

核心 30 行（节选自 [`agents/s02-tool-registry/registry.go`](https://github.com/Ding-Ye/learn-hermes-agent/blob/main/agents/s02-tool-registry/registry.go)）：

```go
func (r *Registry) Register(t Tool, toolset string) error {
    if t == nil || t.Schema().Name == "" { return errInvalid }
    name := t.Schema().Name
    r.mu.Lock(); defer r.mu.Unlock()
    if existing, ok := r.tools[name]; ok {
        if err := canReplace(existing.Toolset, toolset); err != nil {
            return fmt.Errorf("Register %q: %w", name, err)
        }
    }
    r.generation++
    r.tools[name] = ToolEntry{Tool: t, Toolset: toolset, Generation: r.generation}
    return nil
}

func canReplace(existing, incoming string) error {
    if existing == incoming                                 { return nil }   // idempotent
    if existing == ToolsetBuiltin                           { return errShadowBuiltin }
    if incoming == ToolsetBuiltin                           { return errBuiltinReplace }
    if strings.HasPrefix(existing, "mcp-") &&
       strings.HasPrefix(incoming, "mcp-")                  { return nil }   // mcp ↔ mcp ok
    return errCrossToolset
}
```

**三个非显然之处**：

1. **`canReplace` 的"mcp-* ↔ mcp-*"放行是认真的**——多个 MCP server 各自维护自己的 `search` 是合法状态，agent 切换 server 时新 register 必须能盖掉旧的。
2. **`Definitions()` 必须 sorted**。Anthropic 的 prompt cache 看 `tools` 数组逐字节匹配，map 顺序随机会让连续几个 turn 各自付一遍 cache miss。
3. **`Generation` 计数器**这一节没用上——loop 每 turn 都重建 schemas。但 `Generation` 已经在 `ToolEntry` 里记住了"我是第几代注册的"，s07 引入 MCP 动态刷新时直接拿来做缓存 key。提前埋好，省后期重构。

## What Changed / 与 s01 的变化

```diff
  type Loop struct {
      Provider Provider
-     Tools    []Tool
+     Registry *Registry
      MaxTurns int
      Verbose  bool
  }

  func (l *Loop) Run(ctx context.Context, userPrompt string) (string, error) {
-     toolByName := map[string]Tool{}
-     schemas    := make([]ToolSchema, 0, len(l.Tools))
-     for _, t := range l.Tools {
-         s := t.Schema(); toolByName[s.Name] = t
-         schemas = append(schemas, s)
-     }
      ...
      for turn := 0; turn < l.MaxTurns; turn++ {
+         schemas := l.Registry.Definitions()
          resp, _ := l.Provider.CreateMessage(...)
          ...
-         tool, ok := toolByName[block.Name]
+         tool, ok := l.Registry.Get(block.Name)
      }
  }
```

机械变化是一行一行的替换，**语义变化**是工具集从"启动时一次性给死"变成"运行期可以增删、可以拒绝、可以追踪世代"。后续每一节加新工具来源，都从 `Registry.Register` 进来。

## Try It / 动手试一试

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s02-tool-registry

# 让模型在 bash 和 read_file 之间选
go run . -v "show me the first 30 lines of agents/s02-tool-registry/main.go"
go run . -v "what's today's date in ISO 8601 format?"
go run . -v "count the number of Go files in this directory"

# 跑测试看 shadow 规则
go test -v ./...
```

期望输出形态：

```
[registry] 2 tools: [bash read_file] (gen=2)
[turn 0] assistant: I'll read the file.
[turn 0] -> read_file map[path:agents/s02-tool-registry/main.go]
[turn 0] <- package main  import ( ...
[turn 1] assistant: Here are the first 30 lines: ...
```

## Upstream Source Reading / 上游源码阅读

hermes-agent 的注册表在 `tools/registry.py`，约 650 行。我们这版是它的核心骨架——`_tools` / `_generation` / shadow 防护都对得上，没做的是 `_toolset_aliases`、async 派发、deregister-then-register 的事务化（s07 MCP refresh 会用到）。

```upstream:hermes_agent/registry.py#L1-L60
class ToolEntry(NamedTuple):
    handler: Callable
    schema: dict
    toolset: str
    generation: int

class ToolRegistry:
    """All tool sources (builtin, plugin, MCP, skill) share this registry.
    The `toolset` field on each entry drives the shadow protection rules.
    """

    def __init__(self):
        self._tools: dict[str, ToolEntry] = {}
        self._toolset_checks: dict[str, Callable] = {}
        self._toolset_aliases: dict[str, str] = {}
        self._lock = threading.RLock()
        self._generation = 0

    def register(self, name, handler, schema, toolset):
        with self._lock:
            existing = self._tools.get(name)
            if existing and not self._can_replace(existing.toolset, toolset):
                logger.error("refusing to shadow %s (%s) with %s",
                             name, existing.toolset, toolset)
                return False
            self._generation += 1
            self._tools[name] = ToolEntry(
                handler=handler, schema=schema,
                toolset=toolset, generation=self._generation,
            )
            return True

    def _can_replace(self, existing: str, incoming: str) -> bool:
        if existing == incoming: return True
        if existing == "builtin" or incoming == "builtin": return False
        if existing.startswith("mcp-") and incoming.startswith("mcp-"): return True
        return False

    def dispatch(self, name: str, args: dict) -> Any:
        entry = self._tools.get(name)
        if entry is None:
            raise UnknownTool(name)
        result = entry.handler(**args)
        # async handlers are bridged here transparently
        if inspect.isawaitable(result):
            result = self._run_async(result)
        return result
```

**对照阅读要点**：

- **`ToolEntry` 是 NamedTuple**——hermes 的不可变约定。我们用了 struct（Go 的 NamedTuple 就是 struct），效果一致。
- **`_toolset_checks`** 是我们没做的：每个 toolset 注册时给一个"我是否可用"的回调，比如 `mcp-X` 检查 server 是否还活着。s07 加。
- **`_toolset_aliases`** 也是没做的：把 `mcp-github` 别名为 `gh` 这种。s07 加。
- **`dispatch` 内部 async 桥接**：hermes 的工具有同步的也有 async 的，注册表自动 `await` 之。我们 Go 这边一律同步，s07 引入 MCP 时自然会变成 goroutine + channel 的并发模型——同样的问题，不同的工具箱。
- **`logger.error` 而非 `raise`**：hermes 把 shadow 冲突当成 *可恢复事件*，记日志、拒绝注册、继续运行。我们把它当 `error` 返回，CLI 层 fail-fast。生产代码要 hermes 那种姿态。

**想读更多**：从 `tools/registry.py` 的 `register` 入手，跟着 `_can_replace` 看 toolset 规则，跟着 `dispatch` 进 `_run_async` 看异步桥接，最后跟着 `deregister` 进 `tools/mcp_tool.py` 的 server 断连处理——这条线就是 s02 → s07 → s06 (plugin) 的真实代码地图。

---

**下一节预告**：s03 把 `skills/` 里的 Markdown 文件**作为 prompt** 注入 agent——hermes 最特别的设计之一。它会通过 `Registry.Register(skill, "skill-greet")` 进来，验证 s02 的 toolset 抽象能 absorb 一个完全不同形态的工具来源。
