package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
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

func TestKanban_EnqueueListClaim(t *testing.T) {
	k := newTestKanban(t)
	ctx := context.Background()

	id1, err := k.EnqueueJob(ctx, "cli", "first")
	if err != nil {
		t.Fatal(err)
	}
	if id1 == 0 {
		t.Fatal("expected nonzero id")
	}

	jobs, err := k.ListJobs(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Status != JobPending {
		t.Fatalf("want one pending, got %+v", jobs)
	}

	j, ok, err := k.ClaimNextPending(ctx)
	if err != nil || !ok {
		t.Fatalf("claim: %v ok=%v", err, ok)
	}
	if j.ID != id1 || j.Status != JobRunning {
		t.Fatalf("claimed wrong job: %+v", j)
	}

	_, ok, _ = k.ClaimNextPending(ctx)
	if ok {
		t.Fatal("a second claim should miss")
	}

	if err := k.FinishJob(ctx, j.ID, "did it", nil); err != nil {
		t.Fatal(err)
	}
	final, _ := k.GetJob(ctx, j.ID)
	if final.Status != JobDone || final.Result != "did it" {
		t.Fatalf("finish state wrong: %+v", final)
	}
}

func TestKanban_FinishJobFailureRecordsError(t *testing.T) {
	k := newTestKanban(t)
	ctx := context.Background()
	id, _ := k.EnqueueJob(ctx, "cli", "boom")
	_, _, _ = k.ClaimNextPending(ctx)
	_ = k.FinishJob(ctx, id, "partial", fmt.Errorf("kaboom"))
	j, _ := k.GetJob(ctx, id)
	if j.Status != JobFailed || j.Error != "kaboom" || j.Result != "partial" {
		t.Fatalf("failed state wrong: %+v", j)
	}
}

func TestKanban_CronAddAndDue(t *testing.T) {
	k := newTestKanban(t)
	ctx := context.Background()
	if _, err := k.AddCronEntry(ctx, "every 1s", "tick"); err != nil {
		t.Fatal(err)
	}
	due, err := k.DueCronEntries(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("expected fresh entry to be due, got %d", len(due))
	}
	_ = k.TouchCronEntry(ctx, due[0].ID)
	due2, _ := k.DueCronEntries(ctx, time.Now())
	if len(due2) != 0 {
		t.Fatalf("expected no due entries right after touch, got %d", len(due2))
	}
	due3, _ := k.DueCronEntries(ctx, time.Now().Add(2*time.Second))
	if len(due3) != 1 {
		t.Fatalf("expected entry to be due again 2s later, got %d", len(due3))
	}
}

func TestKanban_RejectsBadCronSpec(t *testing.T) {
	k := newTestKanban(t)
	cases := []string{"", "5m", "every potato", "every 100ms"}
	for _, c := range cases {
		if _, err := k.AddCronEntry(context.Background(), c, "x"); err == nil {
			t.Fatalf("expected error for spec %q", c)
		}
	}
}

func TestGateway_PostMsgEnqueuesJob(t *testing.T) {
	k := newTestKanban(t)
	g := NewGateway("127.0.0.1:0", k)
	addr, err := g.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = g.Stop(context.Background())
	})

	body := strings.NewReader(`{"source":"telegram","prompt":"hello scheduler"}`)
	resp, err := http.Post("http://"+addr+"/msg", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var out map[string]int64
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["job_id"] == 0 {
		t.Fatalf("no job_id returned: %v", out)
	}

	jobs, _ := k.ListJobs(context.Background(), 10)
	if len(jobs) != 1 || jobs[0].Source != "telegram" {
		t.Fatalf("expected one telegram job, got %+v", jobs)
	}
}

func TestEndToEnd_GatewayPlusScheduler(t *testing.T) {
	k := newTestKanban(t)
	g := NewGateway("127.0.0.1:0", k)
	addr, _ := g.Start()
	t.Cleanup(func() { _ = g.Stop(context.Background()) })

	logs := make(chan string, 32)
	logger := func(f string, a ...interface{}) {
		select {
		case logs <- fmt.Sprintf(f, a...):
		default:
		}
	}
	sched := NewScheduler(k, EchoRunner{}, 50*time.Millisecond, logger)
	sched.Start()
	t.Cleanup(sched.Stop)

	// post a message
	body := strings.NewReader(`{"source":"e2e","prompt":"do the thing"}`)
	resp, err := http.Post("http://"+addr+"/msg", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// poll until the scheduler completes the job (with a hard timeout)
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
		t.Fatalf("job did not complete within deadline")
	}
	if done.Result != "echo: do the thing" {
		t.Fatalf("unexpected result: %q", done.Result)
	}
}
