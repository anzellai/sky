#!/usr/bin/env bash
#
# scripts/spa-stale-handler-e2e.sh — browser e2e regression for the Sky.Spa
# stale-handler bug: a re-rendered button kept the message payload bound at its
# first render (see scripts/spa-stale-handler-verify.mjs for the mechanism).
#
# Builds the spa-stale-handler fixture (--target web:app) and drives the real
# wasm client in headless Chromium through an in-session list swap and the
# reload/restore boot path. Proven to FAIL on a pre-fix compiler (the buttons
# dispatch the a1 payload) and PASS on the fixed one.
#
# Prereqs (all fail loudly): a fresh sky-out/sky, node + playwright.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"
if [ ! -x "$SKY" ]; then
  echo "spa-stale-handler-e2e: $SKY not found — run ./scripts/build.sh first." >&2
  exit 1
fi
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"
command -v node >/dev/null 2>&1 || { echo "spa-stale-handler-e2e: 'node' is required." >&2; exit 1; }

# A stable fixture directory, emptied first: the shared gate build cache
# (scripts/lib/gate-build-cache.sh) keys on the project path, so a fresh
# `mktemp -d` per run could never reuse a build.
source "$ROOT/scripts/lib/gate-build-cache.sh"
FX="$(gate_e2e_dir "$ROOT" spa-stale-handler)"
rm -rf "$FX"
mkdir -p "$FX"
cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/spa-stale-handler/." "$FX/"

echo "==> building the stale-handler fixture (--target web:app)"
gate_cached_build "$SKY" "$FX" --clean --artefact .skyapp/web-app -- build --target web:app src/Main.sky

APP="$FX/.skyapp/web-app/.split/backend/sky-out/app"
[ -x "$APP" ] || { echo "spa-stale-handler-e2e: backend app not built at $APP" >&2; exit 1; }

echo "==> driving the wasm client (in-session swap + reload/restore boot path)"
node "$ROOT/scripts/spa-stale-handler-verify.mjs" "$APP" --port "${PORT:-9011}"

echo "spa-stale-handler-e2e: PASS — re-rendered buttons dispatch the current payload."
rm -rf "$FX"
