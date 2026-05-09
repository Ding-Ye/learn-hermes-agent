---
title: "s02 · Tool Registry"
chapter: 2
slug: s02-tool-registry
est_read_min: 12
---

# s02 · Tool Registry

> What this teaches: upgrade s01's `[]Tool` slice into a real **`Registry`** — a typed tool namespace that supports add/remove at runtime, blocks MCP/plugin from silently shadowing builtins, and keeps `Definitions` output sorted for prompt-cache stability. This is the core abstraction that lets hermes-agent push every tool source (builtin / MCP / skill / plugin) through one namespace.

---

## Problem

s01's loop took a `[]Tool` slice — fixed at compile time. Fine for a toy, fast to break the moment a real agent shows up:

- **Tools become known at runtime.** An MCP server connects after the agent boots (s07). A skill arrives by the user dropping a `.md` file into `skills/` (s03). A slice can't hold "tools that will exist later".
- **Naming collisions.** A hostile MCP server names its tool `bash`, conflicting with your builtin `bash`. Whoever-was-registered-last winning is a security hole.
- **Schema order isn't stable.** Map iteration is random; the `tools` array sent to the model varies between turns and **defeats prompt caching directly**.
- **You need to ask "what tools do I have right now?"** For users, monitoring, the `_generation` cache key — all of these need one entry point.

A `map[string]Tool` solves half of this but not shadow protection or generation tracking. Hence: a real registry.

## Solution

Introduce a **`Registry`**, three pieces:

1. **Each tool carries a toolset label**: `"builtin"` / `"mcp-<server>"` / `"skill-<name>"` / `"plugin-<name>"`. The label is a convention, not an enum — interpretation is centralised in `canReplace`.
2. **`Register(t, toolset)` enforces shadow rules**: builtins can't be overridden; MCP-to-MCP swap is allowed; same-toolset re-register is idempotent; everything else cross-toolset is rejected.
3. **A monotonic `Generation` counter**: every successful register/deregister bumps it. `Definitions()` returns sorted schemas; the loop can later cache by `(generation, schemas)`.

This same abstraction grows up into hermes's `tools/registry.py`: every tool source — including s07's MCP dynamic refresh — flows through one interface, and the loop never knows whether a tool is builtin or MCP.

## How It Works

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

The core 30 lines (excerpt from [`agents/s02-tool-registry/registry.go`](https://github.com/Ding-Ye/learn-hermes-agent/blob/main/agents/s02-tool-registry/registry.go)):

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

**Three non-obvious points**:

1. **The `mcp-* ↔ mcp-*` rule is intentional**. Multiple MCP servers each owning their own `search` tool is a legitimate state; switching servers must let the new register override the old.
2. **`Definitions()` must be sorted.** Anthropic's prompt cache matches the `tools` array byte-for-byte; random map order causes a cache miss every turn.
3. **`Generation` is unused this chapter** — the loop rebuilds schemas every turn anyway. But each `ToolEntry` already remembers "what generation was I born at", which becomes the cache key in s07 when MCP refresh lands. Plant the field early, save the refactor later.

## What Changed (vs. s01)

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

The mechanical diff is line-for-line. The **semantic** diff is large: the tool set changes from "fixed at boot" to "mutable at runtime, with shadow protection and generation tracking". Every later session adds new tool sources via `Registry.Register`.

## Try It

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s02-tool-registry

# Make the model choose between bash and read_file
go run . -v "show me the first 30 lines of agents/s02-tool-registry/main.go"
go run . -v "what's today's date in ISO 8601 format?"
go run . -v "count the number of Go files in this directory"

# Run the unit tests for the shadow rules
go test -v ./...
```

Expected shape:

```
[registry] 2 tools: [bash read_file] (gen=2)
[turn 0] assistant: I'll read the file.
[turn 0] -> read_file map[path:agents/s02-tool-registry/main.go]
[turn 0] <- package main  import ( ...
[turn 1] assistant: Here are the first 30 lines: ...
```

## Upstream Source Reading

hermes-agent's registry is in `tools/registry.py`, ~650 lines. Our version is the load-bearing skeleton — `_tools` / `_generation` / shadow protection map across cleanly. What we don't yet have: `_toolset_aliases`, async dispatch bridging, the deregister-then-register transactionality used in s07's MCP refresh.

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

**Reading notes**:

- **`ToolEntry` is a NamedTuple** — hermes's immutability convention. We use a struct (Go's analogue); same effect.
- **`_toolset_checks`** is something we don't yet implement: a per-toolset "am I still alive?" callback. `mcp-X` would use this to check if its server is still reachable. Comes in s07.
- **`_toolset_aliases`** is also not yet here: aliasing `mcp-github` to `gh`. Also s07.
- **Async dispatch bridging**: hermes tools mix sync and async; the registry transparently `await`s. Our Go version is sync-only here, but s07's MCP integration will move us into the goroutine + channel model — same problem, different toolkit.
- **`logger.error` instead of `raise`**: hermes treats shadow conflicts as *recoverable events* — log, refuse the registration, keep running. We return an error and let the CLI fail fast. Production code wants the hermes style.

**Read further**: start at `register` in `tools/registry.py`, follow `_can_replace` for the toolset rules, follow `dispatch` into `_run_async` for the async bridging, and finally follow `deregister` into `tools/mcp_tool.py` to see how the registry handles a server going down. That trace is the real-source map for s02 → s07 → s06 (plugins).

---

**Next**: s03 reads Markdown files in `skills/` and **injects them as prompts** — one of hermes's most distinctive designs. They register via `Registry.Register(skill, "skill-greet")`, validating s02's toolset abstraction can absorb a wildly different tool source.
