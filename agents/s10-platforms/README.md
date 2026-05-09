# s10 · Gateway 平台适配器 / Gateway platform adapters

继 s09 之后，Gateway 长出 **平台 adapter** —— Telegram + Discord 两个真实 webhook handler。每个 platform 把它自己的 wire format（Telegram update / Discord interaction）翻译成统一 `Inbound` 形态，写进同一个 Kanban。Scheduler 完成 job 后通过 `PlatformAwareRunner` 把结果送回原平台。

After s09, the Gateway grows **platform adapters** — Telegram + Discord. Each Platform translates its native webhook (Telegram Update / Discord Interaction) into a uniform `Inbound`, writes to the shared Kanban. After completion, `PlatformAwareRunner` dispatches the reply back to the originating platform.

## 运行 / Run

```bash
cd agents/s10-platforms
go build .

# 干跑模式（不需要真 token；Outbound 打日志而非真发）
./s10 gateway -addr :7079 &
./s10 scheduler -interval 1s -echo &

# 模拟 Telegram webhook
curl -X POST http://localhost:7079/webhook/telegram \
  -H 'Content-Type: application/json' \
  -d '{"update_id":1,"message":{"from":{"id":42,"username":"yeding"},"chat":{"id":99},"text":"hi bot"}}'

# 模拟 Discord interaction
curl -X POST http://localhost:7079/webhook/discord \
  -H 'Content-Type: application/json' \
  -d '{"type":2,"channel_id":"987","member":{"user":{"id":"42","username":"yeding"}},"data":{"name":"ask","options":[{"name":"prompt","value":"hello"}]}}'

./s10 jobs

# 真跑：导出 token + ANTHROPIC_API_KEY
export TG_BOT_TOKEN=...
export DISCORD_BOT_TOKEN=...
export ANTHROPIC_API_KEY=...
./s10 gateway -addr :7079 &
./s10 scheduler -interval 1s &

# 测试
go test -v ./...
```

## 文件 / Files

| File | Role |
|---|---|
| `platform.go` | **本节核心**：`Platform` interface + `Inbound` + `PlatformRegistry` |
| `platform_telegram.go` | Telegram webhook 解析 + dry-run / live sendMessage |
| `platform_discord.go` | Discord interaction 解析 + dry-run / live channel post |
| `gateway.go` | `/webhook/<platform>` 路由 + `PlatformAwareRunner` 把回复送回正确平台 |
| `kanban.go` `scheduler.go` `provider.go` `loop.go` | 复用 s09 |

## 教学要点

- **统一 Inbound 形态** 让 scheduler 不必关心 source 是哪——它只调 `PlatformRegistry.Get(j.Source).Outbound(channel_id, result)`
- **payload 存 Inbound JSON**：scheduler 重新 unmarshal 拿回 channel_id，不需要任何额外状态
- **Dry-run 模式**给 CI / 演示——真 token 才打 API。`-tg-token` `-discord-token` 不传就 dry-run
- **Platform 扁平化**为 string key 而不是 type registry——一行 `pr.Register(NewSlackPlatform(...))` 加新平台

完整讲解见 [`docs/zh/s10-platforms.md`](../../docs/zh/s10-platforms.md) / [`docs/en/s10-platforms.md`](../../docs/en/s10-platforms.md)。
