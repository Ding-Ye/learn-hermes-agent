package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	var (
		verbose  = flag.Bool("v", false, "print every turn")
		maxTurns = flag.Int("max-turns", 20, "max agent turns")
		model    = flag.String("model", envOr("MODEL", "claude-sonnet-4-6"), "Anthropic model id")
		resume   = flag.String("resume", "", "resume an existing session by id")
		branch   = flag.String("branch", "", "fork an existing session into a new one")
		list     = flag.Bool("list", false, "list saved sessions")
		memDB    = flag.String("memory-db", "", "path to memory.db (default: ~/.learn-hermes-agent/memory.db)")
		storeDir = flag.String("store", "", "session store directory")
	)
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), `usage:
  s05 "<prompt>"                start a fresh session, memory_search/memory_save available
  s05 -resume <id> "<prompt>"   continue an existing session
  s05 -branch <id> "<prompt>"   fork a session
  s05 -list                     list saved sessions
options:
  -v             print every turn
  -model         Anthropic model id (default $MODEL or claude-sonnet-4-6)
  -memory-db     SQLite memory db path
  -store         session store dir override
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

	if *verbose {
		fmt.Printf("[memory] db=%s\n", memPath)
		fmt.Printf("[session] id=%s parent=%s msgs=%d\n",
			sess.ID, dashIfEmpty(sess.ParentID), len(sess.Messages))
		fmt.Printf("[registry] tools=%v\n", registry.Names())
	}

	loop := &Loop{
		Provider: NewAnthropicProvider(apiKey, *model),
		Registry: registry,
		Store:    store,
		Memory:   mem,
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
		s, err := store.Load(resume)
		if err != nil {
			return nil, err
		}
		return s, nil
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

// DefaultMemoryDBPath returns ~/.learn-hermes-agent/memory.db, mirroring
// the hermes convention of one db per user (shared across sessions and
// gateway sources).
func DefaultMemoryDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".learn-hermes-agent", "memory.db"), nil
}
