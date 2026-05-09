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
			"usage: s02 [-v] [-max-turns N] [-model ID] <prompt>\n\n"+
				"  Set ANTHROPIC_API_KEY in the environment.\n"+
				"  Demo prompts:\n"+
				"    s02 -v \"show me what's in agents/s02-tool-registry/main.go\"\n"+
				"    s02 -v \"what's today's date in ISO 8601?\"\n")
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

	registry := NewRegistry()
	must(registry.Register(NewBashTool(), ToolsetBuiltin))
	must(registry.Register(NewReadFileTool(), ToolsetBuiltin))

	loop := &Loop{
		Provider: NewAnthropicProvider(apiKey, *model),
		Registry: registry,
		MaxTurns: *maxTurns,
		Verbose:  *verbose,
	}

	if *verbose {
		fmt.Printf("[registry] %d tools: %v (gen=%d)\n",
			len(registry.Names()), registry.Names(), registry.Generation())
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
