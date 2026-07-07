#!/bin/bash
set -e

# =============================================================
# gpt2api-image  启动脚本
# =============================================================
# 用法:
#   ./start.sh              启动（自动构建+运行）
#   ./start.sh --build      强制重新构建
#   ./start.sh --curl       强制使用 curl-impersonate 模式
#   ./start.sh --tls        强制使用 tls-client 纯 Go 模式（默认）
#   ./start.sh --port 8080  自定义端口
#   ./start.sh --mode all   单进程启动 API + Worker
#   ./start.sh --worker     仅启动 Worker
#   ./start.sh --web        仅构建前端
#   ./start.sh --help       显示帮助
# =============================================================

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

# ---------- 默认值 ----------
PORT="${GPT2API_IMAGE_ADDR:-3000}"
PORT="${PORT#:}"
TRANSPORT="${GPT2API_IMAGE_UPSTREAM_TRANSPORT:-tls-client}"
MODE="${GPT2API_IMAGE_MODE:-serve}"
BUILD_FRONTEND=false
WEB_ONLY=false
FORCE_BUILD=false
SHOW_HELP=false

# ---------- 解析参数 ----------
while [[ $# -gt 0 ]]; do
    case "$1" in
        --build)   FORCE_BUILD=true; shift ;;
        --curl)    TRANSPORT="curl-impersonate"; shift ;;
        --tls)     TRANSPORT="tls-client"; shift ;;
        --port)    PORT="$2"; shift 2 ;;
        --mode)    MODE="$2"; shift 2 ;;
        --serve)   MODE="serve"; shift ;;
        --worker)  MODE="worker"; shift ;;
        --all)     MODE="all"; shift ;;
        --web)     BUILD_FRONTEND=true; WEB_ONLY=true; FORCE_BUILD=true; shift ;;
        --help|-h) SHOW_HELP=true; shift ;;
        *)         echo "未知参数: $1"; exit 1 ;;
    esac
done

if $SHOW_HELP; then
    sed -n '/^# ============/,/^# ============/p' "$0" | grep '^#' | head -n -1
    exit 0
fi

run_pnpm() {
    if command -v pnpm >/dev/null 2>&1 && pnpm "$@"; then
        return 0
    fi
    if command -v corepack >/dev/null 2>&1; then
        corepack enable >/dev/null 2>&1 || true
        if corepack pnpm "$@"; then
            return 0
        fi
    fi
    if command -v npx >/dev/null 2>&1 && npx --yes pnpm@10.22.0 "$@"; then
        return 0
    fi
    return 1
}

# ---------- 构建 Go ----------
BIN="$SCRIPT_DIR/bin/gpt2api-image"
if [ ! -x "$BIN" ] || $FORCE_BUILD; then
    echo ">>> 构建 Go 后端..."
    mkdir -p bin
    CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w" -o "$BIN" ./cmd/server
    echo ">>> 构建完成: $BIN"
fi

# ---------- 构建前端 ----------
if $BUILD_FRONTEND; then
    if [ -d "$SCRIPT_DIR/web" ] && [ -f "$SCRIPT_DIR/web/package.json" ]; then
        echo ">>> 构建前端..."
        cd "$SCRIPT_DIR/web"
        if [ ! -d "node_modules" ]; then
            if ! run_pnpm install --frozen-lockfile; then
                echo "!!! 缺少 pnpm/corepack，无法按 pnpm-lock.yaml 构建前端"
                exit 1
            fi
        fi
        if ! run_pnpm run build; then
            echo "!!! 前端构建失败"
            exit 1
        fi
        rm -rf "$SCRIPT_DIR/web_dist"
        cp -R out "$SCRIPT_DIR/web_dist"
        cd "$SCRIPT_DIR"
        echo ">>> 前端构建完成"
        if $WEB_ONLY; then
            exit 0
        fi
    else
        echo "!!! web/ 目录不存在，跳过前端构建"
        echo "!!! 确保 web_dist/ 已存在，或运行后端后无前端页面"
    fi
fi

# ---------- 环境变量 ----------
mkdir -p "$SCRIPT_DIR/data"
if [ -z "${GPT2API_IMAGE_CONFIG_FILE:-}" ]; then
    export GPT2API_IMAGE_CONFIG_FILE="$SCRIPT_DIR/data/config.json"
fi
if [ ! -f "$GPT2API_IMAGE_CONFIG_FILE" ]; then
    mkdir -p "$(dirname "$GPT2API_IMAGE_CONFIG_FILE")"
    if [ -f "$SCRIPT_DIR/config.json" ]; then
        cp "$SCRIPT_DIR/config.json" "$GPT2API_IMAGE_CONFIG_FILE"
    elif [ -f "$SCRIPT_DIR/config.example.json" ]; then
        cp "$SCRIPT_DIR/config.example.json" "$GPT2API_IMAGE_CONFIG_FILE"
    fi
fi
export GPT2API_IMAGE_ADDR=":$PORT"
export GPT2API_IMAGE_UPSTREAM_TRANSPORT="$TRANSPORT"
export GPT2API_IMAGE_MODE="$MODE"

# curl-impersonate 自动下载
if [ "$TRANSPORT" = "curl-impersonate" ]; then
    export GPT2API_IMAGE_CURL_IMPERSONATE_AUTO_DOWNLOAD=1
    echo ">>> 传输模式: curl-impersonate（自动下载）"
else
    echo ">>> 传输模式: tls-client（纯 Go）"
fi

# 前端静态目录
if [ -d "$SCRIPT_DIR/web_dist" ]; then
    echo ">>> 前端: web_dist/ 存在"
else
    echo ">>> 前端: web_dist/ 不存在，仅 API 模式"
fi

# ---------- 启动 ----------
echo ">>> 启动服务: http://127.0.0.1:$PORT"
echo ">>> 管理面板: http://127.0.0.1:$PORT"
echo ">>> API 地址: http://127.0.0.1:$PORT/v1"
echo ">>> 运行模式: $MODE"
echo ">>> 停止方式: Ctrl+C"
echo ">>> Config file: $GPT2API_IMAGE_CONFIG_FILE"
echo ""

exec "$BIN" "$MODE"
