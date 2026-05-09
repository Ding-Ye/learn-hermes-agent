package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DiscordPlatform speaks the Discord interactions endpoint flavour:
// users invoke a slash command, Discord POSTs an interaction payload,
// our handler responds with `type: 4` (channel message). For brevity
// we ignore signing verification; production must verify the
// X-Signature-Ed25519 header against the application's public key.
//
// Interaction payload shape (subset):
//
//   {
//     "type": 2,                       // application command
//     "channel_id": "987654321",
//     "member": { "user": { "id": "12345", "username": "yeding" } },
//     "data": {
//       "name": "ask",
//       "options": [ { "name": "prompt", "value": "hello" } ]
//     }
//   }
type DiscordPlatform struct {
	BotToken      string  // empty = dry-run mode
	WebhookID     string  // application id; for follow-up messages
	BaseURL       string  // default https://discord.com/api/v10
	CommandName   string  // expected slash command name; default "ask"
	PromptOption  string  // option name carrying the user prompt; default "prompt"
	Logger        func(string, ...interface{})
}

func NewDiscordPlatform(botToken string, logger func(string, ...interface{})) *DiscordPlatform {
	if logger == nil {
		logger = func(string, ...interface{}) {}
	}
	return &DiscordPlatform{
		BotToken:     botToken,
		BaseURL:      "https://discord.com/api/v10",
		CommandName:  "ask",
		PromptOption: "prompt",
		Logger:       logger,
	}
}

func (d *DiscordPlatform) Name() string { return "discord" }

func (d *DiscordPlatform) Inbound(raw []byte) (*Inbound, error) {
	var ix struct {
		Type      int    `json:"type"`
		ChannelID string `json:"channel_id"`
		Member    *struct {
			User *struct {
				ID       string `json:"id"`
				Username string `json:"username"`
			} `json:"user"`
		} `json:"member"`
		Data *struct {
			Name    string `json:"name"`
			Options []struct {
				Name  string      `json:"name"`
				Value interface{} `json:"value"`
			} `json:"options"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &ix); err != nil {
		return nil, fmt.Errorf("discord: bad json: %w", err)
	}
	if ix.Type != 2 {
		return nil, fmt.Errorf("discord: unsupported interaction type %d", ix.Type)
	}
	if ix.ChannelID == "" {
		return nil, fmt.Errorf("discord: missing channel_id")
	}
	if ix.Data == nil || ix.Data.Name != d.CommandName {
		return nil, fmt.Errorf("discord: not /%s slash command", d.CommandName)
	}
	var text string
	for _, opt := range ix.Data.Options {
		if opt.Name == d.PromptOption {
			if s, ok := opt.Value.(string); ok {
				text = s
			}
		}
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("discord: %s option empty", d.PromptOption)
	}
	uid := ""
	if ix.Member != nil && ix.Member.User != nil {
		uid = ix.Member.User.Username + "#" + ix.Member.User.ID
	}
	return &Inbound{
		ChannelID: ix.ChannelID,
		UserID:    uid,
		Text:      text,
	}, nil
}

func (d *DiscordPlatform) Outbound(ctx context.Context, channelID, text string) error {
	if d.BotToken == "" {
		d.Logger("[discord dry-run] would post to channel=%s: %s", channelID, truncateLine(text, 200))
		return nil
	}
	endpoint := fmt.Sprintf("%s/channels/%s/messages", d.BaseURL, channelID)
	body, _ := json.Marshal(map[string]string{"content": text})
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bot "+d.BotToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("discord sendMessage: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("discord sendMessage %d: %s", resp.StatusCode, respBody)
	}
	return nil
}
