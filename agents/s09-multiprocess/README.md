# s09 · Multi-process architecture

把 agent 拆成 **3 个进程**：CLI（直接 agent）、Gateway（HTTP 收消息）、Scheduler（轮询执行）。中间通过一个共享的 **Kanban SQLite DB**（jobs 表 + crontab 表）协调。这是 hermes 真实部署形态——从 "笔记本玩具" 跨到 "production agent" 的分水岭。

Splits the agent into **three processes**: CLI (direct agent), Gateway (HTTP inbound), Scheduler (polls + runs). They coordinate through one shared **Kanban SQLite DB** (jobs + crontab). This is hermes's real production shape — the watershed from "laptop toy" to "production agent".

## 运行 / Run

```bash
cd agents/s09-multiprocess
go build .

# Terminal 1: gateway
./s09 gateway -addr :7079

# Terminal 2: scheduler (用 -echo 不需要 API key)
./s09 scheduler -interval 1s -echo

# Terminal 3: 投递一个消息
curl -X POST -d '{"source":"demo","prompt":"hello multiprocess"}' \
  http://localhost:7079/msg

# 查看 jobs
./s09 jobs

# 加一个 cron entry（每 30 秒跑一次）
./s09 cron add "every 30s" "what time is it?"
```

真实跑（用 LLM 而非 echo）：scheduler 不带 `-echo`，导出 `ANTHROPIC_API_KEY`。

## 测试 / Tests

```bash
go test -v ./...
```

5 个用例：
- Kanban: enqueue / list / claim / finish (+ 失败状态)
- Cron: add / due / touch / 拒绝坏 spec
- Gateway: POST /msg 入库
- **End-to-end**: gateway + scheduler + EchoRunner 全程跑通——投递消息、等 scheduler 拾起、检验 done

## 文件 / Files

| File | Role |
|---|---|
| `kanban.go` | **本节核心**：Kanban + jobs + crontab + ClaimNextPending（原子）|
| `gateway.go` | Gateway HTTP 服务（POST /msg、GET /jobs、/healthz）|
| `scheduler.go` | Scheduler + JobRunner interface + AgentRunner / EchoRunner |
| `loop.go` | 轻量 agent loop（带 1 个 bash 工具，跑 job 用）|
| `main.go` | 6 个子命令：cli / gateway / scheduler / jobs / send / cron |

## 教学要点

- **进程分离**：Gateway 不调 LLM，Scheduler 不收 HTTP——单一职责
- **`ClaimNextPending` 原子**：BEGIN IMMEDIATE + UPDATE WHERE status=pending 防止两 scheduler 抢同一 job
- **WAL** 让 gateway/cli 读、scheduler 写并发不冲突
- **`JobRunner` interface** 让 scheduler 可注入：production 用 AgentRunner（真 LLM），CI/test 用 EchoRunner（不要 API key）
- **`every <duration>`** 而非 5-field cron——简化教学，hermes 用 `robfig/cron/v3` 的真 cron 语法

完整讲解见 [`docs/zh/s09-multiprocess.md`](../../docs/zh/s09-multiprocess.md) / [`docs/en/s09-multiprocess.md`](../../docs/en/s09-multiprocess.md)。
