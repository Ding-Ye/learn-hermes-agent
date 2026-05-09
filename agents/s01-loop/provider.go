package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Message is a single turn in the conversation. Anthropic's API takes a list
// of these (alternating user / assistant) plus an optional system prompt.
type Message struct {
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
}

// ContentBlock is one item inside a message. Anthropic's wire format is a
// tagged union over Type; the JSON encoder relies on `omitempty` to suppress
// fields that don't apply to a given block type.
type ContentBlock struct {
	Type  string                 `json:"type"`
	Text  string                 `json:"text,omitempty"`
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`

	ToolUseID   string      `json:"tool_use_id,omitempty"`
	ToolContent interface{} `json:"content,omitempty"`
}

type ToolSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

type CreateMessageRequest struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	Messages  []Message    `json:"messages"`
	Tools     []ToolSchema `json:"tools,omitempty"`
	System    string       `json:"system,omitempty"`
}

type CreateMessageResponse struct {
	ID         string         `json:"id"`
	Role       string         `json:"role"`
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      Usage          `json:"usage"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Provider abstracts the LLM call so later sessions can swap Anthropic for
// OpenAI / Bedrock / a fake without touching the loop.
type Provider interface {
	CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error)
}

type AnthropicProvider struct {
	apiKey string
	model  string
	client *http.Client
}

func NewAnthropicProvider(apiKey, model string) *AnthropicProvider {
	return &AnthropicProvider{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

func (a *AnthropicProvider) CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error) {
	if req.Model == "" {
		req.Model = a.model
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = 4096
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("anthropic API %d: %s", resp.StatusCode, string(respBody))
	}

	var out CreateMessageResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w (body=%s)", err, string(respBody))
	}
	return &out, nil
}
