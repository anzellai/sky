#!/usr/bin/env bash
#
# scripts/spa-resilience-e2e.sh — browser e2e: the Sky.Spa (web:app) client
# handles offline, sleep, a hidden page and an overloaded server by itself, so
# the app writes no network code (runtime-go/rt/spa_retry.go, spa_tick.go).
#
# Builds rust/crates/sky/tests/fixtures/spa-resilience (--target web:app) and
# drives it under SKY_CSP=strict (scripts/spa-resilience-verify.mjs):
#
#   fast stage (Chromium + WebKit)
#     blip       a 2 s server outage shows nothing; the click runs once
#     offline    offline: "Reconnecting…" after 3 s (within 10 s), never the red bar;
#                queued clicks run once each, in order, when back online
#     pushback   a 503 with Retry-After: 2 is re-sent 2 s later
#     hidden     a client-only Sub.every keeps ticking while hidden; a poll
#                sends at most its first call while hidden, one on return
#     resume     an RPC on the wire while the page is frozen / hidden / in the
#                back/forward cache for minutes (page.clock) is re-sent at once
#                on resume and runs once; App.withRpcError is never called; an
#                outage while hidden gives nothing up (v0.27.6)
#   slow stage (Chromium; waits out the real budgets)
#     timeout    a hung request is aborted at 30 s and re-sent (same id)
#     exhausted  the red bar and App.withRpcError (once) only after 60 s
#
# Browsers: SKY_E2E_BROWSERS (default "chromium,webkit"); SKY_E2E_CHANNEL=chrome
# uses Google Chrome; SKY_E2E_HEADED=1 runs them headed.
# SPA_RESILIENCE_STAGES (default "fast slow") picks the stages to run.
#
# Prereqs (all fail loudly): a fresh sky-out/sky, go, node + playwright.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"
if [ ! -x "$SKY" ]; then
  echo "spa-resilience-e2e: $SKY not found — run ./scripts/build.sh first." >&2
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
TMP="$(gate_e2e_dir "$ROOT" spa-resilience-e2e)"
rm -rf "$TMP"
mkdir -p "$TMP/app"
cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/spa-resilience/." "$TMP/app/"

echo "==> building fixture spa-resilience (--target web:app)"
(cd "$TMP/app" && with_timeout 1200 "$SKY" build --target web:app src/Main.sky) >"$TMP/build.log" 2>&1 \
  || { cat "$TMP/build.log" >&2; echo "spa-resilience-e2e: web:app build failed" >&2; exit 1; }
SPA="$TMP/app/.skyapp/web-app/.split/backend/sky-out/app"
[ -x "$SPA" ] || { echo "spa-resilience-e2e: backend not built at $SPA" >&2; exit 1; }

STAGES="${SPA_RESILIENCE_STAGES:-fast slow}"
for stage in $STAGES; do
  case "$stage" in
    fast) echo "==> fast stage: blip, offline, pushback, hidden page, resume after a freeze" ;;
    slow) echo "==> slow stage: 30 s timeout, 60 s budget (Chromium)" ;;
    *) echo "spa-resilience-e2e: unknown stage $stage (fast | slow)" >&2; exit 1 ;;
  esac
  SKY_E2E_BROWSERS="${SKY_E2E_BROWSERS:-chromium,webkit}" \
    with_timeout 480 node "$ROOT/scripts/spa-resilience-verify.mjs" "$SPA" --stage "$stage" --port "${SPA_RESILIENCE_PORT:-9369}"
done

echo "spa-resilience-e2e: PASS — transient failures are retried by the runtime, a blip shows nothing,"
echo "  an outage shows a quiet indicator, queued clicks run once in order, a hidden page polls once,"
echo "  and only a FINAL error reaches the red bar and App.withRpcError."
rm -rf "$TMP"
