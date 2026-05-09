package main

import (
	"context"
	"fmt"
	"strings"
)

// Loop — unchanged from s02. The point of s03 is to validate that the loop
// doesn't need to know about the new tool source (skills) at all; only the
// Registry does.

type Loop struct {
	Provider Provider
	Registry *Registry
	MaxTurns int
	Verbose  bool
}

func (l *Loop) Run(ctx context.Context, userPrompt string) (string, error) {
	messages := []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: userPrompt}}},
	}

	for turn := 0; turn < l.MaxTurns; turn++ {
		schemas := l.Registry.Definitions()
		resp, err := l.Provider.CreateMessage(ctx, CreateMessageRequest{
			Messages: messages,
			Tools:    schemas,
		})
		if err != nil {
			return "", fmt.Errorf("turn %d: %w", turn, err)
		}

		messages = append(messages, Message{Role: "assistant", Content: resp.Content})
		if l.Verbose {
			l.dumpAssistant(turn, resp)
		}

		switch resp.StopReason {
		case "end_turn", "stop_sequence":
			return extractText(resp.Content), nil
		case "tool_use":
			results, err := l.runTools(ctx, resp.Content, turn)
			if err != nil {
				return "", err
			}
			messages = append(messages, Message{Role: "user", Content: results})
		case "max_tokens":
			return "", fmt.Errorf("hit max_tokens at turn %d", turn)
		default:
			return "", fmt.Errorf("unexpected stop_reason %q at turn %d", resp.StopReason, turn)
		}
	}
	return "", fmt.Errorf("loop exceeded MaxTurns=%d without end_turn", l.MaxTurns)
}

func (l *Loop) runTools(ctx context.Context, content []ContentBlock, turn int) ([]ContentBlock, error) {
	var results []ContentBlock
	for _, block := range content {
		if block.Type != "tool_use" {
			continue
		}
		tool, ok := l.Registry.Get(block.Name)
		if !ok {
			results = append(results, ContentBlock{
				Type:        "tool_result",
				ToolUseID:   block.ID,
				ToolContent: fmt.Sprintf("unknown tool %q (registered: %v)", block.Name, l.Registry.Names()),
			})
			continue
		}
		if l.Verbose {
			fmt.Printf("[turn %d] -> %s %v\n", turn, block.Name, block.Input)
		}
		out, err := tool.Execute(ctx, block.Input)
		if err != nil {
			out = fmt.Sprintf("tool error: %v", err)
		}
		if l.Verbose {
			fmt.Printf("[turn %d] <- %s\n", turn, truncate(out, 240))
		}
		results = append(results, ContentBlock{
			Type:        "tool_result",
			ToolUseID:   block.ID,
			ToolContent: out,
		})
	}
	return results, nil
}

func (l *Loop) dumpAssistant(turn int, resp *CreateMessageResponse) {
	for _, b := range resp.Content {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			fmt.Printf("[turn %d] assistant: %s\n", turn, b.Text)
		}
	}
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

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
