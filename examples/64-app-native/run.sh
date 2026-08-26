#!/usr/bin/env bash
# Build + run the Sky.Spa NATIVE-CAPABILITY playground for a REAL browser.
#
# A client-only Sky.Spa app: every button drives a `Std.Native.*` capability
# (clipboard, local storage, geolocation, share, vibrate, online status,
# language, tab title) through the ordinary TEA loop. Because the ONLY effects
# are native (client) ones, `sky spa-split` keeps ALL of them in the wasm
# frontend — the derived backend just serves the static bundle (no /_rpc/).
#
# Try it: type in Draft, then Copy→Paste (clipboard round-trip), Save→Load
# (localStorage), Locate (grant location), Online?/Language, Vibrate (a phone),
# Share (a phone's share sheet), Set tab title. Each result shows in the log.
#
# Usage:  ./run.sh          (Ctrl-C to stop; PORT defaults to 8951)
set -euo pipefail
cd "$(dirname "$0")"

ROOT="$(cd ../.. && pwd)"
SKY="${SKY:-$ROOT/sky-out/sky}"

# Never measure a compiler older than this tree.
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"

PORT="${PORT:-8951}"
export PORT

# `sky run` on a Spa.app entry AUTO-SPLITS (wasm frontend + native backend under
# .split/) and runs the backend. Here every effect is a client-native one, so the
# split keeps ALL branches in the wasm frontend and the backend just serves the
# static bundle (no /_rpc). (Explicit form: `sky spa-split src/Main.sky --out
# .split --build` then `cd .split/backend && ./sky-out/app`.)
rm -rf .split
echo "==> sky run auto-splits src/ and runs the backend"

echo ""
echo "==> open  http://localhost:${PORT}/  in a browser"
echo "    Copy→Paste, Save→Load, Locate (grant location), Online?/Language,"
echo "    Vibrate + Share (best on a phone), Set tab title — results in the log."
echo "    (Ctrl-C to stop)"
echo ""
exec "$SKY" run src/Main.sky
