# gpt2api-image 重写说明

本项目以 `jwbb903/ChatGPT2API-GO` 为底座，重写为图片 API 专用项目 `gpt2api-image`。

## 本次重写范围

保留：

- Go 后端入口：`cmd/server/main.go`
- Next.js 静态前端：`web/`
- OpenAI 兼容图片接口：`/v1/images/generations`、`/v1/images/edits`
- 模型列表接口：`/v1/models`
- PostgreSQL 异步图片任务队列
- Worker 后台消费任务
- 账号池、用户密钥、设置、日志、图片管理
- 注册机配置页和 API

移除主链路：

- 文本聊天 API
- Anthropic 兼容 API
- 搜索
- PPT 生成
- PSD 生成
- editable file 下载
- 调试页
- Cloudflare R2 自动备份
- CPA/Sub2API 远程导入

## 运行模式

服务支持 3 种模式：

- `serve`：只启动 API 和 Web 面板
- `worker`：只消费异步图片任务
- `all`：API 和 Worker 在同一进程启动，适合本地开发

Docker Compose 默认启动 3 个容器：

- `gpt2api-image-postgres`
- `gpt2api-image-api`
- `gpt2api-image-worker-1`

生产环境建议保持 API 和 Worker 分离。并发压力上来后，优先增加 Worker 副本：

```bash
docker compose up -d --scale worker=3
```

## 注册机状态

注册机页面和配置结构已经保留，但真实 Go 自动注册执行器尚未迁移完成。

当前实现不会假装注册成功：触发开始注册或修复异常时，会记录失败事件并提示需要接入真实执行器。上线初期建议先手动导入账号，或者把原注册器作为独立服务接进来。

## 本地验证

后端：

```bash
go test ./cmd/... ./internal/...
```

前端：

```bash
cd web
pnpm install --frozen-lockfile
pnpm run build
```

Compose：

```bash
cp .env.example .env
docker compose up -d --build
docker compose logs -f api worker
```
