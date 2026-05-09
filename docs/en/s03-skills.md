---
title: "s03 · Skills"
chapter: 3
slug: s03-skills
est_read_min: 14
---

# s03 · Skills

> What this teaches: wire `skills/*.md` (YAML frontmatter + Markdown body + template vars + inline shell) into the s02 Registry as a **new tool source**. Skills are hermes-agent's most distinctive design — they aren't code, they're prompts. The lesson is that s02's Registry abstraction *actually* absorbs a wildly different tool source as long as it implements the `Tool` interface.

---

## Problem

s02's Registry accepts a tool from any source. But what counts as "any source"? Another builtin function isn't interesting. The real test is bringing in something that **doesn't look like code at all**.

Hermes's signature design: users extend the agent without writing a line of Go/Python — **just markdown**. A file looks like:

```markdown
---
name: greet
description: Greet the user using the current time and working directory.
---

Hello. The time is `date "+%Y-%m-%d %H:%M:%S"`, working dir is ${HERMES_WORKING_DIR}.

Greet the user in one sentence and remind them of today's date.
```

Is that a "tool"? Yes. It has a schema (frontmatter `name` + `description`), it has executable content (variable substitution + inline shell + an instruction to the model). But its "code" is the prompt itself — you can't import it, can't unit-test branches, its type is `string`.

s03 solves: **how does the Registry see this thing and dispatch it as a Tool?**

## Solution

Three pieces:

1. **`Skill` type**: a plain struct parsed from a markdown file — `Name` / `Description` / `BodyTemplate`.
2. **`Skill.Expand(ctx, env)`**: variable substitution → inline-shell expansion. **Vars before shell**, so `` `cat ${HERMES_SKILL_INPUT}` `` resolves the variable to a real path *before* bash runs.
3. **`SkillTool`**: an adapter that wraps a `Skill` as a `Tool`. `Schema()` comes from the frontmatter; `Execute()` calls `Skill.Expand()` and returns the resolved string as the `tool_result`.

Registration uses toolset label `"skill-<name>"`. s02's `canReplace` automatically gives shadow protection — a skill cannot override builtin `bash`, two skills with the same name conflict. The loop knows nothing of this; it just sees more tools in the Registry.

## How It Works

```ascii-anim frames=2
┌────────────────────────────────────────────────────────────────┐
│  skills/                                                       │
│   ├── greet.md                                                 │
│   └── summarize.md          ─── LoadSkills(dir) ───┐           │
│                                                    ▼           │
│   ┌──────────────────────────────────────────────────────────┐ │
│   │ Skill{Name, Description, BodyTemplate}                  │ │
│   │   │                                                      │ │
│   │   ▼  NewSkillTool(skill, env)                           │ │
│   │ SkillTool implements Tool                                │ │
│   └──────────────────────────────────────────────────────────┘ │
│         │                                                      │
│         ▼  registry.Register(skillTool, "skill-"+name)         │
│   ┌──────────────────────────────────────────────────────────┐ │
│   │ Registry (from s02)                                      │ │
│   │   bash       (toolset=builtin)                           │ │
│   │   read_file  (toolset=builtin)                           │ │
│   │   skill_greet     (toolset=skill-greet)     ← added s03  │ │
│   │   skill_summarize (toolset=skill-summarize) ← added s03  │ │
│   └──────────────────────────────────────────────────────────┘ │
│         │                                                      │
│         ▼  When the model invokes skill_summarize:             │
│   SkillTool.Execute → Skill.Expand →                           │
│     1. substituteVars (${HERMES_*})                            │
│     2. expandInlineShell (`cmd`)                               │
│   → resolved prompt is fed back as tool_result                 │
└────────────────────────────────────────────────────────────────┘
```

The `Expand` core (excerpt from [`skill.go`](https://github.com/Ding-Ye/learn-hermes-agent/blob/main/agents/s03-skills/skill.go)):

```go
func (s *Skill) Expand(ctx context.Context, env *Env) (string, error) {
    out := substituteVars(s.BodyTemplate, env)
    return expandInlineShell(ctx, out)
}

var varRE = regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)\}`)

func substituteVars(s string, env *Env) string {
    return varRE.ReplaceAllStringFunc(s, func(match string) string {
        name := match[2 : len(match)-1]
        if v, ok := env.Resolve(name); ok { return v }
        return match // unknown var preserved, LLM sees what's missing
    })
}

var shellRE = regexp.MustCompile("`([^`\n]+)`")

func expandInlineShell(ctx context.Context, s string) (string, error) {
    // Simplified: every single-backtick token runs through bash.
    // Real hermes parses markdown structure and skips fenced code blocks
    // (see agent.skill_preprocessing).
    out := shellRE.ReplaceAllStringFunc(s, func(match string) string {
        cmd := match[1 : len(match)-1]
        b, err := exec.CommandContext(ctx, "bash", "-c", cmd).Output()
        if err != nil { return fmt.Sprintf("(shell error: %v)", err) }
        return strings.TrimRight(string(b), "\n")
    })
    return out, nil
}
```

**Four non-obvious points**:

1. **Vars before shell**. Otherwise `` `cat ${HERMES_SKILL_INPUT}` `` would pass `${HERMES_SKILL_INPUT}` to bash as a literal string (cat would fail to find that file). We resolve the var first in Go, then bash sees the real path.

2. **Unknown variables preserved as literal**, not erroring. This is the hermes style — leave `${UNDEFINED}` in the prompt so the model can tell the user "you forgot to set X". More user-friendly than crashing.

3. **Naive backtick parsing**: our regex matches **any** single-backtick token, including ones inside fenced ` ``` ` blocks. In our `summarize.md` example this happens to be what we want (the fenced block contains exactly one `` `cat ...` ``). But `` ```\nlet x = `dynamic`\n``` `` would also expand the inner `` `dynamic` `` — a bug. **Real hermes splits markdown into segments first** in `agent.skill_preprocessing`. We leave the bug to keep s03 ~100 LOC, and call it out here.

4. **Free shadow protection**: registering with `"skill-"+name` makes s02's `canReplace` automatically reject a skill named `bash` from overriding the builtin. We don't add any code. That's the dividend of s02's abstraction.

## What Changed (vs. s02)

```diff
  // main.go
  registry := NewRegistry()
  must(registry.Register(NewBashTool(), ToolsetBuiltin))
  must(registry.Register(NewReadFileTool(), ToolsetBuiltin))

+ // s03: load skills/, register each .md as a SkillTool
+ skills, err := LoadSkills(skillsAbsDir)
+ if err != nil { log.Fatalf("...", err) }
+ for _, s := range skills {
+     registry.Register(NewSkillTool(s, env), "skill-"+s.Name)
+ }

  loop := &Loop{Provider: ..., Registry: registry, ...}
  loop.Run(ctx, prompt)
```

**`registry.go` and `loop.go` are byte-identical to s02** — that's the proof s02's abstraction is right. All new code lives in `skill.go` and `skill_tool.go`.

## Try It

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s03-skills

# greet.md uses `date` inline shell + ${HERMES_WORKING_DIR}
go run . -v "use skill_greet to say hello"

# summarize.md uses ${HERMES_SKILL_INPUT} + inline cat
go run . -v "summarize ./README.md using skill_summarize, pass input='./README.md'"

# Tests cover var subst / shell expansion / shadow protection
go test -v ./...
```

Expected shape:

```
[registry] 4 tools: [bash read_file skill_greet skill_summarize] (gen=4)
[skills] 2 skills loaded from /Users/yeding/learn-hermes-agent/skills
[turn 0] assistant: I'll use skill_greet.
[turn 0] -> skill_greet map[]
[turn 0] <- Hello. The time is 2026-05-09 13:51:33, working dir is /Users/yeding/learn-hermes-agent/agents/s03-skills...
[turn 1] assistant: Hi! Today is 2026-05-09.
Hi! Today is 2026-05-09.
```

## Upstream Source Reading

hermes loads skills from `tools/skills_tool.py`. Skills are NOT single `.md` files — they're **directories with `SKILL.md`**, so a skill can ship `references/`, `templates/`, `assets/`, `scripts/`. The full file is 800+ lines; below is the scanning skeleton + the registry registration shape.

```upstream:hermes_agent/skills_tool.py#L1-L60
# Excerpted + simplified from tools/skills_tool.py

from pathlib import Path
from typing import Iterable

# hermes scans multiple dirs, each recursively for SKILL.md
DEFAULT_SKILL_DIRS = [
    Path.home() / ".hermes" / "skills",   # user-private
    # config.yaml can add team / org-shared dirs
]

EXCLUDED_DIRS = {".git", ".hub", ".archive"}


def iter_skill_index_files(scan_dir: Path, index_name: str = "SKILL.md") -> Iterable[Path]:
    """Recursively find every <subdir>/SKILL.md, skipping .git/.hub/.archive.
    Category is inferred from path depth: skills/mlops/axolotl/SKILL.md
    yields category 'mlops'.
    """
    for path in scan_dir.rglob(index_name):
        if any(part in EXCLUDED_DIRS for part in path.parts):
            continue
        yield path


def _find_all_skills(config) -> list[dict]:
    seen_names = set()
    skills = []
    for scan_dir in resolve_skill_dirs(config):
        for index in iter_skill_index_files(scan_dir):
            meta = parse_frontmatter(index.read_text())
            name = meta["name"]
            if name in seen_names: continue   # cross-dir dedupe
            if is_disabled(name, config):     continue   # user-disabled
            if not platform_match(meta):      continue   # macos/linux tags
            seen_names.add(name)
            skills.append({
                "name": name,
                "category": _category_from_path(index, scan_dir),
                "description": meta["description"],
                "path": str(index),
                "metadata": meta.get("metadata", {}),
            })
    return skills


# Registered in ToolRegistry as ONE tool `skill_view` — not one per skill.
# The model first calls skill_view(name='greet') to load content (with
# preprocess=True triggering template substitution), then acts —
# "progressive disclosure".
registry.register(
    name="skill_view",
    toolset="skills",
    schema=SKILL_VIEW_SCHEMA,
    handler=_skill_view_with_bump,
    check_fn=check_skills_requirements,
)
```

```upstream:hermes_agent/skill_preprocessing.py#L1-L40
# Excerpted from agent/skill_preprocessing.py
# (template + shell expansion actually live HERE)

def preprocess_skill_content(body: str, env: dict) -> str:
    """Fenced-block-aware: keep ```...``` segments verbatim,
    run var-substitute + inline-shell on everything else.
    """
    segments = split_by_fenced_blocks(body)  # marks each as is_fenced
    out = []
    for seg in segments:
        if seg.is_fenced:
            out.append(seg.text)            # leave alone
            continue
        text = substitute_vars(seg.text, env)
        text = expand_inline_shell(text)
        out.append(text)
    return "".join(out)


def substitute_vars(s: str, env: dict) -> str:
    # ${HERMES_*} placeholders; unknown vars left as literal
    return _VAR_RE.sub(lambda m: env.get(m.group(1), m.group(0)), s)


def expand_inline_shell(s: str) -> str:
    # `cmd` → stdout of bash -c cmd
    return _SHELL_RE.sub(lambda m: _run_bash(m.group(1)), s)
```

**Reading notes**:

- **Directory vs flat file**: upstream's `<dir>/SKILL.md` lets a skill carry `references/`, `scripts/`, etc.; ours is a flat `<dir>/<name>.md`. Production wants the directory shape; teaching wants the flat one (half the code).
- **Recursive scan + dedupe**: upstream `seen_names` dedupes across multiple skill dirs — a user can simultaneously enable `~/.hermes/skills/` and `team-shared/skills/`, first wins on collision. We scan one dir, no recursion.
- **Progressive disclosure**: hermes does NOT register each skill as its own tool — it registers exactly ONE tool, `skill_view`, and the model calls `skill_view(name='X')` to load on demand. Pro: many skills cost only 1 LLM tool slot. Con: one extra round trip. Our mini chose "one tool per skill" — more direct, makes the Registry-absorption lesson visible — at the cost of token economy. **Pick whichever for your real agent based on skill count and context budget**.
- **Fenced-block awareness**: upstream `split_by_fenced_blocks` segments markdown before expanding. Ours blindly expands all single-backticks. Documented.
- **Platform filter, version, required env vars**: upstream frontmatter has many more fields. Production skills declare `version`, `platforms: [macos, linux]`, `required_environment_variables: [{name: API_KEY, prompt: "..."}]` — all sit in metadata for runtime checks.

**Read further**: start in `tools/skills_tool.py` at `_find_all_skills`, follow `iter_skill_index_files` for the scan strategy, follow `skill_view` into `agent/skill_preprocessing.py` for the real template/shell expansion, then read `hermes_cli/curator.py`'s `_idle_days` function — that is the truth of hermes's "self-improving": **track each skill's `last_activity_at`, archive it after N idle days**, NOT auto-generate new skills.

---

**Next**: s04 persists session state (the messages list + token totals) to disk and adds `/resume` `/branch` `/reset` `/new` commands. Skills survive across sessions as static resources; the conversation itself starts gaining a persistent shape.
