package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
)

// MCP (Model Context Protocol) is JSON-RPC 2.0 over a transport. The
// most common transport is stdio: the MCP server is a subprocess, the
// agent writes JSON-RPC requests to its stdin and reads responses from
// its stdout (one JSON object per line).
//
// We implement enough of the protocol to teach the shape:
//   - initialize         (handshake)
//   - tools/list         (discover what the server offers)
//   - tools/call         (invoke a remote tool)
//
// We omit:
//   - notifications/initialized
//   - notifications/tools/list_changed (dynamic refresh; called out in docs)
//   - resources, prompts, sampling (other MCP features)
//   - HTTP/SSE transport (stdio is the simplest demo)

// JSON-RPC 2.0 envelopes.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// MCPTool definition as returned by tools/list. The "input_schema" JSON
// matches Anthropic's expectations, so we surface it directly.
type MCPToolDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// ListToolsResult is the result of tools/list.
type ListToolsResult struct {
	Tools []MCPToolDef `json:"tools"`
}

// CallToolParams is the params payload for tools/call.
type CallToolParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

// CallToolResult is what tools/call returns. Real MCP returns a list of
// content blocks (text, image, etc.); we coalesce text blocks.
type CallToolResult struct {
	Content []MCPContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

type MCPContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ===== client ==============================================================

// MCPClient is a single connection to one MCP server over stdio.
// Concurrency-safe: pending requests are tracked by id, the reader
// goroutine routes each response to the awaiting caller.
type MCPClient struct {
	cmd *exec.Cmd
	w   io.WriteCloser

	mu      sync.Mutex
	nextID  uint64
	pending map[uint64]chan *rpcResponse

	doneOnce sync.Once
	done     chan struct{}
	readErr  error
}

// DialStdio spawns `command` with `args`, hooks up stdin/stdout, and
// completes the MCP `initialize` handshake.
func DialStdio(ctx context.Context, command string, args ...string) (*MCPClient, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = nil // discard stderr by default; main.go can override
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start mcp server: %w", err)
	}

	c := &MCPClient{
		cmd:     cmd,
		w:       stdin,
		pending: map[uint64]chan *rpcResponse{},
		done:    make(chan struct{}),
	}
	go c.readLoop(stdout)

	// Handshake.
	initParams := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"clientInfo":      map[string]string{"name": "learn-hermes-agent/s07", "version": "0.1"},
		"capabilities":    map[string]interface{}{},
	}
	if _, err := c.call(ctx, "initialize", initParams); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize: %w", err)
	}
	return c, nil
}

func (c *MCPClient) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue // unknown JSON; ignore (server might emit notifications)
		}
		if resp.ID == 0 {
			continue // notification, not a response we're waiting on
		}
		c.mu.Lock()
		ch, ok := c.pending[resp.ID]
		delete(c.pending, resp.ID)
		c.mu.Unlock()
		if ok {
			ch <- &resp
		}
	}
	c.readErr = scanner.Err()
	c.doneOnce.Do(func() { close(c.done) })
}

func (c *MCPClient) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	id := atomic.AddUint64(&c.nextID, 1)
	pb, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	req := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: pb}
	line, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	ch := make(chan *rpcResponse, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	if _, err := c.w.Write(append(line, '\n')); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("write: %w", err)
	}
	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case <-c.done:
		return nil, fmt.Errorf("server closed: %v", c.readErr)
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

// ListTools returns the server's tool catalogue.
func (c *MCPClient) ListTools(ctx context.Context) ([]MCPToolDef, error) {
	raw, err := c.call(ctx, "tools/list", map[string]interface{}{})
	if err != nil {
		return nil, err
	}
	var out ListToolsResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out.Tools, nil
}

// CallTool invokes a remote tool by name.
func (c *MCPClient) CallTool(ctx context.Context, name string, args map[string]interface{}) (string, bool, error) {
	raw, err := c.call(ctx, "tools/call", CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return "", false, err
	}
	var out CallToolResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", false, err
	}
	var sb []byte
	for _, b := range out.Content {
		if b.Type == "text" {
			sb = append(sb, b.Text...)
		}
	}
	return string(sb), out.IsError, nil
}

// Close shuts the subprocess down; idempotent.
func (c *MCPClient) Close() error {
	if c.w != nil {
		_ = c.w.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	if c.cmd != nil {
		_ = c.cmd.Wait()
	}
	c.doneOnce.Do(func() { close(c.done) })
	return nil
}

// ===== MCPTool: bridge a remote tool into the s02 Registry ================

type MCPTool struct {
	Server   string
	Def      MCPToolDef
	Client   *MCPClient
}

// LocalName is what we register in the Registry: "mcp_<server>_<remote-name>".
// Mirrors the hermes convention.
func (m *MCPTool) LocalName() string {
	return fmt.Sprintf("mcp_%s_%s", m.Server, m.Def.Name)
}

func (m *MCPTool) Schema() ToolSchema {
	desc := m.Def.Description
	if desc == "" {
		desc = "(no description)"
	}
	return ToolSchema{
		Name:        m.LocalName(),
		Description: fmt.Sprintf("[mcp:%s] %s", m.Server, desc),
		InputSchema: m.Def.InputSchema,
	}
}

func (m *MCPTool) Execute(ctx context.Context, input map[string]interface{}) (string, error) {
	out, isErr, err := m.Client.CallTool(ctx, m.Def.Name, input)
	if err != nil {
		return "", err
	}
	if isErr {
		return fmt.Sprintf("(remote tool error)\n%s", out), nil
	}
	return out, nil
}

// RegisterMCPTools connects to one MCP server and registers all of its
// tools with the registry under toolset "mcp-<server>". Returns the
// number registered. Caller is responsible for closing the client.
func RegisterMCPTools(ctx context.Context, registry *Registry, server string, client *MCPClient) (int, error) {
	defs, err := client.ListTools(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	toolset := "mcp-" + server
	for _, d := range defs {
		t := &MCPTool{Server: server, Def: d, Client: client}
		if err := registry.Register(t, toolset); err != nil {
			fmt.Fprintf(io.Discard, "[mcp] register %s skipped: %v\n", t.LocalName(), err)
			continue
		}
		count++
	}
	return count, nil
}
