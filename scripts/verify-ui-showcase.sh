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
APP_DIR="$ROOT/examples/26-ui-showcase"
RUNNER="$ROOT/scripts/verify-ui-showcase.mjs"
SKY="$ROOT/sky-out/sky"

[[ -x "$SKY" ]] || { echo "missing $SKY — run scripts/build.sh first" >&2; exit 2; }
[[ -d "$APP_DIR" ]] || { echo "missing $APP_DIR" >&2; exit 2; }

UPDATE=0
for arg in "$@"; do
    case "$arg" in
        --update-baseline) UPDATE=1 ;;
        --help|-h) sed -n '2,15p' "$0"; exit 0 ;;
        *) echo "unknown flag: $arg" >&2; exit 2 ;;
    esac
done

# Cross-platform timeout shim (mirrors example-sweep.sh).
if command -v timeout >/dev/null 2>&1; then
    TIMEOUT_CMD="timeout"
elif command -v gtimeout >/dev/null 2>&1; then
    TIMEOUT_CMD="gtimeout"
else
    TIMEOUT_CMD=""
fi
bounded() {
    local secs="$1"; shift
    if [[ -n "$TIMEOUT_CMD" ]]; then "$TIMEOUT_CMD" "$secs" "$@"; return $?; fi
    "$@" &
    local cmd_pid=$!
    ( sleep "$secs" && kill -KILL "$cmd_pid" 2>/dev/null ) &
    local killer_pid=$!
    local rc=0
    wait "$cmd_pid" 2>/dev/null; rc=$?
    kill -KILL "$killer_pid" 2>/dev/null
    wait "$killer_pid" 2>/dev/null
    return $rc
}

# Build if binary is missing or older than the source tree.
need_build=0
if [[ ! -x "$APP_DIR/sky-out/app" ]]; then
    need_build=1
else
    # Rebuild if any .sky file newer than the binary.
    while IFS= read -r f; do
        if [[ "$f" -nt "$APP_DIR/sky-out/app" ]]; then
            need_build=1
            break
        fi
    done < <(find "$APP_DIR/src" -name "*.sky")
fi

if [[ $need_build -eq 1 ]]; then
    echo "[build] $APP_DIR"
    ( cd "$APP_DIR" && rm -rf sky-out/main.go sky-out/app && \
        TMPDIR=/tmp bounded 90 "$SKY" build src/Main.sky ) || {
            echo "FAIL — sky build failed" >&2
            exit 1
        }
fi

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
bounded 120 env ${env_args[@]+"${env_args[@]}"} SKY_UI_SHOWCASE_PORT="$PORT" \
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
