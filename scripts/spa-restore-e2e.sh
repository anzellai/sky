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

# A stable fixture directory, emptied first: the shared gate build cache
# (scripts/lib/gate-build-cache.sh) keys on the project path, so a fresh
# `mktemp -d` per run could never reuse a build.
source "$ROOT/scripts/lib/gate-build-cache.sh"
FX="$(gate_e2e_dir "$ROOT" spa-restore)"
rm -rf "$FX"
mkdir -p "$FX"
cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/spa-hydration-restore/." "$FX/"

echo "==> building the restore fixture (--target web:app)"
gate_cached_build "$SKY" "$FX" --clean --artefact .skyapp/web-app -- build --target web:app src/Main.sky

APP="$FX/.skyapp/web-app/.split/backend/sky-out/app"
[ -x "$APP" ] || { echo "spa-restore-e2e: backend app not built at $APP" >&2; exit 1; }

echo "==> driving the wasm client (seed 2 items, click increment 3x, reload)"
node "$ROOT/scripts/spa-hydration-verify.mjs" "$APP" \
  --url / --port "${PORT:-9007}" \
  --db 'app.db:CREATE TABLE items(name TEXT);INSERT INTO items(name) VALUES ("alpha"),("beta");' \
  --click 'button:has-text("increment")' --clicks 3 \
  --expect "count=3"

# R2 / R3 (register M): the full-load rule. The seed wins only for the fields
# the server settled for the page; everything else is restored. Its data/ is
# staged next to the backend binary, where the reads resolve.
RX="$(gate_e2e_dir "$ROOT" spa-rc-reload)"
rm -rf "$RX"
mkdir -p "$RX"
cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/spa-rc-reload/." "$RX/"
echo "==> building the full-load fixture (--target web:app)"
gate_cached_build "$SKY" "$RX" --clean --artefact .skyapp/web-app -- build --target web:app src/Main.sky
RBE="$RX/.skyapp/web-app/.split/backend"
[ -x "$RBE/sky-out/app" ] || { echo "spa-restore-e2e: backend app not built at $RBE/sky-out/app" >&2; exit 1; }
mkdir -p "$RBE/data"
cp -Rf "$RX/data/." "$RBE/data/"
echo "==> driving the full-load rule (settled fields from the seed, the rest restored)"
node "$ROOT/scripts/spa-reload-verify.mjs" "$RBE/sky-out/app" --port "${RELOAD_PORT:-9372}"

# The identity rule: a stored model restores only for the session identity it
# was stored under. Two pages in one browser context (shared localStorage, as
# two tabs) hold two identities; neither ever shows the other one's data.
IX="$(gate_e2e_dir "$ROOT" spa-identity-slot)"
rm -rf "$IX"
mkdir -p "$IX"
cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/spa-identity-slot/." "$IX/"
echo "==> building the identity fixture (--target web:app)"
gate_cached_build "$SKY" "$IX" --clean --artefact .skyapp/web-app -- build --target web:app src/Main.sky
IBE="$IX/.skyapp/web-app/.split/backend"
[ -x "$IBE/sky-out/app" ] || { echo "spa-restore-e2e: backend app not built at $IBE/sky-out/app" >&2; exit 1; }
mkdir -p "$IBE/data"
cp -Rf "$IX/data/." "$IBE/data/"
echo "==> driving the identity rule (two identities, one localStorage)"
node "$ROOT/scripts/spa-identity-verify.mjs" "$IBE/sky-out/app" --port "${IDENTITY_PORT:-9373}"

echo "spa-restore-e2e: PASS — restored model paints on the first SSR paint; a full load keeps what the page did not settle; a stored model restores only for its own identity."
rm -rf "$FX" "$RX" "$IX"
