# learn-hermes-agent

> 用 Go 从零渐进构建一个 [hermes-agent](https://github.com/NousResearch/hermes-agent)。每节加一个机制，每节末尾对照上游 Python 源码。
>
> **English**: [README.en.md](./README.en.md)

---

这个仓库不教你"用"hermes-agent，它教你"它怎么从零长出来"。每节聚焦一个机制——agent loop、tool registry、skills、memory、plugin、MCP、cron、gateway——用最少的 Go 代码把它立起来，再贴上游 Python 真实实现的等价片段做对照阅读。

教学法仿照 [shareAI-lab/learn-claude-code](https://github.com/shareAI-lab/learn-claude-code)：mental-model 优先 → ASCII 图 → 30-60 行核心代码 → 与上一节的 diff → 动手试 → 上游源码导读。

---

## 课程地图

| #     | 主题 | 教什么 hermes 机制 | 状态 |
| ----- | ---- | ----------- | ---- |
| **s01** | [最小 agent loop](./docs/zh/s01-minimum-loop.md) | messages / tool dispatch / stop_reason / provider 抽象 | ✅ |
| **s02** | [Tool 注册系统](./docs/zh/s02-tool-registry.md) | 统一 registry，shadow 防护，generation 计数器 | ✅ |
| s03   | Skills 系统 | **Markdown prompt + 模板替换 + 内联 shell 展开** ★hermes 招牌 | ⏳ |
| s04   | Session 持久化 | 消息历史 / token 计数 / `/resume` `/branch` `/reset` `/new` | ⏳ |
| s05   | Memory Provider | **Pluggable 接口 + 一个 FTS5 SQLite 实现** | ⏳ |
| s06   | Plugin + Curator | **Plugin 系统 + Curator 后台维护循环** ★hermes 灵魂 | ⏳ |
| s07   | MCP 集成 | stdio + HTTP transport，融入同一 tool registry | ⏳ |
| s08   | Terminal Backend | 工厂模式：local + Docker，预留 SSH/Modal/Daytona | ⏳ |
| s09   | Multi-process 架构 | **CLI ↔ Gateway ↔ Scheduler ↔ Kanban DB** ★架构跃迁 | ⏳ |
| s10   | Gateway 平台适配器 | Telegram + Discord 两个 adapter | ⏳ |
| s_full | 端到端集成 | 跨 session、跨平台、有 cron 的完整业务场景 | ⏳ |
| 附录 A | Atropos / RL 心智模型 | 不重写，只画图讲学习闭环 | ⏳ |
| 附录 B | 上游源码导读地图 | 完整的 hermes-agent Python 阅读路线 | ⏳ |

---

## 为什么 Python 项目用 Go 教？

1. **教学清晰度**。Go 的 interface + 强类型让 `Provider` / `Tool` / `MemoryProvider` 这类契约一眼就懂，省掉 Python 教程里大量的"假装有类型"docstring。
2. **单二进制心智模型**。贴合 hermes "$5 VPS / serverless / GPU clusters" 部署哲学——`go build` 出一个二进制，没有运行时依赖、没有虚拟环境。
3. **每节独立可编译**。`agents/sXX/` 各自一个 Go module，没有共享 `pkg/`，session 之间是 self-contained 的。读者从任意章节切入都能跑。

代价是上游 Python ↔ Go 的概念切换。每节末尾的"上游源码阅读"环节就是为了弥合这个 gap：你能看到 hermes-agent 真实代码里同一个机制是怎么实现的、有什么我们没做的复杂性。

---

## 快速开始

```bash
git clone https://github.com/Ding-Ye/learn-hermes-agent.git
cd learn-hermes-agent

# 跑 s01 的 mini-agent
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s01-loop
go run . -v "compute 17 * 23 by running an expression in bash"
```

需要 **Go ≥ 1.21**。s01 只用 stdlib。

启动 Web doc viewer（双语阅读）：

```bash
cd web
npm install
npm run dev    # 开 http://localhost:3000，会自动跳到 /zh
```

需要 **Node ≥ 20**。

---

## 仓库结构

```
learn-hermes-agent/
├── agents/                  Go 实现，每节一个独立 module
│   └── s01-loop/            ← 当前可用
├── docs/{zh,en}/            双语文档，每节一份 markdown
├── skills/                  示例 skill 文件（s03 起复用）
├── upstream-readings/       hermes-agent 上游 Python 教学摘录
├── web/                     Next.js doc viewer
├── go.work                  跨 session 工作区
└── .github/workflows/       CI（Go 构建、Web 构建、Docs lint）
```

---

## 阅读顺序

1. 直接打开 [`docs/zh/s01-minimum-loop.md`](./docs/zh/s01-minimum-loop.md)，按六段式（Problem / Solution / How It Works / What Changed / Try It / Upstream Source Reading）走完。
2. 跑一下 `agents/s01-loop`（带 `-v` 看每一步）。
3. 等 s02 上线时按章节号继续。

---

## 与 hermes-agent 的关系

本仓库不是 fork、不是替代品、也不是生产级。它是 hermes-agent 的**教学伴读**：用 Go 抽出每个机制的最小骨架，配合上游 Python 源码片段，让你能在心里画出 hermes-agent 的架构图。

学完这十节，**强烈建议** 把 hermes-agent 的源码 clone 下来对照阅读：

```bash
git clone https://github.com/NousResearch/hermes-agent.git
```

附录 B 提供完整的源码导读地图。

---

## License

[MIT](./LICENSE)

致谢：[NousResearch/hermes-agent](https://github.com/NousResearch/hermes-agent) 的所有作者，以及 [shareAI-lab/learn-claude-code](https://github.com/shareAI-lab/learn-claude-code) 给出的优雅教学法范式。
