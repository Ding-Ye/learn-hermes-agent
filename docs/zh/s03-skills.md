---
title: "s03 · Skills 系统"
chapter: 3
slug: s03-skills
est_read_min: 14
---

# s03 · Skills 系统

> 教什么：把 `skills/*.md`（YAML frontmatter + Markdown body + 模板变量 + 内联 shell）作为一种**新的工具来源**接入 s02 的 Registry。这是 hermes-agent 最有特色的设计：技能不是代码，是 prompt。教学目标是让你看到 s02 的 Registry 抽象**真的能** absorb 一个完全异形的 tool source——只要它能 implement `Tool` interface。

---

## Problem / 问题

s02 让 Registry 接受任意来源的 `Tool`。但什么算"任意来源"？光是另一个 builtin 函数没什么意思。真正考验抽象的，是接进来一个**完全不像代码**的东西。

Hermes 的招牌设计：让用户不写一行 Go/Python，**只写 markdown** 就能扩展 agent。文件长这样：

```markdown
---
name: greet
description: 用当前时间和工作目录，打个招呼。
---

你好。现在是 `date "+%Y-%m-%d %H:%M:%S"`，工作目录是 ${HERMES_WORKING_DIR}。

请用一句话向用户打招呼，并提醒他/她今天的日期。
```

这是个"工具"吗？是。它有 schema（frontmatter 里的 name + description）、有可执行的内容（变量替换 + 内联 shell + 一段对模型的指令）。但它的"代码"是 prompt 本身——你没法 import 它、没法单元测试它的逻辑分支、它的 type 是 `string`。

s03 要解决：**怎么让 Registry 看见这种东西，并把它当 Tool 派发**。

## Solution / 解决方案

三件事：

1. **`Skill` 类型**：从 markdown 文件解析出来的结构——`Name` / `Description` / `BodyTemplate`。一个 plain old struct。
2. **`Skill.Expand(ctx, env)`**：变量替换 → 内联 shell 展开，得到最终 prompt 字符串。**变量先于 shell**：让 `\`cat ${HERMES_SKILL_INPUT}\`` 先把变量解析成路径再 bash 跑。
3. **`SkillTool`**：一个 adapter——把 `Skill` 包装成 `Tool` interface。`Schema()` 来自 frontmatter，`Execute()` 调 `Skill.Expand()` 把展开后的 prompt 作为 `tool_result` 返回给模型。

注册时用 toolset 标签 `"skill-<name>"`，s02 Registry 的 `canReplace` 会自动给 shadow 防护——skill 不可能盖掉 builtin `bash`，两个同名 skill 互斥。loop 完全不知道这事，它只看到 Registry 多了几个 tool。

## How It Works / 工作原理

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
│   │ Registry (s02)                                          │ │
│   │   bash       (toolset=builtin)                           │ │
│   │   read_file  (toolset=builtin)                           │ │
│   │   skill_greet     (toolset=skill-greet)     ← s03 加入   │ │
│   │   skill_summarize (toolset=skill-summarize) ← s03 加入   │ │
│   └──────────────────────────────────────────────────────────┘ │
│         │                                                      │
│         ▼  当模型决定调用 skill_summarize:                       │
│   SkillTool.Execute → Skill.Expand →                           │
│     1. substituteVars (${HERMES_*})                            │
│     2. expandInlineShell (`cmd`)                               │
│   → 把展开后的 prompt 作为 tool_result 喂回模型                  │
└────────────────────────────────────────────────────────────────┘
```

核心 `Expand`（节选自 [`skill.go`](https://github.com/Ding-Ye/learn-hermes-agent/blob/main/agents/s03-skills/skill.go)）：

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
        return match // 未知变量原样保留，让 LLM 看到缺什么
    })
}

var shellRE = regexp.MustCompile("`([^`\n]+)`")

func expandInlineShell(ctx context.Context, s string) (string, error) {
    // 简化版：单层反引号一概当 shell 跑。
    // 真实 hermes 在 agent.skill_preprocessing 里会跳过 fenced 代码块。
    out := shellRE.ReplaceAllStringFunc(s, func(match string) string {
        cmd := match[1 : len(match)-1]
        b, err := exec.CommandContext(ctx, "bash", "-c", cmd).Output()
        if err != nil { return fmt.Sprintf("(shell error: %v)", err) }
        return strings.TrimRight(string(b), "\n")
    })
    return out, nil
}
```

**四个非显然之处**：

1. **变量先于 shell** —— 否则 `` `cat ${HERMES_SKILL_INPUT}` `` 里的 `${...}` 会被 bash 当字面量传给 cat（它当然找不到这文件）。我们在 Go 这层先把变量解析掉，bash 收到的是真路径。

2. **未知变量留字面量**，不报错。这是 hermes 风格——把 `${UNDEFINED}` 留在 prompt 里，模型自己看到 `${UNDEFINED}` 知道有问题，可以告诉用户"你忘了配 X"。比抛异常友好。

3. **简化的反引号识别**：我们的正则会匹配**任何**单层反引号，包括 fenced ```` ``` ```` 代码块里的。在我们示例 `summarize.md` 里这正好是想要的（块里就一个 `` `cat ...` ``）。但如果 skill 里有 `` ```\nlet x = `dynamic`\n``` ``，那个 `` `dynamic` `` 也会被当 shell 跑——bug。**真实 hermes 在 `agent.skill_preprocessing` 里通过 markdown 解析跳过 fenced 块**。我们留这个 bug 是为了让代码量保持在 ~100 行，并在文档明确写出。

4. **toolset 自动 shadow 防护**：用 `"skill-"+name` 注册，s02 的 `canReplace` 自动拒绝 skill 把 builtin 盖掉。我们什么都不用做。这就是 s02 抽象的红利。

## What Changed / 与 s02 的变化

```diff
  // main.go
  registry := NewRegistry()
  must(registry.Register(NewBashTool(), ToolsetBuiltin))
  must(registry.Register(NewReadFileTool(), ToolsetBuiltin))

+ // s03: 加载 skills 目录，每个 .md 注册成 SkillTool
+ skills, err := LoadSkills(skillsAbsDir)
+ if err != nil { log.Fatalf("...", err) }
+ for _, s := range skills {
+     registry.Register(NewSkillTool(s, env), "skill-"+s.Name)
+ }

  loop := &Loop{Provider: ..., Registry: registry, ...}
  loop.Run(ctx, prompt)
```

**registry.go / loop.go 一字未改**——这是 s02 抽象到位的证据。新代码全在 `skill.go` 和 `skill_tool.go` 里。

## Try It / 动手试一试

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s03-skills

# greet.md：用 `date` 内联 shell + ${HERMES_WORKING_DIR}
go run . -v "use skill_greet to say hello"

# summarize.md：用 ${HERMES_SKILL_INPUT} + 内联 cat
go run . -v "summarize ./README.md using skill_summarize, pass input='./README.md'"

# 跑测试看模板替换 / shell 展开 / shadow 防护
go test -v ./...
```

期望输出形态：

```
[registry] 4 tools: [bash read_file skill_greet skill_summarize] (gen=4)
[skills] 2 skills loaded from /Users/yeding/learn-hermes-agent/skills
[turn 0] assistant: I'll use skill_greet.
[turn 0] -> skill_greet map[]
[turn 0] <- 你好。现在是 2026-05-09 13:51:33，工作目录是 /Users/yeding/learn-hermes-agent/agents/s03-skills...
[turn 1] assistant: 你好！今天是 2026 年 5 月 9 日。
你好！今天是 2026 年 5 月 9 日。
```

## Upstream Source Reading / 上游源码阅读

hermes 的 skill 加载在 `tools/skills_tool.py`，不是单文件 `.md`，而是**目录里的 `SKILL.md`**——这样 skill 可以带 `references/`、`templates/`、`assets/`、`scripts/` 等附属资源。整个文件 800+ 行；下面是 `_find_all_skills` 的扫描骨架与 registry 注册形态。

```upstream:hermes_agent/skills_tool.py#L1-L60
# 节选 + 简化自 tools/skills_tool.py

from pathlib import Path
from typing import Iterable

# Hermes 扫描多个目录，每个目录递归找 SKILL.md
DEFAULT_SKILL_DIRS = [
    Path.home() / ".hermes" / "skills",   # 用户私有
    # config.yaml 里可加企业内部、团队共享目录
]

EXCLUDED_DIRS = {".git", ".hub", ".archive"}


def iter_skill_index_files(scan_dir: Path, index_name: str = "SKILL.md") -> Iterable[Path]:
    """递归找所有 <subdir>/SKILL.md，跳过 .git/.hub/.archive。
    类别从相对路径深度推断：skills/mlops/axolotl/SKILL.md → 类别 'mlops'.
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
            if name in seen_names: continue   # 跨目录去重
            if is_disabled(name, config):     continue   # 用户禁用
            if not platform_match(meta):      continue   # macos/linux 标签
            seen_names.add(name)
            skills.append({
                "name": name,
                "category": _category_from_path(index, scan_dir),
                "description": meta["description"],
                "path": str(index),
                "metadata": meta.get("metadata", {}),
            })
    return skills


# 注册到 ToolRegistry：注意 hermes 只注册 *一个* 工具 `skill_view`，
# 不是每个 skill 一个。模型先调 skill_view(name='greet') 拿内容（含
# preprocess=True 触发模板替换），再做下一步——progressive disclosure。
registry.register(
    name="skill_view",
    toolset="skills",
    schema=SKILL_VIEW_SCHEMA,
    handler=_skill_view_with_bump,
    check_fn=check_skills_requirements,
)
```

```upstream:hermes_agent/skill_preprocessing.py#L1-L40
# 节选自 agent/skill_preprocessing.py（template + shell 展开真在这里）

def preprocess_skill_content(body: str, env: dict) -> str:
    """跳过 fenced 代码块的版本：先把 ```...``` 段落截下来不动，
    其余部分跑变量替换 + inline shell。
    """
    segments = split_by_fenced_blocks(body)  # 一段段切，标记 is_fenced
    out = []
    for seg in segments:
        if seg.is_fenced:
            out.append(seg.text)            # 原样保留
            continue
        text = substitute_vars(seg.text, env)
        text = expand_inline_shell(text)
        out.append(text)
    return "".join(out)


def substitute_vars(s: str, env: dict) -> str:
    # ${HERMES_*} 占位符，未知变量保留字面量
    return _VAR_RE.sub(lambda m: env.get(m.group(1), m.group(0)), s)


def expand_inline_shell(s: str) -> str:
    # `cmd` → bash -c cmd 的 stdout
    return _SHELL_RE.sub(lambda m: _run_bash(m.group(1)), s)
```

**对照阅读要点**：

- **目录式 vs 单文件**：上游 `<dir>/SKILL.md` 让 skill 能携带 `references/`、`scripts/` 等；我们是 `<dir>/<name>.md` 单文件。生产上目录式更可扩展，教学上单文件代码量减半。
- **递归扫描 + 去重**：上游 `seen_names` 跨多个 skill 目录去重——用户可以同时启用 `~/.hermes/skills/` 和 `team-shared/skills/`，重名时第一个赢。我们只扫一个目录，不递归。
- **`progressive disclosure`**：hermes 不把每个 skill 暴露成独立 tool——只暴露**一个** `skill_view`，模型按需 `skill_view(name='X')` 拿内容。优点是大量 skill 也只占 1 个 tool slot 给到 LLM；缺点是多一次 round trip。我们 mini 版选了"每个 skill 一个 tool"——更直接、Registry 抽象演示更清楚，但 token 经济性差。**两种设计你选哪个，看 skill 总数和 LLM 上下文压力**。
- **fenced 块感知**：上游 `split_by_fenced_blocks` 先做 markdown segmenting 再展开。我们偷懒一律展开。文档已警告。
- **平台过滤、版本号、required_environment_variables**：上游 frontmatter 字段比我们多得多。生产里 skill 还带 `version`, `platforms: [macos, linux]`, `required_environment_variables: [{name: API_KEY, prompt: "..."}]`。这些都进 metadata，运行时按需 prompt 用户。

**想读更多**：从 `tools/skills_tool.py` 的 `_find_all_skills` 入手，跟 `iter_skill_index_files` 看扫描策略，跟 `skill_view` 进 `agent/skill_preprocessing.py` 看真实 template/shell 展开，再看 `hermes_cli/curator.py` 的 `_idle_days` 函数——那是 hermes "self-improving" 的真相：**追踪每个 skill 的 `last_activity_at`，N 天没用就归档**，不是自动生成新 skill。

---

**下一节预告**：s04 把会话状态（messages 列表 + token 计数）写盘，加 `/resume` `/branch` `/reset` `/new` 命令。skill 文件作为静态资源跨会话保留，但会话本身的"我们刚才聊了什么"开始有持久化的形态。
