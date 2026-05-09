# skills/

示例 skill 文件，从 s03 开始被各 session 复用。

Hermes-agent 的 skill 不是代码——它们是 Markdown prompt，里面可以含模板变量
（如 `${HERMES_SKILL_DIR}`、`${HERMES_SESSION_ID}`）和内联 shell 展开（`` `cmd` ``）。

s01 / s02 不需要 skill。s03 才会真正解析这些文件。这里先放进来，是为了让所有
session 共享同一份示例数据，避免每个 session 各拷一份。

| File | 功能 |
|---|---|
| `greet.md` | 一个最简的 skill：固定模板 + 一个动态 shell 注入 |
| `summarize.md` | 一个略复杂的 skill：使用模板变量、有结构化输出要求 |

格式说明（YAML 前置元数据 + 正文）：

```markdown
---
name: skill-name
description: 一句话描述，会被加入 / 命令的发现列表
---

正文是 prompt。可以含 ${VAR} 和 `inline shell`。
```
