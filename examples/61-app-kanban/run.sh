#!/usr/bin/env bash
# Build the client-only Sky.Spa Kanban to wasm and serve the static frontend
# bundle for a real browser. There is NO backend at runtime — the whole app runs
# in the browser and all state is in-memory (lost on reload).
#
# `--target web:app` compiles the `App.web` source to a wasm client. Because
# every `update` branch is pure (`Cmd.none`, no effects), the auto-split's
# backend carries no RPC and the FRONTEND bundle runs standalone: deploying it
# for real is just copying the emitted dist/ folder to any static host.
#
# Usage:  ./run.sh            # serves on http://localhost:8971/
#         KANBAN_PORT=9000 ./run.sh
set -euo pipefail
cd "$(dirname "$0")"

SKY="${SKY:-$(cd ../.. && pwd)/sky-out/sky}"

# Never measure a compiler older than this tree
# (scripts/lib/fresh-compiler.sh; enforced by gates_measure_a_fresh_compiler).
ROOT="$(cd ../.. && pwd)"
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"

KANBAN_PORT="${KANBAN_PORT:-8971}"

echo "==> building the wasm client (--target web:app)"
"$SKY" build --target web:app src/Main.sky >/dev/null

# The servable client bundle the auto-split emits (index.html + hashed main.wasm
# + wasm_exec.js). Copy this folder to any static host to deploy.
DIST=".skyapp/web-app/.split/frontend/dist"

echo ""
echo "==> serving ${DIST} (static — the frontend runs with NO backend) at:"
echo "    http://localhost:${KANBAN_PORT}/"
echo "    (Ctrl-C to stop)"
echo ""
cd "$DIST" && exec python3 -m http.server "$KANBAN_PORT"
