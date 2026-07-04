#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

CONTAINER_NAME="${GPT2API_IMAGE_TEST_POSTGRES_CONTAINER:-gpt2api-image-test-postgres}"
POSTGRES_IMAGE="${GPT2API_IMAGE_TEST_POSTGRES_IMAGE:-postgres:16-alpine}"
POSTGRES_DB="${GPT2API_IMAGE_TEST_POSTGRES_DB:-gpt2api_image_test}"
POSTGRES_USER="${GPT2API_IMAGE_TEST_POSTGRES_USER:-gpt2api_image_test}"
POSTGRES_PASSWORD="${GPT2API_IMAGE_TEST_POSTGRES_PASSWORD:-gpt2api_image_test}"
POSTGRES_PORT="${GPT2API_IMAGE_TEST_POSTGRES_PORT:-55432}"
KEEP_POSTGRES="${GPT2API_IMAGE_TEST_KEEP_POSTGRES:-0}"

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "Missing required command: $1" >&2
    exit 1
  }
}

cleanup() {
  if [[ "$KEEP_POSTGRES" != "1" ]]; then
    docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
  fi
}

need_cmd go

if [[ -n "${GPT2API_IMAGE_TEST_DATABASE_URL:-}" ]]; then
  echo "Using existing GPT2API_IMAGE_TEST_DATABASE_URL."
  go test ./internal/app -run TestPGTaskStoreIntegrationAsyncLifecycle -count=1 -v
  echo "PostgreSQL integration test passed."
  exit 0
fi

need_cmd docker

if ! docker info >/dev/null 2>&1; then
  echo "Docker daemon is not available. Set GPT2API_IMAGE_TEST_DATABASE_URL to use an existing PostgreSQL database, or run this script from a Docker-enabled shell." >&2
  exit 1
fi

if [[ "$KEEP_POSTGRES" != "1" ]]; then
  trap cleanup EXIT
fi

docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true

docker run -d \
  --name "$CONTAINER_NAME" \
  -e POSTGRES_DB="$POSTGRES_DB" \
  -e POSTGRES_USER="$POSTGRES_USER" \
  -e POSTGRES_PASSWORD="$POSTGRES_PASSWORD" \
  -p "127.0.0.1:${POSTGRES_PORT}:5432" \
  "$POSTGRES_IMAGE" >/dev/null

echo "Waiting for PostgreSQL container: $CONTAINER_NAME"
for _ in $(seq 1 60); do
  if docker exec "$CONTAINER_NAME" pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

if ! docker exec "$CONTAINER_NAME" pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB" >/dev/null 2>&1; then
  echo "PostgreSQL did not become ready in time." >&2
  exit 1
fi

export GPT2API_IMAGE_TEST_DATABASE_URL="postgresql://${POSTGRES_USER}:${POSTGRES_PASSWORD}@127.0.0.1:${POSTGRES_PORT}/${POSTGRES_DB}?sslmode=disable"
go test ./internal/app -run TestPGTaskStoreIntegrationAsyncLifecycle -count=1 -v

echo "PostgreSQL integration test passed."
