#!/usr/bin/env bash
# Build + run examples/56-reactive-todos and drive the two-session reactive proof
# (scripts/verify-reactive-todos.mjs). Exits non-zero on failure.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
APP_DIR="$ROOT/examples/56-reactive-todos"
PORT="${REACTIVE_PORT:-8129}"
SKY="${SKY_BIN:-sky}"

cd "$APP_DIR"
rm -rf data sky-out .skycache .skydeps
mkdir -p data
"$SKY" build src/Main.sky >/dev/null

SKY_LIVE_PORT="$PORT" ./sky-out/app >/tmp/reactive-todos-app.log 2>&1 &
APP_PID=$!
cleanup() { kill -9 "$APP_PID" 2>/dev/null || true; }
trap cleanup EXIT

# wait for boot
for _ in $(seq 1 30); do
    if curl -sf "http://localhost:$PORT/" >/dev/null 2>&1; then break; fi
    sleep 0.2
done

REACTIVE_PORT="$PORT" node "$ROOT/scripts/verify-reactive-todos.mjs"
