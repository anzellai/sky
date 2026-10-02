#!/usr/bin/env bash
#
# scripts/live-client-e2e.sh — browser e2e regression for the Sky.Live client
# and the desktop webview applier: handler addressing (one handler per event,
# the render the user clicked on), event binding, input authority, IME, gap
# buffering, the panic banner and multi-tab reconnects. See
# scripts/live-client-verify.mjs for the case list.
#
# Builds the live-client fixture (--target web) from a temp copy and drives it
# in headless Chromium. Proven to FAIL on the pre-fix runtime and PASS on the
# fixed one.
#
# Prereqs (all fail loudly): a fresh sky-out/sky, node + playwright.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"
if [ ! -x "$SKY" ]; then
  echo "live-client-e2e: $SKY not found — run ./scripts/build.sh first." >&2
  exit 1
fi
# Every Node verifier runs under a bound: a verifier that never ends (a
# launched browser keeps Node alive) must fail here, not hang the CI job.
source "$ROOT/scripts/lib/with-timeout.sh"
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"
source "$ROOT/scripts/lib/require-tool.sh"
require_tool node "install Node.js 20+ (and 'npm ci' for playwright)" || exit 0

# A stable fixture directory, emptied first: the shared gate build cache
# (scripts/lib/gate-build-cache.sh) keys on the project path, so a fresh
# `mktemp -d` per run could never reuse a build.
source "$ROOT/scripts/lib/gate-build-cache.sh"
FX="$(gate_e2e_dir "$ROOT" live-client)"
rm -rf "$FX"
mkdir -p "$FX"
cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/live-client/." "$FX/"

echo "==> building the live-client fixture (--target web)"
gate_cached_build "$SKY" "$FX" --clean --artefact .skyapp/web -- build --target web src/Main.sky

APP="$FX/.skyapp/web/sky-out/app"
[ -x "$APP" ] || { echo "live-client-e2e: app not built at $APP" >&2; exit 1; }

echo "==> driving the Sky.Live client + webview applier"
with_timeout 300 node "$ROOT/scripts/live-client-verify.mjs" "$APP" --port "${SKY_LIVE_PORT:-9240}"

echo "live-client-e2e: PASS"
rm -rf "$FX"
