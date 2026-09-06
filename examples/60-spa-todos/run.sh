#!/usr/bin/env bash
# Serve the Sky.Spa Todos app for a REAL BROWSER, same-origin from the single
# stateless backend binary. Builds the backend + the wasm client, publishes the
# client assets into public/, starts the backend, and prints the URL to open.
#
# Open http://localhost:$TODOS_PORT/ in a browser:
#   * type in the input + switch filters (All/Active/Completed via the links) —
#     these are pure client-side transitions, no network (watch DevTools);
#   * Add / toggle / delete / rename round-trip to the backend and persist to
#     SQLite (survives a server restart and a page reload);
#   * the filter links use the History API — Back/Forward work, no page reload.
#
# Usage: SKY=/path/to/sky TODOS_PORT=8951 ./run.sh
set -euo pipefail
cd "$(dirname "$0")"

SKY="${SKY:-$(cd ../.. && pwd)/sky-out/sky}"

# Never measure a compiler older than this tree (scripts/lib/fresh-compiler.sh;
# enforced by the xtask gate gates_measure_a_fresh_compiler).
ROOT="$(cd ../.. && pwd)"
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"

TODOS_PORT="${TODOS_PORT:-8951}"
export TODOS_PORT
# The backend runs with cwd=server/, so the SQLite file lands at server/app.db.
export SKY_DB_PATH="app.db"
GOROOT_WASM="$(go env GOROOT)/lib/wasm/wasm_exec.js"

echo "==> building backend + wasm client"
( cd server && "$SKY" build src/Main.sky >/dev/null )
# This is a MANUAL split (a hand-written client/ + server/ + symlinked shared/),
# so the client is a RAW Sky.Spa wasm client — build it with `--wasm` to skip the
# auto-split that a bare `sky build` on a `Spa.app` entry would run (the auto-split
# is for a single unified source; this client already IS the client half). `--wasm`
# emits the client's Go under client/sky-out/, which the standard Go toolchain then
# compiles to wasm below.
( cd client && "$SKY" build --wasm src/Main.sky >/dev/null \
    && cd sky-out && GOOS=js GOARCH=wasm go build -o ../main.wasm . )
cp -f client/main.wasm public/main.wasm
# The client is built with the STANDARD Go toolchain above (GOOS=js GOARCH=wasm),
# so its loader MUST be that Go's wasm_exec.js — always. A stale
# client/wasm_exec.js left by a TinyGo build has different runtime imports
# (no runtime.scheduleTimeoutEvent) and the browser rejects the module with a
# LinkError, so never prefer it here.
cp -f "$GOROOT_WASM" public/wasm_exec.js

echo ""
echo "==> open  http://localhost:${TODOS_PORT}/  in your browser"
echo "    (Ctrl-C to stop)"
echo ""
cd server && exec ./sky-out/app
