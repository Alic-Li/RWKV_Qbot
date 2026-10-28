# QQ RWKV Bot

纯 Go 的腾讯官方 QQ 机器人服务，使用 `github.com/tencent-connect/botgo v0.2.1`，接收官方 Webhook 的私聊 `C2C_MESSAGE_CREATE`、群 @ `GROUP_AT_MESSAGE_CREATE`，调用 RWKV 原始 batch completions，聚合 SSE 后被动回复。无需 WebSocket、数据库或 GPU 依赖；推理由本机 `127.0.0.1:8000` 的 RWKV 服务负责；机器人默认监听 `127.0.0.1:8653`，公网回调由自行配置的 Cloudflare Tunnel 转发。

## 本地运行

保留项目已有 `.env`，参考 `.env.example` 补充配置，**不要用示例覆盖已有密钥**。需要 Go 1.26.4 或更新版本（沿用原项目 go.mod）。

```sh
go mod download
go test ./...
go run ./cmd/qq-rwkv-bot
```

另一个终端：

```sh
curl -fsS http://127.0.0.1:8653/healthz
```

`/healthz` 返回 `ok`，只检查进程存活，不消耗 QQ/RWKV 配额，也不代表上游凭据或网络可用。程序自动加载工作目录的 `.env`，已有环境变量优先。支持 `KEY=value`、单/双引号和整行注释，不进行 shell 变量替换，也不支持行尾注释。`.env`、`.env.*`（除示例）及 `bin/` 已加入 `.gitignore`；初始目录没有 `.git`，本实现没有初始化 Git 或提交任何文件。

## 配置

| 变量 | 默认值 / 含义 |
| --- | --- |
| `APP_ID`、`APP_SECRET` | 必填，QQ 官方凭据；Secret 同时用于 Webhook Ed25519 验签 |
| `CF_ACCESS_CLIENT_ID`、`CF_ACCESS_CLIENT_SECRET` | 本地 RWKV 无需填写；仅在上游启用 Cloudflare Access 时成对配置 |
| `RWKV_MODEL` | `rwkv-1.5b`；用于上下文预算，不决定 API 地址 |
| `RWKV_API_URL` | `http://127.0.0.1:8000/v1/batch/completions`；可覆盖为其他**完整**接口地址 |
| `RWKV_MAX_TOKENS` | `2048` |
| `RWKV_TEMPERATURE` / `RWKV_TOP_K` / `RWKV_TOP_P` | `1.0` / `50` / `0.3` |
| `RWKV_ALPHA_PRESENCE` / `RWKV_ALPHA_FREQUENCY` / `RWKV_ALPHA_DECAY` | `1.0` / `0.1` / `0.99` |
| `RWKV_CHUNK_SIZE` | `1` |
| `RWKV_TIMEOUT` | `90s`，最大 `180s` |
| `LISTEN_ADDR` | `127.0.0.1:8653` |
| `WORKERS` / `QUEUE_PER_WORKER` | `4` / `16`；最大 `64` / `256` |
| `SESSION_LIMIT` / `SESSION_TTL` | `1000` / `24h`；容量上限 `10000`，过期或最旧记录被淘汰 |
| `QQ_REPLY_RUNES` | `1500`，最大 `2000`；超长答案截断并注明 |

默认调用本机 RWKV 的 `/v1/batch/completions` 接口，保持原始 batch completions + SSE 协议；本地推理服务需提供此接口。`RWKV_MODEL` 只用于上下文预算，修改模型不会切换 API 地址，**不会添加到原协议的请求体**。自定义地址只接受 HTTPS 或 loopback HTTP，不允许 URL 用户信息、查询参数和 fragment。出站请求拒绝重定向，避免转发 Cloudflare 凭据。

## RWKV 迁移依据

只读参考 `/Users/alic-li/project/alic-li.com`：

- `src/lib/chat-server.ts`、`rwkv-models.ts`：路由、实际生成参数、Cloudflare Access 请求头、`User-Agent: Mozilla/5.0`、SSE Accept 和超时。
- `src/lib/rwkv-prompt.ts`：CRLF/空行归一化，`User:` / `Assistant:`，历史 `<think></think>`，补齐历史中断的 reasoning。
- `src/lib/chat-history.ts`、`src/components/Chat.tsx`：最近上下文、同角色合并、30 条 / 每条 8000 UTF-16 单位 / 32768 UTF-8 JSON 字节预算。
- `src/lib/sse.ts`、`rwkv-stream.ts` 及相关测试：按行解析 `data:`，支持 CRLF、UTF-8 分片、`choices[0].delta.content` / `text`、batch slot 过滤、`[DONE]`、跨 chunk stop 标记、错误和不完整流拒绝。

所有 LLM 请求固定使用快思考模板，末尾为 `Assistant: <think></think`，通过空思考块强制跳过思考。沿用博客的续写协议，**没有最后的 `>`**，流首段去掉生成的 `>`。已移除思考模式开关；已有环境中的 `RWKV_THINKING` 即使设为 `true` 也不会生效。遇到 `\n\nUser:`、`\n\nAssistant:`、`<|endoftext|>` 立即关闭上游。历史保留 reasoning，QQ 仅显示 `</think>` 后的最终答案。`Complete` 的 `onText` 回调保留流式回复扩展点。

博客 `tests/chat.test.ts` 有旧参数（例如 top_k=5）；此工程以当前 `chat-server.ts` 的 top_k=50、presence=1.0、frequency=0.1 为准。Go 对 UTF-16 预算截断会避开半个代理对；JSON 中少数特殊 Unicode 字符的转义可能使预算略保守。

QQ 适配层选择仅在生成和发送均成功后提交这一轮历史，失败不污染已完成对话；不保存失败的部分输出。内存保存的是受预算约束的历史，长答案发送截断不改变用于上下文的原始输出内容（仍受历史预算限制）。

## QQ 与 Cloudflare Tunnel

1. 在 QQ 开放平台创建机器人，配置 AppID / AppSecret，获得所需的私聊和群聊权限；开发阶段将测试用户/群加入平台沙箱名单。
2. 在 Linux 或本机启动服务，通过已有 Cloudflare Tunnel 将一个公网 HTTPS 主机转发到 `http://127.0.0.1:8653`。例如 Tunnel ingress 配置片段（补充你自己的 tunnel ID / credentials-file）：

   ```yaml
   ingress:
     - hostname: qq-bot.example.com
       service: http://127.0.0.1:8653
     - service: http_status:404
   ```

3. 平台 Webhook 填写 `https://qq-bot.example.com/webhook/qq`，订阅 `C2C_MESSAGE_CREATE` 和 `GROUP_AT_MESSAGE_CREATE`（`GROUP_AND_C2C_EVENT` / `1<<25`）。保存时平台会发送 `op=13`，服务返回签名挑战结果。
4. **此 Webhook 域名/路径不能启用 Cloudflare Access 交互式登录、浏览器挑战或验证码。** 可使用独立公开主机；若已有 Access 应用覆盖它，需要为回调建立适当的 bypass。安全边界是 QQ 官方 Ed25519 签名。本地 RWKV 接口无需经过 Tunnel，也无需 Access Service Token。
5. 保持服务器时钟同步。每次回调验证原始 body、签名时间戳（过去 5 分钟 / 未来 1 分钟）及可用的 `X-Bot-Appid`。无签名请求返回 401，body 超过 64 KiB 返回 413。
6. QQ 发文字或在有权限的群 @ 机器人；`/reset` 清理当前用户在当前会话中的上下文，不影响别人和其他群。群事件的 content 按当前官方文档已去除机器人 @ 前缀，不会删除用户自己正文中的其他 @。

QQ 平台配置、Tunnel 域名、实际凭据及上游可达性需要在部署环境完成联调。测试不使用 `.env` 密钥、不发真实 QQ 消息。

## 运行行为与限制

- BotGo 提供官方签名、验证应答、事件 ACK 和发送 DTO。没有直接使用 SDK 默认 HTTPHandler：它按 Content-Length 单次读包且会记录原始内容。SDK 日志整体关闭，应用用 `slog` 输出固定操作名与安全错误码，不记录正文、用户 ID、请求头、token 或配置值中的秘密。
- 当前官方接口为 `https://api.bot.qq.com`。轻量标准库 HTTP Client 补齐 v2 Token/回复：Token 支持数字或字符串 expires_in，互斥缓存、提前 60 秒按需刷新；认证失败最多刷新重试一次。HTTP 超时 10 秒，不重试可能已成功的网络异常，不自动重试限流/业务错误。
- 回调在成功入队后立即返回 SDK `op=12,d=0`，无需等待推理；满队列返回 `op=12,d=1`，允许平台重投。相同会话内消息 ID 去重一小时，最多 100000 条。相同回复使用 `msg_id + msg_seq=1`。
- 会话键为私聊用户或群 ID + 群成员 ID。固定 worker 按会话散列分配，保持同一会话的到达顺序；散列到同一 worker 的不同会话会排队。这是有界的简单实现，不保证不同会话完全无等待。`/reset` 也遵循到达顺序，不中途抢占当前推理。
- 保守地在收到消息后 4 分钟内尝试回复；排队超过 3 分钟不再推理。推理默认最多 90 秒。极高负载下队列中的旧消息会丢弃并记告警，建议降低队列容量或按上游能力调整并发。
- 第一版仅文字，一次回复，超长文本显式截断。附件不送入模型，空内容给出文字提示。内容审核、接口权限、回复时效和频控仍以 QQ 平台为准。
- 会话、去重表、已 ACK 的任务都在内存。**单实例运行；重启会丢失历史和未完成任务，不提供持久化 exactly-once 保证。** SIGTERM 停止接收，最多 25 秒排空业务队列，之后取消上游请求。

## 收到消息但没有回复

先向机器人发送 `/reset`：它直接调用 QQ 回复接口，不经过模型。若能收到“上下文已清理。”，说明 QQ 发送链路可用，再检查模型生成。

日志中的 `accepted=true` 只表示消息成功入队。每次处理分配一个本地 `job` 编号，可按编号查看 `message processing started` → `RWKV inference started` → `RWKV first text received` → `RWKV inference completed` → `QQ reply started` → `QQ reply succeeded`。仅首段文本到达时记录一条日志，不记录正文或凭据；普通消息需等待完整生成后才回复，默认生成超时为 90 秒。

- 停在 `RWKV inference started`：检查模型服务是否接收请求；超时后会记录失败并尝试发送失败提示。
- 收到首段文本但迟迟不完成：模型仍在生成，或 SSE 没有发送 `[DONE]` / 结束标记。
- `RWKV inference failed`：查看 HTTP 状态、超时或 SSE 协议错误；本地接口需支持 `/v1/batch/completions`，只提供 OpenAI chat completions 的服务不能直接使用。
- `QQ reply failed`：查看 token / reply 错误及 HTTP、业务错误码，检查凭据、权限及部署网络。
- Webhook 的 `body_too_large`（413）、`body_read_timeout`（408）、`body_read_failed`（400）分别表示超过 64 KiB、读取超时和请求体读取失败，与此前已入队的消息分开处理。

## Linux x86_64 / systemd

本地 Linux x86_64 编译（不依赖 ROCm，也可在 macOS 上交叉编译）：

```sh
mkdir -p bin
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/qq-rwkv-bot-linux-amd64 ./cmd/qq-rwkv-bot
```

将二进制、安全传输的环境文件和 `deploy/qq-rwkv-bot.service` 放到服务器，然后执行（按实际文件位置调整）：

```sh
sudo useradd --system --no-create-home --shell /usr/sbin/nologin qqbot
sudo install -d -o root -g root -m 755 /opt/qq-rwkv-bot
sudo install -m 755 qq-rwkv-bot-linux-amd64 /opt/qq-rwkv-bot/qq-rwkv-bot
sudo install -o root -g root -m 600 .env /etc/qq-rwkv-bot.env
sudo install -m 644 deploy/qq-rwkv-bot.service /etc/systemd/system/qq-rwkv-bot.service
sudo systemctl daemon-reload
sudo systemctl enable --now qq-rwkv-bot
sudo journalctl -u qq-rwkv-bot -f
```

运行前确认本地 RWKV 已监听 `127.0.0.1:8000`，环境文件中的 `LISTEN_ADDR=127.0.0.1:8653`、`RWKV_API_URL=http://127.0.0.1:8000/v1/batch/completions` 已更新；本地模型不需要 `CF_ACCESS_CLIENT_ID` / `CF_ACCESS_CLIENT_SECRET`，可将其留空。服务器需要系统 CA 证书与到 QQ 的出站 HTTPS，以及到本地 RWKV 的 HTTP 连接。Tunnel 建议作为独立 systemd 服务运行；Go 进程只监听 loopback。部署后检查 `/healthz`、平台回调验证、私聊/群聊回复、连续两轮上下文和 `/reset`。

## 验证

```sh
gofmt -w cmd internal
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

单元和本地 HTTP 集成测试覆盖原模板、参数/鉴权/路由、分片 SSE、截断流/取消/重定向、签名挑战与篡改拒绝、Token 并发缓存/刷新、队列去重/关闭、上下文隔离/淘汰/reset，以及完整签名 Webhook → RWKV → QQ 回复路径。

官方依据（2026-10-28 核对）：[BotGo](https://github.com/tencent-connect/botgo)、[Webhook 与 ACK](https://bot.q.qq.com/wiki/develop/api-v2/dev-prepare/interface-framework/event-emit.html)、[签名](https://bot.q.qq.com/wiki/develop/api-v2/dev-prepare/interface-framework/sign.html)、[Access Token](https://bot.q.qq.com/wiki/develop/api-v2/dev-prepare/access-token.html)、[单聊发送](https://bot.q.qq.com/wiki/develop/api-v2/autogen/api/v2_users_user_openid_messages.post.html)、[群聊发送](https://bot.q.qq.com/wiki/develop/api-v2/autogen/api/v2_groups_group_openid_messages.post.html)。
