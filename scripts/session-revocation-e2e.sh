#!/usr/bin/env bash
#
# scripts/session-revocation-e2e.sh — browser e2e for the Sky.Spa sign-out:
# a copy of the `sky_spa` cookie taken before sign-out must not sign the user
# in after sign-out (docs/skyspa/auto-split.md §25).
#
# Builds rust/crates/sky/tests/fixtures/spa-session-revocation with
# --target web:app and drives it in real browsers
# (scripts/session-revocation-verify.mjs): a control replay of a live cookie,
# the client-side sign-out, a server-branch sign-out, and a fresh sign-in.
#
# Browsers: SKY_E2E_BROWSERS (default "chromium,webkit"); SKY_E2E_CHANNEL=chrome
# uses Google Chrome; SKY_E2E_HEADED=1 runs them headed.
#
# Prereqs (all fail loudly): a fresh sky-out/sky, go, node + playwright.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"
if [ ! -x "$SKY" ]; then
  echo "session-revocation-e2e: $SKY not found — run ./scripts/build.sh first." >&2
  exit 1
fi
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"
source "$ROOT/scripts/lib/with-timeout.sh"
source "$ROOT/scripts/lib/require-tool.sh"
require_tool node "install Node.js 20+"
require_tool go "install Go 1.25+"

source "$ROOT/scripts/lib/gate-build-cache.sh"
_gc_compiler_hash "$SKY" >/dev/null
TMP="$(gate_e2e_dir "$ROOT" session-revocation-e2e)"
rm -rf "$TMP"
mkdir -p "$TMP/app"
cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/spa-session-revocation/." "$TMP/app/"

echo "==> building fixture spa-session-revocation (--target web:app)"
(cd "$TMP/app" && with_timeout 1200 "$SKY" build --target web:app src/Main.sky) >"$TMP/build.log" 2>&1 \
  || { cat "$TMP/build.log" >&2; echo "session-revocation-e2e: web:app build failed" >&2; exit 1; }
SPA="$TMP/app/.split/backend/sky-out/app"
[ -x "$SPA" ] || { echo "session-revocation-e2e: backend not built at $SPA" >&2; exit 1; }

echo "==> driving the sign-out in a browser"
SKY_E2E_BROWSERS="${SKY_E2E_BROWSERS:-chromium,webkit}" \
  with_timeout 600 node "$ROOT/scripts/session-revocation-verify.mjs" "$SPA" --port "${SESSION_REVOCATION_E2E_PORT:-9391}"

echo "session-revocation-e2e: PASS — a cookie copied before sign-out is refused after sign-out."
rm -rf "$TMP"
