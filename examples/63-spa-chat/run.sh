#!/usr/bin/env bash
# Build + run the Sky.Spa CHAT app for a REAL browser.
#
# This project is the AUTO-SPLIT INPUT: ONE Sky.Spa project with effects inline
# (src/Main.sky + src/Domain.sky + src/Persist.sky). It is NOT built directly —
# `sky spa-split` derives three normal Sky projects from it:
#
#   .split/frontend/  — the wasm client (pure view + client-local update branches;
#                       each effectful branch rewritten to an RPC). Built --target web.
#   .split/backend/   — the native Sky.Http.Server: owns the SQLite store, runs each
#                       effectful branch behind POST /_rpc/<Msg>, serves the frontend,
#                       AND fans every Cmd.publish out over SSE at GET /_sky/sub.
#   .split/shared/    — the wire contract (per-RPC req/resp records + codecs).
#
# The backend serves the frontend + /_rpc + /_sky/sub SAME-ORIGIN, so ONE binary
# runs the whole demo. The DB (SQLite) PERSISTS across restarts and page reloads.
#
# REAL-TIME PUSH: because `update`'s Send branch uses `Cmd.publish "room:main" …`
# and `subscriptions` uses `Sub.subscribeTopic "room:main" …`, the generator
# mounts an in-process broker + SSE endpoint automatically. A message sent by ANY
# open tab appears LIVE in EVERY open tab — no reload.
#
#   SINGLE replica (this default):  in-process broker — perfect for one instance.
#   CROSS-replica fan-out:          pass a Redis URL so a publish on replica A
#                                   reaches an SSE subscriber on replica B, e.g.
#
#       "$SKY" spa-split src/Main.sky --out .split --build --broker redis://localhost:6379
#
#   (A multi-replica deploy also needs sticky routing so a client's /_sky/sub and
#   /_rpc/* hit a coherent instance. SKY_LIVE_BROKER_URL overrides the baked URL
#   at runtime.)
#
# Usage:  PORT=8972 ./run.sh          (Ctrl-C to stop)
set -euo pipefail
cd "$(dirname "$0")"

ROOT="$(cd ../.. && pwd)"
SKY="${SKY:-$ROOT/sky-out/sky}"

# Never measure a compiler older than this tree (scripts/lib/fresh-compiler.sh;
# enforced by the xtask gate gates_measure_a_fresh_compiler). Rebuild with
# ./scripts/build.sh if this trips.
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"

PORT="${PORT:-8972}"
export PORT
# The backend runs with cwd=.split/backend, so the SQLite file lands there.
export SKY_DB_PATH="${SKY_DB_PATH:-$(pwd)/.split/backend/chat.db}"

echo "==> deriving frontend + backend from src/ via sky spa-split"
rm -rf .split
"$SKY" spa-split src/Main.sky --out .split --build

echo ""
echo "==> open  http://localhost:${PORT}/  in TWO browser tabs"
echo "    - type a name + a message in tab A and hit Send"
echo "    - tab B shows it LIVE over SSE (/_sky/sub), no reload"
echo "    - compose/rename are pure client-local (zero network — watch DevTools)"
echo "    - the history survives a server restart AND a page reload (boot Load RPC)"
echo "    (Ctrl-C to stop)"
echo ""
cd .split/backend && exec ./sky-out/app
