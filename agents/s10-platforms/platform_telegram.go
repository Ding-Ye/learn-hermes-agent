package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// TelegramPlatform parses Telegram bot webhook updates and (in
// non-dry-run mode) replies back via the Bot API.
//
// Webhook update shape (the subset we care about):
//
//   {
//     "update_id": 12345,
//     "message": {
//       "message_id": 1,
//       "from":  {"id": 4242, "username": "yeding"},
//       "chat":  {"id": -987654321, "type": "supergroup"},
//       "date":  1702300000,
//       "text":  "hi bot"
//     }
//   }
//
// Real bot deployments register this URL with Telegram via setWebhook;
// Telegram POSTs each update to it.
type TelegramPlatform struct {
	BotToken string // empty = dry-run mode (Outbound prints, doesn't POST)
	BaseURL  string // override for tests (default https://api.telegram.org)
	Logger   func(string, ...interface{})
}

func NewTelegramPlatform(botToken string, logger func(string, ...interface{})) *TelegramPlatform {
	if logger == nil {
		logger = func(string, ...interface{}) {}
	}
	return &TelegramPlatform{
		BotToken: botToken,
		BaseURL:  "https://api.telegram.org",
		Logger:   logger,
	}
}

func (t *TelegramPlatform) Name() string { return "telegram" }

func (t *TelegramPlatform) Inbound(raw []byte) (*Inbound, error) {
	var update struct {
		Message *struct {
			From *struct {
				ID       int64  `json:"id"`
				Username string `json:"username"`
			} `json:"from"`
			Chat *struct {
				ID int64 `json:"id"`
			} `json:"chat"`
			Text string `json:"text"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &update); err != nil {
		return nil, fmt.Errorf("telegram: bad json: %w", err)
	}
	if update.Message == nil || update.Message.Chat == nil {
		return nil, fmt.Errorf("telegram: update without message.chat")
	}
	if strings.TrimSpace(update.Message.Text) == "" {
		return nil, fmt.Errorf("telegram: empty text")
	}
	uid := ""
	if update.Message.From != nil {
		uid = strconv.FormatInt(update.Message.From.ID, 10)
		if update.Message.From.Username != "" {
			uid = update.Message.From.Username + "#" + uid
		}
	}
	return &Inbound{
		ChannelID: strconv.FormatInt(update.Message.Chat.ID, 10),
		UserID:    uid,
		Text:      update.Message.Text,
	}, nil
}

func (t *TelegramPlatform) Outbound(ctx context.Context, channelID, text string) error {
	if t.BotToken == "" {
		t.Logger("[telegram dry-run] would send to chat=%s: %s", channelID, truncateLine(text, 200))
		return nil
	}
	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", t.BaseURL, t.BotToken)
	form := url.Values{}
	form.Set("chat_id", channelID)
	form.Set("text", text)
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("telegram sendMessage: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("telegram sendMessage %d: %s", resp.StatusCode, body)
	}
	return nil
}

func truncateLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
