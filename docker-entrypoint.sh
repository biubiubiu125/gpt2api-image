#!/bin/sh
set -eu

CONFIG_FILE="${GPT2API_IMAGE_CONFIG_FILE:-/app/data/config.json}"

case "$CONFIG_FILE" in
  /*) ;;
  *) CONFIG_FILE="/app/$CONFIG_FILE" ;;
esac

CONFIG_DIR="$(dirname "$CONFIG_FILE")"
mkdir -p /app/data "$CONFIG_DIR"

if [ ! -f "$CONFIG_FILE" ]; then
  if [ -f /app/config.json ] && [ "$CONFIG_FILE" != "/app/config.json" ]; then
    cp /app/config.json "$CONFIG_FILE"
  elif [ -f /app/config.example.json ]; then
    cp /app/config.example.json "$CONFIG_FILE"
  fi
fi

chown -R app:app /app/data 2>/dev/null || true
if [ "$CONFIG_DIR" != "/" ]; then
  chown app:app "$CONFIG_DIR" 2>/dev/null || true
fi
if [ -f "$CONFIG_FILE" ]; then
  chown app:app "$CONFIG_FILE" 2>/dev/null || true
fi

exec su-exec app "$@"
