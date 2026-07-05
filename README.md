# gpt2api-image

`gpt2api-image` 是面向 ChatGPT 网页端图片能力的 OpenAI 兼容图片 API 服务。项目保留图片生成、图片编辑、异步任务、账号池、图片管理、后台设置、日志查看和注册执行器链路；文本对话、搜索、PPT、PSD、R2、CPA 等非图片主链路不作为本项目目标。

## 真实链路

```text
客户端 / 前端页面
  -> Go API 服务
  -> 账号池选择可用 ChatGPT 账号
  -> ChatGPT 上游图片接口
  -> 保存图片和任务状态
  -> 返回 OpenAI 兼容结果或异步任务结果
```

带 PostgreSQL（PostgreSQL 数据库）时，API 进程负责创建图片任务，worker 进程负责领取任务、调用上游、保存图片和回写状态。无数据库时，部分异步任务使用本地 JSON 存储并由 API 进程内 goroutine 执行，适合本地调试，不建议作为生产多实例方案。

注册执行器是独立的 FastAPI（Python Web 框架）服务。前端或 Go API 发起注册动作后，由注册执行器完成 Outlook 池、OpenAI 注册、账号写回和异常修复等流程。

## 功能范围

- OpenAI 兼容图片接口：`/v1/images/generations`、`/v1/images/edits`。
- 图片兼容子集：`/v1/chat/completions` 和 `/v1/responses` 只接受图片生成相关请求；普通文本请求会返回禁用提示。
- 异步图片任务：创建、查询、取消、worker 领取和状态回写。
- 账号池：账号导入、刷新、状态管理、并发控制和模型列表。
- 图片管理：图片列表、下载、缩略图、归属、标签和清理保护。
- 后台管理：登录、服务密钥、设置、日志、存储信息、系统状态和代理测试。
- 注册执行器：注册启动、停止、重置、异常修复、Outlook 池测试和事件流。

## 目录结构

```text
cmd/server/              Go 服务入口
internal/app/            API、任务、账号、图片、配置、存储、上游调用和注册代理
register-executor/       Python 注册执行器
web/                     Next.js 静态前端
data/                    运行数据、图片、日志和本地二进制
web_dist/                构建后的前端静态文件
```

`data/` 和 `web_dist/` 属于运行或构建产物目录，不应把本地临时数据、日志、导出文件、测试脚本或密钥文件提交到仓库。

## 快速部署

生产部署优先使用 Docker Compose（容器编排）：

```powershell
Copy-Item .env.example .env
```

编辑 `.env`，至少填写：

```env
POSTGRES_PASSWORD=change-me
GPT2API_IMAGE_AUTH_KEY=change-me
GPT2API_IMAGE_BASE_URL=http://127.0.0.1:3000
```

启动服务：

```powershell
docker compose up -d --build
```

默认访问地址：

- 前端后台：`http://127.0.0.1:3000/`
- API 服务：`http://127.0.0.1:3000/v1/images/generations`
- PostgreSQL：容器内 `postgres:5432`
- 注册执行器：容器内 `register-executor:8091`

查看日志：

```powershell
docker compose logs -f api
docker compose logs -f worker
docker compose logs -f register-executor
```

本地 compose 文件使用 `8000` 端口：

```powershell
docker compose -f docker-compose.local.yml up -d --build
```

## 运行模式

Go 服务通过 `GPT2API_IMAGE_MODE` 或第一个命令行参数选择模式：

| 模式 | 说明 |
| --- | --- |
| `serve` | 只启动 HTTP API，默认模式 |
| `worker` | 只启动图片任务 worker |
| `all` | 同一进程同时启动 API 和 worker，适合非 Compose 单机运行 |

worker 模式需要配置数据库和服务访问地址：

```powershell
$env:GPT2API_IMAGE_MODE = "worker"
$env:GPT2API_IMAGE_DATABASE_URL = "postgresql://user:password@127.0.0.1:5432/gpt2api_image?sslmode=disable"
$env:GPT2API_IMAGE_BASE_URL = "http://127.0.0.1:3000"
.\gpt2api-image.exe
```

worker 并发通过 `GPT2API_IMAGE_WORKER_CONCURRENCY` 控制，默认 `4`。

## 环境变量

| 变量 | 说明 |
| --- | --- |
| `GPT2API_IMAGE_AUTH_KEY` | 后台登录和 API 访问主密钥，生产环境必须设置为强随机值 |
| `GPT2API_IMAGE_DATABASE_URL` | PostgreSQL 连接串；启用后使用数据库异步任务链路 |
| `GPT2API_IMAGE_BASE_URL` | 对外可访问的 API 地址，worker 和回调链路会使用 |
| `GPT2API_IMAGE_ADDR` | HTTP 监听地址，Docker 镜像默认 `:80` |
| `GPT2API_IMAGE_MODE` | `serve`、`worker` 或 `all` |
| `GPT2API_IMAGE_REGISTER_EXECUTOR_URL` | 注册执行器地址，Compose 默认指向 `register-executor:8091` |
| `GPT2API_IMAGE_REGISTER_INTERNAL_KEY` | Go API 与注册执行器之间的内部密钥 |
| `GPT2API_IMAGE_WORKER_CONCURRENCY` | worker 并发数 |
| `GPT2API_IMAGE_UPSTREAM_TRANSPORT` | 上游请求传输方式，支持 `tls-client` 或 `curl-impersonate` |
| `GPT2API_IMAGE_ROUTE_STRATEGY` | 图片内部路由策略，支持 `web_first`、`web_only`、`codex_first`、`codex_only`，默认 `web_first` |
| `GPT2API_IMAGE_CURL_IMPERSONATE_BIN` | curl-impersonate 可执行文件路径 |
| `GPT2API_IMAGE_CORS_ALLOWED_ORIGINS` | 允许跨源访问的 Origin，多个值用逗号分隔；为空时只支持同源 |
| `GPT2API_IMAGE_LOG_REQUEST_TEXT` | 是否记录请求 prompt 正文，默认 `0` |
| `NEXT_PUBLIC_API_URL` | 前端构建时固定 API 地址；为空时生产使用同源 |
| `NEXT_PUBLIC_DEV_BACKEND` | 前端开发环境后端地址，默认 `http://127.0.0.1:8000` |

如果未单独设置 `GPT2API_IMAGE_REGISTER_INTERNAL_KEY`，注册执行器会回退使用主密钥。生产环境建议单独设置内部密钥。

## 配置文件

`config.json` 保存运行配置，环境变量会覆盖部分关键项。常用字段：

| 字段 | 说明 |
| --- | --- |
| `auth-key` | 主密钥；也可用 `GPT2API_IMAGE_AUTH_KEY` 覆盖 |
| `database_url` | PostgreSQL 连接串；也可用环境变量覆盖 |
| `base_url` | 服务外部访问地址 |
| `proxy` | 上游代理配置 |
| `upstream_transport` | 上游请求传输方式 |
| `image_route_strategy` | 图片内部路由策略；下游模型统一为 `gpt-image-2`，这里控制内部走 Web 或 Codex |
| `refresh_account_interval_minute` | 账号自动刷新间隔 |
| `image_retention_days` | 图片保留天数 |
| `image_poll_timeout_secs` | 同步图片生成轮询超时 |
| `image_task_timeout_secs` | 异步任务超时 |
| `image_task_claim_ttl_secs` | worker 任务领取 TTL |
| `image_account_concurrency` | 单账号图片并发 |
| `sensitive_words` | 敏感词过滤 |
| `ai_review` | AI 审核配置 |
| `cleanup_protect_user_images` | 清理时保护用户图片 |

## 认证和密钥

所有 `/api/` 和 `/v1/` 接口默认使用 Bearer Token（Bearer 令牌）：

```http
Authorization: Bearer <GPT2API_IMAGE_AUTH_KEY>
```

当前服务密钥会被规范化为管理员级服务密钥，用于后端系统调用，不走普通用户额度模型。也就是说，不需要为下游服务额外配置用户额度；如果要隔离不同下游，应该使用不同密钥并在外层系统做审计和限流。

## OpenAI 兼容接口

### 图片生成

```powershell
curl.exe http://127.0.0.1:3000/v1/images/generations `
  -H "Authorization: Bearer $env:GPT2API_IMAGE_AUTH_KEY" `
  -H "Content-Type: application/json" `
  -d "{\"model\":\"gpt-image-2\",\"prompt\":\"一张现代中文后台系统截图\",\"size\":\"1:1\",\"resolution\":\"1k\",\"n\":1}"
```

支持字段包括 `model`、`prompt`、`size`、`resolution`、`n`、`response_format`、`stream` 和 `client_task_id`。

`model` 字段只作为兼容输入接收。无论下游传 `gpt-image-2`、`gpt-image-1`、`dall-e-3`、`codex-gpt-image-2` 或为空，服务都会统一归一化为公开模型 `gpt-image-2`。真实上游链路由 `image_route_strategy` 决定：默认 `web_first` 优先使用 ChatGPT Web 网页逆向生图，Web 不可用或临时失败时再尝试 Codex；如果只想使用网页链路，可以设置为 `web_only`。

每个生图任务都会在原始 prompt 后自动追加超清图片约束，要求直接生成最终图片、提高细节与清晰度、避免模糊/低清/压缩伪影，并在图生图时要求使用全部参考图；包含中文、Logo、包装、UI 文案等内容时会追加文字准确性要求。

### 图片编辑

`/v1/images/edits` 使用 `multipart/form-data`（表单文件上传），必须包含 `prompt` 和至少一个 `image` 文件。

### 异步任务

传入 `client_task_id` 且配置了数据库时，图片生成和编辑接口会创建异步任务并返回 `202`：

```powershell
curl.exe http://127.0.0.1:3000/v1/images/generations `
  -H "Authorization: Bearer $env:GPT2API_IMAGE_AUTH_KEY" `
  -H "Content-Type: application/json" `
  -d "{\"model\":\"gpt-image-2\",\"prompt\":\"生成一张产品主图\",\"size\":\"1:1\",\"resolution\":\"1k\",\"client_task_id\":\"demo-001\"}"
```

查询任务：

```powershell
curl.exe "http://127.0.0.1:3000/api/image-tasks?id=demo-001" `
  -H "Authorization: Bearer $env:GPT2API_IMAGE_AUTH_KEY"
```

取消任务：

```powershell
curl.exe http://127.0.0.1:3000/api/image-tasks/cancel `
  -H "Authorization: Bearer $env:GPT2API_IMAGE_AUTH_KEY" `
  -H "Content-Type: application/json" `
  -d "{\"id\":\"demo-001\"}"
```

## 管理接口

常用后台接口：

| 接口 | 说明 |
| --- | --- |
| `/api/auth/me` | 当前身份 |
| `/api/auth/users` | 服务密钥管理 |
| `/api/accounts` | 账号池列表和维护 |
| `/api/accounts/refresh` | 刷新账号状态 |
| `/api/settings` | 运行设置 |
| `/api/storage/info` | 存储信息 |
| `/api/system/status` | 系统状态 |
| `/api/proxy`、`/api/proxy/test` | 代理配置和测试 |
| `/api/logs`、`/api/logs/delete` | 日志查看和删除 |
| `/api/images`、`/api/me/images` | 图片列表 |
| `/api/images/download` | 图片下载 |
| `/api/images/tags` | 图片标签 |
| `/api/register/*` | 注册执行器代理接口 |

图片静态访问路径：

- 原图：`/images/<file>`
- 缩略图：`/image-thumbnails/<file>`

## 注册执行器

注册执行器位于 `register-executor/`，由 FastAPI 提供服务。主要接口：

- `/health`
- `/api/register`
- `/api/register/start`
- `/api/register/stop`
- `/api/register/reset`
- `/api/register/repair-abnormal`
- `/api/register/outlook-pool/reset`
- `/api/register/outlook-pool/test`
- `/api/register/events`

执行器通过内部接口把新账号写回 Go API：

- `/internal/register/accounts`
- `/internal/register/accounts/refresh`
- `/internal/register/accounts/delete`

注册执行器要求 `X-Register-Internal-Key` 或 Bearer Token 校验。返回 Outlook 池状态和事件时会脱敏密码、refresh token 等敏感字段。

## 前端

前端位于 `web/`，使用 Next.js（React 框架）静态导出：

```powershell
Set-Location web
pnpm install --frozen-lockfile
pnpm run typecheck
pnpm run build
```

生产构建输出在 `web/out`，Dockerfile 会复制为后端可服务的 `web_dist/`。生产默认使用同源 API；开发环境默认连接 `http://127.0.0.1:8000`，可通过 `NEXT_PUBLIC_DEV_BACKEND` 覆盖。

当前前端页面包括首页、登录、图片生成、图片管理、密钥、账号、日志、任务、注册和设置。

## 本地开发

Go 后端：

```powershell
go mod download
go run ./cmd/server
```

指定端口：

```powershell
$env:GPT2API_IMAGE_ADDR = ":3000"
go run ./cmd/server
```

常用验证：

```powershell
go test ./cmd/... ./internal/...
go vet ./cmd/... ./internal/...
```

Makefile（构建脚本）提供：

```powershell
make build
make test
make test-race
make verify
make web
make docker
```

如果 Windows 环境缺少工具链，可以在 WSL（Windows Subsystem for Linux，Linux 子系统）里运行 Go 测试、前端构建和 Docker Compose 验证。

## 构建和发布

Dockerfile 会完成：

1. 使用 Node 22 构建前端静态文件。
2. 使用 Go 构建 `cmd/server`。
3. 生成包含 Go 服务和 `web_dist/` 的运行镜像。

GitHub Actions（自动化流程）包含：

- `.github/workflows/build.yml`：Go 测试、前端 typecheck/build、PostgreSQL 集成测试、跨平台 release 包。
- `.github/workflows/docker-publish.yml`：tag 或手动触发时发布 GHCR Docker 镜像。

release 包会包含服务二进制、`web_dist/`、README、LICENSE、VERSION、配置示例和 curl-impersonate 运行文件。

## 运行数据

默认运行数据在 `data/`：

| 路径 | 说明 |
| --- | --- |
| `data/images/` | 生成和编辑后的图片 |
| `data/logs/` | 运行日志 |
| `data/bin/curl-impersonate/` | curl-impersonate 可执行文件 |
| `data/*.json` | 本地 JSON 存储数据 |

生产环境建议使用 PostgreSQL 保存任务和关键状态，并定期备份数据库、`data/images/` 和配置文件。

## 安全注意

- 生产环境必须设置强随机 `GPT2API_IMAGE_AUTH_KEY`，不要使用示例值或空值。
- 不要提交 `.env`、账号 cookie、令牌、注册池数据、日志、图片导出、抓包文件或本地测试数据。
- 对外服务建议放在 HTTPS（加密 HTTP）反向代理后面。
- 注册执行器内部密钥建议和主密钥分离。
- 图片和日志可能包含用户输入内容，清理和导出前需要确认权限和敏感信息。
- CORS（跨源资源共享）当前对 API 路由较开放，公网部署时应在反向代理或外层网关限制来源、认证和访问频率。
