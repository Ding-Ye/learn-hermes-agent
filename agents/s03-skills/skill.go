package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Skill is one Markdown file in skills/ — a prompt template plus metadata.
//
// File format:
//
//   ---
//   name: greet
//   description: One-line summary shown to the LLM.
//   ---
//   <markdown body, may contain ${VAR} and `inline shell`>
//
// Hermes treats skills as PROMPTS, not code. When invoked, the body is
// expanded (vars substituted, inline shell run) and the resolved string
// becomes either a user message (slash-command form) or the tool_result
// of a tool call. We do the latter — see skill_tool.go.
type Skill struct {
	Name         string
	Description  string
	SourceFile   string
	BodyTemplate string
}

// Env carries the substitution context for a skill expansion. SkillInput
// is per-call (the model passes it as a tool argument); the others are
// agent-wide and set once.
type Env struct {
	SessionID  string
	WorkingDir string
	SkillsDir  string
	SkillInput string
}

// Resolve maps a template variable name to its value. Unknown names
// return ok=false; the caller leaves the literal `${UNDEFINED}` in place
// so the LLM can see what's missing.
func (e *Env) Resolve(name string) (string, bool) {
	switch name {
	case "HERMES_SESSION_ID":
		return e.SessionID, true
	case "HERMES_WORKING_DIR":
		return e.WorkingDir, true
	case "HERMES_SKILL_DIR":
		return e.SkillsDir, true
	case "HERMES_SKILL_INPUT":
		return e.SkillInput, true
	}
	return "", false
}

// LoadSkills walks dir non-recursively for *.md files and parses each.
// Files that fail to parse are skipped with a stderr log; one bad skill
// doesn't crash the agent. Real hermes recurses into subdirectories — we
// simplify here, see the upstream reading.
func LoadSkills(dir string) ([]*Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read skills dir: %w", err)
	}
	var skills []*Skill
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		// README.md is repo metadata, not a skill.
		if strings.EqualFold(entry.Name(), "README.md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		s, err := parseSkillFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[skill] skipping %s: %v\n", path, err)
			continue
		}
		skills = append(skills, s)
	}
	return skills, nil
}

const (
	stateStart = iota
	stateFrontmatter
	stateBody
)

func parseSkillFile(path string) (*Skill, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var (
		name        string
		description string
		body        strings.Builder
	)
	state := stateStart
	for scanner.Scan() {
		line := scanner.Text()
		switch state {
		case stateStart:
			if strings.TrimRight(line, " \t") != "---" {
				return nil, fmt.Errorf("expected '---' frontmatter delimiter on line 1, got %q", line)
			}
			state = stateFrontmatter
		case stateFrontmatter:
			if strings.TrimRight(line, " \t") == "---" {
				state = stateBody
				continue
			}
			if k, v, ok := splitYAML(line); ok {
				switch k {
				case "name":
					name = v
				case "description":
					description = v
				}
			}
		case stateBody:
			body.WriteString(line)
			body.WriteString("\n")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if name == "" {
		return nil, fmt.Errorf("missing 'name' in frontmatter")
	}
	return &Skill{
		Name:         name,
		Description:  description,
		SourceFile:   path,
		BodyTemplate: body.String(),
	}, nil
}

// splitYAML handles a tiny subset: `key: value` on one line, optional
// quoted value. Just enough for our two fields. Real hermes uses PyYAML;
// pulling in a yaml lib for s03 would dilute the lesson.
func splitYAML(line string) (key, value string, ok bool) {
	idx := strings.Index(line, ":")
	if idx <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	value = strings.TrimSpace(line[idx+1:])
	value = strings.Trim(value, `"'`)
	return key, value, true
}

// Expand performs variable substitution then inline-shell expansion on
// the skill body, returning the final prompt.
//
// Order matters: vars FIRST, then shell. So `${HERMES_SKILL_INPUT}` inside
// a `cat ...` invocation becomes a real path before bash runs the command.
//
// SECURITY: this runs arbitrary shell from a markdown file. Real hermes
// gates skill loading behind explicit installation. Treat skills as code.
func (s *Skill) Expand(ctx context.Context, env *Env) (string, error) {
	out := substituteVars(s.BodyTemplate, env)
	return expandInlineShell(ctx, out)
}

var varRE = regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)\}`)

func substituteVars(s string, env *Env) string {
	return varRE.ReplaceAllStringFunc(s, func(match string) string {
		name := match[2 : len(match)-1] // strip ${ and }
		v, ok := env.Resolve(name)
		if !ok {
			return match // leave literal so the LLM sees what's missing
		}
		return v
	})
}

// shellRE matches a single-backtick token: `cmd`. We deliberately don't
// try to skip fenced ``` code blocks — real hermes does, but the simpler
// version is more legible at this stage. (See upstream reading.)
var shellRE = regexp.MustCompile("`([^`\n]+)`")

func expandInlineShell(ctx context.Context, s string) (string, error) {
	var firstErr error
	out := shellRE.ReplaceAllStringFunc(s, func(match string) string {
		cmd := match[1 : len(match)-1] // strip backticks
		bashOut, err := exec.CommandContext(ctx, "bash", "-c", cmd).Output()
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("inline shell %q failed: %w", cmd, err)
			}
			return fmt.Sprintf("(shell error: %v)", err)
		}
		return strings.TrimRight(string(bashOut), "\n")
	})
	return out, firstErr
}
