# s08 · Terminal Backend factory

把 `bash` 抽象成一个 `Environment` 接口，工厂 `NewEnvironment(spec)` 返回 local subprocess 或 Docker 容器实现。LLM 只看到一个 `terminal` 工具——后端是本地、Docker、SSH、Modal、Daytona 都不知情。

Abstract `bash` into an `Environment` interface; the factory `NewEnvironment(spec)` returns a local subprocess or Docker container implementation. The LLM sees only one `terminal` tool — it has no idea whether the backend is local, Docker, SSH, Modal, or Daytona.

## 运行 / Run

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s08-terminal-backends

# Local subprocess
go run . -v "tell me what shell I'm in (use the terminal tool)"

# Docker (needs docker daemon running)
go run . -v -env "docker:alpine:3.19" "what does cat /etc/os-release show?"

# 测试
go test -v ./...
```

## 文件 / Files

| File | Role |
|---|---|
| `env.go` | **本节核心**：`Environment` interface + `LocalEnvironment` + `NewEnvironment` factory |
| `env_docker.go` | `DockerEnvironment`：`docker run --rm <image> bash -c <cmd>` |
| `tools.go` | `TerminalTool` 包装 Environment，schema 暴露 cwd + timeout |
| `env_test.go` | 7 用例覆盖：echo / 非零退出 / stderr / 超时 / 工厂派发 / docker 缺失降级 / Tool JSON 形态 |

## 教学要点

- **统一输出形态 `ExecResult`**：所有 backend 同一个 dict（stdout/stderr/exit/duration/backend），LLM 看到的 JSON 都一样
- **非零退出 *不是* Go error**：模型读 exit_code 决定下一步——把 shell 失败当 Go error 会把"模型可见信号"变成"loop crash"
- **Backend 名字暴露给 LLM**：description 里写 `Backend: local`，让模型知道环境，能更聪明地选命令（macOS sed vs GNU sed 之类）
- **Docker 降级提示**：daemon 不在/image pull 失败给清晰错误，不静默 hang

完整讲解见 [`docs/zh/s08-terminal-backends.md`](../../docs/zh/s08-terminal-backends.md) / [`docs/en/s08-terminal-backends.md`](../../docs/en/s08-terminal-backends.md)。
