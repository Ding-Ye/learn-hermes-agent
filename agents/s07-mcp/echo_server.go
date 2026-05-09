package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// runEchoMCPServer implements a tiny MCP server over stdio, exposing
// two tools: echo (returns input unchanged) and reverse (string
// reversal). It is invoked from the same binary via -server flag —
// makes the demo + tests fully self-contained.
//
// Protocol coverage: initialize, tools/list, tools/call. Notifications
// are dropped on the floor.
func runEchoMCPServer(in io.Reader, out io.Writer) error {
	tools := []MCPToolDef{
		{
			Name:        "echo",
			Description: "Return the input string unchanged. For protocol smoke testing.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"text": map[string]interface{}{"type": "string"}},
				"required":   []string{"text"},
			},
		},
		{
			Name:        "reverse",
			Description: "Reverse the bytes of the input string. Demonstrates an MCP tool that does some work.",
			InputSchema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"text": map[string]interface{}{"type": "string"}},
				"required":   []string{"text"},
			},
		},
	}

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var req rpcRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		var resp rpcResponse
		resp.JSONRPC = "2.0"
		resp.ID = req.ID
		switch req.Method {
		case "initialize":
			resp.Result = json.RawMessage(`{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"echo-mcp","version":"0.1"}}`)
		case "tools/list":
			body, _ := json.Marshal(ListToolsResult{Tools: tools})
			resp.Result = body
		case "tools/call":
			var p CallToolParams
			_ = json.Unmarshal(req.Params, &p)
			text, _ := p.Arguments["text"].(string)
			var content string
			switch p.Name {
			case "echo":
				content = text
			case "reverse":
				content = reverseString(text)
			default:
				resp.Error = &rpcError{Code: -32601, Message: "unknown tool: " + p.Name}
			}
			if resp.Error == nil {
				body, _ := json.Marshal(CallToolResult{Content: []MCPContent{{Type: "text", Text: content}}})
				resp.Result = body
			}
		default:
			resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
		}
		// Skip notifications (no id, no response expected). For our
		// tiny server there aren't any inbound notifications, but
		// the guard keeps the protocol honest.
		if req.ID == 0 {
			continue
		}
		if err := writeJSONLine(out, resp); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func writeJSONLine(w io.Writer, v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

func reverseString(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

// runServerMode is the main entry when the binary is invoked with -server.
// Defaults to stdio; uses os.Stdin / os.Stdout / os.Stderr for diagnostics.
func runServerMode() {
	// Diagnostic banner goes to stderr so it doesn't pollute the protocol stream.
	fmt.Fprintln(os.Stderr, strings.Repeat("=", 60))
	fmt.Fprintln(os.Stderr, "[echo-mcp] running in MCP server mode on stdio")
	fmt.Fprintln(os.Stderr, "[echo-mcp] tools: echo, reverse")
	fmt.Fprintln(os.Stderr, strings.Repeat("=", 60))
	if err := runEchoMCPServer(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "[echo-mcp] server error: %v\n", err)
		os.Exit(1)
	}
}
