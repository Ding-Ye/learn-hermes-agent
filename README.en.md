# learn-hermes-agent

> Build a [hermes-agent](https://github.com/NousResearch/hermes-agent) from scratch in Go, session by session. Each chapter adds one mechanism and ends with a side-by-side reading of the upstream Python source.
>
> **中文**: [README.md](./README.md)

---

This repo doesn't teach you to *use* hermes-agent — it teaches you how it *grows from scratch*. Each chapter focuses on one mechanism — agent loop, tool registry, skills, memory, plugin, MCP, cron, gateway — implemented as a tiny Go program, paired with the equivalent upstream Python excerpt for cross-reading.

The pedagogy is borrowed from [shareAI-lab/learn-claude-code](https://github.com/shareAI-lab/learn-claude-code): mental-model first → ASCII diagram → 30–60 lines of core code → diff vs. the previous chapter → try it → upstream source reading.

---

## Curriculum

| #     | Topic | Hermes mechanism | Status |
| ----- | ----- | ---------------- | ------ |
| **s01** | [Minimum agent loop](./docs/en/s01-minimum-loop.md) | messages / tool dispatch / stop_reason / provider abstraction | ✅ |
| **s02** | [Tool registry](./docs/en/s02-tool-registry.md) | Unified registry, shadow protection, generation counter | ✅ |
| **s03** | [Skills system](./docs/en/s03-skills.md) | **Markdown prompts + template substitution + inline shell expansion** ★signature | ✅ |
| **s04** | [Session persistence](./docs/en/s04-session.md) | JSON files / atomic save per turn / `-resume` `-branch` `-reset` `-list` | ✅ |
| **s05** | [Memory provider](./docs/en/s05-memory.md) | **Pluggable interface + FTS5 SQLite impl + memory_search/save tools** | ✅ |
| **s06** | [Plugin + Curator](./docs/en/s06-plugins-curator.md) | **Plugin bus + real self-improving = auto-archive idle memories** ★soul | ✅ |
| **s07** | [MCP integration](./docs/en/s07-mcp.md) | JSON-RPC over stdio + dual-mode binary with self-contained e2e test | ✅ |
| **s08** | [Terminal backend](./docs/en/s08-terminal-backends.md) | `Environment` interface + factory: local / docker:image, uniform `ExecResult` | ✅ |
| **s09** | [Multi-process](./docs/en/s09-multiprocess.md) | **3 processes (CLI / Gateway / Scheduler) + Kanban SQLite + atomic claim** ★architectural leap | ✅ |
| **s10** | [Gateway adapters](./docs/en/s10-platforms.md) | Telegram + Discord webhook adapters; dry-run mode demos without tokens | ✅ |
| **s_full** | [End-to-end](./docs/en/s_full-integration.md) | All 10 chapters wired up: architecture diagram + 16-step execution trace + demo script | ✅ |
| **App. A** | [Atropos / RL](./docs/en/appendix-a-atropos-rl.md) | The truth behind "self-improving": trajectory generator + Atropos training | ✅ |
| **App. B** | [Upstream map](./docs/en/appendix-b-upstream-map.md) | Full hermes-agent Python repo index + chapter cross-reference + extension exercises | ✅ |

---

## Why Go for a Python project?

1. **Pedagogical clarity**. Go's interfaces and strong types make `Provider` / `Tool` / `MemoryProvider` contracts visible at a glance — Python tutorials waste a lot of words on "pretend types in docstrings".
2. **Single-binary mental model**. It matches hermes's "$5 VPS / serverless / GPU clusters" deployment philosophy — `go build` produces a single binary with no runtime dependencies and no virtualenvs.
3. **Independently compilable per chapter**. Each `agents/sXX/` is its own Go module — no shared `pkg/`. You can read any chapter cold and run it.

The cost is the Python↔Go context switch. The "Upstream Source Reading" section at the end of each chapter exists exactly to bridge that gap: you see the same mechanism in the real hermes code, including the complexity our mini deliberately omits.

---

## Quickstart

```bash
git clone https://github.com/Ding-Ye/learn-hermes-agent.git
cd learn-hermes-agent

# Run s01's mini agent (Anthropic Claude by default)
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s01-loop
go run . -v "compute 17 * 23 by running an expression in bash"
```

Requires **Go ≥ 1.21**. s01 uses stdlib only (the multi-provider translation in `provider_openai.go` is also stdlib).

### Multi-model support (DeepSeek / Qwen / Moonshot / self-hosted …)

Every session ships an **OpenAI-compatible translation layer**, so any provider that speaks the OpenAI Chat Completions wire format works out of the box:

```bash
# DeepSeek
export DEEPSEEK_API_KEY=sk-...
go run . -provider deepseek -v "compute 17 * 23 with bash"

# Alibaba Tongyi Qwen
export DASHSCOPE_API_KEY=sk-...
go run . -provider qwen "..."

# Moonshot Kimi
export MOONSHOT_API_KEY=sk-... && go run . -provider moonshot "..."

# Groq + Llama-3.3-70b (very fast)
export GROQ_API_KEY=gsk_... && go run . -provider groq "..."

# Self-hosted vLLM / SGLang (OpenAI-compat endpoint on :8000)
go run . -provider local -model your-model -v "..."
```

Eight provider profiles (anthropic / openai / deepseek / moonshot / qwen / groq / openrouter / local) ship out of the box. Unit tests cover the bidirectional Anthropic ↔ OpenAI wire-format translation. Full guide: [Multi-model integration](./docs/en/multi-model.md).

Start the Web doc viewer (bilingual):

```bash
cd web
npm install
npm run dev    # http://localhost:3000 redirects to /zh
```

Requires **Node ≥ 20**.

---

## Repository layout

```
learn-hermes-agent/
├── agents/                  Go implementations, one module per chapter
│   └── s01-loop/            ← currently shipped
├── docs/{zh,en}/            Bilingual docs, one markdown per chapter
├── skills/                  Sample skill files (reused from s03 onward)
├── upstream-readings/       Hermes-agent Python teaching excerpts
├── web/                     Next.js doc viewer
├── go.work                  Cross-session workspace
└── .github/workflows/       CI (Go build, Web build, docs lint)
```

---

## Reading order

1. Open [`docs/en/s01-minimum-loop.md`](./docs/en/s01-minimum-loop.md), follow the six-section spine (Problem / Solution / How It Works / What Changed / Try It / Upstream Source Reading).
2. Run `agents/s01-loop` (use `-v` to see each step).
3. When s02 ships, follow the chapter numbers.

---

## Relationship to hermes-agent

This repo is **not** a fork, **not** a replacement, and **not** production-grade. It is a teaching companion for hermes-agent — Go strips out each mechanism to its bones, and the upstream Python excerpts wire them back to the real code so you can read the real implementation with a map in hand.

After finishing the ten chapters, **strongly recommended**: clone hermes-agent and read in parallel:

```bash
git clone https://github.com/NousResearch/hermes-agent.git
```

Appendix B provides the full reading map.

---

## License

[MIT](./LICENSE)

Acknowledgements: the authors of [NousResearch/hermes-agent](https://github.com/NousResearch/hermes-agent), and [shareAI-lab/learn-claude-code](https://github.com/shareAI-lab/learn-claude-code) for the elegant teaching template.
