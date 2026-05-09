package main

import (
	"context"
	"fmt"
	"strings"
)

// Loop now talks to a PluginManager rather than calling MemoryProvider's
// lifecycle hooks directly. The MemoryProvider lives behind the host, and
// any plugin (Curator, future observability, etc.) gets the same
// dispatch.
type Loop struct {
	Provider Provider
	Registry *Registry
	Store    *Store
	Plugins  *PluginManager
	MaxTurns int
	Verbose  bool
}

func (l *Loop) Run(ctx context.Context, sess *Session) error {
	if l.Plugins != nil {
		l.Plugins.DispatchSessionStart(ctx, sess.ID)
		defer l.Plugins.DispatchSessionEnd(ctx, sess.ID)
	}
	for turn := 0; turn < l.MaxTurns; turn++ {
		schemas := l.Registry.Definitions()
		resp, err := l.Provider.CreateMessage(ctx, CreateMessageRequest{
			Model:    sess.Model,
			Messages: sess.Messages,
			Tools:    schemas,
		})
		if err != nil {
			return fmt.Errorf("turn %d: %w", turn, err)
		}
		sess.AppendAssistant(resp.Content)
		sess.Usage.Add(resp.Usage)
		if err := l.Store.Save(sess); err != nil {
			return fmt.Errorf("persist after turn %d: %w", turn, err)
		}
		if l.Verbose {
			l.dumpAssistant(turn, resp)
		}
		switch resp.StopReason {
		case "end_turn", "stop_sequence":
			return nil
		case "tool_use":
			results, err := l.runTools(ctx, resp.Content, turn)
			if err != nil {
				return err
			}
			sess.AppendUserToolResults(results)
			if err := l.Store.Save(sess); err != nil {
				return fmt.Errorf("persist tool results turn %d: %w", turn, err)
			}
		case "max_tokens":
			return fmt.Errorf("hit max_tokens at turn %d", turn)
		default:
			return fmt.Errorf("unexpected stop_reason %q at turn %d", resp.StopReason, turn)
		}
	}
	return fmt.Errorf("loop exceeded MaxTurns=%d without end_turn", l.MaxTurns)
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
				ToolContent: fmt.Sprintf("unknown tool %q", block.Name),
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

func LastAssistantText(sess *Session) string {
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		if sess.Messages[i].Role != "assistant" {
			continue
		}
		var sb strings.Builder
		for _, b := range sess.Messages[i].Content {
			if b.Type == "text" {
				sb.WriteString(b.Text)
			}
		}
		if sb.Len() > 0 {
			return sb.String()
		}
	}
	return ""
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
