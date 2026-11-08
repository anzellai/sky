#!/usr/bin/env bash
# Public large-model persistence and event-semantics gate.
set -euo pipefail
COLLECTION_MODE=all
BROWSER_MODE=all
while [ "$#" -gt 0 ]; do
    case "$1" in
        --collection)
            [ "$#" -ge 2 ] || { echo "--collection requires dict or tuple" >&2; exit 2; }
            COLLECTION_MODE="$2"
            shift 2
            ;;
        --browser)
            [ "$#" -ge 2 ] || { echo "--browser requires chromium, webkit or firefox" >&2; exit 2; }
            BROWSER_MODE="$2"
            shift 2
            ;;
        *) echo "unknown collections gate argument" >&2; exit 2 ;;
    esac
done
case "$COLLECTION_MODE" in
    dict) COLLECTIONS=(dict) ;;
    tuple) COLLECTIONS=(tuple) ;;
    all) COLLECTIONS=(dict tuple) ;;
    *) echo "--collection must be dict or tuple" >&2; exit 2 ;;
esac
case "$BROWSER_MODE" in
    chromium|webkit|firefox) BROWSERS=("$BROWSER_MODE") ;;
    all) BROWSERS=(chromium webkit firefox) ;;
    *) echo "invalid browser" >&2; exit 2 ;;
esac
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"
source "$ROOT/scripts/lib/fresh-compiler.sh"
source "$ROOT/scripts/lib/require-tool.sh"
source "$ROOT/scripts/lib/with-timeout.sh"
source "$ROOT/scripts/lib/gate-build-cache.sh"
require_fresh_compiler "$SKY" "$ROOT"
require_tool node "install Node.js and the repository Playwright dependencies"

FX="$(gate_e2e_dir "$ROOT" "spa-step-cost-collections-$COLLECTION_MODE-$BROWSER_MODE")"
rm -rf "$FX"
mkdir -p "$FX"
cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/spa-step-cost-collections/." "$FX/"
gate_cached_build "$SKY" "$FX" --clean --artefact .skyapp/web-app -- build --target web:app src/Main.sky
APP="$FX/.skyapp/web-app/.split/backend/sky-out/app"
[ -x "$APP" ] || { echo "spa-step-cost-collections: fixture backend missing" >&2; exit 1; }

# Inspect generated application Go, not runtime generic declarations. This proves
# that the public Sky fixture exercises the newly admitted concrete types.
with_timeout 30 node --input-type=module - "$FX/.skyapp/web-app/.split/frontend/sky-out/main.go" <<'NODE'
import assert from "node:assert/strict";
import fs from "node:fs";
const source = fs.readFileSync(process.argv[2], "utf8");
assert(/Lookup\s+rt\.SkyDict\[string\]/.test(source), "wasm fixture lookup did not lower to rt.SkyDict[string]");
assert(/Pairs\s+\[\]rt\.T2\[int,\s*string\]/.test(source), "wasm fixture pairs did not lower to []rt.T2[int,string]");
NODE

for browser in "${BROWSERS[@]}"; do
    for collection in "${COLLECTIONS[@]}"; do
        for mode in reference incremental; do
            with_timeout 300 node "$ROOT/scripts/spa-step-cost-collections-verify.mjs" "$APP" \
                --browser "$browser" --mode "$mode" --collection "$collection" \
                --output "$FX/$collection-$mode-$browser.json"
        done
        with_timeout 30 node --input-type=module - \
            "$FX/$collection-reference-$browser.json" "$FX/$collection-incremental-$browser.json" <<'NODE'
import assert from "node:assert/strict";
import fs from "node:fs";
const [reference, incremental] = process.argv.slice(2).map(f => JSON.parse(fs.readFileSync(f, "utf8")));
assert(reference.passed && incremental.passed, "collection browser run failed");
assert.equal(reference.collection, incremental.collection);
assert.equal(reference.checkpoints.length, 8, "missing canonical collection checkpoints");
assert.deepEqual(reference.checkpoints, incremental.checkpoints, "reference/incremental stored canonical bytes diverged");
assert.equal(reference.oversizedCheckpoints.length, reference.collection === "dict" ? 3 : 0, "Missing oversized Dict checkpoints");
assert.deepEqual(reference.oversizedCheckpoints, incremental.oversizedCheckpoints, "Oversized Dict publication/reload diverged");
NODE
    done
done
echo "spa-step-cost-collections: PASS, collection=$COLLECTION_MODE browser=$BROWSER_MODE"
