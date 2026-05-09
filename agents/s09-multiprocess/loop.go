package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Tiny agent loop carrying just the bash + read_file tools. The point
// of s09 is the multi-process orchestration; the loop body is not the
// lesson, it's the workload the scheduler runs.

type Tool interface {
	Schema() ToolSchema
	Execute(ctx context.Context, input map[string]interface{}) (string, error)
}

type bashTool struct{}

func (bashTool) Schema() ToolSchema {
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
func (bashTool) Execute(ctx context.Context, input map[string]interface{}) (string, error) {
	cmd, _ := input["command"].(string)
	out, err := exec.CommandContext(ctx, "bash", "-c", cmd).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("(exit error: %v)\n%s", err, out), nil
	}
	return string(out), nil
}

type Loop struct {
	Provider Provider
	MaxTurns int
	Tools    []Tool
}

func (l *Loop) Run(ctx context.Context, prompt string) (string, error) {
	if l.MaxTurns == 0 {
		l.MaxTurns = 10
	}
	toolByName := map[string]Tool{}
	var schemas []ToolSchema
	for _, t := range l.Tools {
		s := t.Schema()
		toolByName[s.Name] = t
		schemas = append(schemas, s)
	}
	messages := []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: prompt}}}}
	for turn := 0; turn < l.MaxTurns; turn++ {
		resp, err := l.Provider.CreateMessage(ctx, CreateMessageRequest{Messages: messages, Tools: schemas})
		if err != nil {
			return "", fmt.Errorf("turn %d: %w", turn, err)
		}
		messages = append(messages, Message{Role: "assistant", Content: resp.Content})
		switch resp.StopReason {
		case "end_turn", "stop_sequence":
			return extractText(resp.Content), nil
		case "tool_use":
			var results []ContentBlock
			for _, b := range resp.Content {
				if b.Type != "tool_use" {
					continue
				}
				tool, ok := toolByName[b.Name]
				if !ok {
					results = append(results, ContentBlock{Type: "tool_result", ToolUseID: b.ID, ToolContent: "unknown tool"})
					continue
				}
				out, err := tool.Execute(ctx, b.Input)
				if err != nil {
					out = fmt.Sprintf("error: %v", err)
				}
				results = append(results, ContentBlock{Type: "tool_result", ToolUseID: b.ID, ToolContent: out})
			}
			messages = append(messages, Message{Role: "user", Content: results})
		default:
			return "", fmt.Errorf("unexpected stop_reason %q", resp.StopReason)
		}
	}
	return "", fmt.Errorf("max turns exceeded")
}

func extractText(content []ContentBlock) string {
	var sb strings.Builder
	for _, b := range content {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}
