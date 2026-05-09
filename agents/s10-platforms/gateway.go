package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Gateway exposes one /webhook/<platform> endpoint per registered
// Platform. Each handler reads the body, asks the Platform to parse
// it into a normalised Inbound, then writes a kanban job tagged with
// the platform name + channel_id (so Outbound knows where to reply).
//
// Difference from s09's Gateway: s09 had a single /msg endpoint with
// {"source":"...","prompt":"..."} JSON; s10's gateway routes per
// platform and each platform parses its own native webhook format.
type Gateway struct {
	addr    string
	k       *Kanban
	pr      *PlatformRegistry

	mu     sync.Mutex
	server *http.Server
	ln     net.Listener
}

func NewGateway(addr string, k *Kanban, pr *PlatformRegistry) *Gateway {
	return &Gateway{addr: addr, k: k, pr: pr}
}

// Start begins serving. Returns the bound address.
func (g *Gateway) Start() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.server != nil {
		return "", fmt.Errorf("gateway already started")
	}
	ln, err := net.Listen("tcp", g.addr)
	if err != nil {
		return "", err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/", g.handleWebhook)
	mux.HandleFunc("/jobs", g.handleJobs)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	g.server = &http.Server{
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	g.ln = ln
	go func() { _ = g.server.Serve(ln) }()
	return ln.Addr().String(), nil
}

func (g *Gateway) Stop(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.server == nil {
		return nil
	}
	return g.server.Shutdown(ctx)
}

func (g *Gateway) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	platformName := strings.TrimPrefix(r.URL.Path, "/webhook/")
	platform, ok := g.pr.Get(platformName)
	if !ok {
		http.Error(w, "unknown platform: "+platformName, http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	inbound, err := platform.Inbound(body)
	if err != nil {
		http.Error(w, "parse: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Encode the full Inbound as JSON payload — Outbound needs
	// channel_id back; storing it inline makes the scheduler stateless.
	payload, _ := json.Marshal(inbound)
	id, err := g.k.EnqueueJob(r.Context(), platform.Name(), string(payload))
	if err != nil {
		http.Error(w, "enqueue: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int64{"job_id": id})
}

func (g *Gateway) handleJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := g.k.ListJobs(r.Context(), 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(jobs)
}

// PlatformAwareRunner wraps an inner JobRunner and, on success,
// dispatches the result back through the Platform that originated
// the job (looked up by job.Source).
type PlatformAwareRunner struct {
	Inner JobRunner
	Reg   *PlatformRegistry
	Logger func(string, ...interface{})
}

func (r *PlatformAwareRunner) Run(ctx context.Context, j Job) (string, error) {
	// Decode the stored payload back into an Inbound so we can recover
	// the original prompt + channel_id.
	var inbound Inbound
	if err := json.Unmarshal([]byte(j.Payload), &inbound); err != nil {
		// Backward compat: older jobs (s09 form) stored just the prompt.
		inbound.Text = j.Payload
	}
	// Substitute the prompt into a fresh job for the inner runner.
	innerJob := j
	innerJob.Payload = inbound.Text
	result, runErr := r.Inner.Run(ctx, innerJob)

	// Outbound dispatch — best-effort; failures are logged, not
	// propagated. The kanban still records the result.
	platform, ok := r.Reg.Get(j.Source)
	if ok && inbound.ChannelID != "" {
		if err := platform.Outbound(ctx, inbound.ChannelID, result); err != nil {
			if r.Logger != nil {
				r.Logger("[%s outbound] %v", j.Source, err)
			}
		}
	}
	return result, runErr
}
