package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// Gateway is an HTTP server that accepts inbound messages and writes
// them to the Kanban as pending jobs. The Scheduler picks them up and
// runs the agent — the Gateway never touches the LLM.
//
// In hermes this is the same shape: a separate process bridges between
// IM platforms (Telegram/Discord/Slack) and the agent core. s10
// implements actual platform adapters; s09 just provides the HTTP
// shell so the scheduling story can be told end-to-end.
//
// Endpoints:
//
//   POST /msg              { "source": "...", "prompt": "..." }
//                          -> { "job_id": 123 }
//   GET  /jobs?limit=N     -> [{ "id":..., "status":..., ... }, ...]
//   GET  /healthz          -> "ok"
type Gateway struct {
	addr string
	k    *Kanban

	server *http.Server
	mu     sync.Mutex
	ln     net.Listener
}

func NewGateway(addr string, k *Kanban) *Gateway {
	return &Gateway{addr: addr, k: k}
}

// Start begins serving. Returns the bound address (useful when addr
// was ":0" — the actual port is needed for tests).
func (g *Gateway) Start() (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.server != nil {
		return "", fmt.Errorf("gateway already started")
	}
	ln, err := net.Listen("tcp", g.addr)
	if err != nil {
		return "", fmt.Errorf("listen %s: %w", g.addr, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/msg", g.handleMsg)
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
	go func() {
		_ = g.server.Serve(ln)
	}()
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

func (g *Gateway) handleMsg(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Source  string `json:"source"`
		Prompt  string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Source == "" {
		body.Source = "gateway"
	}
	id, err := g.k.EnqueueJob(r.Context(), body.Source, body.Prompt)
	if err != nil {
		http.Error(w, "enqueue: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int64{"job_id": id})
}

func (g *Gateway) handleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	limit := 20
	jobs, err := g.k.ListJobs(r.Context(), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type jobView struct {
		ID        int64  `json:"id"`
		Source    string `json:"source"`
		Status    string `json:"status"`
		Payload   string `json:"payload"`
		Result    string `json:"result,omitempty"`
		Error     string `json:"error,omitempty"`
		CreatedAt string `json:"created_at"`
	}
	out := make([]jobView, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, jobView{
			ID: j.ID, Source: j.Source, Status: string(j.Status),
			Payload: j.Payload, Result: j.Result, Error: j.Error,
			CreatedAt: j.CreatedAt.Format(time.RFC3339),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
