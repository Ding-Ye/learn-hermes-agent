---
title: "s08 · Terminal Backend 工厂"
chapter: 8
slug: s08-terminal-backends
est_read_min: 11
---

# s08 · Terminal Backend 工厂

> 教什么：把 `bash` 抽成 `Environment` 接口，工厂 `NewEnvironment(spec)` 返回 local subprocess、Docker 容器、SSH、Modal lambda…… 同一个统一输出形态 (`ExecResult`)，LLM 看到的 `terminal` 工具是不变的。

---

## Problem / 问题

s01-s07 里 `BashTool` 直接 `exec.CommandContext("bash", "-c", ...)`——OK，但只能跑在 agent 主机上。生产 agent 想要：

- **隔离**：跑用户 prompt 出来的命令很危险，扔进 Docker 容器里跑安全得多
- **远端**：agent 在你笔记本，目标机器在 SSH 那头，命令要跑在远端
- **Serverless**：临时拉起一个 Modal/Daytona/Vercel 沙箱跑、用完即弃
- **统一形态**：上面四种环境，输出结构不能各不相同——要让模型看到的 `tool_result` 是一样的形状

s08 给一个最小工厂模式版：local + Docker 两个真后端，SSH/Modal 列出来作为 placeholder。LLM 完全不知道有这些差别。

## Solution / 解决方案

```go
type Environment interface {
    Name() string
    Execute(ctx context.Context, opts ExecOptions) (*ExecResult, error)
    Close() error
}

type ExecResult struct {
    Stdout, Stderr string
    ExitCode       int
    Duration       time.Duration
    Backend        string  // "local" / "docker:alpine:3.19" / ...
    Cwd            string
}

func NewEnvironment(spec string) (Environment, error)
// "local"            → LocalEnvironment
// "docker:<image>"   → DockerEnvironment
// "ssh://..."        → 未实现，错误（占位）
// "modal://..."      → 未实现
```

`TerminalTool` 包一层，把 Environment 暴露成 LLM 看到的 `terminal` 工具，schema 暴露 cwd + timeout_seconds，输出是 JSON 序列化的 ExecResult。

## How It Works / 工作原理

```ascii-anim frames=2
┌──────────────────────────────────────────────────────────────┐
│  LLM sees one tool: terminal                                 │
│    description: "Backend: local"   (or docker:..., ssh:...) │
│  ▼                                                           │
│  TerminalTool.Execute → env.Execute(opts)                    │
│  ▼                                                           │
│  ┌────────────────────────────────────────────────┐          │
│  │ Environment interface                          │          │
│  │   ┌─────────────┐  ┌──────────────┐  ┌──────┐ │          │
│  │   │   Local     │  │   Docker     │  │ SSH? │ │          │
│  │   │ exec.Cmd    │  │ docker run   │  │ ...  │ │          │
│  │   │   bash -c   │  │   --rm       │  │      │ │          │
│  │   └─────────────┘  └──────────────┘  └──────┘ │          │
│  └─────────────┬───────────────┬──────────┬──────┘          │
│                │               │          │                  │
│                ▼               ▼          ▼                  │
│        ExecResult{stdout, stderr, exit, duration, backend}   │
└──────────────────────────────────────────────────────────────┘
```

LocalEnvironment 核心（节选）：

```go
func (e *LocalEnvironment) Execute(ctx context.Context, opts ExecOptions) (*ExecResult, error) {
    if opts.Timeout > 0 {
        var cancel context.CancelFunc
        ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
        defer cancel()
    }
    cmd := exec.CommandContext(ctx, "bash", "-c", opts.Command)
    if opts.Cwd != "" { cmd.Dir = opts.Cwd }
    var stdout, stderr bytes.Buffer
    cmd.Stdout = &stdout; cmd.Stderr = &stderr
    err := cmd.Run()
    exit := 0
    if err != nil {
        if ee, ok := err.(*exec.ExitError); ok {
            exit = ee.ExitCode(); err = nil   // 非零退出不当 Go error
        }
    }
    return &ExecResult{Stdout: stdout.String(), Stderr: stderr.String(),
                       ExitCode: exit, Backend: "local"}, err
}
```

DockerEnvironment 核心：

```go
args := []string{"run", "--rm"}
if opts.Cwd != "" { args = append(args, "-w", opts.Cwd) }
for k, v := range opts.Env { args = append(args, "-e", k+"="+v) }
args = append(args, e.image, "bash", "-c", opts.Command)
cmd := exec.CommandContext(ctx, "docker", args...)
// ... same Run + ExitCode pattern as Local
```

**三个非显然之处**：

1. **非零退出**返回 `err == nil` + `ExitCode != 0`——把 shell 失败当 Go error 是 bug。模型读 exit_code 决定下一步（重试、换命令、求助用户）；让 loop crash 反而剥夺了它的能力。

2. **`Backend` 字段进 ExecResult**——LLM 能看到环境名。`description` 里还重复一次 "Backend: docker:alpine"——模型在选 `sed` flag 还是判断 `coreutils` 版本时用得上。

3. **Docker 不可用要清晰报错**：`exec: "docker": not found` 或 `Cannot connect to the Docker daemon` 都包装成可读 error，不是静默 hang。`isDockerMissing` 识别这两个常见模式。

## What Changed / 与 s01-s07 的变化

s01 的 `BashTool` 死死绑在本地 subprocess。s08 把"在哪儿跑命令"和"怎么暴露给 LLM"解耦：

```diff
- type BashTool struct{}
- func (b *BashTool) Execute(ctx, input) (string, error) {
-     out, _ := exec.CommandContext(ctx, "bash", "-c", input["command"].(string)).CombinedOutput()
-     return string(out), nil
- }
+ type Environment interface {
+     Execute(ctx, opts ExecOptions) (*ExecResult, error)
+ }
+ type TerminalTool struct{ Env Environment }
+ func (t *TerminalTool) Execute(ctx, input) (string, error) {
+     res, _ := t.Env.Execute(ctx, ExecOptions{Command:..., Cwd:..., Timeout:...})
+     j, _ := json.MarshalIndent(res, "", "  ")
+     return string(j), nil   // 输出统一 JSON 形态
+ }
```

## Try It / 动手试一试

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s08-terminal-backends

# 默认 local
go run . -v "use the terminal tool to print my hostname"

# Docker（需要 daemon 起着）
go run . -v -env "docker:alpine:3.19" "what's the OS version in this environment?"

# 测试
go test -v ./...
```

## Upstream Source Reading / 上游源码阅读

hermes 在 `tools/terminal_tool.py` 用 factory pattern + base `_Environment` 类。Backends：local / Docker / SSH / Modal / Singularity / Daytona / Vercel——七种实现，都同接口。

```upstream:hermes_agent/terminal_tool.py#L1-L50
# Excerpted + simplified from tools/terminal_tool.py

import abc, asyncio, os, subprocess

class _Environment(abc.ABC):
    """Base class for all execution environments. Same shape we use in Go."""

    @abc.abstractmethod
    async def execute(self, command: str, *, timeout: float = 60.0,
                      cwd: str | None = None) -> dict:
        """Returns dict with stdout, stderr, exit_code, duration_seconds.
        Concrete implementations differ wildly under the hood — Modal
        shells out to a remote lambda, SSH multiplexes a long-running
        connection, Docker uses `docker exec` against a pre-started
        container — but the dict shape is invariant."""

    @abc.abstractmethod
    async def close(self): ...


def _create_environment(config: dict) -> _Environment:
    """Factory: TERMINAL_ENV picks one of:
       'local'        → LocalEnvironment        (subprocess on host)
       'docker'       → DockerEnvironment       (docker exec into running container)
       'ssh'          → SSHEnvironment          (paramiko, persistent channel)
       'modal'        → ModalEnvironment        (modal.Function)
       'singularity'  → SingularityEnvironment  (HPC clusters)
       'daytona'      → DaytonaEnvironment      (sandboxed dev workspace)
       'vercel'       → VercelEnvironment       (per-tenant edge sandbox)
    """
    kind = config.get("TERMINAL_ENV", "local")
    if kind == "local":  return LocalEnvironment()
    if kind == "docker": return DockerEnvironment(config)
    if kind == "ssh":    return SSHEnvironment(config)
    if kind == "modal":  return ModalEnvironment(config)
    # ...
    raise ValueError(f"unknown TERMINAL_ENV: {kind}")


class DockerEnvironment(_Environment):
    """KEY DIFFERENCE FROM OUR GO MINI: hermes keeps a long-running
    container alive for the whole session and uses `docker exec` for
    follow-on commands. Our mini does `docker run --rm` per command —
    simpler to read, but slow and loses cwd/state between commands."""

    def __init__(self, config):
        self._container_id = self._start_container(config)

    async def execute(self, command, *, timeout=60.0, cwd=None):
        args = ["docker", "exec"]
        if cwd: args += ["-w", cwd]
        args += [self._container_id, "bash", "-c", command]
        return await _run_subprocess(args, timeout)
```

**对照阅读要点**：

- **Long-running container vs per-command run**：上游 `docker exec` 进同一个容器；状态（cwd、env、安装的包）在多次工具调用间保留。我们 `docker run --rm` 每次新容器，干净但慢。生产里 long-running 是必须的——一节 agent loop 可能调 10 次 terminal，每次 cold start 1 秒就把延迟搞坏了。
- **Modal/Daytona/Vercel**：远端 sandbox 三家——每家都是 `Environment` 实现，接同样的 interface。这是 hermes "$5 VPS / serverless / GPU clusters" 部署哲学的具体体现。
- **`_run_subprocess` 抽象**：所有 backend 都收敛到这个公用执行器，做 timeout + 输出捕获 + 退出码归一化。我们 Go 用 `exec.CommandContext` 直接做，省了一层。
- **配置驱动**：`config["TERMINAL_ENV"]` 决定哪个 backend，配置可来自 config.yaml / 环境变量 / CLI flag。我们 mini 只 CLI flag。
- **Idle container reaping**：上游 DockerEnvironment 有"超过 N 分钟没 exec 就停掉"的 reaper，避免容器堆积。生产相关，教学中省略。

**想读更多**：从 `tools/terminal_tool.py` 的 `_create_environment` 入手，跟 `LocalEnvironment` 看本地路径，跟 `DockerEnvironment` 看 long-running 容器策略，跟 `SSHEnvironment` 看 paramiko 长连接复用，跟 `ModalEnvironment` 看 cloud lambda 调用。

---

**下一节预告**：s09 把 agent 拆成 **多进程**——CLI、Gateway（接 IM）、Scheduler（cron）三个 binary 共享一个 Kanban DB。这是 hermes 真实部署形态，也是从"笔记本玩具"过渡到"production agent"的真正分水岭。
