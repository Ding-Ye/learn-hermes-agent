package main

import (
	"context"
	"fmt"
)

// Platform is the gateway-side adapter contract. A Platform takes raw
// inbound bytes from a webhook, translates them into a uniform inbound
// shape, and writes them to the Kanban. On the way out, it takes a
// completed job's result and routes it back to the right channel.
//
// One Platform per IM service: Telegram, Discord, Slack, WhatsApp,
// Signal — hermes ships seven. Our s10 ships two real adapters
// (Telegram and Discord) that demonstrate the shape.
type Platform interface {
	Name() string

	// Inbound parses a webhook body. Returns the normalised payload
	// the Gateway will pass to kanban.EnqueueJob.
	Inbound(rawBody []byte) (*Inbound, error)

	// Outbound takes a finished job result and asks the platform to
	// deliver it. In production this is an API call (Telegram
	// sendMessage, Discord interactions follow-up, etc.). Our s10
	// runs in dry-run mode by default and just prints what it would
	// have sent.
	Outbound(ctx context.Context, channelID, text string) error
}

// Inbound is the normalised shape every Platform produces. The Gateway
// stores `payload` as JSON in the kanban row. `channel_id` and `user_id`
// are kept separate so Outbound knows where to deliver a reply.
type Inbound struct {
	ChannelID string `json:"channel_id"`
	UserID    string `json:"user_id"`
	Text      string `json:"text"`
}

// PlatformRegistry routes webhooks to the right adapter and replies
// back via the same one. Registration is per-name; the gateway URL
// path /webhook/<name> is the routing key.
type PlatformRegistry struct {
	platforms map[string]Platform
}

func NewPlatformRegistry() *PlatformRegistry {
	return &PlatformRegistry{platforms: map[string]Platform{}}
}

func (r *PlatformRegistry) Register(p Platform) error {
	if p == nil {
		return fmt.Errorf("nil platform")
	}
	if p.Name() == "" {
		return fmt.Errorf("platform with empty name")
	}
	if _, exists := r.platforms[p.Name()]; exists {
		return fmt.Errorf("platform %q already registered", p.Name())
	}
	r.platforms[p.Name()] = p
	return nil
}

func (r *PlatformRegistry) Get(name string) (Platform, bool) {
	p, ok := r.platforms[name]
	return p, ok
}

func (r *PlatformRegistry) Names() []string {
	out := make([]string, 0, len(r.platforms))
	for n := range r.platforms {
		out = append(out, n)
	}
	return out
}
