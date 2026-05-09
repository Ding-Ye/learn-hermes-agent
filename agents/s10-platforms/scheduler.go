package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// JobRunner is the contract the Scheduler uses to actually execute a
// job's prompt. Plugged in by main; for tests we substitute a fake.
type JobRunner interface {
	Run(ctx context.Context, j Job) (string, error)
}

// Scheduler polls the Kanban for pending jobs and runs them one at a
// time via a JobRunner. It also fires due crontab entries.
//
// One scheduler process is enough for s09. In production hermes you
// can run several — ClaimNextPending uses BEGIN IMMEDIATE so they
// don't double-claim. We don't run multiple in our demo.
type Scheduler struct {
	k        *Kanban
	runner   JobRunner
	interval time.Duration

	cancel context.CancelFunc
	mu     sync.Mutex
	wg     sync.WaitGroup
	logger func(string, ...interface{})
}

func NewScheduler(k *Kanban, runner JobRunner, interval time.Duration, logger func(string, ...interface{})) *Scheduler {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if logger == nil {
		logger = func(string, ...interface{}) {}
	}
	return &Scheduler{k: k, runner: runner, interval: interval, logger: logger}
}

// Start kicks off a goroutine that polls until Stop is called.
func (s *Scheduler) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	go s.loop(ctx)
}

func (s *Scheduler) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

func (s *Scheduler) loop(ctx context.Context) {
	defer s.wg.Done()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	// run a tick immediately on start, then on each ticker fire
	s.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	// 1) Fire due crontab entries — enqueue each as a fresh job.
	due, err := s.k.DueCronEntries(ctx, time.Now().UTC())
	if err != nil {
		s.logger("[scheduler] DueCronEntries: %v", err)
	}
	for _, c := range due {
		if _, err := s.k.EnqueueJob(ctx, "cron", c.Prompt); err != nil {
			s.logger("[scheduler] enqueue cron %d: %v", c.ID, err)
			continue
		}
		_ = s.k.TouchCronEntry(ctx, c.ID)
	}

	// 2) Drain pending jobs (one at a time; in production, run a worker pool).
	for {
		j, ok, err := s.k.ClaimNextPending(ctx)
		if err != nil {
			s.logger("[scheduler] claim: %v", err)
			return
		}
		if !ok {
			return
		}
		s.runJob(ctx, j)
	}
}

func (s *Scheduler) runJob(ctx context.Context, j *Job) {
	s.logger("[scheduler] running job %d (source=%s)", j.ID, j.Source)
	result, err := s.runner.Run(ctx, *j)
	if err != nil {
		s.logger("[scheduler] job %d failed: %v", j.ID, err)
		_ = s.k.FinishJob(ctx, j.ID, result, err)
		return
	}
	s.logger("[scheduler] job %d done (%d chars)", j.ID, len(result))
	_ = s.k.FinishJob(ctx, j.ID, result, nil)
}

// AgentRunner adapts the agent loop into the JobRunner interface.
type AgentRunner struct {
	NewLoop func() *Loop // each job gets a fresh Loop
}

func (r *AgentRunner) Run(ctx context.Context, j Job) (string, error) {
	loop := r.NewLoop()
	out, err := loop.Run(ctx, j.Payload)
	if err != nil {
		return out, fmt.Errorf("agent loop: %w", err)
	}
	return out, nil
}

// EchoRunner is a JobRunner used by tests — no LLM, just echoes the
// payload. Lets us validate the multi-process plumbing without an API key.
type EchoRunner struct{}

func (EchoRunner) Run(ctx context.Context, j Job) (string, error) {
	return "echo: " + j.Payload, nil
}
