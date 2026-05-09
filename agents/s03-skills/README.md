# s03 · Skills

把 `skills/*.md`（YAML frontmatter + Markdown body + 模板变量 + 内联 shell）作为一种新的 **工具来源** 接入 s02 的 Registry。一次 commit 验证：Registry 抽象能 absorb 一个完全不同形态的 tool source（基于文件的 prompt）。

Wire `skills/*.md` files (YAML frontmatter + Markdown body + template vars + inline shell) into the s02 Registry as a new **tool source**. Validates the Registry abstraction can absorb a wildly different shape of tool (a file-based prompt).

## 运行 / Run

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s03-skills

# 默认从 ../../skills/ 读 skill 文件（仓库根的 skills/）
go run . -v "use skill_greet to say hello"
go run . -v "use skill_summarize with input='./README.md'"

# 自定义 skills 目录
go run . -v -skills-dir /path/to/your/skills "..."
```

需要 Go ≥ 1.21。s03 只用 stdlib（手写一个极简 YAML frontmatter 解析器，不引 yaml lib——见文档说明）。

## 测试 / Tests

```bash
go test -v ./...
```

`skill_test.go` 覆盖：变量替换、未知变量原样保留、单/多内联 shell 展开、变量先于 shell 展开（关键顺序）、`LoadSkills` 解析合法/拒绝非法。

## 文件 / Files

| File | Role |
|---|---|
| `main.go` | 装配 builtin 工具 + 加载并注册 skills |
| `skill.go` | **本节核心**：`Skill` 类型、`Env`、`LoadSkills`、`Expand`（vars + shell） |
| `skill_tool.go` | `SkillTool` 实现 `Tool` 接口，把 Skill 包装成 Registry 里的 entry |
| `provider.go` | 复用 s02 |
| `tools.go` | 复用 s02（BashTool + ReadFileTool） |
| `registry.go` | 复用 s02 |
| `loop.go` | 复用 s02 |

## 教学要点

- **Skill = 数据**，loop 不知道它的存在；Registry 只看到一个 `Tool`
- **toolset 标签** `"skill-<name>"` 通过 s02 的 `canReplace` 自动获得 shadow 防护：skill 不能盖 builtin、两个 skill 同名互斥
- **变量替换在前、shell 在后**：让 `\`cat ${HERMES_SKILL_INPUT}\`` 先把变量解析成路径，再 bash 执行
- **简化与上游差异**：我们不递归子目录、不识别 markdown fenced 代码块（会把里面的反引号当 shell 跑）、用极简手写 YAML 解析。这些都在文档明确写出 "real hermes does X here"

完整讲解见 [`docs/zh/s03-skills.md`](../../docs/zh/s03-skills.md) / [`docs/en/s03-skills.md`](../../docs/en/s03-skills.md)。
