#!/usr/bin/env bash
#
# scripts/nav-e2e.sh — browser e2e for Std.Nav: `update` moves the address bar
# without a page reload, the same way on Sky.Live and on the Sky.Spa client.
#
# Builds rust/crates/sky/tests/fixtures/nav-cmds twice (--target web:app and
# the Sky.Live web target) and drives both in the same browsers under
# SKY_CSP=strict (scripts/nav-verify.mjs):
#
#   go     pushUrl to another path: the route and onNavigate run, one new
#          history entry, no reload; Back returns to the page before
#   home   replaceUrl: the route runs, no new history entry
#   frag   pushUrl "#sec": no page moves, Sub.onFragment receives it
#   clear  clearFragment: the fragment leaves the address bar, no new entry
#   save   a server arm's navigation runs (the Sky.Spa client runs it when it
#          sends the request) and the server result still arrives
#   evil   a URL off the site is refused, and the refusal is logged
#   plain  a page with no Sub.onFragment opened at `/plain#x` keeps its
#          first paint (the load-time fragment report is a no-op)
#
# Browsers: SKY_E2E_BROWSERS (default "chromium,webkit"); SKY_E2E_CHANNEL=chrome
# uses Google Chrome; SKY_E2E_HEADED=1 runs them headed.
#
# Prereqs (all fail loudly): a fresh sky-out/sky, go, node + playwright.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"
if [ ! -x "$SKY" ]; then
  echo "nav-e2e: $SKY not found — run ./scripts/build.sh first." >&2
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
TMP="$(gate_e2e_dir "$ROOT" nav-e2e)"
rm -rf "$TMP"
mkdir -p "$TMP/app"
cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/nav-cmds/." "$TMP/app/"

echo "==> building fixture nav-cmds (--target web:app)"
(cd "$TMP/app" && with_timeout 1200 "$SKY" build --target web:app src/Main.sky) >"$TMP/build-spa.log" 2>&1 \
  || { cat "$TMP/build-spa.log" >&2; echo "nav-e2e: web:app build failed" >&2; exit 1; }
echo "==> building fixture nav-cmds (Sky.Live, --target web)"
(cd "$TMP/app" && with_timeout 1200 "$SKY" build --target web src/Main.sky) >"$TMP/build-live.log" 2>&1 \
  || { cat "$TMP/build-live.log" >&2; echo "nav-e2e: web build failed" >&2; exit 1; }
SPA="$TMP/app/.skyapp/web-app/.split/backend/sky-out/app"
LIVE="$TMP/app/.skyapp/web/sky-out/app"
[ -x "$SPA" ] || { echo "nav-e2e: backend not built at $SPA" >&2; exit 1; }
[ -x "$LIVE" ] || { echo "nav-e2e: Live app not built at $LIVE" >&2; exit 1; }

echo "==> driving both targets in a browser"
SKY_E2E_BROWSERS="${SKY_E2E_BROWSERS:-chromium,webkit}" \
  with_timeout 600 node "$ROOT/scripts/nav-verify.mjs" "$SPA" "$LIVE" --port "${NAV_E2E_PORT:-9381}"

echo "nav-e2e: PASS — Std.Nav moves the address bar the same way on web:app and Sky.Live."
rm -rf "$TMP"
