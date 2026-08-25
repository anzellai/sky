#!/usr/bin/env bash
# Build + run the Sky.Spa NOTES app for a REAL browser.
#
# This project is the AUTO-SPLIT INPUT: ONE Sky.Spa project with effects inline
# (src/Main.sky + src/Domain.sky + src/Persist.sky). It is NOT built directly —
# `sky spa-split` derives three normal Sky projects from it:
#
#   .split/frontend/  — the wasm client (pure view + client-local update branches;
#                       each effectful branch rewritten to an RPC). Built --target web.
#   .split/backend/   — the native Sky.Http.Server: owns the SQLite store, runs each
#                       effectful branch behind POST /_rpc/<Msg>, serves the frontend.
#   .split/shared/    — the wire contract (per-RPC req/resp records + codecs).
#
# The backend serves the frontend + /_rpc same-origin, so ONE binary runs the whole
# demo. The DB (SQLite) PERSISTS across restarts and page reloads.
#
# Usage:  PORT=8971 ./run.sh          (Ctrl-C to stop)
set -euo pipefail
cd "$(dirname "$0")"

ROOT="$(cd ../.. && pwd)"
SKY="${SKY:-$ROOT/sky-out/sky}"

# Never measure a compiler older than this tree (scripts/lib/fresh-compiler.sh;
# enforced by the xtask gate gates_measure_a_fresh_compiler). Rebuild with
# ./scripts/build.sh if this trips.
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"

PORT="${PORT:-8971}"
export PORT
# The backend runs with cwd=.split/backend, so the SQLite file lands there.
export SKY_DB_PATH="${SKY_DB_PATH:-$(pwd)/.split/backend/notes.db}"

# `sky run` on a Spa.app entry AUTO-SPLITS (wasm frontend + native backend under
# .split/) and runs the backend — it serves the frontend + /_rpc same-origin.
# (The explicit form is `sky spa-split src/Main.sky --out .split --build` then
# `cd .split/backend && ./sky-out/app`.)
rm -rf .split
echo "==> sky run auto-splits src/ and runs the backend"
echo ""
echo "==> open  http://localhost:${PORT}/  in your browser"
echo "    - select/edit/search are pure client-local (zero network — watch DevTools)"
echo "    - New note / Save / Delete round-trip to POST /_rpc/<Msg> and persist to SQLite"
echo "    - the note list survives a server restart AND a page reload (boot Load RPC)"
echo "    (Ctrl-C to stop)"
echo ""
exec "$SKY" run src/Main.sky
