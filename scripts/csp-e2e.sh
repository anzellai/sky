#!/usr/bin/env bash
#
# scripts/csp-e2e.sh — browser e2e: every page Sky serves works under a strict
# Content-Security-Policy with NO inline executable script:
#
#   default-src 'self'; script-src 'self' 'wasm-unsafe-eval';
#   style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self'
#
# A real deployment put that policy on a reverse proxy and the Sky Console went
# dead ("Executing inline script violates … script-src"): the Sky.Live client
# was inlined into the page, and the Sky.Spa wasm loader was an inline script.
#
# Each app is driven twice by scripts/csp-e2e-verify.mjs:
#   * --via proxy   a tiny proxy in front of the app sets exactly that header;
#   * --via strict  no proxy; the app runs with SKY_CSP=strict and sends a
#                   strict policy itself.
# Both passes assert ZERO securitypolicyviolation events and a working app:
#
#   console  the Sky Console, all six tabs     (examples/09-live-counter host)
#   counter  Sky.Live: SSE tick, click, nav    (examples/09-live-counter)
#   forum    Std.Ui form submit with CSRF      (examples/19-skyforum)
#   todos    Sky.Spa wasm boot + RPC           (examples/60-spa-todos)
#   notes    Sky.Spa SSR hydrate + RPC         (examples/62-app-notes)
#
# Then notes once more in the deployment layout (--via slot): the backend runs
# from a slot directory with no ../frontend/dist, and the proxy serves only
# *.wasm + /wasm_exec.js from the dist. The client must boot with zero console
# errors (v0.25.19 answered /spa-boot.<hash>.js with HTML there).
#
# Proven to FAIL on the pre-fix runtime (every scenario reports a script-src
# violation and a dead page) and PASS on the fixed one.
#
# Prereqs (all fail loudly): a fresh sky-out/sky, go, node + playwright.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"
if [ ! -x "$SKY" ]; then
  echo "csp-e2e: $SKY not found — run ./scripts/build.sh first." >&2
  exit 1
fi
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"
source "$ROOT/scripts/lib/with-timeout.sh"
command -v node >/dev/null 2>&1 || { echo "csp-e2e: 'node' is required." >&2; exit 1; }
command -v go >/dev/null 2>&1 || { echo "csp-e2e: 'go' is required." >&2; exit 1; }

# Projects build in STABLE per-worktree directories through the shared gate
# build cache (scripts/lib/gate-build-cache.sh), which keys on the project path.
source "$ROOT/scripts/lib/gate-build-cache.sh"
_gc_compiler_hash "$SKY" >/dev/null
TMP="$(gate_e2e_dir "$ROOT" csp-e2e)"
BASE_PORT="${CSP_E2E_PORT:-9520}"

stage_example() { # stage_example <example-dir-name>
  local name="$1"
  rm -rf "$TMP/$name"
  mkdir -p "$TMP/$name"
  cp -Rf "$ROOT/examples/$name/." "$TMP/$name/"
  rm -rf "$TMP/$name/.skyapp" "$TMP/$name/sky-out" "$TMP/$name/.skycache"
}

build_target() { # build_target <example> <target> <artefact>
  local name="$1" target="$2" artefact="$3"
  stage_example "$name"
  echo "==> building examples/$name (--target $target)"
  with_timeout 1200 bash "$ROOT/scripts/lib/gate-build-cache.sh" build "$SKY" "$TMP/$name" \
    --clean --artefact "$artefact" -- build --target "$target" src/Main.sky
}

build_target 09-live-counter web .skyapp/web
build_target 19-skyforum web .skyapp/web
build_target 62-app-notes web:app .skyapp/web-app

# 60-spa-todos is a MANUAL split (client/ + server/ + public/), built the way
# its run.sh builds it: the backend, then the raw wasm client, then its assets
# published into public/ next to the hand-written index.html + boot.js.
stage_example 60-spa-todos
echo "==> building examples/60-spa-todos (manual split)"
(
  cd "$TMP/60-spa-todos/server" && with_timeout 1200 "$SKY" build src/Main.sky >/dev/null
)
(
  cd "$TMP/60-spa-todos/client" && with_timeout 1200 "$SKY" build --wasm src/Main.sky >/dev/null
  cd sky-out && GOOS=js GOARCH=wasm with_timeout 1200 go build -o ../main.wasm .
)
/bin/cp -f "$TMP/60-spa-todos/client/main.wasm" "$TMP/60-spa-todos/public/main.wasm"
/bin/cp -f "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$TMP/60-spa-todos/public/wasm_exec.js"

COUNTER="$TMP/09-live-counter/.skyapp/web/sky-out/app"
FORUM="$TMP/19-skyforum/.skyapp/web/sky-out/app"
NOTES="$TMP/62-app-notes/.skyapp/web-app/.split/backend/sky-out/app"
TODOS="$TMP/60-spa-todos/server/sky-out/app"
for bin in "$COUNTER" "$FORUM" "$NOTES" "$TODOS"; do
  [ -x "$bin" ] || { echo "csp-e2e: app not built at $bin" >&2; exit 1; }
done

rc=0
drive() { # drive <scenario> <binary> <port> <via>
  echo "==> $1 via $4"
  with_timeout 300 node "$ROOT/scripts/csp-e2e-verify.mjs" "$1" "$2" --port "$3" --via "$4" || rc=1
}
for via in proxy strict; do
  off=0
  [ "$via" = strict ] && off=10
  drive counter "$COUNTER" $((BASE_PORT + off + 0)) "$via"
  drive console "$COUNTER" $((BASE_PORT + off + 2)) "$via"
  drive forum "$FORUM" $((BASE_PORT + off + 4)) "$via"
  drive todos "$TODOS" $((BASE_PORT + off + 6)) "$via"
  drive notes "$NOTES" $((BASE_PORT + off + 8)) "$via"
done
# The deployment layout: the backend runs from a slot directory with no
# ../frontend/dist, and the proxy serves only *.wasm + /wasm_exec.js from the
# dist and forwards everything else. The Sky.Spa client must still boot: the
# backend serves its own boot loader (runtime-go/rt/runtime_assets.go).
# v0.25.19 FAILED this pass: /spa-boot.<hash>.js was not served as script (a
# 404 here; the HTML NotFound page in an app with a route table).
echo "==> notes via slot"
with_timeout 300 node "$ROOT/scripts/csp-e2e-verify.mjs" notes "$NOTES" --port $((BASE_PORT + 20)) \
  --via slot --dist "$TMP/62-app-notes/.skyapp/web-app/.split/frontend/dist" || rc=1

# ── The real-proxy topology matrix (a real Caddy in front of the app) ──
#
#   direct        the backend alone, no proxy;
#   caddy-all     Caddy proxies every path;
#   caddy-wasm    Caddy serves only *.wasm + /wasm_exec.js from the dist and
#                 proxies the rest (Sky.Spa notes runs from a slot directory
#                 that cannot reach the dist);
#   caddy-static  Caddy serves the whole dist, proxies /_rpc, /_sky, /api
#                 (Sky.Spa only: a Sky.Live app has no dist);
#   caddy-base    Sky.Live under the sub-path /app (SKY_LIVE_BASE_PATH);
#   stale         a page from an old build on a non-default port: every stale
#                 asset is a 404, never HTML, and the page fails loudly.
#
# Every run asserts a booted, interactive client, zero console errors, zero
# policy violations, and the Content-Type of every script / wasm / style sheet.
# The console behind Caddy signs in with SKY_CONSOLE_AUTH=token.
source "$ROOT/scripts/lib/require-tool.sh"
if require_tool caddy "Caddy 2 (https://caddyserver.com/docs/install) — the proxy topology matrix runs a real Caddy"; then
  export CADDY="$(command -v caddy)"
  NOTES_DIST="$TMP/62-app-notes/.skyapp/web-app/.split/frontend/dist"
  TODOS_DIST="$TMP/60-spa-todos/public"
  EMPTY_DIST="$TMP/empty-dist"
  mkdir -p "$EMPTY_DIST"
  slotn=0
  mx() { # mx <scenario> <binary> <via> [verifier args...]
    local scen="$1" bin="$2" via="$3"
    shift 3
    local port=$((BASE_PORT + 22 + 2 * (slotn % 4)))
    slotn=$((slotn + 1))
    echo "==> $scen via $via $*"
    with_timeout 300 node "$ROOT/scripts/csp-e2e-verify.mjs" "$scen" "$bin" --port "$port" --via "$via" "$@" || rc=1
  }
  for via in direct caddy-all caddy-wasm caddy-static; do
    if [ "$via" != caddy-static ]; then
      mx counter "$COUNTER" "$via" --dist "$EMPTY_DIST"
      mx console "$COUNTER" "$via" --dist "$EMPTY_DIST"
    fi
    mx todos "$TODOS" "$via" --dist "$TODOS_DIST"
    if [ "$via" = caddy-wasm ]; then
      mx notes "$NOTES" "$via" --dist "$NOTES_DIST" --slot
    else
      mx notes "$NOTES" "$via" --dist "$NOTES_DIST"
    fi
  done
  mx counter "$COUNTER" caddy-base
  mx stale "$NOTES" caddy-wasm --dist "$NOTES_DIST" --slot --kind spa
  mx stale "$COUNTER" caddy-wasm --dist "$EMPTY_DIST" --kind live
else
  echo "csp-e2e: the proxy topology matrix is SKIPPED (SKY_LIVE_TESTS=skip)." >&2
fi

if [ "$rc" -ne 0 ]; then
  echo "csp-e2e: FAIL — a page broke or reported a Content-Security-Policy violation (see above)." >&2
  exit 1
fi
echo "csp-e2e: PASS — the Console, Sky.Live, a Std.Ui form and Sky.Spa (boot + SSR) run under script-src 'self' 'wasm-unsafe-eval' with zero violations, behind a proxy, with SKY_CSP=strict, and across the real-Caddy topology matrix."
rm -rf "$TMP"
