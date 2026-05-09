package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestKanban(t *testing.T) *Kanban {
	t.Helper()
	k, err := OpenKanban(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = k.Close() })
	return k
}

func TestTelegram_Inbound_ParsesUpdate(t *testing.T) {
	tg := NewTelegramPlatform("", nil)
	payload := []byte(`{
		"update_id": 1,
		"message": {
			"from": {"id": 4242, "username": "yeding"},
			"chat": {"id": -987654321},
			"text": "hi bot"
		}
	}`)
	in, err := tg.Inbound(payload)
	if err != nil {
		t.Fatal(err)
	}
	if in.Text != "hi bot" {
		t.Fatalf("text wrong: %q", in.Text)
	}
	if in.ChannelID != "-987654321" {
		t.Fatalf("channel: %q", in.ChannelID)
	}
	if !strings.Contains(in.UserID, "yeding") || !strings.Contains(in.UserID, "4242") {
		t.Fatalf("user_id: %q", in.UserID)
	}
}

func TestTelegram_Inbound_RejectsEmptyText(t *testing.T) {
	tg := NewTelegramPlatform("", nil)
	_, err := tg.Inbound([]byte(`{"message":{"chat":{"id":1},"text":""}}`))
	if err == nil {
		t.Fatal("expected error on empty text")
	}
}

func TestDiscord_Inbound_ParsesAskCommand(t *testing.T) {
	dc := NewDiscordPlatform("", nil)
	payload := []byte(`{
		"type": 2,
		"channel_id": "987654321",
		"member": {"user": {"id": "12345", "username": "yeding"}},
		"data": {
			"name": "ask",
			"options": [{"name": "prompt", "value": "what time is it"}]
		}
	}`)
	in, err := dc.Inbound(payload)
	if err != nil {
		t.Fatal(err)
	}
	if in.Text != "what time is it" {
		t.Fatalf("text: %q", in.Text)
	}
	if in.ChannelID != "987654321" {
		t.Fatalf("channel: %q", in.ChannelID)
	}
	if !strings.Contains(in.UserID, "yeding") {
		t.Fatalf("user_id: %q", in.UserID)
	}
}

func TestDiscord_Inbound_RejectsWrongInteractionType(t *testing.T) {
	dc := NewDiscordPlatform("", nil)
	_, err := dc.Inbound([]byte(`{"type": 1, "channel_id": "x"}`))
	if err == nil {
		t.Fatal("expected error on type=1")
	}
}

// recordingLogger captures Outbound dry-run lines.
type recorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *recorder) log(f string, a ...interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf(f, a...))
}
func (r *recorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.lines))
	copy(out, r.lines)
	return out
}

func TestTelegram_OutboundDryRunLogs(t *testing.T) {
	rec := &recorder{}
	tg := NewTelegramPlatform("" /* dry-run */, rec.log)
	if err := tg.Outbound(context.Background(), "12345", "hello user"); err != nil {
		t.Fatal(err)
	}
	if len(rec.all()) == 0 || !strings.Contains(rec.all()[0], "12345") {
		t.Fatalf("dry-run not logged: %v", rec.all())
	}
}

func TestEndToEnd_TelegramWebhookViaGatewayAndScheduler(t *testing.T) {
	k := newTestKanban(t)
	pr := NewPlatformRegistry()
	rec := &recorder{}
	_ = pr.Register(NewTelegramPlatform("", rec.log))
	_ = pr.Register(NewDiscordPlatform("", rec.log))

	g := NewGateway("127.0.0.1:0", k, pr)
	addr, err := g.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Stop(context.Background()) })

	// scheduler with PlatformAwareRunner over EchoRunner — completes
	// jobs and dispatches replies through the dry-run platforms.
	runner := &PlatformAwareRunner{Inner: EchoRunner{}, Reg: pr, Logger: rec.log}
	s := NewScheduler(k, runner, 50*time.Millisecond, rec.log)
	s.Start()
	t.Cleanup(s.Stop)

	body := strings.NewReader(`{
		"update_id": 1,
		"message": {
			"from": {"id": 4242, "username": "yeding"},
			"chat": {"id": 99},
			"text": "hello via telegram"
		}
	}`)
	resp, err := http.Post("http://"+addr+"/webhook/telegram", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}

	// Wait for the scheduler to land the result.
	deadline := time.Now().Add(5 * time.Second)
	var done *Job
	for time.Now().Before(deadline) {
		jobs, _ := k.ListJobs(context.Background(), 10)
		if len(jobs) > 0 && jobs[0].Status == JobDone {
			done = &jobs[0]
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if done == nil {
		t.Fatalf("job didn't finish in time")
	}
	if !strings.Contains(done.Result, "hello via telegram") {
		t.Fatalf("unexpected result: %q", done.Result)
	}

	// And the dry-run telegram outbound should have been logged.
	saw := false
	for _, ln := range rec.all() {
		if strings.Contains(ln, "telegram dry-run") && strings.Contains(ln, "chat=99") {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("dry-run reply not observed; lines=%v", rec.all())
	}

	// Decode the persisted payload — it should be the JSON Inbound, not raw text.
	var inbound Inbound
	if err := json.Unmarshal([]byte(done.Payload), &inbound); err != nil {
		t.Fatalf("payload not Inbound JSON: %v", err)
	}
	if inbound.ChannelID != "99" || inbound.Text != "hello via telegram" {
		t.Fatalf("inbound roundtrip wrong: %+v", inbound)
	}
}
