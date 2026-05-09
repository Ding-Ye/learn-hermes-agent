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
	verbose := flag.Bool("v", false, "print every turn (assistant text + tool calls)")
	maxTurns := flag.Int("max-turns", 20, "max agent turns before giving up")
	model := flag.String("model", envOr("MODEL", "claude-sonnet-4-6"), "Anthropic model id")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(),
			"usage: s01 [-v] [-max-turns N] [-model ID] <prompt>\n\n"+
				"  Set ANTHROPIC_API_KEY in the environment.\n"+
				"  Example: s01 -v \"list the files in the current directory\"\n")
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

	provider := NewAnthropicProvider(apiKey, *model)
	tools := []Tool{NewBashTool()}

	loop := &Loop{
		Provider: provider,
		Tools:    tools,
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
