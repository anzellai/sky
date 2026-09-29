#!/usr/bin/env bash
#
# scripts/spa-client-crypto-e2e.sh — browser e2e for `withClientCrypto`: a
# Sky.Spa (web:app) client that holds its own Noise IK (BLAKE2s) session.
#
# The fixture rust/crates/sky/tests/fixtures/spa-client-crypto-relay runs the
# initiator in the wasm client and relays only public bytes through its
# backend, in two steps of the same shape (`SendHello` → `GotMsg2`, `SendEcho`
# → `GotEcho`). The responder is a native Go process
# (runtime-go/rt/noisewasm/responder) with the rt kernels a Sky program calls.
# The browser completes the handshake and one transport round trip: the page
# must show `echo pong:ping`, which only a client that holds the transport can
# decrypt. Before the fix the second relay step was settled as a server chain
# and the build refused it ("field `tr` … never crosses between client and
# server").
#
# Prereqs (all fail loudly): a fresh sky-out/sky, go, node + playwright.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"
if [ ! -x "$SKY" ]; then
  echo "spa-client-crypto-e2e: $SKY not found — run ./scripts/build.sh first." >&2
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
TMP="$(gate_e2e_dir "$ROOT" spa-client-crypto-e2e)"
rm -rf "$TMP"
mkdir -p "$TMP"

echo "==> building the Go Noise responder"
(cd "$ROOT/runtime-go" && with_timeout 600 go build -o "$TMP/responder" ./rt/noisewasm/responder)

echo "==> building fixture spa-client-crypto-relay (--target web:app)"
mkdir -p "$TMP/app"
cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/spa-client-crypto-relay/." "$TMP/app/"
(cd "$TMP/app" && with_timeout 1200 "$SKY" build --target web:app src/Main.sky) >"$TMP/build.log" 2>&1 \
  || { cat "$TMP/build.log" >&2; echo "spa-client-crypto-e2e: build failed" >&2; exit 1; }
APP="$TMP/app/.skyapp/web-app/.split/backend/sky-out/app"
[ -x "$APP" ] || { echo "spa-client-crypto-e2e: backend not built at $APP" >&2; exit 1; }

echo "==> driving it in a browser"
with_timeout 300 node "$ROOT/scripts/spa-client-crypto-e2e-verify.mjs" "$APP" "$TMP/responder" \
  --port "${CLIENT_CRYPTO_PORT:-9351}"

echo "spa-client-crypto-e2e: PASS — the wasm client completed a Noise IK handshake and a transport round trip through two relay steps."
rm -rf "$TMP"
