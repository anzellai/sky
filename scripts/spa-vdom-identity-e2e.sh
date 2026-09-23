#!/usr/bin/env bash
#
# scripts/spa-vdom-identity-e2e.sh — browser e2e regression for the Sky.Spa
# DOM driver: node identity across the shared diff (keyed and unkeyed sibling
# inserts, a re-keyed row), select value, key / checkbox payloads, the
# user-event reconcile of a refused input, IME composition, clickable labels,
# injected hover styles, hydration text parity and route-param decoding.
#
# Builds the spa-vdom-identity fixture (--target web:app) and drives the real
# wasm client in headless Chromium (scripts/spa-vdom-identity-verify.mjs lists
# each check). Proven to FAIL on a pre-fix compiler and PASS on the fixed one.
#
# Prereqs (all fail loudly): a fresh sky-out/sky, node + playwright.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"
if [ ! -x "$SKY" ]; then
  echo "spa-vdom-identity-e2e: $SKY not found — run ./scripts/build.sh first." >&2
  exit 1
fi
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"
command -v node >/dev/null 2>&1 || { echo "spa-vdom-identity-e2e: 'node' is required." >&2; exit 1; }

FX="$(mktemp -d)/spa-vdom-identity"
mkdir -p "$FX"
cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/spa-vdom-identity/." "$FX/"

echo "==> building the spa-vdom-identity fixture (--target web:app)"
( cd "$FX" && "$SKY" build --target web:app src/Main.sky )

APP="$FX/.skyapp/web-app/.split/backend/sky-out/app"
[ -x "$APP" ] || { echo "spa-vdom-identity-e2e: backend app not built at $APP" >&2; exit 1; }

echo "==> driving the wasm client"
node "$ROOT/scripts/spa-vdom-identity-verify.mjs" "$APP" --port "${PORT:-9200}"

# The same source on Sky.Live: the diff and the Std.Ui controls are shared, so
# node identity, select value, injected styles and labels must hold there too.
LFX="$(dirname "$FX")/spa-vdom-identity-live"
mkdir -p "$LFX"
cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/spa-vdom-identity/." "$LFX/"
echo "==> building the fixture for Sky.Live (--target web)"
( cd "$LFX" && "$SKY" build --target web src/Main.sky )
LAPP="$LFX/.skyapp/web/sky-out/app"
[ -x "$LAPP" ] || { echo "spa-vdom-identity-e2e: Live app not built at $LAPP" >&2; exit 1; }
echo "==> driving the Sky.Live client"
node "$ROOT/scripts/spa-vdom-identity-verify.mjs" "$LAPP" --port "$(( ${PORT:-9200} + 1 ))" --live

echo "spa-vdom-identity-e2e: PASS"
rm -rf "$(dirname "$FX")"
