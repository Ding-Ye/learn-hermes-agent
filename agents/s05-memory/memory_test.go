package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func newTestMemory(t *testing.T) *SQLiteMemory {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mem.db")
	mem, err := NewSQLiteMemory(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	return mem
}

func TestSQLite_SaveAndRetrieveByExactWord(t *testing.T) {
	mem := newTestMemory(t)
	ctx := context.Background()

	id, err := mem.Save(ctx, Memory{Content: "user's favorite color is blue", Tags: []string{"preference"}, SessionID: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("expected non-zero id")
	}

	results, err := mem.Search(ctx, "blue", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1: %+v", len(results), results)
	}
	got := results[0]
	if got.Content != "user's favorite color is blue" {
		t.Fatalf("content roundtrip wrong: %q", got.Content)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "preference" {
		t.Fatalf("tags wrong: %v", got.Tags)
	}
	if got.SessionID != "sess-1" {
		t.Fatalf("session_id wrong: %q", got.SessionID)
	}
}

func TestSQLite_RanksByRelevance(t *testing.T) {
	mem := newTestMemory(t)
	ctx := context.Background()

	memories := []string{
		"weather is fine today",
		"docker is running on port 8080",
		"user uses docker for postgres dev environment",
		"dinner reservation at 7pm",
	}
	for _, c := range memories {
		if _, err := mem.Save(ctx, Memory{Content: c}); err != nil {
			t.Fatal(err)
		}
	}

	results, err := mem.Search(ctx, "docker postgres", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatalf("expected matches; got none")
	}
	// Top result should mention BOTH terms (better BM25 score).
	top := results[0]
	if !strings.Contains(top.Content, "docker") || !strings.Contains(top.Content, "postgres") {
		t.Fatalf("expected top result to contain both terms, got %q", top.Content)
	}
}

func TestSQLite_EmptyQueryReturnsEmpty(t *testing.T) {
	mem := newTestMemory(t)
	ctx := context.Background()
	_, _ = mem.Save(ctx, Memory{Content: "anything"})
	results, err := mem.Search(ctx, "   ", 5)
	if err != nil {
		t.Fatalf("empty query should not error, got %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected zero results for empty query, got %d", len(results))
	}
}

func TestSQLite_LimitRespected(t *testing.T) {
	mem := newTestMemory(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, err := mem.Save(ctx, Memory{Content: "alpha gamma omega"})
		if err != nil {
			t.Fatal(err)
		}
	}
	results, err := mem.Search(ctx, "alpha", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("limit=2 should bound results; got %d", len(results))
	}
}

func TestSQLite_TagsRoundtrip(t *testing.T) {
	mem := newTestMemory(t)
	ctx := context.Background()
	_, err := mem.Save(ctx, Memory{Content: "tagged thing", Tags: []string{"a", "b", "c"}})
	if err != nil {
		t.Fatal(err)
	}
	results, _ := mem.Search(ctx, "tagged", 5)
	if len(results) != 1 {
		t.Fatalf("expected 1 result")
	}
	got := results[0].Tags
	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatalf("tags lost: %v", got)
	}
}

func TestMemorySearchTool_Schema(t *testing.T) {
	mem := newTestMemory(t)
	ts := NewMemorySearchTool(mem).Schema()
	if ts.Name != "memory_search" {
		t.Fatalf("name wrong: %q", ts.Name)
	}
	props, ok := ts.InputSchema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("schema properties missing")
	}
	if _, ok := props["query"]; !ok {
		t.Fatalf("schema is missing 'query' property")
	}
}

func TestMemorySaveTool_RoundtripsThroughLLM(t *testing.T) {
	mem := newTestMemory(t)
	ctx := context.Background()
	tool := NewMemorySaveTool(mem, "sess-test")
	out, err := tool.Execute(ctx, map[string]interface{}{
		"content": "the cat is named Mittens",
		"tags":    []interface{}{"pet", "name"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"saved":true`) {
		t.Fatalf("save tool didn't acknowledge: %q", out)
	}

	// Now search and confirm the row landed with the correct session id.
	results, err := mem.Search(ctx, "Mittens", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("save→search roundtrip failed; len=%d", len(results))
	}
	if results[0].SessionID != "sess-test" {
		t.Fatalf("session_id not propagated: %q", results[0].SessionID)
	}
}
