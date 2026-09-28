#!/usr/bin/env bash
#
# scripts/islands-e2e.sh — browser e2e for widget islands (a third-party JS
# widget inside a Std.Ui / Std.Html view, runtime-go/rt/island_core.go).
#
# Builds the widget-islands fixture twice from a temp copy — Sky.Live
# (--target web) and Sky.Spa (--target web:app, run from its split backend
# directory) — and drives each in headless Chromium with SKY_CSP=strict. The
# widget file is a same-origin <script defer> loaded through App.withHead.
# scripts/islands-e2e-verify.mjs has the case list: typing in the widget
# survives >= 100 re-renders of its parent, widget events arrive as typed Msgs
# (a rejected payload is dropped), Cmd.toIsland reaches the widget, a new id
# remounts it from props, and zero policy violations or console errors.
#
# Proven to FAIL with island adoption disabled in the client runtime (the
# widget remounted on every render and lost the typed text) and on the Sky.Spa
# static shell before App.withHead was applied there (the widget file never
# loaded), and to PASS on the fixed runtime.
#
# Prereqs (all fail loudly): a fresh sky-out/sky, go, node + playwright.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"
if [ ! -x "$SKY" ]; then
  echo "islands-e2e: $SKY not found — run ./scripts/build.sh first." >&2
  exit 1
fi
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"
source "$ROOT/scripts/lib/with-timeout.sh"
command -v node >/dev/null 2>&1 || { echo "islands-e2e: 'node' is required." >&2; exit 1; }
command -v go >/dev/null 2>&1 || { echo "islands-e2e: 'go' is required." >&2; exit 1; }

# A stable fixture directory per target, emptied first: the shared gate build
# cache (scripts/lib/gate-build-cache.sh) keys on the project path.
source "$ROOT/scripts/lib/gate-build-cache.sh"
BASE_PORT="${ISLANDS_E2E_PORT:-9560}"

stage() { # stage <name>
  local dir
  dir="$(gate_e2e_dir "$ROOT" "islands-$1")"
  rm -rf "$dir"
  mkdir -p "$dir"
  cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/widget-islands/." "$dir/"
  printf '%s\n' "$dir"
}

LIVE_DIR="$(stage web)"
echo "==> building the widget-islands fixture (--target web)"
with_timeout 1200 bash "$ROOT/scripts/lib/gate-build-cache.sh" build "$SKY" "$LIVE_DIR" \
  --clean --artefact .skyapp/web -- build --target web src/Main.sky
LIVE_APP="$LIVE_DIR/.skyapp/web/sky-out/app"

SPA_DIR="$(stage web-app)"
echo "==> building the widget-islands fixture (--target web:app)"
with_timeout 1200 bash "$ROOT/scripts/lib/gate-build-cache.sh" build "$SKY" "$SPA_DIR" \
  --clean --artefact .skyapp/web-app -- build --target web:app src/Main.sky
SPA_BACKEND="$SPA_DIR/.skyapp/web-app/.split/backend"
SPA_APP="$SPA_BACKEND/sky-out/app"

for bin in "$LIVE_APP" "$SPA_APP"; do
  [ -x "$bin" ] || { echo "islands-e2e: app not built at $bin" >&2; exit 1; }
done

rc=0
echo "==> Sky.Live (--target web)"
with_timeout 300 node "$ROOT/scripts/islands-e2e-verify.mjs" "$LIVE_APP" \
  --port "$BASE_PORT" --mode live --cwd "$LIVE_DIR" || rc=1
echo "==> Sky.Spa (--target web:app, from the split backend)"
with_timeout 300 node "$ROOT/scripts/islands-e2e-verify.mjs" "$SPA_APP" \
  --port $((BASE_PORT + 2)) --mode spa --cwd "$SPA_BACKEND" || rc=1

if [ "$rc" -ne 0 ]; then
  echo "islands-e2e: FAIL (see above)." >&2
  exit 1
fi
echo "islands-e2e: PASS — widget islands keep their state across re-renders, exchange typed messages and commands, and remount on a new id, on Sky.Live and Sky.Spa under a strict Content-Security-Policy."
rm -rf "$LIVE_DIR" "$SPA_DIR"
