package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Subcommands:
//
//   s09 cli "<prompt>"          single-shot agent (no kanban)
//   s09 gateway                  HTTP listener; writes inbound msgs to kanban
//   s09 scheduler                polls kanban, runs jobs via the agent loop
//   s09 jobs                     list recent jobs from the kanban
//   s09 send "<prompt>"          enqueue a job into the kanban directly
//   s09 cron add "every 5m" "<prompt>"
//
// The three "main" processes (cli/gateway/scheduler) share one
// kanban.db at $LHA_KANBAN or ~/.learn-hermes-agent/kanban.db.

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	sub := os.Args[1]
	args := os.Args[2:]
	switch sub {
	case "cli", "agent":
		cmdCLI(args)
	case "gateway":
		cmdGateway(args)
	case "scheduler":
		cmdScheduler(args)
	case "jobs", "list":
		cmdJobs(args)
	case "send":
		cmdSend(args)
	case "cron":
		cmdCron(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %q\n\n", sub)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  s09 cli "<prompt>"               single-shot agent (no kanban)
  s09 gateway [-addr :7079]        HTTP listener; writes inbound msgs to kanban
  s09 scheduler [-interval 2s]     polls kanban, runs queued jobs via agent loop
  s09 jobs [-limit 20]             list recent jobs from the kanban
  s09 send "<prompt>"              enqueue a job (acts like a gateway POST)
  s09 cron add "every 5m" "<prompt>"   add a recurring job
environment:
  ANTHROPIC_API_KEY (cli/scheduler in agent mode)
  LHA_KANBAN (default ~/.learn-hermes-agent/kanban.db)
  MODEL (default claude-sonnet-4-6)
`)
}

func defaultKanbanPath() string {
	if v := os.Getenv("LHA_KANBAN"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".learn-hermes-agent", "kanban.db")
}

func openKanbanOrDie() *Kanban {
	path := defaultKanbanPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Fatalf("mkdir kanban: %v", err)
	}
	k, err := OpenKanban(path)
	if err != nil {
		log.Fatalf("open kanban %s: %v", path, err)
	}
	return k
}

// ---------- cli ------------------------------------------------------------

func cmdCLI(args []string) {
	fs := flag.NewFlagSet("cli", flag.ExitOnError)
	verbose := fs.Bool("v", false, "verbose")
	model := fs.String("model", os.Getenv("MODEL"), "Anthropic model id")
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		log.Fatal("cli: prompt required")
	}
	prompt := strings.Join(fs.Args(), " ")
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		log.Fatal("ANTHROPIC_API_KEY required")
	}
	loop := newLoop(apiKey, *model)
	out, err := loop.Run(context.Background(), prompt)
	if err != nil {
		log.Fatalf("cli loop: %v", err)
	}
	if *verbose {
		fmt.Fprintln(os.Stderr, "[cli] done")
	}
	fmt.Println(out)
}

// ---------- gateway --------------------------------------------------------

func cmdGateway(args []string) {
	fs := flag.NewFlagSet("gateway", flag.ExitOnError)
	addr := fs.String("addr", ":7079", "listen address")
	_ = fs.Parse(args)
	k := openKanbanOrDie()
	defer k.Close()
	g := NewGateway(*addr, k)
	bound, err := g.Start()
	if err != nil {
		log.Fatalf("gateway start: %v", err)
	}
	fmt.Fprintf(os.Stderr, "[gateway] listening on %s; kanban=%s\n", bound, defaultKanbanPath())
	waitForSignal()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = g.Stop(ctx)
}

// ---------- scheduler ------------------------------------------------------

func cmdScheduler(args []string) {
	fs := flag.NewFlagSet("scheduler", flag.ExitOnError)
	interval := fs.Duration("interval", 2*time.Second, "poll interval")
	model := fs.String("model", os.Getenv("MODEL"), "Anthropic model id")
	echoOnly := fs.Bool("echo", false, "use the EchoRunner instead of the agent loop (for smoke testing)")
	_ = fs.Parse(args)
	k := openKanbanOrDie()
	defer k.Close()

	var runner JobRunner
	if *echoOnly {
		runner = EchoRunner{}
	} else {
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		if apiKey == "" {
			log.Fatal("ANTHROPIC_API_KEY required (or use -echo for smoke testing)")
		}
		runner = &AgentRunner{NewLoop: func() *Loop { return newLoop(apiKey, *model) }}
	}
	logger := func(f string, a ...interface{}) {
		fmt.Fprintf(os.Stderr, f+"\n", a...)
	}
	s := NewScheduler(k, runner, *interval, logger)
	s.Start()
	fmt.Fprintf(os.Stderr, "[scheduler] interval=%s kanban=%s echo=%v\n", *interval, defaultKanbanPath(), *echoOnly)
	waitForSignal()
	s.Stop()
}

// ---------- jobs / send / cron --------------------------------------------

func cmdJobs(args []string) {
	fs := flag.NewFlagSet("jobs", flag.ExitOnError)
	limit := fs.Int("limit", 20, "max jobs")
	_ = fs.Parse(args)
	k := openKanbanOrDie()
	defer k.Close()
	jobs, err := k.ListJobs(context.Background(), *limit)
	if err != nil {
		log.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) == 0 {
		fmt.Println("(no jobs)")
		return
	}
	for _, j := range jobs {
		fmt.Printf("#%-4d  %-7s  %-8s  %-19s  %s\n",
			j.ID, j.Source, j.Status, j.CreatedAt.Format("2006-01-02 15:04:05"),
			truncate(j.Payload, 60))
		if j.Result != "" {
			fmt.Printf("       result: %s\n", truncate(j.Result, 80))
		}
		if j.Error != "" {
			fmt.Printf("       error : %s\n", truncate(j.Error, 80))
		}
	}
}

func cmdSend(args []string) {
	if len(args) == 0 {
		log.Fatal("send: prompt required")
	}
	prompt := strings.Join(args, " ")
	k := openKanbanOrDie()
	defer k.Close()
	id, err := k.EnqueueJob(context.Background(), "cli-send", prompt)
	if err != nil {
		log.Fatalf("enqueue: %v", err)
	}
	fmt.Printf("enqueued job %d\n", id)
}

func cmdCron(args []string) {
	if len(args) == 0 || args[0] != "add" || len(args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: s09 cron add \"every 5m\" \"<prompt>\"")
		os.Exit(2)
	}
	spec := args[1]
	prompt := strings.Join(args[2:], " ")
	k := openKanbanOrDie()
	defer k.Close()
	id, err := k.AddCronEntry(context.Background(), spec, prompt)
	if err != nil {
		log.Fatalf("add cron: %v", err)
	}
	fmt.Printf("cron entry %d added: %s -> %q\n", id, spec, prompt)
}

// ---------- helpers --------------------------------------------------------

func newLoop(apiKey, model string) *Loop {
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	return &Loop{
		Provider: NewAnthropicProvider(apiKey, model),
		MaxTurns: 10,
		Tools:    []Tool{bashTool{}},
	}
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func waitForSignal() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
}

// kept for documentation: a JSON dump helper used by hand-debugging.
var _ = json.Marshal
var _ = http.Get
