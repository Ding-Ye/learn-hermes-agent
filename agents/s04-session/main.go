package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

func main() {
	var (
		verbose  = flag.Bool("v", false, "print every turn")
		maxTurns = flag.Int("max-turns", 20, "max agent turns")
		model    = flag.String("model", envOr("MODEL", "claude-sonnet-4-6"), "Anthropic model id")
		resume   = flag.String("resume", "", "resume an existing session by id")
		branch   = flag.String("branch", "", "fork an existing session into a new one")
		reset    = flag.String("reset", "", "delete a session by id (no prompt expected)")
		list     = flag.Bool("list", false, "list saved sessions")
		storeDir = flag.String("store", "", "session store directory (default: ~/.learn-hermes-agent/sessions)")
	)
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), `usage:
  s04 "<prompt>"                      start a fresh session
  s04 -resume <id> "<prompt>"         continue an existing session
  s04 -branch <id> "<prompt>"         fork a session and continue from the fork
  s04 -reset <id>                     delete a session
  s04 -list                           list saved sessions
options:
  -v             print every turn
  -model         Anthropic model id (default $MODEL or claude-sonnet-4-6)
  -max-turns N   loop cap (default 20)
  -store DIR     override session store dir
environment:
  ANTHROPIC_API_KEY (required for prompted modes)
`)
	}
	flag.Parse()

	store, err := buildStore(*storeDir)
	if err != nil {
		log.Fatal(err)
	}

	switch {
	case *list:
		runList(store)
		return
	case *reset != "":
		runReset(store, *reset)
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

	registry := NewRegistry()
	must(registry.Register(NewBashTool(), ToolsetBuiltin))
	must(registry.Register(NewReadFileTool(), ToolsetBuiltin))

	sess, err := pickSession(store, *resume, *branch, *model)
	if err != nil {
		log.Fatal(err)
	}
	sess.AppendUser(prompt)

	if *verbose {
		fmt.Printf("[session] id=%s parent=%s msgs=%d turns=%d in/out=%d/%d\n",
			sess.ID, dashIfEmpty(sess.ParentID), len(sess.Messages),
			sess.Usage.Turns, sess.Usage.InputTokens, sess.Usage.OutputTokens)
	}

	loop := &Loop{
		Provider: NewAnthropicProvider(apiKey, *model),
		Registry: registry,
		Store:    store,
		MaxTurns: *maxTurns,
		Verbose:  *verbose,
	}

	if err := loop.Run(context.Background(), sess); err != nil {
		log.Fatalf("loop error (session %s persisted): %v", sess.ID, err)
	}

	fmt.Println(LastAssistantText(sess))
	if !*verbose {
		fmt.Fprintf(os.Stderr, "[session] %s saved (turns=%d in=%d out=%d)\n",
			sess.ID, sess.Usage.Turns, sess.Usage.InputTokens, sess.Usage.OutputTokens)
	}
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

// pickSession decides which session to operate on based on the -resume
// and -branch flags. Mutually exclusive flags trip a friendly error
// rather than silently picking one.
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
		parent := dashIfEmpty(s.ParentID)
		ago := time.Since(s.UpdatedAt).Round(time.Second)
		fmt.Printf("%-44s msgs=%-3d turns=%-3d in/out=%-5d/%-5d updated=%v ago parent=%s\n",
			s.ID, len(s.Messages), s.Usage.Turns,
			s.Usage.InputTokens, s.Usage.OutputTokens, ago, parent)
	}
}

func runReset(store *Store, id string) {
	if err := store.Delete(id); err != nil {
		if errors.Is(err, ErrNotFound) {
			fmt.Fprintf(os.Stderr, "[session] %s not found (no-op)\n", id)
			return
		}
		log.Fatalf("reset %s: %v", id, err)
	}
	fmt.Fprintf(os.Stderr, "[session] %s deleted\n", id)
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
