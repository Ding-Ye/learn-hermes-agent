package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

// MCPSpec describes how to launch one MCP server. Format on the
// command line (-mcp): "<name>=<command>[ args...]".
//
// Multiple -mcp flags allowed. Example:
//
//   -mcp echo=/path/to/s07 -server
//
// would launch /path/to/s07 with -server as the echo MCP server.
type MCPSpec struct {
	Name string
	Cmd  string
	Args []string
}

type mcpFlag []MCPSpec

func (f *mcpFlag) String() string { return fmt.Sprint(*f) }
func (f *mcpFlag) Set(v string) error {
	idx := strings.Index(v, "=")
	if idx <= 0 {
		return fmt.Errorf("expected NAME=COMMAND, got %q", v)
	}
	name := v[:idx]
	parts := strings.Fields(v[idx+1:])
	if len(parts) == 0 {
		return fmt.Errorf("empty command for mcp %q", name)
	}
	*f = append(*f, MCPSpec{Name: name, Cmd: parts[0], Args: parts[1:]})
	return nil
}

func main() {
	// -server short-circuits everything: the same binary acts as the demo
	// MCP server. Nothing else gets parsed in that mode.
	for _, a := range os.Args[1:] {
		if a == "-server" || a == "--server" {
			runServerMode()
			return
		}
	}

	var (
		verbose  = flag.Bool("v", false, "print every turn")
		maxTurns = flag.Int("max-turns", 20, "max agent turns")
		model    = flag.String("model", envOr("MODEL", "claude-sonnet-4-6"), "Anthropic model id")
	)
	var mcps mcpFlag
	flag.Var(&mcps, "mcp", "MCP server: NAME=COMMAND [args...]; repeat for multiple servers")
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), `usage:
  s07 -server                       run as a demo MCP server (echo+reverse)
  s07 [-v] [-mcp NAME=CMD ...] "<prompt>"
                                    run as the agent; each -mcp launches a server
options:
  -v             print every turn
  -model         Anthropic model id
  -mcp NAME=CMD  spawn an MCP subprocess named NAME, args after =
                 example: -mcp "echo=./s07 -server"
environment:
  ANTHROPIC_API_KEY (required for agent mode)
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

	registry := NewRegistry()
	must(registry.Register(NewBashTool(), ToolsetBuiltin))
	must(registry.Register(NewReadFileTool(), ToolsetBuiltin))

	// Connect to each MCP server and register its tools.
	ctx := context.Background()
	clients := make([]*MCPClient, 0, len(mcps))
	defer func() {
		for _, c := range clients {
			_ = c.Close()
		}
	}()
	for _, spec := range mcps {
		// Resolve a relative command path to an absolute one — the test
		// often passes "./s07" from a temp dir.
		cmd := spec.Cmd
		if absPath, err := exec.LookPath(spec.Cmd); err == nil {
			cmd = absPath
		}
		client, err := DialStdio(ctx, cmd, spec.Args...)
		if err != nil {
			log.Fatalf("[mcp] dial %s (%s): %v", spec.Name, spec.Cmd, err)
		}
		n, err := RegisterMCPTools(ctx, registry, spec.Name, client)
		if err != nil {
			log.Fatalf("[mcp] register %s tools: %v", spec.Name, err)
		}
		if *verbose {
			fmt.Fprintf(os.Stderr, "[mcp] %s connected; %d tools registered\n", spec.Name, n)
		}
		clients = append(clients, client)
	}

	if *verbose {
		fmt.Fprintf(os.Stderr, "[registry] %d tools (gen=%d): %v\n",
			len(registry.Names()), registry.Generation(), registry.Names())
	}

	loop := &Loop{
		Provider: NewAnthropicProvider(apiKey, *model),
		Registry: registry,
		MaxTurns: *maxTurns,
		Verbose:  *verbose,
	}
	final, err := loop.Run(ctx, prompt)
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
