package main

import (
	"context"
	"fmt"
)

// SkillTool wraps a Skill so it can live in the s02 Registry alongside
// builtin tools. The Registry sees the same Tool interface; whether a
// tool is a builtin Go function or an expanded markdown file is opaque
// to the loop.
//
// Toolset label convention: "skill-<name>". This drives the registry's
// shadow-protection rules (Skills can't override builtins, two skills
// can't share a name).
type SkillTool struct {
	Skill *Skill
	Env   *Env
}

func NewSkillTool(s *Skill, env *Env) *SkillTool {
	return &SkillTool{Skill: s, Env: env}
}

func (st *SkillTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "skill_" + st.Skill.Name,
		Description: st.Skill.Description,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"input": map[string]interface{}{
					"type":        "string",
					"description": "Optional input. Substituted into ${HERMES_SKILL_INPUT} during expansion.",
				},
			},
		},
	}
}

func (st *SkillTool) Execute(ctx context.Context, input map[string]interface{}) (string, error) {
	// Per-call env inherits the agent-wide one, with SkillInput optionally
	// overridden by the model's tool-call arguments.
	callEnv := *st.Env
	if v, ok := input["input"].(string); ok {
		callEnv.SkillInput = v
	}
	out, err := st.Skill.Expand(ctx, &callEnv)
	if err != nil {
		// Keep partial output: the model can usually salvage the result
		// even when one inline shell failed.
		return fmt.Sprintf("(skill expansion warning: %v)\n%s", err, out), nil
	}
	return out, nil
}
