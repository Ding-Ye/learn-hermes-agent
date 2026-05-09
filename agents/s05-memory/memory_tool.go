package main

import (
	"context"
	"encoding/json"
	"fmt"
)

// MemorySearchTool exposes MemoryProvider.Search to the LLM. The agent
// calls it like any other tool; the result is the JSON-encoded memories
// it found.
type MemorySearchTool struct {
	Provider MemoryProvider
}

func NewMemorySearchTool(p MemoryProvider) *MemorySearchTool {
	return &MemorySearchTool{Provider: p}
}

func (t *MemorySearchTool) Schema() ToolSchema {
	return ToolSchema{
		Name: "memory_search",
		Description: "Search the user's persistent memory store (FTS5). " +
			"Use this when the user asks about facts, preferences, or past events that may have been saved before. " +
			"Returns JSON array of memories ordered by relevance.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "FTS5 query string. Examples: 'favorite color', 'docker AND postgres', 'name OR project'.",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Max number of memories to return (default 5).",
				},
			},
			"required": []string{"query"},
		},
	}
}

func (t *MemorySearchTool) Execute(ctx context.Context, input map[string]interface{}) (string, error) {
	query, _ := input["query"].(string)
	limit := 5
	if v, ok := input["limit"].(float64); ok && int(v) > 0 {
		limit = int(v)
	}
	mems, err := t.Provider.Search(ctx, query, limit)
	if err != nil {
		return fmt.Sprintf("memory_search error: %v", err), nil
	}
	if len(mems) == 0 {
		return `{"results":[],"note":"no memories matched"}`, nil
	}
	out, _ := json.MarshalIndent(map[string]interface{}{"results": mems}, "", "  ")
	return string(out), nil
}

// MemorySaveTool exposes MemoryProvider.Save. The agent uses it when the
// user asks it to "remember" something across sessions.
type MemorySaveTool struct {
	Provider  MemoryProvider
	SessionID string
}

func NewMemorySaveTool(p MemoryProvider, sessionID string) *MemorySaveTool {
	return &MemorySaveTool{Provider: p, SessionID: sessionID}
}

func (t *MemorySaveTool) Schema() ToolSchema {
	return ToolSchema{
		Name: "memory_save",
		Description: "Save a fact/preference/note to the user's persistent memory. " +
			"Use this when the user explicitly asks you to remember something across sessions, " +
			"or when you've learned a stable preference worth recalling next time.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"content": map[string]interface{}{
					"type":        "string",
					"description": "The fact or note to remember. Be specific and self-contained.",
				},
				"tags": map[string]interface{}{
					"type":        "array",
					"items":       map[string]interface{}{"type": "string"},
					"description": "Optional tags for categorisation, e.g. ['preference','color'].",
				},
			},
			"required": []string{"content"},
		},
	}
}

func (t *MemorySaveTool) Execute(ctx context.Context, input map[string]interface{}) (string, error) {
	content, _ := input["content"].(string)
	if content == "" {
		return "memory_save error: content is required", nil
	}
	var tags []string
	if raw, ok := input["tags"].([]interface{}); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok && s != "" {
				tags = append(tags, s)
			}
		}
	}
	id, err := t.Provider.Save(ctx, Memory{
		Content:   content,
		Tags:      tags,
		SessionID: t.SessionID,
	})
	if err != nil {
		return fmt.Sprintf("memory_save error: %v", err), nil
	}
	return fmt.Sprintf(`{"saved":true,"id":%d,"tags":%v}`, id, tags), nil
}
