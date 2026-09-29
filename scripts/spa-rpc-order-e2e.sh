#!/usr/bin/env bash
#
# scripts/spa-rpc-order-e2e.sh — browser e2e: the Sky.Spa (web:app) client runs
# every Msg's `update` once, in arrival order, with its Cmds, as Sky.Live does,
# and runs independent server RPCs together.
#
# Builds rust/crates/sky/tests/fixtures/spa-rpc-order twice (--target web:app
# and the Sky.Live web target) and drives both in the same browsers under
# SKY_CSP=strict (scripts/spa-rpc-order-verify.mjs):
#
#   seal   a client arm that spends a single-use Noise state during a server
#          RPC runs once (the old client re-ran it after the RPC and failed:
#          "this state value was already used")
#   timer  a timer that starts a call only when none runs keeps calling (the
#          old client re-ran the ticks after the result without their Cmds and
#          stalled with busy = True)
#   pair   two server Msgs are in flight together (one delay, not two)
#
# Browsers: SKY_E2E_BROWSERS (default "chromium,webkit"); SKY_E2E_CHANNEL=chrome
# uses Google Chrome; SKY_E2E_HEADED=1 runs them headed.
#
# Prereqs (all fail loudly): a fresh sky-out/sky, go, node + playwright.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"
if [ ! -x "$SKY" ]; then
  echo "spa-rpc-order-e2e: $SKY not found — run ./scripts/build.sh first." >&2
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
TMP="$(gate_e2e_dir "$ROOT" spa-rpc-order-e2e)"
rm -rf "$TMP"
mkdir -p "$TMP/app"
cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/spa-rpc-order/." "$TMP/app/"

echo "==> building fixture spa-rpc-order (--target web:app)"
(cd "$TMP/app" && with_timeout 1200 "$SKY" build --target web:app src/Main.sky) >"$TMP/build-spa.log" 2>&1 \
  || { cat "$TMP/build-spa.log" >&2; echo "spa-rpc-order-e2e: web:app build failed" >&2; exit 1; }
echo "==> building fixture spa-rpc-order (Sky.Live, --target web)"
(cd "$TMP/app" && with_timeout 1200 "$SKY" build --target web src/Main.sky) >"$TMP/build-live.log" 2>&1 \
  || { cat "$TMP/build-live.log" >&2; echo "spa-rpc-order-e2e: web build failed" >&2; exit 1; }
SPA="$TMP/app/.skyapp/web-app/.split/backend/sky-out/app"
LIVE="$TMP/app/.skyapp/web/sky-out/app"
[ -x "$SPA" ] || { echo "spa-rpc-order-e2e: backend not built at $SPA" >&2; exit 1; }
[ -x "$LIVE" ] || { echo "spa-rpc-order-e2e: Live app not built at $LIVE" >&2; exit 1; }

echo "==> driving both targets in a browser"
SKY_E2E_BROWSERS="${SKY_E2E_BROWSERS:-chromium,webkit}" \
  with_timeout 600 node "$ROOT/scripts/spa-rpc-order-verify.mjs" "$SPA" "$LIVE" --port "${RPC_ORDER_PORT:-9361}"

echo "spa-rpc-order-e2e: PASS — web:app runs each Msg once, in order, with its Cmds, as Sky.Live does."
rm -rf "$TMP"
