package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	var (
		verbose      = flag.Bool("v", false, "print every turn + plugin events")
		maxTurns     = flag.Int("max-turns", 20, "max agent turns")
		model        = flag.String("model", envOr("MODEL", "claude-sonnet-4-6"), "Anthropic model id")
		resume       = flag.String("resume", "", "resume an existing session by id")
		branch       = flag.String("branch", "", "fork an existing session")
		list         = flag.Bool("list", false, "list saved sessions")
		memDB        = flag.String("memory-db", "", "memory.db path")
		storeDir     = flag.String("store", "", "session store dir")
		curStaleStr  = flag.String("curator-stale-after", "168h", "memories untouched for this long are archived (default 1 week)")
		curLimit     = flag.Int("curator-limit", 100, "max memories archived per session start")
		disableCur   = flag.Bool("no-curator", false, "disable the curator plugin (for testing)")
	)
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), `usage:
  s06 "<prompt>"                          start a fresh session
  s06 -resume <id> "<prompt>"
  s06 -branch <id> "<prompt>"
  s06 -list
options:
  -v                      print every turn and plugin event
  -model                  Anthropic model id
  -memory-db PATH         memory.db path
  -store DIR              session store dir
  -curator-stale-after D  Go duration; memories untouched for this long are archived
  -curator-limit N        max memories archived per session start
  -no-curator             skip the curator plugin
environment:
  ANTHROPIC_API_KEY (required)
`)
	}
	flag.Parse()

	store, err := buildStore(*storeDir)
	if err != nil {
		log.Fatal(err)
	}
	if *list {
		runList(store)
		return
	}
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		log.Fatal("ANTHROPIC_API_KEY is not set")
	}
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	prompt := strings.Join(flag.Args(), " ")

	memPath := *memDB
	if memPath == "" {
		p, err := DefaultMemoryDBPath()
		if err != nil {
			log.Fatal(err)
		}
		memPath = p
	}
	if err := os.MkdirAll(filepath.Dir(memPath), 0o755); err != nil {
		log.Fatalf("create memory dir: %v", err)
	}
	mem, err := NewSQLiteMemory(memPath)
	if err != nil {
		log.Fatalf("open memory db: %v", err)
	}
	defer mem.Close()

	sess, err := pickSession(store, *resume, *branch, *model)
	if err != nil {
		log.Fatal(err)
	}
	sess.AppendUser(prompt)

	registry := NewRegistry()
	must(registry.Register(NewBashTool(), ToolsetBuiltin))
	must(registry.Register(NewReadFileTool(), ToolsetBuiltin))
	must(registry.Register(NewMemorySearchTool(mem), ToolsetBuiltin))
	must(registry.Register(NewMemorySaveTool(mem, sess.ID), ToolsetBuiltin))

	logger := &Logger{Prefix: "[s06]", Out: func(s string) {
		if *verbose {
			fmt.Fprintln(os.Stderr, s)
		}
	}}
	pm := NewPluginManager(logger)
	pm.Register(NewLoggingPlugin())
	if !*disableCur {
		stale, err := time.ParseDuration(*curStaleStr)
		if err != nil {
			log.Fatalf("invalid -curator-stale-after: %v", err)
		}
		pm.Register(NewCuratorPlugin(stale, *curLimit))
	}

	host := &Host{Registry: registry, Memory: mem, Logger: logger}
	if err := pm.Init(context.Background(), host); err != nil {
		fmt.Fprintf(os.Stderr, "[s06] plugin init had errors: %v\n", err)
	}
	defer pm.Close()

	if *verbose {
		fmt.Fprintf(os.Stderr, "[s06] memory db=%s\n", memPath)
		fmt.Fprintf(os.Stderr, "[s06] session id=%s msgs=%d\n", sess.ID, len(sess.Messages))
		fmt.Fprintf(os.Stderr, "[s06] plugins=%v\n", pm.Names())
		fmt.Fprintf(os.Stderr, "[s06] tools=%v\n", registry.Names())
	}

	loop := &Loop{
		Provider: NewAnthropicProvider(apiKey, *model),
		Registry: registry,
		Store:    store,
		Plugins:  pm,
		MaxTurns: *maxTurns,
		Verbose:  *verbose,
	}
	if err := loop.Run(context.Background(), sess); err != nil {
		log.Fatalf("loop error (session %s persisted): %v", sess.ID, err)
	}
	fmt.Println(LastAssistantText(sess))
}

func buildStore(override string) (*Store, error) {
	dir := override
	if dir == "" {
		d, err := DefaultStoreDir()
		if err != nil {
			return nil, err
		}
		dir = d
	}
	return &Store{Dir: dir}, nil
}

func pickSession(store *Store, resume, branch, model string) (*Session, error) {
	if resume != "" && branch != "" {
		return nil, fmt.Errorf("-resume and -branch are mutually exclusive")
	}
	switch {
	case resume != "":
		return store.Load(resume)
	case branch != "":
		parent, err := store.Load(branch)
		if err != nil {
			return nil, err
		}
		fork := parent.Branch()
		fmt.Fprintf(os.Stderr, "[session] branched %s from %s\n", fork.ID, parent.ID)
		return fork, nil
	default:
		return NewSession(model), nil
	}
}

func runList(store *Store) {
	sessions, err := store.List()
	if err != nil {
		log.Fatalf("list sessions: %v", err)
	}
	if len(sessions) == 0 {
		fmt.Println("(no sessions)")
		return
	}
	for _, s := range sessions {
		fmt.Printf("%-44s msgs=%-3d turns=%-3d in/out=%-5d/%-5d parent=%s\n",
			s.ID, len(s.Messages), s.Usage.Turns, s.Usage.InputTokens, s.Usage.OutputTokens,
			dashIfEmpty(s.ParentID))
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func DefaultMemoryDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".learn-hermes-agent", "memory.db"), nil
}
