#!/usr/bin/env bash
#
# scripts/header-session-e2e.sh — browser e2e for Sky.Live without cookies:
# the header session transport (App.withSessionTransport HeaderToken,
# runtime-go/rt/live_session_header.go).
#
# Builds the header-session fixture (--target web) from a temp copy and drives
# it in headless Chromium whose profile BLOCKS ALL COOKIES, with
# SKY_CSP=strict. scripts/header-session-verify.mjs has the case list: the
# counter works over event POSTs, server pushes arrive over the fetch-stream
# SSE, a dropped stream reconnects with the state intact, sign-in rotates the
# session and the tab adopts the new token (Live.sessionKey unchanged, the old
# token refused), the one-time SSE ticket fallback works without streaming
# fetch, and there are zero policy violations, console errors or cookies.
#
# Proven to FAIL before the header transport existed (the fixture's builder
# is unknown to the compiler; with SKY_LIVE_SESSION_TRANSPORT ignored the
# cookie-blocked browser loses its session on every request) and to PASS on
# the fixed runtime.
#
# Prereqs (all fail loudly): a fresh sky-out/sky, go, node + playwright (the
# full Chromium from `npx playwright install chromium`).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"
if [ ! -x "$SKY" ]; then
  echo "header-session-e2e: $SKY not found — run ./scripts/build.sh first." >&2
  exit 1
fi
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"
source "$ROOT/scripts/lib/with-timeout.sh"
command -v node >/dev/null 2>&1 || { echo "header-session-e2e: 'node' is required." >&2; exit 1; }
command -v go >/dev/null 2>&1 || { echo "header-session-e2e: 'go' is required." >&2; exit 1; }

# A stable fixture directory, emptied first: the shared gate build cache
# (scripts/lib/gate-build-cache.sh) keys on the project path.
source "$ROOT/scripts/lib/gate-build-cache.sh"
FX="$(gate_e2e_dir "$ROOT" header-session)"
rm -rf "$FX"
mkdir -p "$FX"
cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/header-session/." "$FX/"

echo "==> building the header-session fixture (--target web)"
with_timeout 1200 bash "$ROOT/scripts/lib/gate-build-cache.sh" build "$SKY" "$FX" \
  --clean --artefact .skyapp/web -- build --target web src/Main.sky

APP="$FX/.skyapp/web/sky-out/app"
[ -x "$APP" ] || { echo "header-session-e2e: app not built at $APP" >&2; exit 1; }

echo "==> driving Sky.Live with cookies blocked"
with_timeout 300 node "$ROOT/scripts/header-session-verify.mjs" "$APP" \
  --port "${HEADER_SESSION_E2E_PORT:-9580}" --cwd "$FX"

echo "header-session-e2e: PASS — a Sky.Live app runs, streams, reconnects and rotates its session in a browser that keeps no cookies, under a strict Content-Security-Policy."
rm -rf "$FX"
