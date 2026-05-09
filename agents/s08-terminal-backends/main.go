package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
)

func main() {
	var (
		verbose  = flag.Bool("v", false, "print every turn")
		maxTurns = flag.Int("max-turns", 20, "max agent turns")
		model    = flag.String("model", envOr("MODEL", "claude-sonnet-4-6"), "Anthropic model id")
		envSpec  = flag.String("env", envOr("TERMINAL_ENV", "local"), "terminal backend: local | docker:<image>")
	)
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), `usage:
  s08 [-v] [-env BACKEND] "<prompt>"
options:
  -v             print every turn
  -model         Anthropic model id
  -env BACKEND   local | docker:<image>   (default $TERMINAL_ENV or local)
environment:
  ANTHROPIC_API_KEY (required)
  TERMINAL_ENV       (default for -env)
demo prompts:
  s08 -v "what's the kernel version? use terminal"
  s08 -v -env "docker:alpine:3.19" "what does cat /etc/os-release print?"
`)
	}
	flag.Parse()

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		log.Fatal("ANTHROPIC_API_KEY is not set")
	}
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	prompt := strings.Join(flag.Args(), " ")

	env, err := NewEnvironment(*envSpec)
	if err != nil {
		log.Fatalf("create environment: %v", err)
	}
	defer env.Close()

	registry := NewRegistry()
	must(registry.Register(NewTerminalTool(env), ToolsetBuiltin))
	must(registry.Register(NewReadFileTool(), ToolsetBuiltin))

	if *verbose {
		fmt.Fprintf(os.Stderr, "[s08] backend=%s tools=%v\n", env.Name(), registry.Names())
	}

	loop := &Loop{
		Provider: NewAnthropicProvider(apiKey, *model),
		Registry: registry,
		MaxTurns: *maxTurns,
		Verbose:  *verbose,
	}
	final, err := loop.Run(context.Background(), prompt)
	if err != nil {
		log.Fatalf("loop error: %v", err)
	}
	fmt.Println(final)
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
