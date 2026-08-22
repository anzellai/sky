#!/usr/bin/env bash
# Reproducible end-to-end proof of the Sky.Spa EXPLICIT TYPED SERVER BOUNDARY:
# builds the stateless Sky.Http.Server backend and the Sky.Spa wasm client (both
# importing the ONE symlinked Shared.sky), runs the backend, then drives the
# real wasm client against it headlessly and asserts the shared-codec decode.
#
# Usage: SKY=/path/to/sky PORT=8942 ./run_roundtrip.sh   (SKY defaults to the
# repo's sky-out/sky two levels up; PORT defaults to 8942, matching the client's
# apiBase in client/src/Main.sky).
set -euo pipefail
cd "$(dirname "$0")"

# Absolute so the `cd server`/`cd client` subshells resolve it correctly.
SKY="${SKY:-$(cd .. && pwd)/sky-out/sky}"
PORT="${PORT:-8942}"
GOROOT_WASM="$(go env GOROOT)/lib/wasm/wasm_exec.js"

echo "==> building stateless backend (server/)"
( cd server && "$SKY" build src/Main.sky >/dev/null )

echo "==> building Sky.Spa client -> wasm (client/)"
( cd client && "$SKY" build src/Main.sky >/dev/null \
    && cd sky-out && GOOS=js GOARCH=wasm go build -o ../main.wasm . \
    && rm -f ../wasm_exec.js && cp "$GOROOT_WASM" ../wasm_exec.js \
    && chmod u+w ../wasm_exec.js )

echo "==> starting backend on :$PORT"
( cd server && ./sky-out/app ) >/tmp/spa-boundary-server.log 2>&1 &
SERVER_PID=$!
trap 'kill "$SERVER_PID" 2>/dev/null || true' EXIT
sleep 2

echo "==> backend serves the shared-codec JSON:"
curl -sS -m 5 "http://localhost:$PORT/api/widget"; echo

echo "==> driving the real wasm client against the real backend (headless)"
( cd client && node run_headless.cjs )
