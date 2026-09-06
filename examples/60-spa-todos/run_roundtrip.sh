#!/usr/bin/env bash
# Reproducible END-TO-END acceptance for the Sky.Spa Todos app: builds the
# stateless Sky.Http.Server backend (SQLite store) and the Sky.Spa wasm client
# (both importing the ONE symlinked Shared.sky), starts the backend on a CLEAN
# database, then drives the real wasm client against it headlessly — asserting
# durable add/toggle/rename/delete round-trips, the zero-round-trip pure-UI
# property, client-side routing, and rehydration on reload.
#
# Usage: SKY=/path/to/sky TODOS_PORT=8951 ./run_roundtrip.sh
#   SKY defaults to the repo's sky-out/sky three levels up.
#   TODOS_PORT defaults to 8951 (a unique high port to avoid squatters).
set -euo pipefail
cd "$(dirname "$0")"

SKY="${SKY:-$(cd ../.. && pwd)/sky-out/sky}"

# Never measure a compiler older than this tree: a bare `cargo build -p sky`
# leaves sky-out/sky untouched, so a stale binary could "prove" a round-trip it
# never compiled. (scripts/lib/fresh-compiler.sh; enforced by the xtask gate
# gates_measure_a_fresh_compiler.)
ROOT="$(cd ../.. && pwd)"
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"

TODOS_PORT="${TODOS_PORT:-8951}"
export TODOS_PORT
export TODOS_BASE="http://localhost:${TODOS_PORT}"
# The backend runs with cwd=server/, so the SQLite file lands at server/app.db.
export SKY_DB_PATH="app.db"
GOROOT_WASM="$(go env GOROOT)/lib/wasm/wasm_exec.js"

echo "==> building stateless backend (server/)"
( cd server && "$SKY" build src/Main.sky >/dev/null )

echo "==> building Sky.Spa client -> wasm (client/)"
# Manual split: build the client as a RAW wasm client (`--wasm`), NOT via the
# auto-split that a bare `sky build` on a `Spa.app` entry now runs. See run.sh.
( cd client && "$SKY" build --wasm src/Main.sky >/dev/null \
    && cd sky-out && GOOS=js GOARCH=wasm go build -o ../main.wasm . \
    && rm -f ../wasm_exec.js && cp "$GOROOT_WASM" ../wasm_exec.js \
    && chmod u+w ../wasm_exec.js )

echo "==> publishing static assets for the same-origin browser path (public/)"
cp -f client/main.wasm public/main.wasm
cp -f client/wasm_exec.js public/wasm_exec.js

echo "==> bundle size"
RAW=$(wc -c < client/main.wasm)
GZ=$(gzip -c client/main.wasm | wc -c)
echo "    main.wasm raw=${RAW} bytes  gzip=${GZ} bytes"

echo "==> starting backend on :$TODOS_PORT (clean DB)"
rm -f server/app.db server/app.db-* server/todos.db server/todos.db-* 2>/dev/null || true
( cd server && ./sky-out/app ) >/tmp/spa-todos-server-$TODOS_PORT.log 2>&1 &
SERVER_PID=$!
trap 'kill "$SERVER_PID" 2>/dev/null || true' EXIT
sleep 2

echo "==> backend answers the shared-codec JSON (empty list to start):"
curl -sS -m 5 "$TODOS_BASE/api/todos"; echo

echo "==> driving the real wasm client against the real backend (headless)"
( cd client && node run_headless.cjs )
