package main

import (
	"context"
	"strings"
	"testing"
)

// stubTool is a no-op Tool used to exercise the registry in isolation.
type stubTool struct{ name string }

func (s *stubTool) Schema() ToolSchema                                              { return ToolSchema{Name: s.name} }
func (s *stubTool) Execute(_ context.Context, _ map[string]interface{}) (string, error) { return s.name, nil }

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&stubTool{name: "alpha"}, ToolsetBuiltin); err != nil {
		t.Fatal(err)
	}
	got, ok := r.Get("alpha")
	if !ok {
		t.Fatalf("alpha not found")
	}
	if got.Schema().Name != "alpha" {
		t.Fatalf("unexpected schema name: %q", got.Schema().Name)
	}
	if r.Generation() != 1 {
		t.Fatalf("generation=%d, want 1", r.Generation())
	}
}

func TestRegistry_BuiltinShadowingForbidden(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&stubTool{name: "bash"}, ToolsetBuiltin); err != nil {
		t.Fatal(err)
	}
	err := r.Register(&stubTool{name: "bash"}, "mcp-evil")
	if err == nil || !strings.Contains(err.Error(), "cannot shadow builtin") {
		t.Fatalf("expected shadow error, got: %v", err)
	}
}

func TestRegistry_McpToMcpAllowed(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&stubTool{name: "search"}, "mcp-foo"); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&stubTool{name: "search"}, "mcp-bar"); err != nil {
		t.Fatalf("mcp-bar replacing mcp-foo should be allowed, got: %v", err)
	}
	if r.Generation() != 2 {
		t.Fatalf("generation=%d, want 2", r.Generation())
	}
}

func TestRegistry_SameToolsetIdempotent(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&stubTool{name: "x"}, "skill-greet"); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&stubTool{name: "x"}, "skill-greet"); err != nil {
		t.Fatalf("same-toolset re-register should be idempotent, got: %v", err)
	}
}

func TestRegistry_DefinitionsSortedAndStable(t *testing.T) {
	r := NewRegistry()
	for _, n := range []string{"gamma", "alpha", "beta"} {
		if err := r.Register(&stubTool{name: n}, ToolsetBuiltin); err != nil {
			t.Fatal(err)
		}
	}
	defs := r.Definitions()
	want := []string{"alpha", "beta", "gamma"}
	for i, d := range defs {
		if d.Name != want[i] {
			t.Fatalf("Definitions()[%d]=%q, want %q", i, d.Name, want[i])
		}
	}
}

func TestRegistry_DeregisterBumpsGeneration(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&stubTool{name: "x"}, ToolsetBuiltin)
	g := r.Generation()
	if !r.Deregister("x") {
		t.Fatal("Deregister returned false")
	}
	if r.Generation() <= g {
		t.Fatalf("generation did not advance: was %d now %d", g, r.Generation())
	}
	if r.Deregister("x") {
		t.Fatal("Deregister of missing tool should return false")
	}
}
