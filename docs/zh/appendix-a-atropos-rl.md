---
title: "附录 A · Atropos / RL 心智模型"
chapter: A
slug: appendix-a-atropos-rl
est_read_min: 12
---

# 附录 A · Atropos / RL 心智模型

> 教什么：解释 hermes-agent 与"训练"的关系——它**不是**一个会自动改自己代码的 agent，而是一个**生成训练数据**的工具，配合 NousResearch 的 [Atropos](https://github.com/NousResearch/atropos) RL 框架训练新模型。本附录只讲心智模型，**不重写**——我们的 Go mini 关注 hermes 的运行时形态，训练侧请直接读上游 Python。

---

## "Self-improving" 的两种误解

刚接触 hermes-agent 时容易被 "self-improving AI agent" 这个 marketing 词组带偏。两个常见误解：

1. **误解 A：agent 会自动写新 skill / 改 plugin / 修自己 bug**
   ❌ **不是**。Skills 是人写的 Markdown 文件；plugin 是编译产物；agent 不能自我修改代码。
   
2. **误解 B：agent 在线学习、每次对话都"变聪明"**
   ❌ **也不是**。Anthropic API 调用是无状态的——每个 LLM 调用都用相同的固定 weights，没有 in-flight gradient update。

**那 hermes 的"self-improving" 到底是什么？两件事：**

1. **运行时维护**：Curator plugin（s06）追踪 memory 使用频率，自动归档闲置。这是**自动 garden**，不是自动学习。
2. **训练数据收集**：每次 agent 对话都被记录成 trajectory（messages + tools used + outcomes），这些 trajectories 输入 Atropos 训练框架，**人为决定**用哪些去 fine-tune 一个新基模。

第二点是 hermes-agent 的真正存在意义。它是一个 **trajectory generator**——通过让 agent 大量解决实际任务，产出可用于训练的数据。

## Atropos 是什么

[Atropos](https://github.com/NousResearch/atropos) 是 NousResearch 的 RL training framework。简单说：

```ascii-anim frames=2
┌────────────────────────────────────────────────────────────────┐
│  hermes-agent  ←── prompts (来自用户 / cron / 测试集)           │
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
│       │ apply RL/SFT/DPO/KTO updates                            │
│       ▼                                                         │
│  new model weights                                              │
│       │                                                         │
│       │ deploy as the next "Hermes-X"                           │
│       ▼                                                         │
│  hermes-agent 用新基模 → 更强 → 产更好 trajectories → ...        │
└────────────────────────────────────────────────────────────────┘
```

这就是 NousResearch 公开的 Hermes 系列模型（Hermes 2, Hermes 3, Hermes 4 等）的来源——他们用 hermes-agent 跑大量 agent 任务、用 Atropos 处理 trajectory、训练下一代基模。**整个 loop 跨越数月、人类持续监督**，不是 agent 在线自学。

## hermes-agent 仓库里的 environments/

hermes-agent 仓库的 `environments/` 目录存的是 Atropos environment——它把 hermes 的 agent loop 包装成 Atropos 能直接消费的 RL environment 接口。关键文件：

- `environments/__init__.py` — `HermesAgentBaseEnv` 基类
- `environments/coding/` — coding-task environment：给一个 task description，让 agent 解决，verify 后给 reward
- `environments/skill_creation/` — skill creation environment：让 agent 写新 skill 并测试
- `environments/multi_turn_tool/` — 多轮工具调用 environment

每个 environment 实现 Atropos 的标准接口（`reset()` `step(action)` `reward(state)` 等），所以 Atropos 训练流水线可以无差异地拉一批 trajectory。

## 一条 trajectory 长什么样

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

这种数据格式既能拿来 SFT（直接学 messages），也能 RL（reward 信号驱动 policy update）。Atropos 同时支持两种 setup。

## 为什么我们 mini 不重写这部分

三个原因：

1. **Atropos 重度依赖 PyTorch / Hugging Face / GPU 集群**——Go 重写没意义
2. **训练 loop 是 offline batch process**，不是 agent runtime 的一部分。我们的课程聚焦 hermes 的"运行期"，不重训练
3. **Atropos 本身有自己的教程**——上游 README + paper 解释得比我们更好

**正确的学习路径**：

1. 把这 10 节课读完，理解 hermes-agent 运行期机制
2. 读 Atropos README 学 RL environment 接口
3. 跟 hermes-agent `environments/coding/__init__.py` 看 trajectory 怎么从 hermes loop 里产出
4. 如果你真要自己训练：抓一批高质量 trajectory，用 Atropos pipeline，租 GPU。这超出本课程范围。

## 心智模型总结

| 维度 | hermes-agent (运行期) | Atropos (训练侧) |
|---|---|---|
| 实现语言 | Python | Python + PyTorch |
| 触发器 | 用户消息 / cron / API call | 训练任务清单 |
| 状态 | sessions + memories + skills | 模型 checkpoints + dataset |
| 输出 | 给用户的回复 | 训练好的新模型 weights |
| 频率 | 实时（毫秒到秒） | 离线 batch（小时到天） |
| 我们 mini 覆盖 | 是（s01-s10） | 否（本附录只讲心智模型） |
| 我们 mini 不覆盖 | curator 一些边角 / 多平台 | 全部 |

理解了 hermes-agent 是 Atropos 的 **trajectory source**（之一），整个项目的 architecture 就 click 了：每个 mechanism——session 持久化、memory FTS5、plugin lifecycle、multi-process gateway——都既要支持 production runtime，又要让 trajectory 提取干净。所有 instrumentation（token 计数、工具调用记录、cost tracking）的存在都是为了这个双重目标。

---

**接着读**：附录 B [上游源码导读地图](./appendix-b-upstream-map.md)——给你一份完整的 hermes-agent Python 仓库导览。
