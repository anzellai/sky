#!/usr/bin/env bash
# scripts/check-forbidden.sh — guard against the forbidden-pattern
# classes that v1+ Sky rules out of public surfaces.
#
# Fails if any of these appear in user-facing Sky sources
# (src/, sky-stdlib/, examples/*/src/):
#
#   * `Result String …` / `Task String …` — old stringly errors
#   * `Std.IoError` — deleted pre-v1 error ADT
#   * `RemoteData` — deleted pre-v1 async-state type
#
# Counterparts of these are enforced in cabal via
# test/Sky/ErrorUnificationSpec.hs. This script mirrors the check
# for quick local / CI runs.

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

FAIL=0

check() {
    local label="$1"
    local pattern="$2"
    local matches
    matches=$(grep -rn --include='*.sky' --exclude-dir=.skycache \
        --exclude-dir=.skydeps --exclude-dir=sky-out \
        "$pattern" \
        src sky-stdlib examples/*/src 2>/dev/null \
        | grep -vE '^[^:]*:[0-9]+:[[:space:]]*--')
    if [[ -n "$matches" ]]; then
        echo "FORBIDDEN ($label):"
        echo "$matches" | head -10
        FAIL=1
    fi
}

check "Result String"  'Result[[:space:]]\+String[[:space:]]'
check "Task String"    'Task[[:space:]]\+String[[:space:]]'
check "Std.IoError"    'Std\.IoError'
check "RemoteData"     '\bRemoteData\b'

if (( FAIL == 0 )); then
    echo "forbidden-pattern gate: clean"
    exit 0
fi
exit 1
