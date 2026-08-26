#!/usr/bin/env bash
# Build the client-only Sky.Spa Kanban to wasm and serve the static bundle for a
# real browser. There is NO backend — the whole app runs in the browser and all
# state is in-memory (lost on reload). Deploying it for real is just copying the
# three files in dist/ to any static host.
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

echo "==> building the wasm client → dist/"
"$SKY" build --target web src/Main.sky >/dev/null

echo ""
echo "==> serving dist/ (static — NO backend) at:"
echo "    http://localhost:${KANBAN_PORT}/"
echo "    (Ctrl-C to stop)"
echo ""
cd dist && exec python3 -m http.server "$KANBAN_PORT"
