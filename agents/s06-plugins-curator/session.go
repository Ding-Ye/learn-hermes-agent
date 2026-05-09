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

type Session struct {
	ID        string       `json:"id"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
	Model     string       `json:"model"`
	ParentID  string       `json:"parent_id,omitempty"`
	Messages  []Message    `json:"messages"`
	Usage     SessionUsage `json:"usage"`
}

type SessionUsage struct {
	Turns                int `json:"turns"`
	InputTokens          int `json:"input_tokens"`
	OutputTokens         int `json:"output_tokens"`
	CacheReadInputTokens int `json:"cache_read_input_tokens"`
}

func (u *SessionUsage) Add(api Usage) {
	u.Turns++
	u.InputTokens += api.InputTokens
	u.OutputTokens += api.OutputTokens
	u.CacheReadInputTokens += api.CacheReadInputTokens
}

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
	return fmt.Sprintf("s06-%s-%s", now.Format("20060102-150405"), hex.EncodeToString(rb[:]))
}

func (s *Session) Branch() *Session {
	now := time.Now().UTC()
	return &Session{
		ID:        newSessionID(now),
		CreatedAt: now,
		UpdatedAt: now,
		Model:     s.Model,
		ParentID:  s.ID,
		Messages:  append([]Message(nil), s.Messages...),
	}
}

func (s *Session) AppendUser(text string) {
	s.Messages = append(s.Messages, Message{Role: "user", Content: []ContentBlock{{Type: "text", Text: text}}})
	s.UpdatedAt = time.Now().UTC()
}

func (s *Session) AppendAssistant(content []ContentBlock) {
	s.Messages = append(s.Messages, Message{Role: "assistant", Content: content})
	s.UpdatedAt = time.Now().UTC()
}

func (s *Session) AppendUserToolResults(blocks []ContentBlock) {
	s.Messages = append(s.Messages, Message{Role: "user", Content: blocks})
	s.UpdatedAt = time.Now().UTC()
}

type Store struct{ Dir string }

func DefaultStoreDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".learn-hermes-agent", "sessions"), nil
}

func (st *Store) ensureDir() error          { return os.MkdirAll(st.Dir, 0o755) }
func (st *Store) pathFor(id string) string { return filepath.Join(st.Dir, id+".json") }

func (st *Store) Save(s *Session) error {
	if err := st.ensureDir(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	final := st.pathFor(s.ID)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

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
		return nil, err
	}
	return &s, nil
}

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
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

var ErrNotFound = fmt.Errorf("not found")
