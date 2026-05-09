package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

type ToolEntry struct {
	Tool       Tool
	Toolset    string
	Generation int
}

const ToolsetBuiltin = "builtin"

type Registry struct {
	mu         sync.RWMutex
	tools      map[string]ToolEntry
	generation int
}

func NewRegistry() *Registry { return &Registry{tools: map[string]ToolEntry{}} }

func (r *Registry) Register(t Tool, toolset string) error {
	if t == nil {
		return fmt.Errorf("Register: tool is nil")
	}
	name := t.Schema().Name
	if name == "" {
		return fmt.Errorf("Register: empty name")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
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
	if existing == incoming {
		return nil
	}
	if existing == ToolsetBuiltin {
		return fmt.Errorf("toolset %q cannot shadow builtin", incoming)
	}
	if incoming == ToolsetBuiltin {
		return fmt.Errorf("builtin cannot retroactively replace %q", existing)
	}
	if strings.HasPrefix(existing, "mcp-") && strings.HasPrefix(incoming, "mcp-") {
		return nil
	}
	return fmt.Errorf("toolset %q would shadow %q", incoming, existing)
}

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

// DeregisterToolset removes every tool whose Toolset == toolset.
// Returns the number removed. Used when an MCP server disconnects:
// we pull all of its tools out at once.
func (r *Registry) DeregisterToolset(toolset string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	removed := 0
	for name, e := range r.tools {
		if e.Toolset == toolset {
			delete(r.tools, name)
			removed++
		}
	}
	if removed > 0 {
		r.generation++
	}
	return removed
}

func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.tools[name]
	if !ok {
		return nil, false
	}
	return e.Tool, true
}

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

func (r *Registry) Generation() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.generation
}
