# gpt2api-image

`gpt2api-image` 是一个面向图片生成接口的本地网关和管理后台。项目把图片生成、图片编辑、账号管理、注册执行器、图片归档、任务队列和 OpenAI 兼容接口放在同一个服务链路里，适合单机运行，也支持用 PostgreSQL（数据库）拆分 API 与 Worker（后台任务进程）。

## 当前能力

- OpenAI 兼容图片接口：`/v1/images/generations`、`/v1/images/edits`
- 图片任务闭环：创建任务、领取任务、执行生成/编辑、保存图片、查询结果、取消任务、失败退款
- 管理后台：账号、代理、图片库、任务、日志、系统状态、注册执行器
- 图片归档：生成结果保存到本地 `data/images`，并按需生成缩略图
- 注册执行器：通过独立容器处理账号注册和刷新相关动作
- 多运行模式：`serve`、`worker`、`all`
- PostgreSQL 队列模式：API 进程负责接收请求，Worker 进程负责消费任务
- 兼容部分图片化调用：`/v1/chat/completions`、`/v1/responses` 只处理图片生成请求

普通文本聊天代理不是这个项目的目标。当前公开路由中，`/v1/chat/completions` 和 `/v1/responses` 只接受可转换为图片生成的请求；普通文本请求会返回禁用提示，`/v1/messages` 也保持禁用。

## 目录结构

```text
cmd/server/              Go 服务入口
internal/app/            API、任务、账号、存储、日志、Worker 逻辑
web/                     Next.js 管理后台
scripts/                 安装、打包和辅助脚本
assets/                  页面图标和静态资源
config.example.json      配置模板
docker-compose.yml       生产形态 Compose
docker-compose.local.yml 本地调试 Compose
```

运行数据默认放在 `data/`：

```text
data/accounts.json           账号数据
data/users.json              后台用户
data/settings.json           运行设置
data/images/                 原图归档
data/image_thumbnails/       缩略图
data/task_inputs/            异步编辑任务的输入图片
data/logs.jsonl              JSONL 日志
```

`data/`、`.env`、`config.json`、构建产物和发布包默认不会提交到仓库。

## 快速启动

### 1. Docker Compose

生产形态使用 `docker-compose.yml`，默认对外暴露 `3000`：

```powershell
Copy-Item .env.example .env
Copy-Item config.example.json config.json
notepad .env
notepad config.json
docker compose up -d --build
```

至少需要配置：

```env
GPT2API_IMAGE_AUTH_KEY=please-change-me
POSTGRES_PASSWORD=please-change-me
GPT2API_IMAGE_BASE_URL=http://127.0.0.1:3000
```

启动后访问：

- 管理后台：`http://127.0.0.1:3000`
- 图片接口：`http://127.0.0.1:3000/v1/images/generations`

### 2. 本地 Compose

`docker-compose.local.yml` 默认对外暴露 `8000`，更适合本机验证：

```powershell
docker compose -f docker-compose.local.yml up -d --build
```

默认服务地址：

- 管理后台：`http://127.0.0.1:8000`
- API 内部服务：`api:80`
- Worker 回调基准地址：`http://127.0.0.1:8000`

### 3. 直接运行 Go 服务

本地直接运行时，先准备配置：

```powershell
Copy-Item config.example.json config.json
$env:GPT2API_IMAGE_AUTH_KEY="please-change-me"
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

- `serve`：只启动 HTTP API 和管理后台
- `worker`：只启动任务 Worker，需要 `GPT2API_IMAGE_DATABASE_URL` 和 `GPT2API_IMAGE_BASE_URL`
- `all`：API 与 Worker 同进程运行

默认模式是 `serve`，默认监听地址是 `:3000`。

## 配置

配置来源按优先级从高到低：

1. 环境变量
2. `config.json`
3. 代码默认值

`config.example.json` 是模板，正式运行建议复制成 `config.json` 后再修改。`auth-key` 不能留空，也不能使用模板占位值；Docker 场景可以用 `GPT2API_IMAGE_AUTH_KEY` 覆盖。

### 常用环境变量

| 变量 | 说明 |
| --- | --- |
| `GPT2API_IMAGE_AUTH_KEY` | 管理后台和 API 的根密钥 |
| `POSTGRES_PASSWORD` | Compose 内 PostgreSQL 密码 |
| `GPT2API_IMAGE_ADDR` | HTTP 监听地址，默认 `:3000` |
| `GPT2API_IMAGE_MODE` | 运行模式：`serve`、`worker`、`all` |
| `GPT2API_IMAGE_DATABASE_URL` | PostgreSQL 连接串 |
| `GPT2API_IMAGE_BASE_URL` | 返回图片 URL 时使用的外部基准地址 |
| `GPT2API_IMAGE_IMAGE` | API/Worker 镜像名 |
| `GPT2API_IMAGE_REGISTER_EXECUTOR_IMAGE` | 注册执行器镜像名 |
| `GPT2API_IMAGE_REGISTER_EXECUTOR_URL` | 注册执行器地址 |
| `GPT2API_IMAGE_REGISTER_INTERNAL_KEY` | API 与注册执行器之间的内部密钥 |
| `GPT2API_IMAGE_WORKER_CONCURRENCY` | Worker 并发数，默认 `4` |
| `GPT2API_IMAGE_WORKER_HEARTBEAT_INTERVAL_SECS` | Worker 心跳间隔 |
| `GPT2API_IMAGE_DB_MAX_OPEN_CONNS` | 数据库最大打开连接数 |
| `GPT2API_IMAGE_DB_MAX_IDLE_CONNS` | 数据库最大空闲连接数 |
| `GPT2API_IMAGE_UPSTREAM_TRANSPORT` | 上游传输方式 |
| `GPT2API_IMAGE_ROUTE_STRATEGY` | 图片账号路由策略 |
| `GPT2API_IMAGE_CORS_ALLOWED_ORIGINS` | CORS（跨域资源共享）允许来源，逗号分隔 |
| `GPT2API_IMAGE_LOG_REQUEST_TEXT` | 是否记录请求文本，默认关闭 |
| `GPT2API_IMAGE_CURL_IMPERSONATE_AUTO_DOWNLOAD` | curl-impersonate 自动下载开关 |
| `TZ` | 容器时区 |

### `config.json` 字段

| 字段 | 说明 |
| --- | --- |
| `auth-key` | 根密钥，必填 |
| `database_url` | PostgreSQL 连接串 |
| `refresh_account_interval_minute` | 账号刷新间隔，默认 `60` |
| `image_retention_days` | 图片保留天数，默认 `15` |
| `image_poll_timeout_secs` | 同步轮询超时，默认 `120` |
| `image_poll_interval_secs` | 同步轮询间隔，默认 `4` |
| `image_poll_initial_wait_secs` | 首次轮询等待时间，默认 `0` |
| `image_task_timeout_secs` | 图片任务超时，默认 `300`，最小 `60` |
| `image_task_claim_ttl_secs` | Worker 领取任务 TTL，默认 `300`，最小 `15` |
| `image_worker_poll_interval_secs` | Worker 空闲轮询间隔，默认 `1` |
| `image_account_concurrency` | 单账号图片并发，默认 `3` |
| `auto_remove_rate_limited_accounts` | 自动移除限流账号 |
| `auto_remove_invalid_accounts` | 自动移除无效账号 |
| `cleanup_protect_user_images` | 清理旧图片时保护普通用户图片 |
| `log_levels` | 日志等级过滤 |
| `log_request_text` | 是否记录请求文本 |
| `cors_allowed_origins` | CORS 允许来源列表 |
| `proxy` | 默认代理地址 |
| `upstream_transport` | 上游传输方式 |
| `image_route_strategy` | 图片账号路由策略 |
| `base_url` | 图片 URL 外部基准地址 |
| `sensitive_words` | 本地敏感词列表 |
| `global_system_prompt` | 全局系统提示词 |

内容控制目前只保留本地 `sensitive_words` 字符串匹配。

## 认证与用户

根密钥 `auth-key` 和服务密钥都可以作为 Bearer Token（持有者令牌）调用接口。服务密钥会规范化为固定身份：

- `admin`
- `premium`
- `unlimited`

管理后台登录和 API 访问都依赖同一套认证逻辑。生产环境必须更换默认密钥，并避免把 `.env`、`config.json`、数据库文件或 `data/` 目录提交到仓库。

## OpenAI 兼容接口

### 图片生成

```http
POST /v1/images/generations
Authorization: Bearer <token>
Content-Type: application/json
```

常用字段：

```json
{
  "model": "gpt-image-1",
  "prompt": "a clean product photo",
  "n": 1,
  "size": "1024x1024",
  "response_format": "url"
}
```

说明：

- `n` 会限制在 `1` 到 `4`
- `response_format` 支持 `url` 和 `b64_json`
- `stream: true` 时返回 SSE（服务器发送事件）
- `resolution` 会按项目规则转换为上游可识别尺寸
- 有数据库且传入 `client_task_id` 时走异步任务，返回 `202`
- 没有数据库或没有 `client_task_id` 时走同步执行

### 图片编辑

```http
POST /v1/images/edits
Authorization: Bearer <token>
Content-Type: multipart/form-data
```

常用字段：

- `prompt`
- `image` 或 `image[]`
- `model`
- `n`
- `size`
- `response_format`
- `client_task_id`

异步编辑任务会把输入图片临时保存到 `data/task_inputs/`，任务完成、失败或取消后会清理。

### 模型列表

```http
GET /v1/models
Authorization: Bearer <token>
```

服务优先返回上游动态模型列表；不可用时返回内置图片模型列表。

### 图片化 Chat/Responses

```http
POST /v1/chat/completions
POST /v1/responses
```

这两个接口只处理能识别为图片生成的请求，用于兼容部分客户端。普通文本聊天不会转发到上游文本模型。

## 任务链路

有 PostgreSQL 时，真实链路是：

```text
客户端请求
  -> API 鉴权和参数校验
  -> 写入 image_tasks_v3
  -> Worker 领取任务并心跳续约
  -> 账号路由和上游图片请求
  -> 保存图片到 data/images
  -> 写入任务结果
  -> 客户端查询任务或获取返回 URL
```

Worker 会处理：

- 任务领取和锁续约
- 生成和编辑两类任务
- 任务超时
- 取消任务
- 失败退款
- 编辑任务输入文件清理

没有 PostgreSQL 时，后台页面创建的本地任务会在 API 进程内执行，适合单机调试，不适合作为多进程队列。

## 图片存储与清理

图片保存路径：

```text
data/images/YYYY/MM/DD/<timestamp>_<random>_<md5>.<ext>
```

扩展名根据图片内容识别，支持：

- `png`
- `jpg` / `jpeg`
- `gif`
- `webp`
- `avif`
- `bmp`
- `tif` / `tiff`

公开访问路径是：

```text
/images/<relative-path>
```

下载、删除、标签和缩略图通过后台接口管理。旧图片清理遵守 `image_retention_days`，开启 `cleanup_protect_user_images` 后会保护普通用户归属的图片。

## 注册执行器

Compose 中包含独立的 `register-executor` 服务。API 通过：

```env
GPT2API_IMAGE_REGISTER_EXECUTOR_URL=http://register-executor:8091
GPT2API_IMAGE_REGISTER_INTERNAL_KEY=<internal-key>
```

调用注册执行器。生产环境必须设置内部密钥，避免注册执行器被外部直接调用。

相关后台接口包括：

- `/api/register/start`
- `/api/register/stop`
- `/api/register/reset`
- `/api/register/events`
- `/internal/register/accounts`

`/internal/*` 接口只应在可信网络内使用。

## 前端开发

管理后台位于 `web/`，使用 Next.js（React 框架）静态导出。

```powershell
cd web
corepack enable
pnpm install
pnpm run dev
```

构建：

```powershell
cd web
pnpm run typecheck
pnpm run build
```

正式镜像构建会执行前端类型检查和静态导出，并把结果放入运行镜像的 `web_dist`。

## 常用命令

```powershell
# Go 测试
go test ./...

# 后端构建
go build -o bin/gpt2api-image.exe ./cmd/server

# 前端构建
cd web
pnpm run typecheck
pnpm run build

# 项目验证
make verify

# 发布包
bash scripts/package_release.sh --web
```

Windows 没有 `make` 或 Bash 时，可以直接运行对应的 `go`、`pnpm` 和 Docker 命令。

## 打包与安装脚本

- `scripts/package_release.sh`：生成发布包，包含服务二进制、前端静态文件、配置模板、README 和启动脚本
- `scripts/install_latest.sh`：从 Release 或源码安装，保留已有 `config.json` 和 `data/`

发布包启动脚本支持指定端口和模式：

```bash
./start.sh --port 3000 --mode all
```

## 安全注意

- 生产环境必须更换 `auth-key`、`POSTGRES_PASSWORD` 和注册执行器内部密钥
- 只在需要跨域访问时配置 `cors_allowed_origins`，不要随意放开到不可信域名
- `log_request_text` 默认关闭，开启后可能记录提示词等敏感内容
- `sensitive_words` 只是本地字符串过滤，不等同于完整内容安全系统
- 不要把 `.env`、`config.json`、`data/`、数据库文件、日志、导出数据、构建产物提交到仓库
- 反向代理应只暴露必要 HTTP 入口，注册执行器和 PostgreSQL 不应直接暴露到公网

## 排障入口

- 管理后台系统状态：`/api/system/status`
- 上游传输状态：`/api/transport/status`
- 日志接口：`/api/logs`
- 图片任务：`/api/image-tasks`
- 图片归档：`/api/images`

排查顺序建议：

1. 看服务是否启动、端口是否正确
2. 看 `auth-key`、Bearer Token 和后台登录是否一致
3. 看 PostgreSQL 是否可连接
4. 看 Worker 是否在运行并领取任务
5. 看上游账号、代理和传输方式是否可用
6. 看 `data/logs.jsonl` 和后台日志
7. 看 `data/images` 是否成功写入图片
