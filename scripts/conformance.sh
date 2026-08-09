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
# Usage: scripts/conformance.sh                 # run all suites
#        scripts/conformance.sh Store           # run only *Store* suites
#        scripts/conformance.sh --json <path>   # + a machine-readable manifest
#
# --json writes a manifest of (suite, exit_code, per-suite Sky.Test report).
# It deliberately does NOT aggregate: this script emits only values it
# controls (a basename, an integer, a path) and never parses JSON, because a
# shell that parses its own output is how `grep -qE "0 fail"` came to match
# inside "10 fail". The aggregation and the verdict belong to the caller, which
# has a real JSON parser — see `xtask harness`'s `conformance` gate.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PROJ="$ROOT/tests/conformance"

FILTER=""
JSON_OUT=""
while [ $# -gt 0 ]; do
    case "$1" in
        --json)
            [ $# -ge 2 ] || { echo "conformance: --json requires a path" >&2; exit 2; }
            JSON_OUT="$2"; shift 2 ;;
        --*)
            echo "conformance: unknown option $1" >&2; exit 2 ;;
        *)
            FILTER="$1"; shift ;;
    esac
done
# Prefer the compiler in THIS tree over whatever `sky` is installed on PATH.
# Defaulting to PATH meant the behavioural conformance suite could certify a
# months-old installed binary while the tree under test was never exercised —
# a pass that says nothing about the change being verified. doc-examples.sh
# already resolves in this order; conformance now agrees with it.
SKY="${SKY_BIN:-}"
if [ -z "$SKY" ]; then
    if [ -x "$ROOT/sky-out/sky" ]; then SKY="$ROOT/sky-out/sky"; else SKY="sky"; fi
fi

# Portable per-suite timeout. GNU coreutils `timeout` is NOT on macOS runners by
# default (only `gtimeout` via `brew install coreutils`), so a bare `timeout`
# fails with "command not found" and every suite errors. Resolve to whichever
# exists; if neither, run without a per-suite timeout (the CI job still has its
# own outer timeout, so a hang is still bounded).
TIMEOUT_BIN="$(command -v timeout 2>/dev/null || command -v gtimeout 2>/dev/null || true)"
run_suite() { # run_suite <secs> <cmd...>
    local secs="$1"; shift
    if [ -n "$TIMEOUT_BIN" ]; then "$TIMEOUT_BIN" "$secs" "$@"; return $?; fi
    # No GNU timeout — do NOT fall through unbounded. macOS runners ship
    # neither `timeout` nor `gtimeout`, and the macos-determinism job that runs
    # this script has no timeout-minutes either, so the "the CI job still has
    # its own outer timeout" assumption in the note above was false: a wedged
    # `sky test` burned the full 6-hour GitHub default at the macOS minute
    # multiplier. Race a killer against the command instead (the same portable
    # fallback example-sweep.sh and verify-ui-showcase.sh already use).
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

if [ ! -d "$PROJ/tests" ]; then
    echo "conformance: no suites at $PROJ/tests" >&2
    exit 2
fi

cd "$PROJ"
fail=0
ran=0

# Per-suite Sky.Test JSON reports live here; the manifest points at them.
REPORT_DIR="$ROOT/.skycache/conformance-reports"
if [ -n "$JSON_OUT" ]; then
    rm -rf "$REPORT_DIR"
    mkdir -p "$REPORT_DIR"
    : >| "$JSON_OUT.entries"
fi

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
    report=""
    if [ -n "$JSON_OUT" ]; then
        report="$REPORT_DIR/$base.json"
        rm -f "$report"
    fi
    rc=0
    # Exported explicitly rather than as an assignment prefix: the prefix form
    # in front of a shell FUNCTION has shell-dependent export semantics, and a
    # silently-unset SKY_TEST_JSON would produce an empty manifest that the
    # caller would have to treat as a failure.
    export SKY_TEST_JSON="$report"
    run_suite 180 "$SKY" test "$suite" --out "$out" 2>&1 \
        | tee "/tmp/conf-$base.log" | tail -30
    rc=${PIPESTATUS[0]}
    unset SKY_TEST_JSON
    if [ "$rc" -ne 0 ]; then
        echo "!! $base: sky test exited non-zero (rc=$rc)" >&2
        fail=$((fail + 1))
    fi
    if [ -n "$JSON_OUT" ]; then
        printf '{"name":"%s","exit_code":%d,"report":"%s"},\n' \
            "$base" "$rc" "$report" >> "$JSON_OUT.entries"
    fi
    echo ""
done

if [ -n "$JSON_OUT" ]; then
    {
        printf '{\n  "suites": [\n'
        # strip the trailing comma from the last entry
        sed '$ s/,$//' "$JSON_OUT.entries"
        printf '  ],\n  "suites_run": %d,\n  "filter": "%s"\n}\n' "$ran" "$FILTER"
    } >| "$JSON_OUT"
    rm -f "$JSON_OUT.entries"
fi

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
