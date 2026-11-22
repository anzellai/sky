#!/usr/bin/env bash
#
# scripts/i18n-e2e.sh — browser e2e for Std.I18n: the server page and the
# Sky.Spa client give the same translated and formatted text, and the root
# <html> lang and dir follow the locale (I18n.document + App.withDocument).
#
# Builds examples/78-i18n-booking twice (--target web:app and the Sky.Live web
# target) and drives both in the same browsers under SKY_CSP=strict
# (scripts/i18n-verify.mjs). For each of /, /zh, /ar and /xa:
#
#   first-paint  the server writes the locale's lang and dir on <html>
#   hydrated     the web:app client clears data-sky-hydrating
#   same-text    the live page text equals the server page text (no
#                hydration mismatch: the wasm client formats as the server)
#   html         document.documentElement.lang / .dir after the client starts
#
# Then a sky-nav link from / to /ar switches lang and dir with no reload.
#
# Browsers: SKY_E2E_BROWSERS (default "chromium,webkit"); SKY_E2E_CHANNEL=chrome
# uses Google Chrome; SKY_E2E_HEADED=1 runs them headed.
#
# Prereqs (all fail loudly): a fresh sky-out/sky, go, node + playwright.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"
if [ ! -x "$SKY" ]; then
  echo "i18n-e2e: $SKY not found — run ./scripts/build.sh first." >&2
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
TMP="$(gate_e2e_dir "$ROOT" i18n-e2e)"
rm -rf "$TMP"
mkdir -p "$TMP/app"
cp -Rf "$ROOT/examples/78-i18n-booking/." "$TMP/app/"
rm -rf "$TMP/app/sky-out" "$TMP/app/.skycache" "$TMP/app/.skyapp"

echo "==> building examples/78-i18n-booking (--target web:app)"
(cd "$TMP/app" && with_timeout 1200 "$SKY" build --target web:app src/Main.sky) >"$TMP/build-spa.log" 2>&1 \
  || { cat "$TMP/build-spa.log" >&2; echo "i18n-e2e: web:app build failed" >&2; exit 1; }
echo "==> building examples/78-i18n-booking (Sky.Live, --target web)"
(cd "$TMP/app" && with_timeout 1200 "$SKY" build --target web src/Main.sky) >"$TMP/build-live.log" 2>&1 \
  || { cat "$TMP/build-live.log" >&2; echo "i18n-e2e: web build failed" >&2; exit 1; }
SPA="$TMP/app/.skyapp/web-app/.split/backend/sky-out/app"
LIVE="$TMP/app/.skyapp/web/sky-out/app"
[ -x "$SPA" ] || { echo "i18n-e2e: backend not built at $SPA" >&2; exit 1; }
[ -x "$LIVE" ] || { echo "i18n-e2e: Live app not built at $LIVE" >&2; exit 1; }

echo "==> driving both targets in a browser"
SKY_E2E_BROWSERS="${SKY_E2E_BROWSERS:-chromium,webkit}" \
  with_timeout 600 node "$ROOT/scripts/i18n-verify.mjs" "$SPA" "$LIVE" --port "${I18N_E2E_PORT:-9411}" --fixture-root "$TMP/app"

echo "i18n-e2e: PASS — the server and the client give the same text, and <html> lang and dir follow the locale, on web:app and Sky.Live."
rm -rf "$TMP"
