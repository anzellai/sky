#!/usr/bin/env bash
#
# scripts/ui-canvas-terminal-e2e.sh — browser e2e for Std.Ui.Canvas, the
# Ui.text wrapping and Std.Ui.Terminal (v0.27.0).
#
# Builds, from temp copies:
#   * the ui-canvas fixture for Sky.Live (--target web) and Sky.Spa
#     (--target web:app, run from its split backend directory);
#   * the ui-terminal fixture for Sky.Live (--target web), and checks that
#     the same program built for Sky.Spa is REFUSED with the error that names
#     Std.Ui.Terminal and the target that works.
# Then drives each app in headless Chromium with SKY_CSP=strict
# (UI_E2E_HEADED=1 shows the browser). scripts/ui-canvas-terminal-verify.mjs
# has the case list: scene pointer events in scene units, a new shape patched
# into a live Sky.Live scene is an SVG element; on Sky.Spa (Chromium and
# WebKit) the scene is a canvas the client draws: labelled, with a text
# alternative, at devicePixelRatio 2 backing pixels, its shapes' pixels drawn,
# a new shape drawn in one more paint and hit-tested; a
# click on a shape, two texts in a column are two lines and a long text wraps
# in a narrow box; a terminal bound to `sh` draws on a canvas, prints `echo
# hi` (text layer and lit canvas pixels), lets a mouse selection of `hi` be
# copied, runs a full-screen redraw loop within its frame budget, follows a
# resize (`stty size`), shows output written while the SSE connection was
# down, and is repainted after a reload by one repaint frame from the
# server's screen (no byte replay). Zero policy violations or console errors.
#
# The terminal-race case (Chromium and WebKit) attaches a fresh `sh` to the
# mounted widget 16 times and runs vim in it: the server's screen must have
# the widget's size, and vim's Escape must keep row 0. It FAILS on the runtime
# before the screen was made under the resize lock (procHandle.sizeMu): the
# first screen read and the widget's resize ran at once, the resize found no
# screen, and the screen stayed at the spawn size, 80x24, under a wider PTY;
# vim's "^[" then wrapped at the bottom row and scrolled row 0 away (about 3
# runs in 10 in either browser).
#
# The Sky.Live WebKit case FAILS on the client that parsed each new SVG child
# through its own Range (3,000 new shapes: 10,418 ms, the bound is 2 s;
# docs/perf/runs/canvas-20260930/README.md section 5) and PASSES on the fixed
# one. Before Sky.Spa drew scenes on a canvas, the Sky.Spa case
# was proven to FAIL when the wasm renderer created SVG elements in the HTML
# namespace (the scene drew nothing and took no pointer events); it now
# checks the canvas. Proven to FAIL on the terminal reload case before widget
# events sent from mount() were held until the page's client is ready and
# before widget commands pushed with no SSE connection were kept for the next
# one (the terminal stayed blank after a reload); PASSES on the fixed runtime.
# The terminal cases FAIL (13 of them) on the DOM widget with the base64 byte
# stream this replaced (f4e98f10): no canvas, no text layer, and a reload
# replays "output" bytes instead of one repaint frame.
#
# Prereqs (all fail loudly): a fresh sky-out/sky, go, node + playwright with
# Chromium and WebKit, sh, vim.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"
if [ ! -x "$SKY" ]; then
  echo "ui-canvas-terminal-e2e: $SKY not found — run ./scripts/build.sh first." >&2
  exit 1
fi
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"
source "$ROOT/scripts/lib/with-timeout.sh"
source "$ROOT/scripts/lib/require-tool.sh"
# SKY_LIVE_TESTS=skip is the one opt-out; it skips the whole gate, loudly.
require_tool node "install Node.js 20+ (and 'npm ci' for playwright)" || exit 0
require_tool go "install Go 1.25+ (https://go.dev/dl/)" || exit 0
require_tool sh "the terminal runs /bin/sh" || exit 0
HAVE_VIM=1
require_tool vim "install vim (the terminal-race case runs it in the terminal; apt-get install vim)" || HAVE_VIM=0

source "$ROOT/scripts/lib/gate-build-cache.sh"
BASE_PORT="${UI_CANVAS_TERMINAL_E2E_PORT:-9570}"

stage() { # stage <fixture> <name>
  local dir
  dir="$(gate_e2e_dir "$ROOT" "ui-$2")"
  rm -rf "$dir"
  mkdir -p "$dir"
  cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/$1/." "$dir/"
  printf '%s\n' "$dir"
}

CANVAS_WEB="$(stage ui-canvas canvas-web)"
echo "==> building the ui-canvas fixture (--target web)"
with_timeout 1200 bash "$ROOT/scripts/lib/gate-build-cache.sh" build "$SKY" "$CANVAS_WEB" \
  --clean --artefact .skyapp/web -- build --target web src/Main.sky
CANVAS_WEB_APP="$CANVAS_WEB/.skyapp/web/sky-out/app"

CANVAS_SPA="$(stage ui-canvas canvas-web-app)"
echo "==> building the ui-canvas fixture (--target web:app)"
with_timeout 1200 bash "$ROOT/scripts/lib/gate-build-cache.sh" build "$SKY" "$CANVAS_SPA" \
  --clean --artefact .skyapp/web-app -- build --target web:app src/Main.sky
CANVAS_SPA_BACKEND="$CANVAS_SPA/.skyapp/web-app/.split/backend"
CANVAS_SPA_APP="$CANVAS_SPA_BACKEND/sky-out/app"

TERM_WEB="$(stage ui-terminal terminal-web)"
echo "==> building the ui-terminal fixture (--target web)"
with_timeout 1200 bash "$ROOT/scripts/lib/gate-build-cache.sh" build "$SKY" "$TERM_WEB" \
  --clean --artefact .skyapp/web -- build --target web src/Main.sky
TERM_WEB_APP="$TERM_WEB/.skyapp/web/sky-out/app"

for bin in "$CANVAS_WEB_APP" "$CANVAS_SPA_APP" "$TERM_WEB_APP"; do
  [ -x "$bin" ] || { echo "ui-canvas-terminal-e2e: app not built at $bin" >&2; exit 1; }
done

rc=0
TERM_SPA="$(stage ui-terminal terminal-web-app)"
echo "==> the ui-terminal fixture must be refused for Sky.Spa (--target web:app)"
set +e
spa_out="$(cd "$TERM_SPA" && with_timeout 1200 "$SKY" build --target web:app src/Main.sky 2>&1)"
spa_rc=$?
set -e
if [ "$spa_rc" -eq 0 ]; then
  echo "FAIL a Std.Ui.Terminal app built for Sky.Spa (it must be refused)" >&2
  rc=1
elif ! printf '%s' "$spa_out" | grep -q "Std.Ui.Terminal is not available on a Sky.Spa target"; then
  echo "FAIL the Sky.Spa refusal does not name Std.Ui.Terminal:" >&2
  printf '%s\n' "$spa_out" | tail -20 >&2
  rc=1
else
  echo "ok   the Sky.Spa build of a terminal app is refused with the reason"
fi

echo "==> Std.Ui.Canvas on Sky.Live (--target web)"
with_timeout 300 node "$ROOT/scripts/ui-canvas-terminal-verify.mjs" "$CANVAS_WEB_APP" \
  --port "$BASE_PORT" --mode canvas-live --cwd "$CANVAS_WEB" || rc=1
echo "==> Std.Ui.Canvas on Sky.Live (--target web), WebKit (the desktop window's engine)"
with_timeout 300 node "$ROOT/scripts/ui-canvas-terminal-verify.mjs" "$CANVAS_WEB_APP" \
  --port $((BASE_PORT + 1)) --mode canvas-live --browser webkit --cwd "$CANVAS_WEB" || rc=1
echo "==> Std.Ui.Canvas on Sky.Spa (--target web:app, from the split backend): the canvas backend, Chromium"
with_timeout 300 node "$ROOT/scripts/ui-canvas-terminal-verify.mjs" "$CANVAS_SPA_APP" \
  --port $((BASE_PORT + 2)) --mode canvas-spa --cwd "$CANVAS_SPA_BACKEND" || rc=1
echo "==> Std.Ui.Canvas on Sky.Spa: the canvas backend, WebKit (the desktop window's engine)"
with_timeout 300 node "$ROOT/scripts/ui-canvas-terminal-verify.mjs" "$CANVAS_SPA_APP" \
  --port $((BASE_PORT + 3)) --mode canvas-spa --browser webkit --cwd "$CANVAS_SPA_BACKEND" || rc=1
echo "==> Std.Ui.Terminal on Sky.Live (--target web)"
with_timeout 300 node "$ROOT/scripts/ui-canvas-terminal-verify.mjs" "$TERM_WEB_APP" \
  --port $((BASE_PORT + 4)) --mode terminal --cwd "$TERM_WEB" || rc=1
if [ "$HAVE_VIM" -eq 1 ]; then
  echo "==> Std.Ui.Terminal attach race: 16 fresh shells + vim Escape (Chromium)"
  with_timeout 600 node "$ROOT/scripts/ui-canvas-terminal-verify.mjs" "$TERM_WEB_APP" \
    --port $((BASE_PORT + 6)) --mode terminal-race --browser chromium --cwd "$TERM_WEB" || rc=1
  echo "==> Std.Ui.Terminal attach race: 16 fresh shells + vim Escape (WebKit)"
  with_timeout 600 node "$ROOT/scripts/ui-canvas-terminal-verify.mjs" "$TERM_WEB_APP" \
    --port $((BASE_PORT + 8)) --mode terminal-race --browser webkit --cwd "$TERM_WEB" || rc=1
fi

if [ "$rc" -ne 0 ]; then
  echo "ui-canvas-terminal-e2e: FAIL (see above)." >&2
  exit 1
fi
echo "ui-canvas-terminal-e2e: PASS — canvas scenes (Sky.Live and Sky.Spa), text wrapping and a PTY terminal work under a strict Content-Security-Policy."
rm -rf "$CANVAS_WEB" "$CANVAS_SPA" "$TERM_WEB" "$TERM_SPA"
