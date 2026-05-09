package main

import (
	"context"
	"encoding/json"
	"fmt"
)

type MemorySearchTool struct{ Provider MemoryProvider }

func NewMemorySearchTool(p MemoryProvider) *MemorySearchTool { return &MemorySearchTool{Provider: p} }

func (t *MemorySearchTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "memory_search",
		Description: "Search persistent memory (FTS5). Returns JSON array ordered by relevance. Hits also bump last_activity_at so the curator won't archive recently-used memories.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string"},
				"limit": map[string]interface{}{"type": "integer"},
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

type MemorySaveTool struct {
	Provider  MemoryProvider
	SessionID string
}

func NewMemorySaveTool(p MemoryProvider, sid string) *MemorySaveTool {
	return &MemorySaveTool{Provider: p, SessionID: sid}
}

func (t *MemorySaveTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "memory_save",
		Description: "Save a fact/preference/note to persistent memory.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"content": map[string]interface{}{"type": "string"},
				"tags": map[string]interface{}{
					"type":  "array",
					"items": map[string]interface{}{"type": "string"},
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
