#!/usr/bin/env bash
#
# scripts/document-e2e.sh — browser e2e for App.withDocument: the attributes of
# the root <html> element follow the model on Sky.Live and on the Sky.Spa
# client, with no page reload.
#
# Builds rust/crates/sky/tests/fixtures/std-app-document twice (--target
# web:app and the Sky.Live web target) and drives both in the same browsers
# under SKY_CSP=strict (scripts/document-verify.mjs):
#
#   first-paint  the server writes lang and dir on <html>
#   load         document.documentElement.lang / .dir after the client starts
#   ar           a language switch sets lang="ar", dir="rtl" and an escaped
#                data-theme value, with no reload
#   note         a view change with the same set keeps the attributes
#   fr           a set without data-theme removes it
#   en           back to English
#
# Browsers: SKY_E2E_BROWSERS (default "chromium,webkit"); SKY_E2E_CHANNEL=chrome
# uses Google Chrome; SKY_E2E_HEADED=1 runs them headed.
#
# Prereqs (all fail loudly): a fresh sky-out/sky, go, node + playwright.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"
if [ ! -x "$SKY" ]; then
  echo "document-e2e: $SKY not found — run ./scripts/build.sh first." >&2
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
TMP="$(gate_e2e_dir "$ROOT" document-e2e)"
rm -rf "$TMP"
mkdir -p "$TMP/app"
cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/std-app-document/." "$TMP/app/"

echo "==> building fixture std-app-document (--target web:app)"
(cd "$TMP/app" && with_timeout 1200 "$SKY" build --target web:app src/Main.sky) >"$TMP/build-spa.log" 2>&1 \
  || { cat "$TMP/build-spa.log" >&2; echo "document-e2e: web:app build failed" >&2; exit 1; }
echo "==> building fixture std-app-document (Sky.Live, --target web)"
(cd "$TMP/app" && with_timeout 1200 "$SKY" build --target web src/Main.sky) >"$TMP/build-live.log" 2>&1 \
  || { cat "$TMP/build-live.log" >&2; echo "document-e2e: web build failed" >&2; exit 1; }
SPA="$TMP/app/.skyapp/web-app/.split/backend/sky-out/app"
LIVE="$TMP/app/.skyapp/web/sky-out/app"
[ -x "$SPA" ] || { echo "document-e2e: backend not built at $SPA" >&2; exit 1; }
[ -x "$LIVE" ] || { echo "document-e2e: Live app not built at $LIVE" >&2; exit 1; }

echo "==> driving both targets in a browser"
SKY_E2E_BROWSERS="${SKY_E2E_BROWSERS:-chromium,webkit}" \
  with_timeout 600 node "$ROOT/scripts/document-verify.mjs" "$SPA" "$LIVE" --port "${DOCUMENT_E2E_PORT:-9401}" --fixture-root "$TMP/app"

echo "document-e2e: PASS — the <html> attributes follow the model on web:app and Sky.Live."
rm -rf "$TMP"
