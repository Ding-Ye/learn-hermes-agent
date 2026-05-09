package main

import (
	"context"
	"fmt"
	"os/exec"
)

// Tool is the contract every tool must satisfy. The loop only sees this
// interface — built-in tools, MCP tools, and skill commands later all plug in
// the same way.
type Tool interface {
	Schema() ToolSchema
	Execute(ctx context.Context, input map[string]interface{}) (string, error)
}

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
	// Surface non-zero exit as content (so the model sees stderr) rather than
	// as a Go error: from the model's POV, a command that "failed" is still a
	// valid tool result it can reason about.
	if err != nil {
		return fmt.Sprintf("(exit error: %v)\n%s", err, string(out)), nil
	}
	return string(out), nil
}
