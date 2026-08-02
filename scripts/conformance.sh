#!/usr/bin/env bash
# conformance.sh — run the Layer-1 stdlib behavioral conformance suites.
#
# These are Sky-source `sky test` suites under tests/conformance/tests/ that
# assert documented stdlib SEMANTICS with ADVERSARIAL inputs (not happy-path) —
# the layer that catches "compiles-clean-behaves-wrong" bugs the corpus gates +
# differential oracle miss (they check "builds" + "matches oracle", not "the
# stdlib behaves correctly at runtime").
#
# Each suite runs against an in-process sqlite DB where relevant. Exit non-zero
# if ANY suite has a failing assertion — wire this into `sky verify` / CI /
# the release gate.
#
# Usage: scripts/conformance.sh            # run all suites
#        scripts/conformance.sh Store      # run only *Store* suites
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PROJ="$ROOT/tests/conformance"
FILTER="${1:-}"
SKY="${SKY_BIN:-sky}"

# Portable per-suite timeout. GNU coreutils `timeout` is NOT on macOS runners by
# default (only `gtimeout` via `brew install coreutils`), so a bare `timeout`
# fails with "command not found" and every suite errors. Resolve to whichever
# exists; if neither, run without a per-suite timeout (the CI job still has its
# own outer timeout, so a hang is still bounded).
TIMEOUT_BIN="$(command -v timeout 2>/dev/null || command -v gtimeout 2>/dev/null || true)"
run_suite() { # run_suite <secs> <cmd...>
    local secs="$1"; shift
    if [ -n "$TIMEOUT_BIN" ]; then "$TIMEOUT_BIN" "$secs" "$@"; else "$@"; fi
}

if [ ! -d "$PROJ/tests" ]; then
    echo "conformance: no suites at $PROJ/tests" >&2
    exit 2
fi

cd "$PROJ"
fail=0
ran=0
for suite in tests/*Test.sky; do
    [ -e "$suite" ] || continue
    base="$(basename "$suite" .sky)"
    if [ -n "$FILTER" ] && [[ "$base" != *"$FILTER"* ]]; then
        continue
    fi
    ran=$((ran + 1))
    echo "── $base ──────────────────────────────────────────"
    # unique out dir per suite so parallel/repeat runs don't clobber
    out="sky-out-conf-$base"
    if run_suite 180 "$SKY" test "$suite" --out "$out" 2>&1 | tee "/tmp/conf-$base.log" | tail -30; then
        # `sky test` exit code is the source of truth; also guard on the summary line
        if grep -qE "[1-9][0-9]* failed" "/tmp/conf-$base.log"; then
            fail=$((fail + 1))
        fi
    else
        echo "!! $base: sky test exited non-zero" >&2
        fail=$((fail + 1))
    fi
    echo ""
done

echo "════════════════════════════════════════════════════════"
if [ "$ran" -eq 0 ]; then
    echo "conformance: no suites matched filter '${FILTER}'" >&2
    exit 2
fi
if [ "$fail" -eq 0 ]; then
    echo "conformance: ALL $ran suite(s) passed"
    exit 0
else
    echo "conformance: $fail of $ran suite(s) FAILED"
    exit 1
fi
