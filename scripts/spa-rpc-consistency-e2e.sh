#!/usr/bin/env bash
#
# scripts/spa-rpc-consistency-e2e.sh — browser e2e: a Sky.Spa (web:app) client
# gives the answer Sky.Live gives for the same Msg sequence. Covers RPC
# serialisation + send-time snapshots + ordered rebase (a draft typed during an
# in-flight RPC survives), the client + server guard, a server branch's
# follow-up Cmd, retry-without-double-apply (request-id dedupe), the ordered
# retry queue, and reload persistence with server-only fields from the SSR seed.
# See scripts/spa-rpc-consistency-verify.mjs for each check. Proven to FAIL on
# a pre-fix compiler and PASS on the fixed one.
#
# Prereqs (all fail loudly): a fresh sky-out/sky, node + playwright.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"
if [ ! -x "$SKY" ]; then
  echo "spa-rpc-consistency-e2e: $SKY not found — run ./scripts/build.sh first." >&2
  exit 1
fi
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"
command -v node >/dev/null 2>&1 || { echo "spa-rpc-consistency-e2e: 'node' is required." >&2; exit 1; }

# A stable fixture directory, emptied first: the shared gate build cache
# (scripts/lib/gate-build-cache.sh) keys on the project path, so a fresh
# `mktemp -d` per run could never reuse a build.
source "$ROOT/scripts/lib/gate-build-cache.sh"
FX="$(gate_e2e_dir "$ROOT" spa-rpc-consistency)"
rm -rf "$FX"
mkdir -p "$FX"
cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/spa-rpc-consistency/." "$FX/"

echo "==> building the rpc-consistency fixture (--target web:app)"
gate_cached_build "$SKY" "$FX" --clean --artefact .skyapp/web-app -- build --target web:app src/Main.sky

APP="$FX/.skyapp/web-app/.split/backend/sky-out/app"
[ -x "$APP" ] || { echo "spa-rpc-consistency-e2e: backend app not built at $APP" >&2; exit 1; }

echo "==> driving the wasm client"
node "$ROOT/scripts/spa-rpc-consistency-verify.mjs" "$APP" --port "${PORT:-9221}"

echo "spa-rpc-consistency-e2e: PASS — web:app matches Sky.Live."
rm -rf "$FX"
