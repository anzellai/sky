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
# Usage:  PORT=8974 ./run.sh          (Ctrl-C to stop)
set -euo pipefail
cd "$(dirname "$0")"

ROOT="$(cd ../.. && pwd)"
SKY="${SKY:-$ROOT/sky-out/sky}"

# Never measure a compiler older than this tree.
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"

PORT="${PORT:-8974}"
export PORT

echo "==> deriving frontend + backend from src/ via sky spa-split"
rm -rf .split
"$SKY" spa-split src/Main.sky --out .split --build

echo ""
echo "==> open  http://localhost:${PORT}/  in a browser"
echo "    Copy→Paste, Save→Load, Locate (grant location), Online?/Language,"
echo "    Vibrate + Share (best on a phone), Set tab title — results in the log."
echo "    (Ctrl-C to stop)"
echo ""
cd .split/backend && exec ./sky-out/app
