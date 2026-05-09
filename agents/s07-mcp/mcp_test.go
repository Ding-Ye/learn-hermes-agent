package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildSelf compiles the current binary into a temp dir so the test can
// spawn it in -server mode. Goal is a self-contained end-to-end test of
// the MCP roundtrip without any external dependency.
func buildSelf(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "s07-test")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build self: %v", err)
	}
	return bin
}

func TestMCP_EndToEnd_StdioRoundtrip(t *testing.T) {
	bin := buildSelf(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client, err := DialStdio(ctx, bin, "-server")
	if err != nil {
		t.Fatalf("DialStdio: %v", err)
	}
	defer client.Close()

	tools, err := client.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d: %+v", len(tools), tools)
	}
	names := []string{tools[0].Name, tools[1].Name}
	if !contains(names, "echo") || !contains(names, "reverse") {
		t.Fatalf("expected echo+reverse, got %v", names)
	}

	out, isErr, err := client.CallTool(ctx, "echo", map[string]interface{}{"text": "hello mcp"})
	if err != nil || isErr {
		t.Fatalf("echo: %v isErr=%v", err, isErr)
	}
	if out != "hello mcp" {
		t.Fatalf("echo result: %q", out)
	}

	out, isErr, err = client.CallTool(ctx, "reverse", map[string]interface{}{"text": "abcde"})
	if err != nil || isErr {
		t.Fatalf("reverse: %v isErr=%v", err, isErr)
	}
	if out != "edcba" {
		t.Fatalf("reverse result: %q", out)
	}
}

func TestMCP_RegisterIntoRegistry(t *testing.T) {
	bin := buildSelf(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client, err := DialStdio(ctx, bin, "-server")
	if err != nil {
		t.Fatalf("DialStdio: %v", err)
	}
	defer client.Close()

	r := NewRegistry()
	count, err := RegisterMCPTools(ctx, r, "demo", client)
	if err != nil {
		t.Fatalf("RegisterMCPTools: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 registrations, got %d", count)
	}
	names := r.Names()
	expected := []string{"mcp_demo_echo", "mcp_demo_reverse"}
	for _, e := range expected {
		if _, ok := r.Get(e); !ok {
			t.Fatalf("missing tool %q in registry; got %v", e, names)
		}
	}
}

func TestMCP_DeregisterToolset(t *testing.T) {
	r := NewRegistry()
	tBuiltin := &BashTool{}
	if err := r.Register(tBuiltin, ToolsetBuiltin); err != nil {
		t.Fatal(err)
	}
	tMCP1 := &fakeMCP{name: "alpha", toolset: "mcp-demo"}
	tMCP2 := &fakeMCP{name: "beta", toolset: "mcp-demo"}
	if err := r.Register(tMCP1, "mcp-demo"); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(tMCP2, "mcp-demo"); err != nil {
		t.Fatal(err)
	}
	if got := len(r.Names()); got != 3 {
		t.Fatalf("expected 3 tools before deregister, got %d (%v)", got, r.Names())
	}
	gen0 := r.Generation()
	removed := r.DeregisterToolset("mcp-demo")
	if removed != 2 {
		t.Fatalf("expected 2 removals, got %d", removed)
	}
	if got := len(r.Names()); got != 1 || r.Names()[0] != "bash" {
		t.Fatalf("expected only bash to remain, got %v", r.Names())
	}
	if r.Generation() <= gen0 {
		t.Fatalf("generation should advance on deregister")
	}
}

// fakeMCP is a stand-in Tool used only by registry tests where we don't
// want to spawn a real subprocess.
type fakeMCP struct {
	name    string
	toolset string
}

func (f *fakeMCP) Schema() ToolSchema {
	return ToolSchema{Name: f.name, Description: "fake", InputSchema: map[string]interface{}{"type": "object"}}
}
func (f *fakeMCP) Execute(_ context.Context, _ map[string]interface{}) (string, error) {
	return f.name, nil
}

// Make sure the test file compiles even when io is unused on some platforms.
var _ = io.EOF

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// guard against very fast machines completing before the server is
// ready; the DialStdio handshake already waits for `initialize`, so
// this should be unnecessary, but keep it as a small spin if the
// server reports its banner before being ready.
func init() {
	if v := os.Getenv("S07_TEST_WARMUP_MS"); v != "" {
		// no-op placeholder; documented for users debugging flakes.
	}
	_ = strings.TrimSpace
}
