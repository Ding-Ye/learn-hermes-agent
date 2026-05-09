package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// ============================================================================
// translateRequestToOpenAI — one Anthropic-style request → OpenAI shape
// ============================================================================

func TestTranslateRequest_PlainTextUser(t *testing.T) {
	req := CreateMessageRequest{
		System: "You are helpful.",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "Hi"}}},
		},
	}
	out := translateRequestToOpenAI(req, "deepseek-chat", 1024)
	if out.Model != "deepseek-chat" || out.MaxTokens != 1024 {
		t.Fatalf("model/max_tokens wrong: %+v", out)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("expected system+user = 2 messages, got %d", len(out.Messages))
	}
	if out.Messages[0].Role != "system" || out.Messages[0].Content != "You are helpful." {
		t.Fatalf("system message wrong: %+v", out.Messages[0])
	}
	if out.Messages[1].Role != "user" || out.Messages[1].Content != "Hi" {
		t.Fatalf("user message wrong: %+v", out.Messages[1])
	}
}

func TestTranslateRequest_AssistantToolUseBecomesToolCalls(t *testing.T) {
	req := CreateMessageRequest{
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "compute"}}},
			{Role: "assistant", Content: []ContentBlock{
				{Type: "text", Text: "Let me compute."},
				{Type: "tool_use", ID: "toolu_001", Name: "bash", Input: map[string]interface{}{"command": "echo $((2+2))"}},
			}},
		},
	}
	out := translateRequestToOpenAI(req, "x", 0)
	// 2 messages: user + assistant
	if len(out.Messages) != 2 {
		t.Fatalf("got %d messages: %+v", len(out.Messages), out.Messages)
	}
	asst := out.Messages[1]
	if asst.Role != "assistant" {
		t.Fatalf("expected assistant, got %s", asst.Role)
	}
	if asst.Content != "Let me compute." {
		t.Fatalf("text content wrong: %v", asst.Content)
	}
	if len(asst.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool_call, got %d", len(asst.ToolCalls))
	}
	tc := asst.ToolCalls[0]
	if tc.ID != "toolu_001" || tc.Function.Name != "bash" {
		t.Fatalf("tool_call wrong: %+v", tc)
	}
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		t.Fatalf("arguments not JSON: %q", tc.Function.Arguments)
	}
	if args["command"] != "echo $((2+2))" {
		t.Fatalf("arguments lost: %v", args)
	}
}

func TestTranslateRequest_UserToolResultBecomesToolMessages(t *testing.T) {
	req := CreateMessageRequest{
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{
				{Type: "tool_result", ToolUseID: "toolu_001", ToolContent: "4"},
			}},
		},
	}
	out := translateRequestToOpenAI(req, "x", 0)
	if len(out.Messages) != 1 {
		t.Fatalf("expected 1 tool message, got %d", len(out.Messages))
	}
	m := out.Messages[0]
	if m.Role != "tool" || m.ToolCallID != "toolu_001" || m.Content != "4" {
		t.Fatalf("tool message wrong: %+v", m)
	}
}

func TestTranslateRequest_MixedUserBlocks(t *testing.T) {
	// Anthropic allows a single user message to mix text + tool_result.
	// OpenAI doesn't; we split into separate messages.
	req := CreateMessageRequest{
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{
				{Type: "text", Text: "And another thing."},
				{Type: "tool_result", ToolUseID: "toolu_001", ToolContent: "ok"},
			}},
		},
	}
	out := translateRequestToOpenAI(req, "x", 0)
	if len(out.Messages) != 2 {
		t.Fatalf("expected user + tool messages, got %d", len(out.Messages))
	}
	if out.Messages[0].Role != "user" || out.Messages[1].Role != "tool" {
		t.Fatalf("ordering wrong: %+v", out.Messages)
	}
}

func TestTranslateRequest_ToolsFlatToFunctions(t *testing.T) {
	req := CreateMessageRequest{
		Tools: []ToolSchema{
			{
				Name:        "bash",
				Description: "Run a shell command",
				InputSchema: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"command": map[string]interface{}{"type": "string"},
					},
					"required": []string{"command"},
				},
			},
		},
	}
	out := translateRequestToOpenAI(req, "x", 0)
	if len(out.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(out.Tools))
	}
	if out.Tools[0].Type != "function" {
		t.Fatalf("type wrong: %s", out.Tools[0].Type)
	}
	if out.Tools[0].Function.Name != "bash" {
		t.Fatalf("name lost: %s", out.Tools[0].Function.Name)
	}
	props, ok := out.Tools[0].Function.Parameters["properties"].(map[string]interface{})
	if !ok || props["command"] == nil {
		t.Fatalf("parameters lost: %+v", out.Tools[0].Function.Parameters)
	}
}

// ============================================================================
// translateResponseFromOpenAI — OpenAI response → Anthropic-style
// ============================================================================

func TestTranslateResponse_PlainTextStop(t *testing.T) {
	resp := &openAIChatCompletionResp{
		Choices: []openAIChatChoice{
			{Index: 0, FinishReason: "stop", Message: openAIMessage{Role: "assistant", Content: "Hello!"}},
		},
		Usage: openAIUsage{PromptTokens: 10, CompletionTokens: 5},
	}
	out := translateResponseFromOpenAI(resp)
	if out.StopReason != "end_turn" {
		t.Fatalf("stop_reason: %q want end_turn", out.StopReason)
	}
	if len(out.Content) != 1 || out.Content[0].Type != "text" || out.Content[0].Text != "Hello!" {
		t.Fatalf("content wrong: %+v", out.Content)
	}
	if out.Usage.InputTokens != 10 || out.Usage.OutputTokens != 5 {
		t.Fatalf("usage wrong: %+v", out.Usage)
	}
}

func TestTranslateResponse_ToolCallsBecomeToolUse(t *testing.T) {
	resp := &openAIChatCompletionResp{
		Choices: []openAIChatChoice{
			{
				Index:        0,
				FinishReason: "tool_calls",
				Message: openAIMessage{
					Role: "assistant",
					ToolCalls: []openAIToolCall{
						{
							ID:   "call_abc123",
							Type: "function",
							Function: openAIToolCallFunc{
								Name:      "bash",
								Arguments: `{"command":"date"}`,
							},
						},
					},
				},
			},
		},
	}
	out := translateResponseFromOpenAI(resp)
	if out.StopReason != "tool_use" {
		t.Fatalf("stop_reason: %q want tool_use", out.StopReason)
	}
	if len(out.Content) != 1 {
		t.Fatalf("content blocks: %d", len(out.Content))
	}
	b := out.Content[0]
	if b.Type != "tool_use" || b.ID != "call_abc123" || b.Name != "bash" {
		t.Fatalf("tool_use wrong: %+v", b)
	}
	if b.Input["command"] != "date" {
		t.Fatalf("input lost: %+v", b.Input)
	}
}

func TestTranslateResponse_LengthBecomesMaxTokens(t *testing.T) {
	resp := &openAIChatCompletionResp{
		Choices: []openAIChatChoice{{FinishReason: "length", Message: openAIMessage{Role: "assistant", Content: "trunc"}}},
	}
	if got := translateResponseFromOpenAI(resp).StopReason; got != "max_tokens" {
		t.Fatalf("got %q want max_tokens", got)
	}
}

func TestTranslateResponse_MalformedArgumentsPreservedAsRaw(t *testing.T) {
	resp := &openAIChatCompletionResp{
		Choices: []openAIChatChoice{{FinishReason: "tool_calls", Message: openAIMessage{
			Role: "assistant",
			ToolCalls: []openAIToolCall{
				{ID: "id1", Type: "function", Function: openAIToolCallFunc{Name: "x", Arguments: "not json"}},
			},
		}}},
	}
	out := translateResponseFromOpenAI(resp)
	if out.Content[0].Input["_raw_arguments"] != "not json" {
		t.Fatalf("malformed args not preserved: %+v", out.Content[0].Input)
	}
}

// Some OpenAI-compatible servers (notably DeepSeek when content is structured)
// return content as an array of {type:"text", text:"..."}. Make sure we cope.
func TestTranslateResponse_StructuredContentArray(t *testing.T) {
	resp := &openAIChatCompletionResp{
		Choices: []openAIChatChoice{{FinishReason: "stop", Message: openAIMessage{
			Role: "assistant",
			Content: []interface{}{
				map[string]interface{}{"type": "text", "text": "Hello "},
				map[string]interface{}{"type": "text", "text": "world!"},
			},
		}}},
	}
	out := translateResponseFromOpenAI(resp)
	if len(out.Content) != 1 || !strings.Contains(out.Content[0].Text, "Hello world!") {
		t.Fatalf("structured content not concatenated: %+v", out.Content)
	}
}
