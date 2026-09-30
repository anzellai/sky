#!/usr/bin/env bash
# docs/perf/runs/canvas-20260930/run.sh — the whole matrix of bench.mjs runs.
#
#   run.sh <live-app-dir> <spa-backend-dir> <out-dir>
#
# <live-app-dir>: bench-app built with `sky build --target web`, the directory
# holding .skyapp/web/sky-out/app. <spa-backend-dir>: bench-app built with
# `sky build --target web:app`, its .skyapp/web-app/.split/backend. Every run
# is headed (Google Chrome, and Playwright's WebKit as the engine of the
# macOS desktop window), devicePixelRatio 2, SKY_CSP=strict, one at a time.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
LIVE="$1"; SPA="$2"; OUT="$3"
mkdir -p "$OUT"
SIZES="${SIZES:-10,100,1000,5000,20000}"
for browser in chromium webkit; do
  chan=(); [ "$browser" = chromium ] && chan=(--channel chrome)
  for backend in svg canvas; do
    node "$HERE/bench.mjs" "$SPA/sky-out/app" --cwd "$SPA" --port 9641 --target spa \
      --backend "$backend" --browser "$browser" "${chan[@]}" --sizes "$SIZES" \
      --frames 20 --reps 3 --budget-s 60 --out "$OUT/spa-$backend-$browser.json" || echo "FAILED spa-$backend-$browser"
  done
  node "$HERE/bench.mjs" "$LIVE/.skyapp/web/sky-out/app" --cwd "$LIVE" --port 9643 --target live \
    --backend svg --browser "$browser" "${chan[@]}" --sizes "$SIZES" \
    --frames 20 --reps 3 --budget-s 60 --out "$OUT/live-svg-$browser.json" || echo "FAILED live-svg-$browser"
done
