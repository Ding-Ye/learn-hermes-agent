---
title: "Appendix A · Atropos / RL mental model"
chapter: A
slug: appendix-a-atropos-rl
est_read_min: 12
---

# Appendix A · Atropos / RL mental model

> What this teaches: explain the relationship between hermes-agent and "training". hermes is **not** an agent that auto-edits its own code; it is a **trajectory generator** paired with NousResearch's [Atropos](https://github.com/NousResearch/atropos) RL framework that trains new models from those trajectories. This appendix only covers the mental model — we **don't reimplement** the training side. Our Go mini focuses on hermes's runtime; the training side is best read directly in the upstream Python.

---

## Two misreadings of "self-improving"

Newcomers easily get misled by the "self-improving AI agent" tagline. Two common misreadings:

1. **Misreading A**: the agent automatically writes new skills / patches plugins / fixes its own bugs.
   ❌ **No**. Skills are hand-written Markdown files; plugins are compile-time artifacts; the agent cannot self-modify code.

2. **Misreading B**: the agent learns online and gets smarter every conversation.
   ❌ **Also no**. Anthropic API calls are stateless — each LLM call uses the same fixed weights; no in-flight gradient update.

**So what does "self-improving" actually mean? Two things:**

1. **Runtime maintenance**: the Curator plugin (s06) tracks memory usage and auto-archives stale entries. That's automatic *gardening*, not automatic learning.
2. **Training data collection**: every agent conversation gets recorded as a trajectory (messages + tools used + outcomes). Those trajectories feed Atropos, which **humans choose** to use to fine-tune a new base model.

Point #2 is the real reason hermes-agent exists. It's a **trajectory generator** — by having the agent solve large numbers of real tasks, you produce training data.

## What Atropos is

[Atropos](https://github.com/NousResearch/atropos) is NousResearch's RL training framework. Roughly:

```ascii-anim frames=2
┌────────────────────────────────────────────────────────────────┐
│  hermes-agent  ←── prompts (from users / cron / test sets)      │
│       │                                                         │
│       │ runs many sessions                                      │
│       ▼                                                         │
│  trajectories: [                                                │
│    {messages: [...], tools_used: [...], reward: 0.8},          │
│    {messages: [...], tools_used: [...], reward: 0.2},          │
│    ...                                                          │
│  ]                                                              │
│       │                                                         │
│       │ batch & filter (high reward / verified outcomes)        │
│       ▼                                                         │
│  Atropos training environment                                   │
│       │                                                         │
│       │ apply RL / SFT / DPO / KTO updates                      │
│       ▼                                                         │
│  new model weights                                              │
│       │                                                         │
│       │ deploy as the next "Hermes-X"                           │
│       ▼                                                         │
│  hermes-agent uses the new base → smarter → better trajectories │
│  → ...                                                          │
└────────────────────────────────────────────────────────────────┘
```

That is how the public Hermes series of models (Hermes 2, Hermes 3, Hermes 4, …) gets made — NousResearch runs hermes-agent on tons of agent tasks, processes the trajectories with Atropos, and trains the next generation of base models. **The loop spans months and humans supervise throughout** — not online self-learning.

## hermes-agent's environments/ directory

The `environments/` directory in the hermes-agent repo contains Atropos environments — they wrap hermes's agent loop into the standard Atropos RL environment interface that Atropos can consume directly. Key files:

- `environments/__init__.py` — `HermesAgentBaseEnv` base class
- `environments/coding/` — coding-task environment: given a task description, let the agent solve it, verify, reward
- `environments/skill_creation/` — skill-creation environment: have the agent write and test a new skill
- `environments/multi_turn_tool/` — multi-turn tool-use environment

Each environment implements Atropos's standard interface (`reset()` / `step(action)` / `reward(state)` / etc.), so the Atropos training pipeline can pull batches of trajectories uniformly.

## What a trajectory looks like

```jsonc
{
  "session_id": "atropos-coding-task-42",
  "task_description": "Find all .py files in /tmp/ larger than 1MB and print their sizes",
  "messages": [
    {"role": "user", "content": "[task description here]"},
    {"role": "assistant", "content": [
      {"type": "tool_use", "name": "bash", "input": {"command": "find /tmp -name '*.py' -size +1M -exec ls -lh {} \\;"}}
    ]},
    {"role": "user", "content": [{"type": "tool_result", "content": "..."}]},
    {"role": "assistant", "content": [{"type": "text", "text": "Here are the files:..."}]}
  ],
  "outcome": {
    "success": true,
    "verifier_output": "matches expected: 3 files",
    "reward": 0.95,
    "tool_calls": 2,
    "wall_seconds": 3.4,
    "tokens_in": 412,
    "tokens_out": 89
  }
}
```

This format works for both SFT (learn from messages directly) and RL (reward signal drives policy update). Atropos supports both setups.

## Why our mini doesn't reimplement this

Three reasons:

1. **Atropos heavily depends on PyTorch / Hugging Face / GPU clusters** — a Go rewrite would be pointless.
2. **The training loop is an offline batch process**, not part of the agent runtime. Our curriculum focuses on hermes's *runtime*, not its training pipeline.
3. **Atropos has its own tutorials** — the upstream README + paper explain it better than we could.

**The right learning path**:

1. Finish the 10 chapters of this course; understand hermes-agent's runtime mechanics.
2. Read Atropos's README to learn the RL environment interface.
3. Walk hermes-agent's `environments/coding/__init__.py` to see how trajectories pop out of the hermes loop.
4. If you actually want to train: collect a batch of high-quality trajectories, run them through the Atropos pipeline, rent some GPUs. That's beyond this course.

## Mental-model summary

| Dimension | hermes-agent (runtime) | Atropos (training side) |
|---|---|---|
| Language | Python | Python + PyTorch |
| Trigger | user message / cron / API call | training-task list |
| State | sessions + memories + skills | model checkpoints + dataset |
| Output | reply to user | new model weights |
| Frequency | real-time (ms–s) | offline batch (hours–days) |
| Our mini covers | yes (s01–s10) | no (this appendix is mental model only) |
| Our mini omits | curator edge cases / multi-platform | the entire training side |

Once you internalise that hermes-agent is one of Atropos's **trajectory sources**, the whole project clicks: every mechanism — session persistence, memory FTS5, plugin lifecycle, multi-process gateway — has to support both production runtime AND clean trajectory extraction. The instrumentation we shipped throughout (token counts, tool-call records, cost tracking) exists for this dual goal.

---

**Read next**: Appendix B [Upstream source-reading map](./appendix-b-upstream-map.md) — a complete tour of the hermes-agent Python repo.
