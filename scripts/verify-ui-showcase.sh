#!/usr/bin/env bash
# scripts/verify-ui-showcase.sh
#
# Std.Ui regression gates — Cycle 5 renderer-churn guard.
# Builds examples/26-ui-showcase if needed, then runs the Playwright
# runner under a bounded timeout. CLAUDE.md §2.3 — every long
# command MUST be timeout-bounded.
#
# Flags:
#   --update-baseline   re-record the snapshots/ baselines (review
#                       the diff with human eyes; never commit
#                       a baseline update blind)
#
# Exit: 0 = green, 1 = any computed-style or snapshot regression.

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# `with_timeout <secs> <cmd...>` — the one time bound. See the header of
# scripts/lib/with-timeout.sh for what a bare `timeout` did when it went missing.
source "$ROOT/scripts/lib/with-timeout.sh"
# `require_tool <name> <hint>` — see scripts/lib/require-tool.sh.
source "$ROOT/scripts/lib/require-tool.sh"
require_tool node "install Node 20+ — scripts/verify-ui-showcase.mjs is a Playwright program"
APP_DIR="$ROOT/examples/26-ui-showcase"
RUNNER="$ROOT/scripts/verify-ui-showcase.mjs"
SKY="$ROOT/sky-out/sky"

# `require_fresh_compiler <bin>` — a snapshot baseline taken with a stale
# compiler pins the rendering of a tree that is no longer here. See the header
# of scripts/lib/fresh-compiler.sh.
source "$ROOT/scripts/lib/fresh-compiler.sh"
require_fresh_compiler "$SKY" "$ROOT"
[[ -d "$APP_DIR" ]] || { echo "missing $APP_DIR" >&2; exit 2; }

UPDATE=0
for arg in "$@"; do
    case "$arg" in
        --update-baseline) UPDATE=1 ;;
        --help|-h) sed -n '2,15p' "$0"; exit 0 ;;
        *) echo "unknown flag: $arg" >&2; exit 2 ;;
    esac
done


# Build through the shared gate build cache (scripts/lib/gate-build-cache.sh):
# a hit restores the artefact a clean build of this exact compiler + source
# produced (usually the example sweep's), a miss builds clean and stores.
#
# This used to rebuild only when a `src/*.sky` file was newer than the binary,
# so a rebuilt COMPILER with unchanged source left the previous compiler's
# binary under test — the ui-showcase gate then certified codegen it never ran.
echo "[build] $APP_DIR"
with_timeout 900 env TMPDIR=/tmp bash "$ROOT/scripts/lib/gate-build-cache.sh" build \
    "$SKY" "$APP_DIR" --clean -- build src/Main.sky || {
    echo "FAIL — sky build failed" >&2
    exit 1
}

# Kill any leftover holder of the port.
PORT="${SKY_UI_SHOWCASE_PORT:-8826}"
existing=$(lsof -ti ":$PORT" 2>/dev/null || true)
[[ -n "$existing" ]] && kill -9 $existing 2>/dev/null || true

env_args=()
[[ $UPDATE -eq 1 ]] && env_args+=("UPDATE_BASELINE=1")

# Honour TMPDIR from caller (CLAUDE.md prefers /tmp); fall back to
# /tmp if the inherited TMPDIR points somewhere Playwright can't
# create artefact dirs in (nix-shell sandbox).
export TMPDIR="${TMPDIR:-/tmp}"
mkdir -p "$TMPDIR" 2>/dev/null || TMPDIR=/tmp

echo "[run] node $RUNNER (port $PORT, timeout 120s, TMPDIR=$TMPDIR)"
with_timeout 120 env ${env_args[@]+"${env_args[@]}"} SKY_UI_SHOWCASE_PORT="$PORT" \
    TMPDIR="$TMPDIR" \
    node "$RUNNER"
rc=$?

if [[ $rc -ne 0 ]]; then
    echo ""
    echo "FAIL — ui-showcase regression gates failed (exit $rc)"
    echo "  Diffs (if any) in .skycache/ui-showcase-diffs/"
    echo "  To re-record baselines deliberately: scripts/verify-ui-showcase.sh --update-baseline"
    exit $rc
fi
exit 0
