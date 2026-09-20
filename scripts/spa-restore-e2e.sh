#!/usr/bin/env bash
#
# scripts/spa-restore-e2e.sh — browser e2e regression for the Sky.Spa first-paint
# localStorage-restore bug (fixed by live_wasm.go's two-step first paint).
#
# Builds the spa-hydration-restore fixture (--target web:app), then drives the
# real wasm client in headless Chromium via spa-hydration-verify.mjs: the SSR
# page paints count=0, three clicks on `increment` persist n=3 to localStorage,
# and a full reload must restore n=3 on the FIRST SSR paint — with hydration
# actually used (not a fallback rebuild). Proven to FAIL on a pre-fix compiler
# (stale count=0 after reload) and PASS on the fixed one.
#
# Prereqs (all fail loudly): a fresh sky-out/sky, node + playwright, sqlite3.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"

if [ ! -x "$SKY" ]; then
  echo "spa-restore-e2e: $SKY not found — run ./scripts/build.sh first." >&2
  exit 1
fi
# The installed compiler must have been built from THIS tree, or the e2e would
# certify wasm the current source never produced (see scripts/lib/fresh-compiler.sh).
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"
for tool in node sqlite3; do
  command -v "$tool" >/dev/null 2>&1 || { echo "spa-restore-e2e: '$tool' is required." >&2; exit 1; }
done

FX="$(mktemp -d)/spa-restore"
mkdir -p "$FX"
cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/spa-hydration-restore/." "$FX/"

echo "==> building the restore fixture (--target web:app)"
( cd "$FX" && "$SKY" build --target web:app src/Main.sky )

APP="$FX/.skyapp/web-app/.split/backend/sky-out/app"
[ -x "$APP" ] || { echo "spa-restore-e2e: backend app not built at $APP" >&2; exit 1; }

echo "==> driving the wasm client (seed 2 items, click increment 3x, reload)"
node "$ROOT/scripts/spa-hydration-verify.mjs" "$APP" \
  --url / --port "${PORT:-9007}" \
  --db 'app.db:CREATE TABLE items(name TEXT);INSERT INTO items(name) VALUES ("alpha"),("beta");' \
  --click 'button:has-text("increment")' --clicks 3 \
  --expect "count=3"

echo "spa-restore-e2e: PASS — restored model paints on the first SSR paint."
rm -rf "$(dirname "$FX")"
