---
title: "s08 · Terminal backend factory"
chapter: 8
slug: s08-terminal-backends
est_read_min: 11
---

# s08 · Terminal backend factory

> What this teaches: abstract `bash` into an `Environment` interface; the factory `NewEnvironment(spec)` returns a local subprocess, a Docker container, an SSH session, a Modal lambda… all returning the same `ExecResult` shape so the LLM's `terminal` tool stays invariant.

---

## Problem

s01–s07 used `BashTool` calling `exec.CommandContext("bash", "-c", ...)` directly — fine, but only runs on the agent host. Production agents want:

- **Isolation**: running prompt-derived commands is dangerous; doing it inside a Docker container is much safer
- **Remote**: the agent runs on your laptop, the target host is at the other end of an SSH connection
- **Serverless**: spin up a Modal / Daytona / Vercel sandbox per command and discard
- **Uniform output**: across all four environments, the shape of `tool_result` must be identical so the model sees the same JSON

s08 ships the smallest factory: local + Docker as real backends, SSH/Modal as placeholders. The LLM has no idea any of this exists.

## Solution

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
// "ssh://..."        → not implemented (placeholder)
// "modal://..."      → not implemented
```

`TerminalTool` wraps the Environment, exposes a single `terminal` tool to the LLM with cwd + timeout_seconds in the schema, and JSON-marshals `ExecResult` as the tool result.

## How It Works

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

LocalEnvironment core (excerpt):

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
            exit = ee.ExitCode(); err = nil   // non-zero exit isn't a Go error
        }
    }
    return &ExecResult{Stdout: stdout.String(), Stderr: stderr.String(),
                       ExitCode: exit, Backend: "local"}, err
}
```

DockerEnvironment core:

```go
args := []string{"run", "--rm"}
if opts.Cwd != "" { args = append(args, "-w", opts.Cwd) }
for k, v := range opts.Env { args = append(args, "-e", k+"="+v) }
args = append(args, e.image, "bash", "-c", opts.Command)
cmd := exec.CommandContext(ctx, "docker", args...)
// ... same Run + ExitCode pattern as Local
```

**Three non-obvious points**:

1. **Non-zero exit returns `err == nil` + `ExitCode != 0`**. Treating a shell failure as a Go error is a bug — the model reads `exit_code` to decide what's next (retry, switch command, ask the user). Crashing the loop strips that capability.

2. **`Backend` lands in `ExecResult`** — the LLM sees the environment name. The `description` also repeats it ("Backend: docker:alpine"). Useful when the model has to choose between `sed` flags or check coreutils version.

3. **Docker absence reports cleanly**: `exec: "docker": not found` and `Cannot connect to the Docker daemon` both surface as readable errors instead of silently hanging. `isDockerMissing` recognises both.

## What Changed (vs. s01–s07)

s01's `BashTool` was hard-bound to local subprocess. s08 decouples "where the command runs" from "how it's exposed to the LLM":

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
+     return string(j), nil   // uniform JSON shape
+ }
```

## Try It

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s08-terminal-backends

# Default: local
go run . -v "use the terminal tool to print my hostname"

# Docker (requires daemon)
go run . -v -env "docker:alpine:3.19" "what's the OS version in this environment?"

# Tests
go test -v ./...
```

## Upstream Source Reading

hermes uses a factory + a base `_Environment` class in `tools/terminal_tool.py`. Backends: local / Docker / SSH / Modal / Singularity / Daytona / Vercel — seven of them, all behind the same interface.

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

**Reading notes**:

- **Long-running container vs per-command run**: upstream uses `docker exec` against the same container; state (cwd, env, installed packages) survives across tool calls. We use `docker run --rm` — clean but slow and loses state. Production needs the long-running variant — a single agent turn might call `terminal` 10 times, and a 1-second cold start each ruins the latency.
- **Modal/Daytona/Vercel**: three remote sandbox providers — each is an `Environment` implementation behind the same interface. This is hermes's "$5 VPS / serverless / GPU clusters" deployment philosophy made concrete.
- **`_run_subprocess` abstraction**: all backends converge on this shared executor for timeout + output capture + exit-code normalisation. Our Go uses `exec.CommandContext` directly and skips the indirection.
- **Config-driven**: `config["TERMINAL_ENV"]` picks the backend; config can come from config.yaml, env vars, or a CLI flag. Our mini only handles the CLI flag.
- **Idle container reaping**: upstream DockerEnvironment has a reaper that stops containers untouched for N minutes — avoids container sprawl. Production-relevant, omitted in teaching.

**Read further**: start at `_create_environment` in `tools/terminal_tool.py`, follow `LocalEnvironment` for the local path, `DockerEnvironment` for long-running container reuse, `SSHEnvironment` for paramiko channel multiplexing, `ModalEnvironment` for cloud lambda invocation.

---

**Next**: s09 splits the agent into **multiple processes** — a CLI, a Gateway (handles IM platforms), a Scheduler (cron) — sharing one Kanban DB. This is hermes's real deployment shape and the watershed between "laptop toy" and "production agent".
