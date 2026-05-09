# s04 · Session 持久化 / Session persistence

会话状态写盘，跨进程恢复 — `messages` 列表 + `usage` 计数 → JSON 文件 → `~/.learn-hermes-agent/sessions/<id>.json`。每 turn 后立即原子写。

Persist conversation state across processes — `messages` + `usage` totals → JSON file → `~/.learn-hermes-agent/sessions/<id>.json`. Atomic write after every turn.

## 运行 / Run

```bash
export ANTHROPIC_API_KEY=sk-ant-...
cd agents/s04-session

# 新 session
go run . -v "what is 17 * 23?"
# stderr: [session] s04-20260509-135133-a1b2c3 saved (turns=2 in=312 out=89)

# resume：在同一个 session 上继续
go run . -resume s04-20260509-135133-a1b2c3 "now add 100"

# branch：从某个 session 拉一个分支
go run . -branch s04-20260509-135133-a1b2c3 "actually subtract 50 from the first answer"

# 列表
go run . -list

# 删除
go run . -reset s04-20260509-135133-a1b2c3
```

## 测试 / Tests

```bash
go test -v ./...
```

7 个用例覆盖：ID 唯一性、save/load 往返、未找到返回 `ErrNotFound`、branch 浅复制 + ParentID + reset usage、atomic write 不留 .tmp、List 按 UpdatedAt 倒序、append 推进 UpdatedAt。

## 文件 / Files

| File | Role |
|---|---|
| `main.go` | CLI: 解析 `-resume` `-branch` `-reset` `-list`，装配 loop |
| `session.go` | **本节核心**：`Session` / `SessionUsage` / `Store`（save/load/list/delete + atomic write） |
| `loop.go` | 改为操作 `*Session`，每 turn 后 `store.Save()` |
| `provider.go` / `tools.go` / `registry.go` | 复用 s02（skills 在 s04 里去掉，聚焦持久化） |

## 教学要点

- **每 turn 写盘**：hermes 哲学是 "Ctrl-C survival"——每次 LLM 调用后立刻 `Save`，进程被打断也只丢部分工具结果，下次 resume 自动重做
- **原子写**：`os.WriteFile(tmp) → os.Rename(tmp, final)`，断电只可能"完整旧版本"或"完整新版本"，不会损坏
- **Branch = 浅复制 + ParentID + 清零 usage**：每个 fork 是独立的预算账本；要看血统总和就在 report 时往上爬
- **ID 格式**：`s04-YYYYMMDD-HHMMSS-<6 hex>`——可读、可排序、字典序约等于时间序

完整讲解见 [`docs/zh/s04-session.md`](../../docs/zh/s04-session.md) / [`docs/en/s04-session.md`](../../docs/en/s04-session.md)。
