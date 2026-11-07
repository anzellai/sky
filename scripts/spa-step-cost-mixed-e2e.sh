#!/usr/bin/env bash
# Public large-model persistence and event-semantics gate.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"
source "$ROOT/scripts/lib/fresh-compiler.sh"
source "$ROOT/scripts/lib/require-tool.sh"
source "$ROOT/scripts/lib/with-timeout.sh"
source "$ROOT/scripts/lib/gate-build-cache.sh"
require_fresh_compiler "$SKY" "$ROOT"
require_tool node "install Node.js and the repository Playwright dependencies"

FX="$(gate_e2e_dir "$ROOT" spa-step-cost-mixed)"
rm -rf "$FX"
mkdir -p "$FX"
cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/spa-step-cost-mixed/." "$FX/"
gate_cached_build "$SKY" "$FX" --clean --artefact .skyapp/web-app -- build --target web:app src/Main.sky
APP="$FX/.skyapp/web-app/.split/backend/sky-out/app"
[ -x "$APP" ] || { echo "spa-step-cost-mixed: fixture backend missing" >&2; exit 1; }

for browser in chromium webkit firefox; do
    for mode in reference incremental; do
        with_timeout 300 node "$ROOT/scripts/spa-step-cost-verify.mjs" "$APP" \
            --browser "$browser" --mode "$mode" --model-domain mixed --output "$FX/$mode-$browser.json"
    done
done
echo "spa-step-cost-mixed: PASS, actual wasm in all three browser engines"
