# gpt2api-image

`gpt2api-image` 是一个面向图片生成接口的本地网关和管理后台。它把图片生成、图片编辑、账号池、图片归档、异步任务、注册执行器和 OpenAI 兼容接口放在同一套服务里，既可以单机运行，也可以用 PostgreSQL（数据库）拆成 API 进程和 Worker（后台任务进程）。

这个项目只聚焦图片链路。公开的 `/v1/chat/completions` 和 `/v1/responses` 只处理能转换成图片生成的请求，普通文本聊天不会代理到上游文本模型；`/v1/messages` 保持禁用。

## 功能概览

- OpenAI 兼容图片接口：`/v1/images/generations`、`/v1/images/edits`、`/v1/models`
- 图片化兼容接口：image-only `/v1/chat/completions`、image-only `/v1/responses`
- 图片任务闭环：创建、查询、领取、心跳、执行、保存、取消、超时、失败退款、部分成功退款
- 双任务模式：无 PostgreSQL 时本地 JSON 任务，有 PostgreSQL 时 API/Worker 队列任务
- 账号池与路由：支持 web 路由、Codex 路由、单账号并发限制、跨进程账号 lease
- 图片归档：结果保存到 `data/images`，按需生成 `data/image_thumbnails`
- 管理后台：账号、API 密钥、图片工作台、图片管理、任务、设置、日志、注册执行器
- 注册执行器：独立 Python 服务，用于注册和账号补充流程
- CI（持续集成）与发布：Go 测试、前端构建、PostgreSQL 集成测试、发布包、Docker 镜像发布

## 架构

```text
Web 管理后台 / OpenAI 兼容客户端
  -> Go API 鉴权、校验、扣费
  -> 同步生成或写入 image_tasks_v3
  -> Worker 领取任务、心跳续约
  -> 账号池选择 web/codex 上游账号
  -> 上游生成/编辑图片
  -> 保存到 data/images 并记录 owner/prompt metadata
  -> API 返回 URL / b64_json，或前端轮询任务结果
```

主要目录：

```text
cmd/server/              Go 服务入口
internal/app/            API、任务、账号、存储、日志、上游、Worker 逻辑
web/                     Next.js 管理后台
register-executor/       Python 注册执行器
scripts/                 安装、打包、集成测试脚本
.github/workflows/       GitHub Actions 工作流
assets/                  README 和页面用静态图片
docs/                    补充文档
config.example.json      配置模板
.env.example             Compose 环境变量模板
docker-compose.yml       生产形态 Compose
docker-compose.local.yml 本地调试 Compose
```

运行数据默认在 `data/`：

```text
data/accounts.json           上游账号池
data/auth_keys.json          后台/API 服务密钥
data/settings.json           运行设置
data/images/                 生成图片归档
data/image_thumbnails/       缩略图缓存
data/task_inputs/            异步编辑任务输入图
data/image_tasks.json        无 PostgreSQL 时的本地任务
data/image_owners.json       图片归属
data/image_prompts.json      图片 prompt 元数据
data/logs.jsonl              JSONL 日志
```

`data/`、`.env`、`config.json`、`bin/`、`web_dist/`、`release/` 等运行数据和构建产物默认不提交。

## 快速启动

### Docker Compose

生产形态使用 `docker-compose.yml`，默认暴露 `3000`，同时启动 `api`、`worker`、`postgres` 和 `register-executor`。

```powershell
Copy-Item .env.example .env
Copy-Item config.example.json config.json
notepad .env
notepad config.json
docker compose up -d --build
```

至少要设置：

```env
GPT2API_IMAGE_AUTH_KEY=replace-with-a-long-random-admin-key
POSTGRES_PASSWORD=replace-with-a-long-random-postgres-password
GPT2API_IMAGE_BASE_URL=http://127.0.0.1:3000
```

启动后访问：

- 管理后台：`http://127.0.0.1:3000`
- 图片接口：`http://127.0.0.1:3000/v1/images/generations`
- 系统状态：`http://127.0.0.1:3000/api/system/status`

### 本地 Compose

本地调试使用 `docker-compose.local.yml`，默认暴露 `8000`，PostgreSQL 密码有本地默认值，但仍需要提供 `GPT2API_IMAGE_AUTH_KEY`。

```powershell
Copy-Item .env.example .env
Copy-Item config.example.json config.json
notepad .env
docker compose -f docker-compose.local.yml up -d --build
```

默认地址：

- 管理后台：`http://127.0.0.1:8000`
- Worker 返回图片 URL 的基准地址：`http://127.0.0.1:8000`
- Compose 内部 API 地址：`http://api`
- Compose 内部注册执行器地址：`http://register-executor:8091`

### 使用已发布 Docker 镜像

仓库更新后会自动构建并发布到 GHCR（GitHub Container Registry）。如果不想本地构建，可以在 `.env` 里改成已发布镜像：

```env
GPT2API_IMAGE_IMAGE=ghcr.io/<owner>/gpt2api-image:latest
GPT2API_IMAGE_REGISTER_EXECUTOR_IMAGE=ghcr.io/<owner>/gpt2api-image-register-executor:latest
```

然后运行：

```powershell
docker compose pull
docker compose up -d
```

`docker-compose.yml` 仍保留 `build` 配置，适合本地开发时直接 `docker compose up -d --build`。

### 直接运行 Go 服务

```powershell
Copy-Item config.example.json config.json
$env:GPT2API_IMAGE_AUTH_KEY="replace-with-a-long-random-admin-key"
$env:GPT2API_IMAGE_ADDR=":3000"
go run ./cmd/server
```

运行模式通过 `GPT2API_IMAGE_MODE` 或第一个命令行参数指定：

```powershell
$env:GPT2API_IMAGE_MODE="all"
go run ./cmd/server

go run ./cmd/server serve
go run ./cmd/server worker
go run ./cmd/server all
```

模式说明：

- `serve`：只启动 HTTP API 和管理后台，默认模式
- `worker`：只启动 Worker，需要 `GPT2API_IMAGE_DATABASE_URL` 和 `GPT2API_IMAGE_BASE_URL`
- `all`：API 与 Worker 同进程运行，适合不用 Compose 但仍想跑 PostgreSQL 异步任务的部署

默认监听地址是 `:3000`。

## 配置

配置优先级从高到低：

1. 环境变量
2. `config.json`
3. 代码默认值

`auth-key` 必须设置，不能留空，也不能使用明显的默认占位值。Docker 场景通常用 `GPT2API_IMAGE_AUTH_KEY` 覆盖。

### 常用环境变量

| 变量 | 说明 |
| --- | --- |
| `GPT2API_IMAGE_AUTH_KEY` | 管理后台和 API 根密钥 |
| `GPT2API_IMAGE_ADDR` | HTTP 监听地址，默认 `:3000` |
| `GPT2API_IMAGE_MODE` | 运行模式：`serve`、`worker`、`all` |
| `GPT2API_IMAGE_DATABASE_URL` | PostgreSQL 连接串，启用 DB 任务队列 |
| `GPT2API_IMAGE_BASE_URL` | 生成图片 URL 的外部基准地址 |
| `POSTGRES_PASSWORD` | Compose 内 PostgreSQL 密码 |
| `GPT2API_IMAGE_IMAGE` | API/Worker 镜像名 |
| `GPT2API_IMAGE_REGISTER_EXECUTOR_IMAGE` | 注册执行器镜像名 |
| `GPT2API_IMAGE_REGISTER_EXECUTOR_URL` | API 调用注册执行器的地址 |
| `GPT2API_IMAGE_REGISTER_INTERNAL_KEY` | API 与注册执行器之间的内部密钥 |
| `GPT2API_IMAGE_WORKER_ID` | Worker 标识，默认自动生成 |
| `GPT2API_IMAGE_WORKER_CONCURRENCY` | Worker 并发数，默认 `4` |
| `GPT2API_IMAGE_WORKER_HEARTBEAT_INTERVAL_SECS` | Worker 心跳间隔，默认 `5` |
| `GPT2API_IMAGE_DB_MAX_OPEN_CONNS` | 每个进程的 DB 最大打开连接数，默认 `20` |
| `GPT2API_IMAGE_DB_MAX_IDLE_CONNS` | 每个进程的 DB 最大空闲连接数，默认 `10` |
| `GPT2API_IMAGE_UPSTREAM_TRANSPORT` | 上游传输方式：`tls-client` 或 `curl-impersonate` |
| `GPT2API_IMAGE_ROUTE_STRATEGY` | 图片路由策略：`web_first`、`web_only`、`codex_first`、`codex_only` |
| `GPT2API_IMAGE_CURL_IMPERSONATE_BIN` | 手动指定 curl-impersonate 可执行文件 |
| `GPT2API_IMAGE_CURL_IMPERSONATE_URL` | 手动指定 curl-impersonate 下载地址 |
| `GPT2API_IMAGE_CURL_IMPERSONATE_AUTO_DOWNLOAD` | 是否允许自动下载 curl-impersonate，默认开启 |
| `GPT2API_IMAGE_CORS_ALLOWED_ORIGINS` | CORS（跨域资源共享）允许来源，逗号、分号或换行分隔 |
| `GPT2API_IMAGE_LOG_REQUEST_TEXT` | 是否记录请求文本，默认关闭 |
| `GPT2API_IMAGE_TRACE` | 开启详细链路日志 |
| `GPT2API_IMAGE_NETWORK_TRACE` | 开启网络链路日志 |
| `GPT2API_IMAGE_TLS_PROFILE` | 上游 TLS 指纹配置 |
| `GPT2API_IMAGE_USER_AGENT` | 覆盖上游请求 User-Agent |
| `GPT2API_IMAGE_SEC_CH_UA` | 覆盖上游请求 sec-ch-ua |
| `NEXT_PUBLIC_API_URL` | 前端构建时指定 API 地址，默认同源 |
| `NEXT_PUBLIC_DEV_BACKEND` | 前端开发模式后端地址，默认 `http://127.0.0.1:8000` |
| `TZ` | 容器时区 |

### `config.json` 字段

| 字段 | 说明 |
| --- | --- |
| `auth-key` | 根密钥，必填 |
| `database_url` | PostgreSQL 连接串 |
| `refresh_account_interval_minute` | 账号刷新间隔，默认 `60` |
| `image_retention_days` | 图片保留天数，默认 `15` |
| `image_poll_timeout_secs` | 上游图片轮询超时，默认 `120` |
| `image_poll_interval_secs` | 上游图片轮询间隔，默认 `4` |
| `image_poll_initial_wait_secs` | 首次轮询等待秒数，默认 `0` |
| `image_task_timeout_secs` | 异步图片任务超时，默认 `300`，最小 `60` |
| `image_task_claim_ttl_secs` | Worker 任务 claim TTL，默认 `300`，最小 `15` |
| `image_worker_poll_interval_secs` | Worker 空闲轮询间隔，默认 `1` |
| `image_account_concurrency` | 单个上游账号图片并发数，默认 `3` |
| `auto_remove_rate_limited_accounts` | 自动移除限流账号 |
| `auto_remove_invalid_accounts` | 自动移除无效账号，默认开启 |
| `cleanup_protect_user_images` | 清理旧图时保护普通用户归属图片，默认开启 |
| `log_levels` | 日志等级过滤 |
| `log_request_text` | 是否记录请求文本 |
| `cors_allowed_origins` | CORS 允许来源列表 |
| `proxy` | 默认上游代理地址 |
| `upstream_transport` | 上游传输方式 |
| `image_route_strategy` | 图片账号路由策略 |
| `base_url` | 图片 URL 外部基准地址 |
| `sensitive_words` | 本地敏感词列表 |
| `global_system_prompt` | 全局系统提示词 |
| `register_executor_url` | 注册执行器地址 |
| `register_internal_key` | 注册执行器内部密钥 |

内容控制目前只做本地 `sensitive_words` 字符串匹配，不等同于完整内容安全系统。

## 认证与权限

所有 API 使用 Bearer Token（持有者令牌）：

```http
Authorization: Bearer <token>
```

根密钥来自 `auth-key` 或 `GPT2API_IMAGE_AUTH_KEY`。后台创建的服务密钥也可作为 Bearer Token 使用。当前服务密钥会规范化为 `admin`、`premium`、`unlimited`，也就是拥有后台管理权限和无限额度。

生产环境必须更换根密钥，并避免把 `.env`、`config.json`、数据库文件、导出数据或 `data/` 提交到仓库。

## OpenAI 兼容接口

### 图片生成

```http
POST /v1/images/generations
Authorization: Bearer <token>
Content-Type: application/json
```

示例：

```json
{
  "model": "gpt-image-2",
  "prompt": "a clean product photo",
  "n": 1,
  "size": "1024x1024",
  "resolution": "1k",
  "response_format": "url"
}
```

说明：

- `model` 会归一化到项目公开图片模型 `gpt-image-2`
- `n` 会限制在 `1` 到 `4`
- `response_format` 支持 `url` 和 `b64_json`，默认 `b64_json`
- `stream: true` 返回 SSE（Server-Sent Events，服务器发送事件）
- 有 PostgreSQL 且传入 `client_task_id` 时创建异步任务并返回 `202`
- 没有 PostgreSQL 或没有 `client_task_id` 时同步执行

### 图片编辑

```http
POST /v1/images/edits
Authorization: Bearer <token>
Content-Type: multipart/form-data
```

字段：

- `prompt`
- `image` 或 `image[]`
- `model`
- `n`
- `size`
- `resolution`
- `response_format`
- `client_task_id`

异步编辑任务会把输入图片暂存到 `data/task_inputs/`，任务完成、失败、取消或超时后会清理。

### 模型列表

```http
GET /v1/models
Authorization: Bearer <token>
```

服务优先返回上游动态模型列表；上游不可用时返回内置图片模型列表。

### 图片化 Chat/Responses

```http
POST /v1/chat/completions
POST /v1/responses
```

这两个接口只接受能识别为图片生成的请求。可通过 `client_task_id` 创建异步图片任务。普通文本请求会返回禁用提示。

```http
POST /v1/messages
```

该接口保持禁用。

## 任务 API

后台图片工作台使用任务 API，每张前端图片都有稳定的 `client_task_id`，便于刷新恢复、轮询、取消和失败重试。

| 接口 | 方法 | 说明 |
| --- | --- | --- |
| `/api/image-tasks` | `GET` | 查询任务列表或按 `ids` 查询 |
| `/api/image-tasks/generations` | `POST` | 创建文生图任务 |
| `/api/image-tasks/edits` | `POST` | 创建图生图任务 |
| `/api/image-tasks/cancel` | `POST` | 取消 queued/running 任务 |

PostgreSQL 模式下，任务写入 `image_tasks_v3`，Worker 用 `FOR UPDATE SKIP LOCKED` 领取任务。取消、超时、保存失败、metadata 写入失败都会退回对应图片额度。多图任务部分成功时，只退回失败数量。

无 PostgreSQL 时，本地任务保存到 `data/image_tasks.json`，并在 API 进程内执行，适合单机调试，不适合作为多进程队列。

## 图片归档 API

生成图片保存路径：

```text
data/images/YYYY/MM/DD/<timestamp>_<random>_<md5>.<ext>
```

公开访问路径：

```text
/images/<relative-path>
/image-thumbnails/<relative-path>
```

支持的扩展名按图片内容识别，包括 `png`、`jpg`、`jpeg`、`gif`、`webp`、`avif`、`bmp`、`tif`、`tiff`。

管理接口：

| 接口 | 方法 | 权限 | 说明 |
| --- | --- | --- | --- |
| `/api/images` | `GET` | admin | 查询图库 |
| `/api/me/images` | `GET` | 登录用户 | 查询自己的图片 |
| `/api/images/owners` | `GET` | admin | 查询图片归属统计 |
| `/api/images/delete` | `POST` | 登录用户 | 删除图片，普通用户只能删自己的图片 |
| `/api/images/download` | `POST` | admin | 批量打包下载 |
| `/api/images/download/<path>` | `GET` | admin | 单图下载 |
| `/api/images/tags` | `GET/POST` | admin | 查询或设置标签 |
| `/api/images/tags/<tag>` | `DELETE` | admin | 删除标签 |

路径会经过 `safeImageRel` 校验，防止目录穿越。`/images/` 是公开结果 URL，用于让外部客户端直接访问生成结果。

## 管理后台 API

| 接口 | 说明 |
| --- | --- |
| `/auth/login` | 登录验证 |
| `/api/auth/me` | 当前身份和额度 |
| `/api/auth/users` | 服务密钥管理 |
| `/api/accounts` | 上游账号导入、删除、列表 |
| `/api/accounts/refresh` | 刷新账号状态 |
| `/api/accounts/update` | 更新账号状态、类型、额度 |
| `/api/settings` | 运行配置读取和保存 |
| `/api/storage/info` | 存储后端状态 |
| `/api/system/status` | 系统状态 |
| `/api/transport/status` | 上游传输状态 |
| `/api/proxy` | 代理配置 |
| `/api/proxy/test` | 代理测试 |
| `/api/logs` | 日志查询 |
| `/api/logs/delete` | 日志删除 |
| `/version` | 版本号 |

## 注册执行器

Compose 中包含独立 `register-executor` 服务。API 通过下面配置调用它：

```env
GPT2API_IMAGE_REGISTER_EXECUTOR_URL=http://register-executor:8091
GPT2API_IMAGE_REGISTER_INTERNAL_KEY=<internal-key>
```

相关接口：

| 接口 | 说明 |
| --- | --- |
| `/api/register` | 注册执行器配置和状态 |
| `/api/register/start` | 启动注册任务 |
| `/api/register/stop` | 停止注册任务 |
| `/api/register/reset` | 重置注册状态 |
| `/api/register/repair-abnormal` | 修复异常注册任务 |
| `/api/register/outlook-pool/reset` | 重置 Outlook 邮箱池 |
| `/api/register/outlook-pool/test` | 测试 Outlook 邮箱池 |
| `/api/register/events` | 注册事件流 |
| `/internal/register/accounts` | 注册执行器回写账号 |
| `/internal/register/accounts/refresh` | 注册执行器刷新账号 |
| `/internal/register/accounts/delete` | 注册执行器删除账号 |

`/internal/*` 接口只应在可信网络内使用。生产环境应设置内部密钥，并且不要把注册执行器直接暴露到公网。

## 前端开发

管理后台位于 `web/`，使用 Next.js（React 框架）静态导出。

```powershell
cd web
corepack enable
pnpm install --frozen-lockfile
pnpm run dev
```

构建：

```powershell
cd web
pnpm run typecheck
pnpm run build
```

运行镜像构建时会执行前端类型检查和静态导出，并把 `web/out` 复制进镜像内的 `/app/web_dist`。

## 构建、测试和发布

常用验证命令：

```powershell
# Go 单元测试
go test ./cmd/... ./internal/...

# Go 静态检查
go vet ./cmd/... ./internal/...

# race 检查
go test -race ./internal/app

# 前端类型检查和构建
cd web
pnpm run typecheck
pnpm run build

# Python 和 shell 脚本语法检查
python -m compileall -q register-executor
bash -n start.sh scripts/package_release.sh scripts/install_latest.sh scripts/run_pg_integration.sh

# PostgreSQL 集成测试
bash scripts/run_pg_integration.sh
```

`scripts/run_pg_integration.sh` 默认会用 Docker 启动临时 PostgreSQL；没有 Docker daemon 时，可以设置 `GPT2API_IMAGE_TEST_DATABASE_URL` 指向现有 PostgreSQL。

打包脚本：

```bash
bash scripts/package_release.sh --web
bash scripts/install_latest.sh --dir /opt/gpt2api-image
```

发布包启动脚本：

```bash
./start.sh --port 3000 --mode all
./start.sh --port 3000 --worker
./start.sh --curl
./start.sh --tls
```

## GitHub Actions

`.github/workflows/build.yml`：

- push 到 `main`、创建 `v*` tag、PR 到 `main` 时运行
- 执行 Go 测试、前端类型检查和构建、PostgreSQL 集成测试
- `v*` tag 会打包 release assets 并上传 GitHub Release

`.github/workflows/docker-publish.yml`：

- 每次 push 任意分支都会构建并推送 Docker 镜像到 GHCR
- `v*` tag 会额外生成 semver 标签
- 默认分支和 `v*` tag 会生成 `latest`
- 同时发布两个镜像：

```text
ghcr.io/<owner>/gpt2api-image
ghcr.io/<owner>/gpt2api-image-register-executor
```

默认标签包括分支名、commit sha、tag、semver 和 `latest`。

## 安全注意

- 生产环境必须更换 `auth-key`、`POSTGRES_PASSWORD` 和注册执行器内部密钥
- 服务密钥当前是 admin/unlimited 级别，不要发给不可信调用方
- 只在确实需要跨域时配置 `cors_allowed_origins`
- `log_request_text` 默认关闭，开启后可能记录 prompt、图片意图等敏感内容
- `sensitive_words` 只是本地字符串过滤，不是完整内容安全系统
- 不要提交 `.env`、`config.json`、`data/`、数据库文件、日志、导出数据、构建产物或 release 包
- 反向代理只暴露必要 HTTP 入口，PostgreSQL 和 register-executor 不应直接暴露公网
- 如果公开 `/images/`，要理解它是生成结果 URL 的公开访问面

## 排障入口

常用检查点：

- 服务状态：`/api/system/status`
- 上游传输状态：`/api/transport/status`
- 日志：`/api/logs`
- 图片任务：`/api/image-tasks`
- 图片归档：`/api/images`
- 本地日志文件：`data/logs.jsonl`
- 图片文件：`data/images`
- PostgreSQL 任务表：`image_tasks_v3`

建议顺序：

1. 确认 API 端口和反向代理是否正常
2. 确认 `auth-key`、Bearer Token 和后台登录是否一致
3. 确认 PostgreSQL 是否可连接
4. 确认 Worker 是否运行并能领取任务
5. 确认上游账号、账号类型、代理和传输方式是否可用
6. 查看 `/api/logs`、`data/logs.jsonl` 和 trace 日志
7. 查看 `data/images` 是否成功写入图片
8. 查看任务是否卡在 queued/running、是否被取消或超时
