#!/usr/bin/env bash
#
# scripts/console-live-e2e.sh — browser e2e: the Sky Console shows LIVE data and
# keeps its live channel, for a Sky.Live app AND a Sky.Spa (web:app) backend,
# directly AND behind a real Caddy (HTTPS + HTTP/2, local_certs, encode zstd
# gzip, reverse_proxy flush_interval -1 / read_timeout 2m / active health checks
# / two slots with lb_policy first, strict CSP script-src 'self'
# 'wasm-unsafe-eval').
#
# Field report behind it (v0.25.17 – v0.25.19, production behind Caddy):
#   * the console's data never updated: "Sky — · dev · uptime 0s", every panel
#     empty (its reads were refused 401 — the internal token was never sent);
#   * on a Sky.Spa backend the console's SSE was cut every 30 s (Server.listen's
#     WriteTimeout), Caddy logged "aborting with incomplete response …
#     unexpected EOF", the browser showed ERR_HTTP2_PROTOCOL_ERROR and the page
#     sat on "Reconnecting".
# scripts/verify-console-e2e.mjs passed through all of it: it checks the tabs
# render and the JSON APIs answer, never that a value in the console changes.
#
# scripts/console-live-e2e.mjs asserts, per app × topology: sign-in, a live
# header whose uptime counts up, requests > 0 after traffic, the stamped commit
# and build time, a log line, a span, 40 s with no "Reconnecting" and no failed
# SSE, then a backend restart the console recovers from by itself, zero console
# errors with the backend up, zero CSP violations. The analytics-* scenarios
# (fixture rust/crates/sky/tests/fixtures/console-analytics) add: a visitor
# signs up, and the Analytics tab lists the tracked event and counts an
# identified user.
#
# Proven to FAIL on v0.25.19 (CONSOLE_LIVE_E2E_SKY=<a v0.25.19 sky>) and PASS on
# the fixed tree.
#
# Prereqs (all fail loudly): a fresh sky-out/sky, go, node + playwright, caddy
# (CADDY=<path>, else `caddy` on PATH).
#
# Env:
#   CONSOLE_LIVE_E2E_PORT  first of 20 ports used (default 9620)
#   CONSOLE_LIVE_E2E_SKY   measure another compiler (e.g. a released one, to
#                          prove the gate fails there); default sky-out/sky
#   CONSOLE_LIVE_E2E_ONLY  run one scenario: live-direct | live-caddy |
#                          spa-direct | spa-caddy | analytics-direct |
#                          analytics-caddy
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
source "$ROOT/scripts/lib/with-timeout.sh"
source "$ROOT/scripts/lib/require-tool.sh"

if [ -n "${CONSOLE_LIVE_E2E_SKY:-}" ]; then
  SKY="$CONSOLE_LIVE_E2E_SKY"
  echo "console-live-e2e: MEASURING $SKY ($("$SKY" --version 2>/dev/null || echo '?')), NOT this tree's compiler" >&2
else
  SKY="$ROOT/sky-out/sky"
  if [ ! -x "$SKY" ]; then
    echo "console-live-e2e: $SKY not found — run ./scripts/build.sh first." >&2
    exit 1
  fi
  source "$ROOT/scripts/lib/fresh-compiler.sh"
  require_fresh_compiler "$SKY" "$ROOT"
fi
require_tool node "install Node.js (and 'npm ci' for playwright)"
require_tool go "install Go (https://go.dev/dl/)"
CADDY="${CADDY:-$(command -v caddy || true)}"
if [ -z "$CADDY" ] || [ ! -x "$CADDY" ]; then
  echo "console-live-e2e: caddy is required (set CADDY=<path> or put caddy on PATH; https://caddyserver.com/download)." >&2
  exit 1
fi

source "$ROOT/scripts/lib/gate-build-cache.sh"
_gc_compiler_hash "$SKY" >/dev/null
TMP="$(gate_e2e_dir "$ROOT" console-live-e2e)"
BASE_PORT="${CONSOLE_LIVE_E2E_PORT:-9620}"
ONLY="${CONSOLE_LIVE_E2E_ONLY:-}"
# The build identity the console must show. Sky.Live and the analytics fixture
# pin it with the optional overrides (SKY_BUILD_COMMIT, and SKY_BUILD_EPOCH,
# 1790000000 = 2026-09-21T14:13:20Z). The Sky.Spa app takes the AUTOMATIC path
# with no override, the way a real deploy does: its sources carry the commit
# time as mtime (as `git archive | tar -x` leaves them), there is no `.git`,
# and the CI commit variable GitHub Actions always sets names the commit.
OVERRIDE_COMMIT="e2e0c0ffee01"
AUTO_SHA="a11ce0ffee0123456789abcdef0123456789abcd"
BUILT_AT="2026-09-21T14:13:20Z"

build_target() { # build_target <example-or-fixture> <target> <artefact> <override|auto>
  local name="$1" target="$2" artefact="$3" stamp="$4"
  local src="$ROOT/examples/$name"
  [ -d "$src" ] || src="$ROOT/rust/crates/sky/tests/fixtures/$name"
  rm -rf "$TMP/$name"
  mkdir -p "$TMP/$name"
  cp -Rf "$src/." "$TMP/$name/"
  rm -rf "$TMP/$name/.skyapp" "$TMP/$name/sky-out" "$TMP/$name/.skycache"
  local stamp_env=(-u SKY_BUILD_COMMIT -u SKY_BUILD_EPOCH -u SKY_BUILD_STAMP_PINNED)
  if [ "$stamp" = override ]; then
    stamp_env+=(SKY_BUILD_COMMIT="$OVERRIDE_COMMIT" SKY_BUILD_EPOCH=1790000000)
  else
    find "$TMP/$name" -type f -exec env TZ=UTC touch -t 202609211413.20 {} +
    stamp_env+=(GITHUB_SHA="$AUTO_SHA" GIT_CEILING_DIRECTORIES="$(dirname "$TMP")")
  fi
  echo "==> building ${src#"$ROOT"/} (--target $target, build identity: $stamp)"
  with_timeout 1500 env "${stamp_env[@]}" bash "$ROOT/scripts/lib/gate-build-cache.sh" build "$SKY" "$TMP/$name" \
    --clean --artefact "$artefact" -- build --target "$target" src/Main.sky
}

want() { [ -z "$ONLY" ] || [ "$ONLY" = "$1" ]; }
if want live-direct || want live-caddy; then
  build_target 09-live-counter web .skyapp/web override
fi
if want spa-direct || want spa-caddy; then
  build_target 62-app-notes web:app .skyapp/web-app auto
fi
# The Analytics tab needs an app that tracks identified events; no example
# does that on a scripted gesture, so a fixture does (console-analytics).
if want analytics-direct || want analytics-caddy; then
  build_target console-analytics web .skyapp/web override
fi
LIVE_BIN="$TMP/09-live-counter/.skyapp/web/sky-out/app"
SPA_BIN="$TMP/62-app-notes/.skyapp/web-app/.split/backend/sky-out/app"
ANALYTICS_BIN="$TMP/console-analytics/.skyapp/web/sky-out/app"

rc=0
drive() { # drive <scenario> <binary> <cwd> <port> <direct|caddy> [driver args...]
  local scenario="$1" bin="$2" cwd="$3" port="$4" via="$5"
  shift 5
  want "$scenario" || return 0
  [ -x "$bin" ] || { echo "console-live-e2e: app not built at $bin" >&2; rc=1; return 0; }
  echo "==> $scenario"
  local commit="$OVERRIDE_COMMIT"
  case "$scenario" in spa-*) commit="${AUTO_SHA:0:12}" ;; esac
  local args=(--app "$bin" --name "$scenario" --port "$port" --cwd "$cwd" --commit "$commit" --built-at "$BUILT_AT")
  [ "$via" = caddy ] && args+=(--caddy "$CADDY" --caddy-port $((port + 5)))
  with_timeout 300 node "$ROOT/scripts/console-live-e2e.mjs" "${args[@]}" "$@" || rc=1
}
drive live-direct "$LIVE_BIN" "$(dirname "$LIVE_BIN")" "$BASE_PORT" direct
drive live-caddy "$LIVE_BIN" "$(dirname "$LIVE_BIN")" $((BASE_PORT + 10)) caddy
drive spa-direct "$SPA_BIN" "$(dirname "$SPA_BIN")" "$BASE_PORT" direct
drive spa-caddy "$SPA_BIN" "$(dirname "$SPA_BIN")" $((BASE_PORT + 10)) caddy
drive analytics-direct "$ANALYTICS_BIN" "$(dirname "$ANALYTICS_BIN")" "$BASE_PORT" direct --analytics
drive analytics-caddy "$ANALYTICS_BIN" "$(dirname "$ANALYTICS_BIN")" $((BASE_PORT + 10)) caddy --analytics

if [ "$rc" -ne 0 ]; then
  echo "console-live-e2e: FAIL — the Sky Console did not show live data, lost its live channel, or did not recover from a restart (see above)." >&2
  exit 1
fi
echo "console-live-e2e: PASS — the Sky Console shows live data (incl. Analytics), holds its live channel past 40 s and recovers from a backend restart by itself, for Sky.Live, Sky.Spa and an analytics fixture, directly and behind Caddy (HTTP/2, encode, strict CSP)."
rm -rf "$TMP"
