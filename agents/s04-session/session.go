package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Session is the persistent shape of one conversation. The struct serialises
// directly as JSON — no migrations layer in s04, but adding new optional
// fields stays backwards compatible because Go's json package ignores
// unknown fields on decode.
//
// hermes calls this entity a "session" and stores one per file. Same here.
type Session struct {
	ID        string       `json:"id"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
	Model     string       `json:"model"`
	ParentID  string       `json:"parent_id,omitempty"` // set on branch
	Messages  []Message    `json:"messages"`
	Usage     SessionUsage `json:"usage"`
}

// SessionUsage tracks running totals across every turn of this session.
// Mirrors what hermes records (see hermes_cli session metadata): a per-turn
// counter for input tokens, output tokens, and cache-read tokens.
type SessionUsage struct {
	Turns                int `json:"turns"`
	InputTokens          int `json:"input_tokens"`
	OutputTokens         int `json:"output_tokens"`
	CacheReadInputTokens int `json:"cache_read_input_tokens"`
}

// Add accumulates one turn's API usage into the running totals.
func (u *SessionUsage) Add(api Usage) {
	u.Turns++
	u.InputTokens += api.InputTokens
	u.OutputTokens += api.OutputTokens
	u.CacheReadInputTokens += api.CacheReadInputTokens
}

// NewSession creates a fresh session with a fresh ID. The ID format is
// human-readable and roughly sortable: s04-YYYYMMDD-HHMMSS-<6 hex>.
func NewSession(model string) *Session {
	now := time.Now().UTC()
	return &Session{
		ID:        newSessionID(now),
		CreatedAt: now,
		UpdatedAt: now,
		Model:     model,
		Messages:  []Message{},
	}
}

func newSessionID(now time.Time) string {
	var rb [3]byte
	_, _ = rand.Read(rb[:])
	return fmt.Sprintf("s04-%s-%s", now.Format("20060102-150405"), hex.EncodeToString(rb[:]))
}

// Branch forks the receiver into a new session with a new ID. The forked
// session inherits the messages list (deep-copied to avoid aliasing the
// original's slice), sets ParentID, and resets usage to a fresh budget.
//
// "Reset usage" is a teaching choice — every fork is its own balance
// sheet. If you wanted lineage-aware accounting, sum forwards from
// ParentID at report time.
func (s *Session) Branch() *Session {
	now := time.Now().UTC()
	fork := &Session{
		ID:        newSessionID(now),
		CreatedAt: now,
		UpdatedAt: now,
		Model:     s.Model,
		ParentID:  s.ID,
		Messages:  append([]Message(nil), s.Messages...),
		// Usage left zero on purpose.
	}
	return fork
}

// AppendUser puts a fresh user-text message at the end. Used by main.go
// before calling Loop.Run for both /new and /resume flows.
func (s *Session) AppendUser(text string) {
	s.Messages = append(s.Messages, Message{
		Role:    "user",
		Content: []ContentBlock{{Type: "text", Text: text}},
	})
	s.UpdatedAt = time.Now().UTC()
}

// AppendAssistant adds an assistant turn (may contain text and/or tool_use
// blocks). Called by the loop after every successful CreateMessage.
func (s *Session) AppendAssistant(content []ContentBlock) {
	s.Messages = append(s.Messages, Message{Role: "assistant", Content: content})
	s.UpdatedAt = time.Now().UTC()
}

// AppendUserToolResults adds a user-role message carrying tool_result
// blocks. Anthropic protocol requires tool results to come from "user".
func (s *Session) AppendUserToolResults(blocks []ContentBlock) {
	s.Messages = append(s.Messages, Message{Role: "user", Content: blocks})
	s.UpdatedAt = time.Now().UTC()
}

// ----------------------------------------------------------- persistence

// Store is the on-disk session store, rooted at one directory.
type Store struct {
	Dir string
}

// DefaultStoreDir returns ~/.learn-hermes-agent/sessions, mirroring the
// hermes convention of ~/.hermes/sessions.
func DefaultStoreDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".learn-hermes-agent", "sessions"), nil
}

func (st *Store) ensureDir() error {
	return os.MkdirAll(st.Dir, 0o755)
}

func (st *Store) pathFor(id string) string {
	return filepath.Join(st.Dir, id+".json")
}

// Save writes the session via temp-file + rename for atomicity. A power
// failure mid-write either leaves the previous good copy intact or yields
// a fully-written new copy — never a half-written one.
func (st *Store) Save(s *Session) error {
	if err := st.ensureDir(); err != nil {
		return fmt.Errorf("ensure session dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	final := st.pathFor(s.ID)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp) // best-effort cleanup
		return fmt.Errorf("rename tmp -> final: %w", err)
	}
	return nil
}

// Load reads a session by ID. Returns ErrNotFound if no file exists.
func (st *Store) Load(id string) (*Session, error) {
	data, err := os.ReadFile(st.pathFor(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("session %q: %w", id, ErrNotFound)
		}
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("decode session %q: %w", id, err)
	}
	return &s, nil
}

// Delete removes a session file. Idempotent.
func (st *Store) Delete(id string) error {
	err := os.Remove(st.pathFor(id))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// List returns all sessions in the store, newest first by UpdatedAt.
func (st *Store) List() ([]*Session, error) {
	entries, err := os.ReadDir(st.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Session
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		id := e.Name()[:len(e.Name())-len(".json")]
		s, err := st.Load(id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[session] skipping %s: %v\n", id, err)
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// ErrNotFound is returned by Load when no session matches the given id.
var ErrNotFound = fmt.Errorf("not found")
