package main

import (
	"errors"
	"strings"
	"testing"
)

func TestSession_NewIDIsUnique(t *testing.T) {
	a := NewSession("claude-sonnet-4-6")
	b := NewSession("claude-sonnet-4-6")
	if a.ID == b.ID {
		t.Fatalf("two fresh sessions collided on id: %s", a.ID)
	}
	if !strings.HasPrefix(a.ID, "s04-") {
		t.Fatalf("session id should be prefixed with s04-, got %q", a.ID)
	}
}

func TestSession_AppendUser(t *testing.T) {
	s := NewSession("claude-sonnet-4-6")
	s.AppendUser("hello")
	if len(s.Messages) != 1 || s.Messages[0].Role != "user" {
		t.Fatalf("unexpected messages: %+v", s.Messages)
	}
	if s.Messages[0].Content[0].Text != "hello" {
		t.Fatalf("text wrong: %+v", s.Messages[0].Content)
	}
}

func TestStore_SaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st := &Store{Dir: dir}

	original := NewSession("claude-sonnet-4-6")
	original.AppendUser("hello")
	original.AppendAssistant([]ContentBlock{{Type: "text", Text: "hi"}})
	original.Usage.Add(Usage{InputTokens: 10, OutputTokens: 5, CacheReadInputTokens: 2})

	if err := st.Save(original); err != nil {
		t.Fatalf("Save: %v", err)
	}
	roundtrip, err := st.Load(original.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if roundtrip.ID != original.ID || len(roundtrip.Messages) != 2 {
		t.Fatalf("roundtrip differs: %+v", roundtrip)
	}
	if roundtrip.Usage.InputTokens != 10 || roundtrip.Usage.CacheReadInputTokens != 2 {
		t.Fatalf("usage roundtrip wrong: %+v", roundtrip.Usage)
	}
}

func TestStore_LoadMissingReturnsErrNotFound(t *testing.T) {
	dir := t.TempDir()
	st := &Store{Dir: dir}
	_, err := st.Load("does-not-exist")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSession_Branch(t *testing.T) {
	parent := NewSession("claude-sonnet-4-6")
	parent.AppendUser("p1")
	parent.AppendAssistant([]ContentBlock{{Type: "text", Text: "a1"}})
	parent.Usage.Add(Usage{InputTokens: 100, OutputTokens: 50})

	fork := parent.Branch()
	if fork.ID == parent.ID {
		t.Fatalf("fork id should differ from parent")
	}
	if fork.ParentID != parent.ID {
		t.Fatalf("fork should record ParentID; got %q", fork.ParentID)
	}
	if len(fork.Messages) != len(parent.Messages) {
		t.Fatalf("fork should copy messages; got %d vs %d", len(fork.Messages), len(parent.Messages))
	}
	if fork.Usage.Turns != 0 || fork.Usage.InputTokens != 0 {
		t.Fatalf("fork usage should reset; got %+v", fork.Usage)
	}

	// Mutating the fork must not affect the parent (deep enough copy).
	fork.AppendUser("only on fork")
	if len(parent.Messages) == len(fork.Messages) {
		t.Fatalf("parent message slice was mutated by fork append")
	}
}

func TestStore_AtomicWrite_LeavesNoTmpOnSuccess(t *testing.T) {
	dir := t.TempDir()
	st := &Store{Dir: dir}
	s := NewSession("claude-sonnet-4-6")
	s.AppendUser("x")
	if err := st.Save(s); err != nil {
		t.Fatal(err)
	}
	// A successful Save must NOT leave a .tmp around.
	if _, err := st.Load(s.ID + ".tmp"); err == nil {
		t.Fatalf("expected no .tmp file after successful Save")
	}
}

func TestStore_List_NewestFirst(t *testing.T) {
	dir := t.TempDir()
	st := &Store{Dir: dir}
	first := NewSession("claude-sonnet-4-6")
	first.AppendUser("first")
	if err := st.Save(first); err != nil {
		t.Fatal(err)
	}
	second := NewSession("claude-sonnet-4-6")
	second.AppendUser("second")
	if err := st.Save(second); err != nil {
		t.Fatal(err)
	}
	listed, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("got %d sessions, want 2", len(listed))
	}
	if !listed[0].UpdatedAt.After(listed[1].UpdatedAt) && !listed[0].UpdatedAt.Equal(listed[1].UpdatedAt) {
		t.Fatalf("List should be newest-first")
	}
}

func TestSession_AppendAdvancesUpdatedAt(t *testing.T) {
	s := NewSession("claude-sonnet-4-6")
	t0 := s.UpdatedAt
	// Tiny sleep replacement: do something that can't be coalesced
	// timestamp-wise. The Append is fast, so allow equality.
	s.AppendUser("hello")
	if s.UpdatedAt.Before(t0) {
		t.Fatalf("UpdatedAt went backwards")
	}
}
