package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

type Tool interface {
	Schema() ToolSchema
	Execute(ctx context.Context, input map[string]interface{}) (string, error)
}

type BashTool struct{}

func NewBashTool() *BashTool { return &BashTool{} }

func (b *BashTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "bash",
		Description: "Run a shell command via /bin/bash -c.",
		InputSchema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"command": map[string]interface{}{"type": "string"}},
			"required":   []string{"command"},
		},
	}
}

func (b *BashTool) Execute(ctx context.Context, input map[string]interface{}) (string, error) {
	cmd, _ := input["command"].(string)
	out, err := exec.CommandContext(ctx, "bash", "-c", cmd).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("(exit error: %v)\n%s", err, out), nil
	}
	return string(out), nil
}

type ReadFileTool struct{}

func NewReadFileTool() *ReadFileTool { return &ReadFileTool{} }

func (r *ReadFileTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "read_file",
		Description: "Read a UTF-8 text file from disk.",
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
