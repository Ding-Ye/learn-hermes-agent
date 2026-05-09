package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// TerminalTool is the *only* shell-like tool the agent sees. It dispatches
// every invocation to the configured Environment — local, docker:image,
// or whatever else the factory understands. The LLM has no way (and no
// need) to know which backend handles its request.
type TerminalTool struct {
	Env Environment
}

func NewTerminalTool(env Environment) *TerminalTool { return &TerminalTool{Env: env} }

func (t *TerminalTool) Schema() ToolSchema {
	return ToolSchema{
		Name: "terminal",
		Description: fmt.Sprintf("Execute a shell command. Backend: %s. "+
			"Returns JSON with stdout, stderr, exit_code, duration. "+
			"Optional cwd / timeout_seconds.", t.Env.Name()),
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{
					"type":        "string",
					"description": "Shell command line.",
				},
				"cwd": map[string]interface{}{
					"type":        "string",
					"description": "Working directory (optional).",
				},
				"timeout_seconds": map[string]interface{}{
					"type":        "number",
					"description": "Hard timeout in seconds (default 60).",
				},
			},
			"required": []string{"command"},
		},
	}
}

func (t *TerminalTool) Execute(ctx context.Context, input map[string]interface{}) (string, error) {
	command, _ := input["command"].(string)
	cwd, _ := input["cwd"].(string)
	timeout := 60 * time.Second
	if v, ok := input["timeout_seconds"].(float64); ok && v > 0 {
		timeout = time.Duration(v * float64(time.Second))
	}
	res, err := t.Env.Execute(ctx, ExecOptions{
		Command: command,
		Cwd:     cwd,
		Timeout: timeout,
	})
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error()), nil
	}
	out, _ := json.MarshalIndent(res, "", "  ")
	return string(out), nil
}

// ReadFileTool — same shape as earlier sessions; carried for parity with
// the Anthropic-side tool surface across the curriculum.
type ReadFileTool struct{}

func NewReadFileTool() *ReadFileTool { return &ReadFileTool{} }

func (r *ReadFileTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "read_file",
		Description: "Read a UTF-8 text file from the AGENT host (not the terminal backend).",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}},
			"required":   []string{"path"},
		},
	}
}

func (r *ReadFileTool) Execute(ctx context.Context, input map[string]interface{}) (string, error) {
	path, _ := input["path"].(string)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("read error: %v", err), nil
	}
	return string(data), nil
}
