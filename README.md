# QQ RWKV Bot

纯 Go 的腾讯官方 QQ 机器人服务，使用 `github.com/tencent-connect/botgo v0.2.1`，接收官方 Webhook 的私聊 `C2C_MESSAGE_CREATE`、群 @ `GROUP_AT_MESSAGE_CREATE`，调用 RWKV 原始 batch completions，聚合 SSE 后被动回复。无需 WebSocket、数据库或 GPU 依赖；W7900 推理由已有的 RWKV 服务负责。

## 本地运行

保留项目已有 `.env`，参考 `.env.example` 补充配置，**不要用示例覆盖已有密钥**。需要 Go 1.26.4 或更新版本（沿用原项目 go.mod）。

```sh
go mod download
go test ./...
go run ./cmd/qq-rwkv-bot
```

另一个终端：

```sh
curl -fsS http://127.0.0.1:8080/healthz
```

`/healthz` 返回 `ok`，只检查进程存活，不消耗 QQ/RWKV 配额，也不代表上游凭据或网络可用。程序自动加载工作目录的 `.env`，已有环境变量优先。支持 `KEY=value`、单/双引号和整行注释，不进行 shell 变量替换，也不支持行尾注释。`.env`、`.env.*`（除示例）及 `bin/` 已加入 `.gitignore`；初始目录没有 `.git`，本实现没有初始化 Git 或提交任何文件。

## 配置

| 变量 | 默认值 / 含义 |
| --- | --- |
| `APP_ID`、`APP_SECRET` | 必填，QQ 官方凭据；Secret 同时用于 Webhook Ed25519 验签 |
| `CF_ACCESS_CLIENT_ID`、`CF_ACCESS_CLIENT_SECRET` | 成对填写，仅发往 RWKV；受 Access 保护的现有上游需要这两个值 |
| `RWKV_MODEL` | `rwkv-1.5b`；还支持 `rwkv-3b`、`rwkv-7b`、`rwkv-13b` 的博客路由 |
| `RWKV_API_URL` | 可选，**完整** `/v1/batch/completions` 地址；自定义模型必须指定 |
| `RWKV_MAX_TOKENS` | `2048` |
| `RWKV_TEMPERATURE` / `RWKV_TOP_K` / `RWKV_TOP_P` | `1.0` / `50` / `0.3` |
| `RWKV_ALPHA_PRESENCE` / `RWKV_ALPHA_FREQUENCY` / `RWKV_ALPHA_DECAY` | `1.0` / `0.1` / `0.99` |
| `RWKV_CHUNK_SIZE` | `1` |
| `RWKV_TIMEOUT` | `90s`，最大 `180s` |
| `LISTEN_ADDR` | `127.0.0.1:8080` |
| `WORKERS` / `QUEUE_PER_WORKER` | `4` / `16`；最大 `64` / `256` |
| `SESSION_LIMIT` / `SESSION_TTL` | `1000` / `24h`；容量上限 `10000`，过期或最旧记录被淘汰 |
| `QQ_REPLY_RUNES` | `1500`，最大 `2000`；超长答案截断并注明 |

默认路由严格来自博客：`rwkv-1.5b → https://api-1b5.rwkvos.com`，其余分别为 `api-3b`、`api-7b`、`api-13b`，追加 `/v1/batch/completions`。`model` 是路由/上下文预算配置，**不会添加到原协议的请求体**。自定义地址只接受 HTTPS 或 loopback HTTP，不允许 URL 用户信息、查询参数和 fragment。出站请求拒绝重定向，避免转发 Cloudflare 凭据。

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
2. 在 Linux 或本机启动服务，通过已有 Cloudflare Tunnel 将一个公网 HTTPS 主机转发到 `http://127.0.0.1:8080`。例如 Tunnel ingress 配置片段（补充你自己的 tunnel ID / credentials-file）：

   ```yaml
   ingress:
     - hostname: qq-bot.example.com
       service: http://127.0.0.1:8080
     - service: http_status:404
   ```

3. 平台 Webhook 填写 `https://qq-bot.example.com/webhook/qq`，订阅 `C2C_MESSAGE_CREATE` 和 `GROUP_AT_MESSAGE_CREATE`（`GROUP_AND_C2C_EVENT` / `1<<25`）。保存时平台会发送 `op=13`，服务返回签名挑战结果。
4. **此 Webhook 域名/路径不能启用 Cloudflare Access 交互式登录、浏览器挑战或验证码。** 可使用独立公开主机；若已有 Access 应用覆盖它，需要为回调建立适当的 bypass。安全边界是 QQ 官方 Ed25519 签名。RWKV 域名继续使用 Access Service Token。
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

## Linux x86_64 / systemd

macOS 交叉编译（不依赖 ROCm）：

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

服务器需要系统 CA 证书与到 QQ / RWKV 的出站 HTTPS。Tunnel 建议作为独立 systemd 服务运行；Go 进程只监听 loopback。部署后检查 `/healthz`、平台回调验证、私聊/群聊回复、连续两轮上下文和 `/reset`。

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
