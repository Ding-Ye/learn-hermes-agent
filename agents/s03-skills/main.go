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
	verbose := flag.Bool("v", false, "print every turn (assistant text + tool calls)")
	maxTurns := flag.Int("max-turns", 20, "max agent turns before giving up")
	model := flag.String("model", envOr("MODEL", "claude-sonnet-4-6"), "Anthropic model id")
	skillsDir := flag.String("skills-dir", "../../skills", "directory of skill .md files (default: ../../skills relative to this binary)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(),
			"usage: s03 [-v] [-max-turns N] [-model ID] [-skills-dir DIR] <prompt>\n\n"+
				"  Set ANTHROPIC_API_KEY in the environment.\n"+
				"  Skills are loaded from -skills-dir; each .md becomes a tool named skill_<name>.\n"+
				"  Demo prompts:\n"+
				"    s03 -v \"use skill_greet to say hello\"\n"+
				"    s03 -v \"summarize ./README.md using skill_summarize (pass input='./README.md')\"\n")
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

	skillsAbsDir, err := filepath.Abs(*skillsDir)
	if err != nil {
		log.Fatalf("resolve skills dir: %v", err)
	}
	cwd, _ := os.Getwd()
	env := &Env{
		SessionID:  fmt.Sprintf("s03-%d", time.Now().Unix()),
		WorkingDir: cwd,
		SkillsDir:  skillsAbsDir,
	}

	skills, err := LoadSkills(skillsAbsDir)
	if err != nil {
		log.Fatalf("load skills from %s: %v", skillsAbsDir, err)
	}
	for _, s := range skills {
		if err := registry.Register(NewSkillTool(s, env), "skill-"+s.Name); err != nil {
			fmt.Fprintf(os.Stderr, "[skill] %s rejected: %v\n", s.Name, err)
		}
	}

	if *verbose {
		fmt.Printf("[registry] %d tools: %v (gen=%d)\n",
			len(registry.Names()), registry.Names(), registry.Generation())
		fmt.Printf("[skills] %d skills loaded from %s\n", len(skills), skillsAbsDir)
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
