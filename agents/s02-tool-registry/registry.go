package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ToolEntry is what the registry stores per tool: the tool itself + which
// "toolset" it came from. The toolset label is a CONVENTION, not an enum:
//
//   "builtin"        — code we ship in this binary (this session's two tools)
//   "mcp-<server>"   — registered from an MCP server (s07 will introduce these)
//   "skill-<name>"   — a skill exposed as a tool (s03)
//   "plugin-<name>"  — registered by a plugin (s06)
//
// The label drives the shadowing rules in canReplace(): a builtin tool can
// never be silently overridden by an MCP tool, and a non-MCP tool can't be
// replaced by an MCP tool of the same name. This is the registry's main job
// beyond plain map[string]Tool: keeping the namespace honest.
type ToolEntry struct {
	Tool       Tool
	Toolset    string
	Generation int
}

const ToolsetBuiltin = "builtin"

// Registry holds the active tool set for one agent instance. Safe for
// concurrent use — a future MCP refresh in s07 will register tools on a
// background goroutine while the loop reads schemas on the main one.
//
// `generation` is a monotonic counter, bumped on every successful Register
// or Deregister. Production hermes uses it as a cache key to skip rebuilding
// the schemas list when nothing has changed; we don't optimise yet.
type Registry struct {
	mu         sync.RWMutex
	tools      map[string]ToolEntry
	generation int
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]ToolEntry{}}
}

// Register adds (or replaces) a tool from the given toolset.
//
// Errors:
//   - tool is nil or has empty name
//   - registering a non-builtin with the name of an existing builtin
//   - registering a builtin onto an existing non-builtin
//   - cross-non-builtin shadow that isn't mcp ↔ mcp
//
// Allowed:
//   - same toolset re-registering (idempotent refresh)
//   - mcp-A replacing mcp-B (servers swap during a session)
func (r *Registry) Register(t Tool, toolset string) error {
	if t == nil {
		return fmt.Errorf("Register: tool is nil")
	}
	name := t.Schema().Name
	if name == "" {
		return fmt.Errorf("Register: tool has empty name")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.tools[name]; ok {
		if err := canReplace(existing.Toolset, toolset); err != nil {
			return fmt.Errorf("Register %q: %w", name, err)
		}
	}

	r.generation++
	r.tools[name] = ToolEntry{
		Tool:       t,
		Toolset:    toolset,
		Generation: r.generation,
	}
	return nil
}

func canReplace(existing, incoming string) error {
	if existing == incoming {
		return nil // idempotent refresh — same source replacing itself
	}
	if existing == ToolsetBuiltin {
		return fmt.Errorf("toolset %q cannot shadow builtin", incoming)
	}
	if incoming == ToolsetBuiltin {
		return fmt.Errorf("builtin cannot retroactively replace toolset %q", existing)
	}
	if strings.HasPrefix(existing, "mcp-") && strings.HasPrefix(incoming, "mcp-") {
		return nil // one MCP server replacing another's tool is allowed
	}
	return fmt.Errorf("toolset %q would shadow existing toolset %q", incoming, existing)
}

// Deregister removes a tool by name. Returns whether anything was removed.
// Bumps generation only on actual removal.
func (r *Registry) Deregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[name]; !ok {
		return false
	}
	delete(r.tools, name)
	r.generation++
	return true
}

// Get returns the tool registered under name, if any.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.tools[name]
	if !ok {
		return nil, false
	}
	return e.Tool, true
}

// Definitions returns the schemas for every registered tool, in
// name-sorted order so successive calls return identical bytes — important
// for prompt caching downstream.
func (r *Registry) Definitions() []ToolSchema {
	names := r.Names()
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ToolSchema, 0, len(names))
	for _, n := range names {
		out = append(out, r.tools[n].Tool.Schema())
	}
	return out
}

// Generation returns the current monotonic counter.
func (r *Registry) Generation() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.generation
}

// Names returns registered tool names in sorted order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
