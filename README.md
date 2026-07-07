# gpt2api-image

`gpt2api-image` 是一个面向图片生成链路的 OpenAI 兼容网关和管理后台。项目由 Go API/Worker、Next.js 管理端、Python 注册执行器和 PostgreSQL 任务队列组成，推荐部署在 Linux 服务器上运行。

项目只处理图片相关请求。公开的 `/v1/chat/completions` 和 `/v1/responses` 是 image-only（仅图片模式）兼容接口，普通文本聊天不会被代理到上游文本模型；`/v1/messages` 默认禁用。

## 功能

- OpenAI 兼容图片接口：`/v1/images/generations`、`/v1/images/edits`、`/v1/models`
- image-only 兼容接口：`/v1/chat/completions`、`/v1/responses`
- 图片任务闭环：创建、入队、领取、心跳、执行、保存、取消、超时、失败退款、部分成功退款
- 账号池：支持 web/codex 两类账号、刷新、导入、并发限制、异常账号清理
- 图片归档：结果保存到 `data/images`，缩略图保存到 `data/image_thumbnails`
- 管理后台：账号管理、API 密钥、图片工作台、图片管理、任务、设置、日志、注册机
- 注册执行器：独立 Python 服务，负责邮箱取码、注册、刷新校验、异常修复和自动补号
- PostgreSQL 队列：生产部署下 API 和 Worker 分离，支持跨进程领取和续约

## 架构

```text
客户端 / 管理后台
  -> Go API 鉴权、校验、扣费
  -> 同步生成，或写入 PostgreSQL image_tasks_v3
  -> Worker 领取任务并续约
  -> 账号池选择 web/codex 上游账号
  -> 上游生成或编辑图片
  -> 保存到 data/images 并记录 owner/prompt
  -> 返回 url / b64_json，或由前端轮询任务结果
```

主要目录：

```text
cmd/server/              Go 服务入口
internal/app/            API、任务、账号、存储、日志、上游、Worker 逻辑
web/                     Next.js 管理后台
register-executor/       Python 注册执行器
scripts/                 安装、打包、集成测试脚本
assets/                  README 和页面静态图
docs/                    补充文档
config.example.json      配置模板
.env.example             Docker Compose 环境变量模板
docker-compose.yml       服务器部署 Compose
docker-compose.local.yml 本地调试 Compose
```

运行数据默认写入 `data/`：

```text
data/accounts.json           上游账号池
data/auth_keys.json          后台/API 服务密钥
data/settings.json           运行设置
data/register.json           注册机配置和统计
data/images/                 生成图片归档
data/image_thumbnails/       缩略图缓存
data/task_inputs/            异步编辑任务输入图
data/image_tasks.json        无 PostgreSQL 时的本地任务
data/image_owners.json       图片归属
data/image_prompts.json      图片 prompt 元数据
data/logs.jsonl              JSONL 日志
```

## 服务器部署

推荐使用 Docker Compose。生产形态会启动 4 个常驻服务，并在启动前运行 1 个一次性迁移服务：

- `config-migrate`：一次性初始化或迁移 `data/config.json`
- `postgres`：PostgreSQL 任务队列
- `api`：Go HTTP API 和管理后台
- `worker`：后台图片任务 Worker
- `register-executor`：Python 注册执行器

### 1. 准备配置

```bash
cd /opt/gpt2api-image
cp .env.example .env
mkdir -p data
cp config.example.json data/config.json

openssl rand -hex 32
nano .env
nano data/config.json
```

`.env` 至少设置：

```env
GPT2API_IMAGE_AUTH_KEY=replace-with-a-long-random-admin-key
GPT2API_IMAGE_REGISTER_INTERNAL_KEY=replace-with-a-different-long-random-internal-key
POSTGRES_PASSWORD=replace-with-a-long-random-postgres-password
GPT2API_IMAGE_BASE_URL=https://your-api.example.com
```

说明：

- `GPT2API_IMAGE_AUTH_KEY` 是管理后台和根 API 密钥，不能留空，也不能使用默认占位值。
- `GPT2API_IMAGE_BASE_URL` 用于 Worker 返回图片 URL，生产环境要填外部可访问域名。
- `GPT2API_IMAGE_REGISTER_INTERNAL_KEY` 是 API 和注册执行器内部通信密钥，启用注册执行器时必填，且不要复用 `GPT2API_IMAGE_AUTH_KEY`。
- `data/config.json` 是后台设置页保存的配置文件。旧部署根目录下的 `config.json` 只作为迁移来源，不再作为 Compose 的运行挂载。
- 反向代理只需要转发到 API 暴露端口，注册执行器不要直接暴露到公网。

### 2. 启动

```bash
docker compose up -d --build
docker compose ps
docker compose logs -f api worker register-executor
```

默认端口：

- 管理后台：`http://服务器IP:3000`
- OpenAI 兼容接口：`http://服务器IP:3000/v1`
- 状态接口：`http://服务器IP:3000/api/system/status`

如果使用反向代理，建议把外部 HTTPS 域名写入 `GPT2API_IMAGE_BASE_URL`，并把 `3000` 只开放给反向代理或可信来源。

### 3. 更新

```bash
git pull
docker compose up -d --build
docker compose logs -f api worker register-executor
```

`data/` 和 PostgreSQL volume 会被保留。更新前建议备份：

```bash
tar -czf backup-data-$(date +%F-%H%M%S).tar.gz data .env
docker compose exec postgres pg_dump -U gpt2api_image gpt2api_image > backup-db-$(date +%F-%H%M%S).sql
```

## 使用已发布镜像

如果不想在服务器本地构建镜像，可以把 `.env` 中的镜像名改为已发布镜像：

```env
GPT2API_IMAGE_IMAGE=ghcr.io/<owner>/gpt2api-image:latest
GPT2API_IMAGE_REGISTER_EXECUTOR_IMAGE=ghcr.io/<owner>/gpt2api-image-register-executor:latest
```

然后运行：

```bash
docker compose pull
docker compose up -d
```

`docker-compose.yml` 仍保留 `build` 配置，所以开发环境也可以继续使用 `docker compose up -d --build`。

## 直接运行

直接运行适合开发或单机排障，生产仍推荐 Compose。

### Go API

```bash
mkdir -p data
cp config.example.json data/config.json
export GPT2API_IMAGE_AUTH_KEY="$(openssl rand -hex 32)"
export GPT2API_IMAGE_ADDR=":3000"
go run ./cmd/server serve
```

运行模式：

```bash
go run ./cmd/server serve   # 只启动 HTTP API 和管理后台
go run ./cmd/server worker  # 只启动 Worker，需要 PostgreSQL 和 base_url
go run ./cmd/server all     # API 和 Worker 同进程运行
```

`worker` 和 `all` 模式必须设置：

```bash
export GPT2API_IMAGE_DATABASE_URL="postgresql://user:pass@127.0.0.1:5432/gpt2api_image"
export GPT2API_IMAGE_BASE_URL="https://your-api.example.com"
```

### 前端

```bash
cd web
corepack enable
pnpm install --frozen-lockfile
pnpm run typecheck
pnpm run build
```

Dockerfile 会自动构建前端并把静态产物复制到 `/app/web_dist`。直接运行 Go 二进制时，如果项目根目录存在 `web_dist/`，后端会同时托管管理后台。

### 注册执行器

```bash
cd register-executor
python3 -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt

export GPT2API_IMAGE_API_BASE_URL="http://127.0.0.1:3000"
export GPT2API_IMAGE_AUTH_KEY="same-as-api-auth-key"
export GPT2API_IMAGE_REGISTER_INTERNAL_KEY="same-as-go-api-register-internal-key"
export REGISTER_EXECUTOR_ADDR="0.0.0.0"
export REGISTER_EXECUTOR_PORT="8091"
python app.py
```

注册执行器只应被 Go API 访问，不建议公网暴露。

## 配置

配置优先级：

1. 环境变量
2. `GPT2API_IMAGE_CONFIG_FILE` 指向的 JSON 文件
3. `data/config.json`
4. 兼容旧路径 `config.json`
5. 代码默认值

默认配置文件是 `data/config.json`。如果设置了 `GPT2API_IMAGE_CONFIG_FILE`，服务会从该路径读取并把后台设置保存回该路径；相对路径按项目根目录解析。

Compose 默认将 `GPT2API_IMAGE_CONFIG_FILE` 设置为 `/app/data/config.json`，并通过 `config-migrate` 在 `data/config.json` 不存在时从旧 `config.json` 或 `config.example.json` 初始化。

直接运行 `start.sh` 时也会自动初始化 `data/config.json`；如果自定义了 `GPT2API_IMAGE_CONFIG_FILE`，脚本会先创建目标目录再复制配置模板。

常用环境变量：

| 变量 | 说明 |
| --- | --- |
| `GPT2API_IMAGE_AUTH_KEY` | 管理后台和根 API 密钥 |
| `GPT2API_IMAGE_ADDR` | HTTP 监听地址，默认 `:3000`，Compose 内为 `:80` |
| `GPT2API_IMAGE_MODE` | 运行模式：`serve`、`worker`、`all` |
| `GPT2API_IMAGE_CONFIG_FILE` | 配置 JSON 文件路径，默认 `data/config.json` |
| `GPT2API_IMAGE_DATABASE_URL` | PostgreSQL 连接串，启用 DB 任务队列 |
| `GPT2API_IMAGE_BASE_URL` | 返回图片 URL 的外部基准地址 |
| `GPT2API_IMAGE_REGISTER_EXECUTOR_URL` | API 调用注册执行器的地址 |
| `GPT2API_IMAGE_REGISTER_INTERNAL_KEY` | API 与注册执行器内部通信密钥；启用注册执行器时必填，不要复用根 API 密钥 |
| `GPT2API_IMAGE_WORKER_CONCURRENCY` | Worker 并发数 |
| `GPT2API_IMAGE_WORKER_HEARTBEAT_INTERVAL_SECS` | Worker 续约和取消检查间隔 |
| `GPT2API_IMAGE_DB_MAX_OPEN_CONNS` | 每个 API/Worker 进程的最大 DB 连接数 |
| `GPT2API_IMAGE_DB_MAX_IDLE_CONNS` | 每个 API/Worker 进程的空闲 DB 连接数 |
| `GPT2API_IMAGE_UPSTREAM_TRANSPORT` | 上游传输：`tls-client` 或 `curl-impersonate` |
| `GPT2API_IMAGE_ROUTE_STRATEGY` | 图片账号路由：`web_first`、`web_only`、`codex_first`、`codex_only` |
| `GPT2API_IMAGE_CORS_ALLOWED_ORIGINS` | 允许跨域来源，多个用逗号分隔 |
| `GPT2API_IMAGE_LOG_REQUEST_TEXT` | 是否记录请求 prompt，默认关闭 |
| `POSTGRES_PASSWORD` | Compose 内 PostgreSQL 密码 |
| `TZ` | 容器时区，默认 `Asia/Shanghai` |

`data/config.json` 常用字段：

| 字段 | 说明 |
| --- | --- |
| `auth-key` | 管理后台和根 API 密钥 |
| `database_url` | PostgreSQL 连接串 |
| `base_url` | 返回图片 URL 的外部基准地址 |
| `proxy` | 访问上游的代理 |
| `upstream_transport` | 上游传输方式 |
| `image_route_strategy` | 图片账号路由策略 |
| `image_account_concurrency` | 单账号图片并发上限 |
| `image_retention_days` | 本地图片保留天数 |
| `image_max_storage_mb` | 本地图片容量上限，`0` 表示不按容量清理 |
| `image_task_timeout_secs` | 异步图片任务超时时间 |
| `image_task_claim_ttl_secs` | Worker 领取任务租约时间 |
| `image_worker_poll_interval_secs` | Worker 空队列轮询间隔 |
| `auto_remove_invalid_accounts` | 自动清理无效账号 |
| `auto_remove_rate_limited_accounts` | 自动清理限流账号 |
| `cleanup_protect_user_images` | 清理图片时保护普通用户图片 |
| `sensitive_words` | 请求敏感词 |
| `global_system_prompt` | 全局提示词 |
| `cors_allowed_origins` | CORS 白名单 |
| `log_request_text` | 是否记录请求 prompt |

兼容旧部署：如果服务器根目录仍有旧版 `config.json`，Docker 启动时会由 `config-migrate` 服务在 `data/config.json` 不存在时自动迁移；没有旧配置时会用 `config.example.json` 初始化。后台设置之后只写 `data/config.json`。

## API

所有 `/api/*` 管理接口和 `/v1/*` OpenAI 兼容接口都需要鉴权。

支持的鉴权方式：

```http
Authorization: Bearer <key>
```

或：

```http
x-api-key: <key>
```

常用端点：

| 端点 | 说明 |
| --- | --- |
| `GET /api/system/status` | 系统状态、存储类型、传输模式、账号数、任务数 |
| `GET/POST/DELETE /api/accounts` | 账号列表、导入、删除 |
| `POST /api/accounts/refresh` | 刷新账号信息 |
| `GET/POST /api/settings` | 读取和保存设置 |
| `GET /api/images` | 图片管理列表 |
| `POST /api/images/delete` | 删除图片 |
| `POST /api/images/download` | 打包下载图片 |
| `GET /api/image-tasks` | 图片任务列表 |
| `POST /api/image-tasks/generations` | 创建异步文生图任务 |
| `POST /api/image-tasks/edits` | 创建异步编辑任务 |
| `POST /api/image-tasks/cancel` | 取消图片任务 |
| `GET/POST /api/register` | 注册机配置 |
| `POST /api/register/start` | 启动注册任务 |
| `POST /api/register/stop` | 停止注册任务 |
| `POST /api/register/reset` | 重置注册统计 |
| `POST /api/register/repair-abnormal` | 修复异常账号 |
| `GET /api/register/events` | 注册机 SSE 实时事件 |
| `GET /api/logs` | 日志查询 |
| `POST /api/logs/delete` | 删除日志 |
| `GET /v1/models` | OpenAI 兼容模型列表 |
| `POST /v1/images/generations` | OpenAI 兼容文生图 |
| `POST /v1/images/edits` | OpenAI 兼容图片编辑 |
| `POST /v1/chat/completions` | image-only Chat Completions |
| `POST /v1/responses` | image-only Responses |

## 账号导入

账号入口支持两种来源：

- `web`：ChatGPT web 账号，通常只需要 `access_token`
- `codex`：Codex 账号，必须有 `refresh_token`，用于刷新和补全信息

导入流程：

1. 接收 `tokens` 或 `account_records`
2. 写入 `data/accounts.json`
3. 立即刷新导入账号
4. 刷新失败、额度未知、额度为 0、状态不可用或被标记删除时，按无效账号清理逻辑处理
5. 响应返回 `status`、`added`、`skipped`、`write_attempted`、`saved`、`validated_saved`、`refreshed`、`refresh_failed`、`removed_unusable`、`cleanup_removed`、`errors`、`items`

示例：

```bash
curl -sS http://127.0.0.1:3000/api/accounts \
  -H "Authorization: Bearer $GPT2API_IMAGE_AUTH_KEY" \
  -H "Content-Type: application/json" \
  -d '{"source_type":"web","tokens":["access-token-1","access-token-2"]}'
```

Codex 账号示例：

```bash
curl -sS http://127.0.0.1:3000/api/accounts \
  -H "Authorization: Bearer $GPT2API_IMAGE_AUTH_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "source_type": "codex",
    "account_records": [
      {
        "access_token": "access-token",
        "refresh_token": "refresh-token",
        "account_id": "chatgpt-account-id",
        "client_id": "client-id"
      }
    ]
  }'
```

## 注册机

注册机由 Go API 代理管理端请求，再调用 `register-executor` 执行真实注册流程。

当前注册闭环规则：

- 注册流程必须拿到 `access_token`、`refresh_token`、`id_token` 三件套；缺任意 token 会按 `token_exchange_failed` 失败，不写入账号。
- 注册成功拿到 token 后先写入账号，再立即调用 API 刷新账号信息。
- 只有刷新成功、读回状态正常、额度已知且额度大于 0 的账号才计入 `usable_success`。
- 刷新失败、刷新返回错误、读回不可用、额度未知、额度为 0 或状态异常时，会删除刚写入账号并计失败。
- 不保留不可用注册结果；注册机账号成本低，运营口径以“可用账号数”和“可用额度”为准。
- `refresh_failed` 统计刷新失败次数，`token_acquired_refresh_failed` 统计已拿到 token 但保存后刷新失败的次数，`failure_reasons` 统计失败原因。
- `/api/register/events` 提供 SSE 实时状态，前端注册页面会持续显示运行状态和日志。

注册机常用操作：

```bash
curl -sS http://127.0.0.1:3000/api/register \
  -H "Authorization: Bearer $GPT2API_IMAGE_AUTH_KEY"

curl -sS -X POST http://127.0.0.1:3000/api/register/start \
  -H "Authorization: Bearer $GPT2API_IMAGE_AUTH_KEY"

curl -sS -X POST http://127.0.0.1:3000/api/register/stop \
  -H "Authorization: Bearer $GPT2API_IMAGE_AUTH_KEY"

curl -sS -X POST http://127.0.0.1:3000/api/register/repair-abnormal \
  -H "Authorization: Bearer $GPT2API_IMAGE_AUTH_KEY"
```

邮箱域名黑名单相关接口：

```text
GET  /api/register/yyds-domain-blacklist
POST /api/register/yyds-domain-blacklist
POST /api/register/yyds-domain-blacklist/remove
POST /api/register/yyds-domain-blacklist/replace
POST /api/register/yyds-domain-blacklist/reset
```

## 图片任务

无 PostgreSQL 时，任务存储在 `data/image_tasks.json`，适合单进程调试。

配置 `GPT2API_IMAGE_DATABASE_URL` 后，任务写入 PostgreSQL：

- API 进程创建任务、扣费、保存任务输入图
- Worker 进程领取任务并定期心跳
- 任务取消或租约失效时停止保存结果
- 失败或超时时退款并清理输入图
- 部分成功时按缺失数量退款

Worker 模式必须配置 `GPT2API_IMAGE_BASE_URL`，否则生成结果无法返回稳定 URL。

## 图片管理

图片文件保存在 `data/images/YYYY/MM/DD/`。管理后台的“图片管理”页面会读取图片归属、prompt、标签、缩略图和文件信息。

清理策略：

- `image_retention_days`：按时间清理旧图片
- `image_max_storage_mb`：按容量清理最旧图片
- `cleanup_protect_user_images`：清理时保护普通用户图片

删除图片时会同步清理 owner 和 prompt 元数据。

## 测试和检查

Linux/WSL 下常用检查：

```bash
go test ./internal/app ./cmd/...

cd web
corepack enable
pnpm install --frozen-lockfile
pnpm run typecheck
cd ..

python3 -m venv register-executor/.venv
source register-executor/.venv/bin/activate
pip install -r register-executor/requirements.txt
python -m unittest discover -s register-executor/tests -v
python -m py_compile \
  register-executor/app.py \
  register-executor/services/register_service.py \
  register-executor/services/register/openai_register.py \
  register-executor/services/register/proxy_pool.py
```

完整 Go 检查：

```bash
make test
make verify
```

PostgreSQL 集成测试需要设置测试库：

```bash
export GPT2API_IMAGE_TEST_DATABASE_URL="postgresql://user:pass@127.0.0.1:5432/gpt2api_image_test"
bash scripts/run_pg_integration.sh
```

## 打包

构建前端：

```bash
make web
```

打包 Linux 发布包：

```bash
scripts/package_release.sh --web
```

从 GitHub Release 安装或更新：

```bash
bash scripts/install_latest.sh --dir /opt/gpt2api-image
```

从源码构建安装：

```bash
bash scripts/install_latest.sh --dir /opt/gpt2api-image --from-source --web
```

## 排障

查看容器状态：

```bash
docker compose ps
docker compose logs -f api
docker compose logs -f worker
docker compose logs -f register-executor
```

检查系统状态：

```bash
curl -sS http://127.0.0.1:3000/api/system/status \
  -H "Authorization: Bearer $GPT2API_IMAGE_AUTH_KEY"
```

常见问题：

- 启动时报 `auth-key 未设置`：设置 `GPT2API_IMAGE_AUTH_KEY` 或修改 `data/config.json` 的 `auth-key`。
- 注册执行器代理返回 `register_internal_key is required`：设置 `GPT2API_IMAGE_REGISTER_INTERNAL_KEY`，并保证 API 与注册执行器使用同一个内部密钥。
- Worker 启动失败：确认 `GPT2API_IMAGE_DATABASE_URL` 和 `GPT2API_IMAGE_BASE_URL` 已设置。
- 返回图片 URL 不对：检查 `GPT2API_IMAGE_BASE_URL` 是否是外部访问域名。
- 注册页面没有实时状态：检查 `register-executor` 容器是否运行，以及 `/api/register/events` 是否能返回 SSE。
- 注册结果多失败：看注册日志里的 `failure_reasons`，再检查邮箱服务、代理、验证码等待时间和上游账号刷新结果。
- 导入账号后没有可用数：检查 `/api/accounts` 返回的 `refresh_failed` 和 `errors`，以及账号是否额度未知、0 额度或状态异常。
- 上游连接失败：检查 `proxy`、`GPT2API_IMAGE_UPSTREAM_TRANSPORT` 和 `/api/proxy/test`。
- 管理后台白屏：确认前端已构建到 `web_dist/`，或 Docker 镜像构建阶段 `pnpm run build` 成功。

## 安全

- 不要提交 `.env`、`data/config.json`、`data/`、日志、导出账号、密钥或令牌。
- `GPT2API_IMAGE_AUTH_KEY`、注册机内部密钥、邮箱服务密钥和账号 token 必须保存在服务器配置中。
- 注册执行器不要直接暴露公网，只允许 API 容器或内网访问。
- 默认关闭 `GPT2API_IMAGE_LOG_REQUEST_TEXT`，只有排障时再临时开启。
- 反向代理建议启用 HTTPS，并限制管理后台访问来源。
- 备份文件不要放在 Web 可访问目录下。
