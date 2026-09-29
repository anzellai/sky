#!/usr/bin/env bash
#
# scripts/spa-websocket-e2e.sh — browser e2e: a Sky.Spa (web:app) client holds
# its own WebSocket (Sky.Core.WebSocket over the browser WebSocket API,
# runtime-go/rt/websocket_wasm.go) to its backend, which serves it with
# `Sky.Http.Server.WebSocket.upgrade` mounted by `App.api`.
#
# Builds rust/crates/sky/tests/fixtures/spa-websocket (--target web:app) and
# drives it under SKY_CSP=strict (scripts/spa-websocket-verify.mjs): text and
# binary frames through a Sub-read socket, and a Task-read socket.
#
# Browsers: SKY_E2E_BROWSERS (default "chromium,webkit"); SKY_E2E_CHANNEL=chrome
# uses Google Chrome; SKY_E2E_HEADED=1 runs them headed.
#
# Prereqs (all fail loudly): a fresh sky-out/sky, go, node + playwright.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"
if [ ! -x "$SKY" ]; then
  echo "spa-websocket-e2e: $SKY not found — run ./scripts/build.sh first." >&2
  exit 1
fi
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"
source "$ROOT/scripts/lib/with-timeout.sh"
source "$ROOT/scripts/lib/require-tool.sh"
require_tool node "install Node.js 20+"
require_tool go "install Go 1.25+"

source "$ROOT/scripts/lib/gate-build-cache.sh"
_gc_compiler_hash "$SKY" >/dev/null
TMP="$(gate_e2e_dir "$ROOT" spa-websocket-e2e)"
rm -rf "$TMP"
mkdir -p "$TMP/app"
cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/spa-websocket/." "$TMP/app/"

echo "==> building fixture spa-websocket (--target web:app)"
(cd "$TMP/app" && with_timeout 1200 "$SKY" build --target web:app src/Main.sky) >"$TMP/build.log" 2>&1 \
  || { cat "$TMP/build.log" >&2; echo "spa-websocket-e2e: build failed" >&2; exit 1; }
APP="$TMP/app/.skyapp/web-app/.split/backend/sky-out/app"
[ -x "$APP" ] || { echo "spa-websocket-e2e: backend not built at $APP" >&2; exit 1; }

echo "==> driving it in a browser"
SKY_E2E_BROWSERS="${SKY_E2E_BROWSERS:-chromium,webkit}" \
  with_timeout 300 node "$ROOT/scripts/spa-websocket-verify.mjs" "$APP" --port "${SPA_WS_PORT:-9371}"

echo "spa-websocket-e2e: PASS — the web:app client exchanged text and binary frames over its own WebSocket."
rm -rf "$TMP"
