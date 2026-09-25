#!/usr/bin/env bash
#
# scripts/ui-forms-e2e.sh — browser e2e for three "compiles, then fails at run
# time" holes, on BOTH web targets (Sky.Live --target web, Sky.Spa --target
# web:app):
#
#   * Ui.onKeyDown crashed the view on every render (the stdlib passed a bare
#     Msg to the key-carrying Html handler).
#   * Ui.onSubmit into a typed record zero-filled Int / Bool fields; a form
#     whose field does not parse must now be dropped with a FormDecode error.
#   * A literal-topic publish + subscribe that agree still work (Sky.Live).
#
# Builds rust/crates/sky/tests/fixtures/ui-forms-e2e from a scratch copy and
# drives scripts/ui-forms-e2e-verify.mjs. Proven to FAIL on a pre-fix compiler
# (Live: "Render error rt.Coerce: expected func(string) …"; Spa: a 500 on the
# first render) and PASS on the fixed one.
#
# Prereqs (all fail loudly): a fresh sky-out/sky, node + playwright.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SKY="$ROOT/sky-out/sky"
if [ ! -x "$SKY" ]; then
  echo "ui-forms-e2e: $SKY not found — run ./scripts/build.sh first." >&2
  exit 1
fi
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"
command -v node >/dev/null 2>&1 || { echo "ui-forms-e2e: 'node' is required." >&2; exit 1; }

# A stable fixture directory, emptied first: the shared gate build cache
# (scripts/lib/gate-build-cache.sh) keys on the project path, so a fresh
# `mktemp -d` per run could never reuse a build.
source "$ROOT/scripts/lib/gate-build-cache.sh"
FX="$(gate_e2e_dir "$ROOT" ui-forms-e2e)"
rm -rf "$FX"
mkdir -p "$FX"
cp -Rf "$ROOT/rust/crates/sky/tests/fixtures/ui-forms-e2e/." "$FX/"

echo "==> building the fixture (--target web)"
gate_cached_build "$SKY" "$FX" --clean --artefact .skyapp/web -- build --target web src/Main.sky
echo "==> building the fixture (--target web:app)"
gate_cached_build "$SKY" "$FX" --clean --artefact .skyapp/web-app -- build --target web:app src/Main.sky

LIVE_APP="$FX/.skyapp/web/sky-out/app"
SPA_APP="$FX/.skyapp/web-app/.split/backend/sky-out/app"
[ -x "$LIVE_APP" ] || { echo "ui-forms-e2e: Sky.Live app not built at $LIVE_APP" >&2; exit 1; }
[ -x "$SPA_APP" ] || { echo "ui-forms-e2e: Sky.Spa backend not built at $SPA_APP" >&2; exit 1; }

echo "==> driving Sky.Live"
node "$ROOT/scripts/ui-forms-e2e-verify.mjs" "$LIVE_APP" --mode live --port "${LIVE_PORT:-9262}"
echo "==> driving Sky.Spa"
node "$ROOT/scripts/ui-forms-e2e-verify.mjs" "$SPA_APP" --mode spa --port "${SPA_PORT:-9263}"

echo "ui-forms-e2e: PASS — onKeyDown renders + dispatches, typed forms decode strictly, literal-topic pub/sub delivers."
rm -rf "$FX"
