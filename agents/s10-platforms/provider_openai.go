package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAIProvider works with ANY OpenAI-compatible Chat Completions API:
// OpenAI itself, DeepSeek, Moonshot/Kimi, Qwen via DashScope's
// OpenAI-compatible endpoint, Together AI, Groq, OpenRouter,
// self-hosted vLLM / SGLang, etc.
//
// We keep the agent loop's INTERNAL types Anthropic-style (Message,
// ContentBlock with type=tool_use/tool_result, stop_reason). This
// provider does the wire-format translation in both directions:
//
//	Anthropic-style request → OpenAI Chat Completions request
//	OpenAI Chat Completions response → Anthropic-style response
//
// Why? Because s07's MCP design and the agent loop's protocol notes
// (s01 doc) are written against the Anthropic block model. Translating
// at the provider boundary keeps every other file unchanged.
type OpenAIProvider struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client
}

// NewOpenAIProvider builds a provider against the given base URL.
// Common values:
//
//	https://api.openai.com/v1
//	https://api.deepseek.com/v1
//	https://api.moonshot.cn/v1                                (Moonshot/Kimi)
//	https://dashscope.aliyuncs.com/compatible-mode/v1         (Qwen / 通义千问)
//	https://api.groq.com/openai/v1                            (Groq)
//	https://openrouter.ai/api/v1                              (OpenRouter)
//	http://localhost:8000/v1                                  (self-hosted vLLM/SGLang)
func NewOpenAIProvider(apiKey, baseURL, model string) *OpenAIProvider {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAIProvider{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

func (o *OpenAIProvider) CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error) {
	model := req.Model
	if model == "" {
		model = o.model
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	oaiReq := translateRequestToOpenAI(req, model, maxTokens)
	body, err := json.Marshal(oaiReq)
	if err != nil {
		return nil, fmt.Errorf("encode openai request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		o.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("openai-compat API %d: %s", resp.StatusCode, string(respBody))
	}

	var oaiResp openAIChatCompletionResp
	if err := json.Unmarshal(respBody, &oaiResp); err != nil {
		return nil, fmt.Errorf("decode openai response: %w (body=%s)", err, string(respBody))
	}
	return translateResponseFromOpenAI(&oaiResp), nil
}

// ---------------------------------------------------------------- wire types

type openAIChatRequest struct {
	Model     string             `json:"model"`
	Messages  []openAIMessage    `json:"messages"`
	Tools     []openAITool       `json:"tools,omitempty"`
	MaxTokens int                `json:"max_tokens,omitempty"`
}

type openAIMessage struct {
	Role       string           `json:"role"`              // "system" | "user" | "assistant" | "tool"
	Content    interface{}      `json:"content,omitempty"` // string OR null when tool_calls is present
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"` // only for role="tool"
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"` // "function"
	Function openAIToolCallFunc `json:"function"`
}

type openAIToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded string
}

type openAITool struct {
	Type     string        `json:"type"` // "function"
	Function openAIToolDef `json:"function"`
}

type openAIToolDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters"`
}

type openAIChatCompletionResp struct {
	Choices []openAIChatChoice `json:"choices"`
	Usage   openAIUsage        `json:"usage"`
}

type openAIChatChoice struct {
	Index        int           `json:"index"`
	Message      openAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// --------------------------------------------------- translation: out (req)

// translateRequestToOpenAI converts our Anthropic-style request into an
// OpenAI Chat Completions request, in three passes:
//
//  1. system prompt becomes a {role:"system"} message
//  2. each Anthropic message expands into 1+ OpenAI messages; assistant
//     tool_use blocks become tool_calls on the assistant message; user
//     tool_result blocks become separate {role:"tool"} messages
//  3. tool definitions get wrapped under {type:"function", function:{...}}
func translateRequestToOpenAI(req CreateMessageRequest, model string, maxTokens int) openAIChatRequest {
	out := openAIChatRequest{
		Model:     model,
		MaxTokens: maxTokens,
	}
	if req.System != "" {
		out.Messages = append(out.Messages, openAIMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		out.Messages = append(out.Messages, anthropicMessageToOpenAI(m)...)
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, openAITool{
			Type: "function",
			Function: openAIToolDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}
	return out
}

// anthropicMessageToOpenAI handles the four cases:
//
//	user   + text blocks      → one user message with concatenated text
//	user   + tool_result      → one tool message PER tool_result block
//	assistant + text          → one assistant message with content
//	assistant + tool_use      → one assistant message with tool_calls
//
// A single Anthropic user message can contain BOTH text and tool_result,
// in which case we emit a user message followed by N tool messages.
func anthropicMessageToOpenAI(m Message) []openAIMessage {
	var out []openAIMessage
	switch m.Role {
	case "user":
		var texts []string
		var tools []openAIMessage
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				if b.Text != "" {
					texts = append(texts, b.Text)
				}
			case "tool_result":
				content := stringifyToolResult(b.ToolContent)
				tools = append(tools, openAIMessage{
					Role:       "tool",
					ToolCallID: b.ToolUseID,
					Content:    content,
				})
			}
		}
		if len(texts) > 0 {
			out = append(out, openAIMessage{Role: "user", Content: strings.Join(texts, "\n")})
		}
		out = append(out, tools...)

	case "assistant":
		var texts []string
		var calls []openAIToolCall
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				if b.Text != "" {
					texts = append(texts, b.Text)
				}
			case "tool_use":
				args, _ := json.Marshal(b.Input)
				calls = append(calls, openAIToolCall{
					ID:   b.ID,
					Type: "function",
					Function: openAIToolCallFunc{
						Name:      b.Name,
						Arguments: string(args),
					},
				})
			}
		}
		msg := openAIMessage{Role: "assistant"}
		if len(texts) > 0 {
			msg.Content = strings.Join(texts, "\n")
		}
		if len(calls) > 0 {
			msg.ToolCalls = calls
		}
		out = append(out, msg)
	}
	return out
}

func stringifyToolResult(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	if data, err := json.Marshal(v); err == nil {
		return string(data)
	}
	return fmt.Sprintf("%v", v)
}

// --------------------------------------------------- translation: in (resp)

// translateResponseFromOpenAI converts an OpenAI Chat Completions response
// back into our Anthropic-style CreateMessageResponse.
//
// finish_reason mapping:
//
//	"stop"        → "end_turn"
//	"tool_calls"  → "tool_use"
//	"length"      → "max_tokens"
//	other         → "end_turn" (best-effort; some providers emit "function_call")
func translateResponseFromOpenAI(resp *openAIChatCompletionResp) *CreateMessageResponse {
	out := &CreateMessageResponse{
		Usage: Usage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		},
	}
	if len(resp.Choices) == 0 {
		out.StopReason = "end_turn"
		return out
	}
	choice := resp.Choices[0]
	if text, ok := contentToString(choice.Message.Content); ok && strings.TrimSpace(text) != "" {
		out.Content = append(out.Content, ContentBlock{Type: "text", Text: text})
	}
	for _, tc := range choice.Message.ToolCalls {
		var input map[string]interface{}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
			input = map[string]interface{}{"_raw_arguments": tc.Function.Arguments}
		}
		out.Content = append(out.Content, ContentBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: input,
		})
	}
	switch choice.FinishReason {
	case "stop", "":
		out.StopReason = "end_turn"
	case "tool_calls", "function_call":
		out.StopReason = "tool_use"
	case "length":
		out.StopReason = "max_tokens"
	default:
		out.StopReason = "end_turn"
	}
	return out
}

func contentToString(v interface{}) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case nil:
		return "", false
	case []interface{}:
		// Some providers return content as an array of {type:"text", text:"..."} blocks
		var parts []string
		for _, item := range x {
			if m, ok := item.(map[string]interface{}); ok {
				if t, ok := m["type"].(string); ok && t == "text" {
					if txt, ok := m["text"].(string); ok {
						parts = append(parts, txt)
					}
				}
			}
		}
		return strings.Join(parts, ""), len(parts) > 0
	}
	return "", false
}
