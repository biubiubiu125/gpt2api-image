# gpt2api-image

`gpt2api-image` 是一个以 `jwbb903/ChatGPT2API-GO` 为底座重写的图片 API 项目，核心目标是给下游系统稳定调用 ChatGPT 网页端生图能力。

当前版本只保留图片 API、异步图片任务、账号池、用户密钥、日志、图片管理、设置和注册机配置页。搜索、PPT 生成、PSD 生成、editable file 下载、调试页、文本聊天 API、R2 自动备份、CPA/Sub2API 远程导入都不作为新项目主链路保留。

## 架构

推荐使用 Docker Compose 部署，一共 3 个容器：

| 容器 | 作用 |
| --- | --- |
| `gpt2api-image-postgres` | PostgreSQL，保存异步图片任务队列 |
| `gpt2api-image-api` | HTTP API 和 Web 管理面板 |
| `gpt2api-image-worker-1` | 后台消费异步生图任务 |

这样 API 容器负责接请求，Worker 容器负责跑耗时生图任务。后续要提高并发，优先横向增加 `worker` 副本，而不是把所有任务压在一个容器里。

扩容 Worker：

```bash
docker compose up -d --scale worker=3
```

## 保留接口

OpenAI 兼容接口：

- `GET /v1/models`
- `POST /v1/images/generations`
- `POST /v1/images/edits`

异步任务接口：

- `POST /api/image-tasks/generations`
- `POST /api/image-tasks/edits`
- `GET /api/image-tasks`
- `POST /api/image-tasks/cancel`

管理接口：

- `/api/accounts*`
- `/api/auth*`
- `/api/settings`
- `/api/logs*`
- `/api/images*`
- `/api/register*`

## 快速部署

```bash
cp .env.example .env
```

编辑 `.env`，至少改掉：

```env
GPT2API_IMAGE_AUTH_KEY=<openssl-rand-hex-32>
GPT2API_IMAGE_BASE_URL=https://你的域名
POSTGRES_PASSWORD=<openssl-rand-hex-32>
```

可以用 `openssl rand -hex 32` 生成上面的随机值。`.env.example` 里的必填项默认留空，不填完整时 Compose 会拒绝启动；服务也会拒绝 `change-me`、`replace-with...` 这类默认占位密钥。

启动：

```bash
docker compose up -d --build
```

查看日志：

```bash
docker compose logs -f api worker
```

访问：

- Web 管理面板：`http://服务器IP:3000`
- API 地址：`http://服务器IP:3000/v1`

## API 示例

所有 API 请求都需要：

```http
Authorization: Bearer <GPT2API_IMAGE_AUTH_KEY>
```

文生图：

```bash
curl http://localhost:3000/v1/images/generations \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $GPT2API_IMAGE_AUTH_KEY" \
  -d '{
    "model": "gpt-image-2",
    "prompt": "一张现代产品海报，干净背景，高级质感",
    "n": 1,
    "size": "1:1",
    "resolution": "1k",
    "response_format": "b64_json"
  }'
```

异步文生图：

```bash
curl http://localhost:3000/api/image-tasks/generations \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $GPT2API_IMAGE_AUTH_KEY" \
  -d '{
    "model": "gpt-image-2",
    "prompt": "一张现代产品海报，干净背景，高级质感",
    "n": 1,
    "size": "1:1",
    "resolution": "1k"
  }'
```

查询任务：

```bash
curl http://localhost:3000/api/image-tasks \
  -H "Authorization: Bearer $GPT2API_IMAGE_AUTH_KEY"
```

## 配置

常用环境变量：

| 变量 | 说明 |
| --- | --- |
| `GPT2API_IMAGE_AUTH_KEY` | 管理员和 API 默认鉴权 Key |
| `GPT2API_IMAGE_BASE_URL` | 生成图片返回 URL 时使用的公网地址 |
| `GPT2API_IMAGE_DATABASE_URL` | PostgreSQL 连接串，Compose 内已自动配置 |
| `GPT2API_IMAGE_MODE` | `serve`、`worker` 或 `all` |
| `GPT2API_IMAGE_WORKER_CONCURRENCY` | Worker 并发任务数 |
| `GPT2API_IMAGE_WORKER_HEARTBEAT_INTERVAL_SECS` | Worker 检查任务取消和续租的间隔，默认 5 秒 |
| `GPT2API_IMAGE_DB_MAX_OPEN_CONNS` | 每个 API / Worker 进程的 PostgreSQL 最大连接数，默认 20 |
| `GPT2API_IMAGE_DB_MAX_IDLE_CONNS` | 每个 API / Worker 进程的 PostgreSQL 空闲连接数，默认 10 |
| `GPT2API_IMAGE_UPSTREAM_TRANSPORT` | 上游传输方式，默认 `tls-client` |
| `GPT2API_IMAGE_CURL_IMPERSONATE_AUTO_DOWNLOAD` | 是否自动准备 curl-impersonate |

`config.json` 仍保留部分运行配置，例如代理、图片超时、账号刷新间隔、图片清理策略、敏感词和 AI 审核配置。

如果不用 Docker Compose，而是直接运行 release 包并启用 PostgreSQL 异步任务，请用 `GPT2API_IMAGE_MODE=all ./start.sh --port 3000`，否则只会启动 API，不会消费队列。

## 注册机说明

注册机页面和配置 API 已保留，方便后续接入真实自动注册执行器。

当前 Go 版不会伪造“注册成功”：点击开始注册会写入失败日志并明确提示执行器尚未迁移。急需上线时，建议先使用手动导入账号或接入独立注册执行器，避免把不可用的自动注册链路带到生产。

## 本地开发

后端：

```bash
go test ./cmd/... ./internal/...
go run ./cmd/server
```

前端：

```bash
cd web
pnpm install --frozen-lockfile
pnpm run build
```

整体构建：

```bash
make web
make build
```

默认验证：

```bash
make verify
```

真实 PostgreSQL 集成测试有两种跑法。默认会用 Docker 临时启动一个 `postgres:16-alpine` 容器并自动清理：

```bash
make integration
```

如果当前 shell 访问不了 Docker daemon，也可以直接指定一个现成 PostgreSQL 测试库：

```bash
GPT2API_IMAGE_TEST_DATABASE_URL='postgresql://user:password@127.0.0.1:5432/gpt2api_image_test?sslmode=disable' make integration
```

## 已移除主链路

以下能力不再作为新项目主链路暴露：

- 文本聊天接口：`/v1/chat/completions`、`/v1/responses`、`/v1/messages`
- 搜索
- PPT 生成
- PSD 生成
- editable file 下载
- 调试页
- Cloudflare R2 自动备份
- CPA/Sub2API 远程导入

代码中可能仍保留少量未路由的兼容函数，目的是降低一次性重写风险；实际路由和前端入口已按图片 API 项目收敛。

## 安全建议

- 不要把未鉴权服务直接暴露公网。
- 生产环境建议使用 HTTPS 反向代理。
- 不要使用重要 ChatGPT 账号。
- 不要在日志、截图、Issue 中公开 access token、cookie、代理地址和邮箱。
- 不同下游系统建议使用不同用户密钥，并配置额度。
