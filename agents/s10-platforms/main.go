package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

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
	case "jobs":
		cmdJobs(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %q\n\n", sub)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  s10 cli "<prompt>"                  single-shot agent
  s10 gateway [-addr :7079] [-tg-token TOKEN] [-discord-token TOKEN]
                                       HTTP server with /webhook/telegram and
                                       /webhook/discord routes; tokens optional
                                       (empty = dry-run mode)
  s10 scheduler [-interval 2s] [-echo]
                                       polls kanban, runs jobs via agent loop;
                                       on completion routes reply through the
                                       originating platform's Outbound
  s10 jobs                            list recent jobs
environment:
  ANTHROPIC_API_KEY
  TG_BOT_TOKEN              defaults for -tg-token
  DISCORD_BOT_TOKEN         defaults for -discord-token
  LHA_KANBAN                kanban.db path (default ~/.learn-hermes-agent/kanban.db)
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
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	k, err := OpenKanban(path)
	if err != nil {
		log.Fatalf("open kanban: %v", err)
	}
	return k
}

func cmdCLI(args []string) {
	fs := flag.NewFlagSet("cli", flag.ExitOnError)
	model := fs.String("model", os.Getenv("MODEL"), "")
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		log.Fatal("cli: prompt required")
	}
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		log.Fatal("ANTHROPIC_API_KEY required")
	}
	loop := newLoop(apiKey, *model)
	out, err := loop.Run(context.Background(), strings.Join(fs.Args(), " "))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(out)
}

func cmdGateway(args []string) {
	fs := flag.NewFlagSet("gateway", flag.ExitOnError)
	addr := fs.String("addr", ":7079", "listen address")
	tgToken := fs.String("tg-token", os.Getenv("TG_BOT_TOKEN"), "Telegram bot token (empty = dry-run)")
	discordToken := fs.String("discord-token", os.Getenv("DISCORD_BOT_TOKEN"), "Discord bot token (empty = dry-run)")
	_ = fs.Parse(args)

	logger := func(f string, a ...interface{}) {
		fmt.Fprintf(os.Stderr, "[gw] "+f+"\n", a...)
	}
	pr := NewPlatformRegistry()
	_ = pr.Register(NewTelegramPlatform(*tgToken, logger))
	_ = pr.Register(NewDiscordPlatform(*discordToken, logger))

	k := openKanbanOrDie()
	defer k.Close()
	g := NewGateway(*addr, k, pr)
	bound, err := g.Start()
	if err != nil {
		log.Fatal(err)
	}
	mode := func(t string) string {
		if t == "" {
			return "dry-run"
		}
		return "live"
	}
	logger("listening on %s", bound)
	logger("telegram: %s | discord: %s", mode(*tgToken), mode(*discordToken))
	waitForSignal()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = g.Stop(ctx)
}

func cmdScheduler(args []string) {
	fs := flag.NewFlagSet("scheduler", flag.ExitOnError)
	interval := fs.Duration("interval", 2*time.Second, "")
	model := fs.String("model", os.Getenv("MODEL"), "")
	echoOnly := fs.Bool("echo", false, "use EchoRunner instead of agent")
	tgToken := fs.String("tg-token", os.Getenv("TG_BOT_TOKEN"), "for outbound replies")
	discordToken := fs.String("discord-token", os.Getenv("DISCORD_BOT_TOKEN"), "for outbound replies")
	_ = fs.Parse(args)

	logger := func(f string, a ...interface{}) {
		fmt.Fprintf(os.Stderr, "[sched] "+f+"\n", a...)
	}
	pr := NewPlatformRegistry()
	_ = pr.Register(NewTelegramPlatform(*tgToken, logger))
	_ = pr.Register(NewDiscordPlatform(*discordToken, logger))

	k := openKanbanOrDie()
	defer k.Close()

	var inner JobRunner
	if *echoOnly {
		inner = EchoRunner{}
	} else {
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		if apiKey == "" {
			log.Fatal("ANTHROPIC_API_KEY required (or use -echo)")
		}
		inner = &AgentRunner{NewLoop: func() *Loop { return newLoop(apiKey, *model) }}
	}
	runner := &PlatformAwareRunner{Inner: inner, Reg: pr, Logger: logger}

	s := NewScheduler(k, runner, *interval, logger)
	s.Start()
	logger("interval=%s echo=%v", *interval, *echoOnly)
	waitForSignal()
	s.Stop()
}

func cmdJobs(args []string) {
	k := openKanbanOrDie()
	defer k.Close()
	jobs, _ := k.ListJobs(context.Background(), 50)
	if len(jobs) == 0 {
		fmt.Println("(no jobs)")
		return
	}
	for _, j := range jobs {
		fmt.Printf("#%-4d %-9s %-7s  %s\n", j.ID, j.Source, j.Status, truncate(j.Payload, 80))
		if j.Result != "" {
			fmt.Printf("       result: %s\n", truncate(j.Result, 80))
		}
		if j.Error != "" {
			fmt.Printf("       error : %s\n", truncate(j.Error, 80))
		}
	}
}

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
