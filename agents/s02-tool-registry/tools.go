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

// BashTool — unchanged from s01.
type BashTool struct{}

func NewBashTool() *BashTool { return &BashTool{} }

func (b *BashTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "bash",
		Description: "Run a shell command via /bin/bash -c and return combined stdout+stderr.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{
					"type":        "string",
					"description": "The shell command to execute.",
				},
			},
			"required": []string{"command"},
		},
	}
}

func (b *BashTool) Execute(ctx context.Context, input map[string]interface{}) (string, error) {
	cmd, ok := input["command"].(string)
	if !ok {
		return "", fmt.Errorf("input.command must be a string, got %T", input["command"])
	}
	out, err := exec.CommandContext(ctx, "bash", "-c", cmd).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("(exit error: %v)\n%s", err, string(out)), nil
	}
	return string(out), nil
}

// ReadFileTool — the second tool. Its existence is the point of s02:
// the registry now mediates between TWO tools, and the model has to choose
// which one fits the user's prompt. Adding a third later is one Register call.
type ReadFileTool struct{}

func NewReadFileTool() *ReadFileTool { return &ReadFileTool{} }

func (r *ReadFileTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "read_file",
		Description: "Read the contents of a UTF-8 text file from disk. Prefer this over bash for plain file reads — the output is the file content directly with no shell quoting.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "The file path. Relative or absolute.",
				},
			},
			"required": []string{"path"},
		},
	}
}

func (r *ReadFileTool) Execute(ctx context.Context, input map[string]interface{}) (string, error) {
	path, ok := input["path"].(string)
	if !ok {
		return "", fmt.Errorf("input.path must be a string, got %T", input["path"])
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("read error: %v", err), nil
	}
	return string(data), nil
}
