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

# How many suites to run CONCURRENTLY. `CONF_JOBS`, else cores capped at 8.
#
# This loop was serial, and it was the single most expensive step in CI: 1315s
# of the macos-determinism job's 2244s — a job that is setup-INDEPENDENT, so it
# enters the T1 budget formula directly as `indep_max` and was on its own the
# reason the tier blew its 990s ceiling.
#
# Nothing about a suite depends on its neighbours. Each already had a unique
# out dir (the comment below has said "so parallel/repeat runs don't clobber"
# since the day it was written), its own JSON report path and its own log. The
# isolation was designed for this and simply never used.
if [ -z "${CONF_JOBS:-}" ]; then
    CONF_JOBS="$( (sysctl -n hw.ncpu 2>/dev/null || nproc 2>/dev/null || echo 2) )"
    [ "$CONF_JOBS" -gt 8 ] 2>/dev/null && CONF_JOBS=8
fi
[ "$CONF_JOBS" -ge 1 ] 2>/dev/null || CONF_JOBS=1

# `wait -n` is bash 4.3+; macOS runners ship bash 3.2, where it does not exist.
# Throttle by polling the running-job count instead — portable to both.
throttle() { # throttle <max>
    while [ "$(jobs -r | wc -l | tr -d ' ')" -ge "$1" ]; do
        sleep 0.2
    done
}

# One suite, start to finish, in its own process. Writes its exit code to a
# file because a background subshell cannot assign to the parent's variables —
# and a lost failure here would be a suite that silently stops counting.
run_one() { # run_one <suite> <base> <report>
    local suite="$1" base="$2" report="$3"
    export SKY_TEST_JSON="$report"
    run_suite 180 "$SKY" test "$suite" --out "sky-out-conf-$base" \
        > "$WORK/$base.log" 2>&1
    echo $? >| "$WORK/$base.rc"
}

WORK="$(mktemp -d "${TMPDIR:-/tmp}/conf-XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

# Collect the suite list FIRST, so output and the manifest can be emitted in
# corpus order no matter what order the runs finish in. A report whose rows
# reorder run to run cannot be diffed, and this one exists to be compared.
suites=()
for suite in tests/*Test.sky; do
    [ -e "$suite" ] || continue
    base="$(basename "$suite" .sky)"
    if [ -n "$FILTER" ] && [[ "$base" != *"$FILTER"* ]]; then
        continue
    fi
    suites+=("$suite")
done

echo "conformance: ${#suites[@]} suite(s), $CONF_JOBS at a time"

for suite in "${suites[@]}"; do
    base="$(basename "$suite" .sky)"
    report=""
    if [ -n "$JSON_OUT" ]; then
        report="$REPORT_DIR/$base.json"
        rm -f "$report"
    fi
    throttle "$CONF_JOBS"
    run_one "$suite" "$base" "$report" &
done
wait

for suite in "${suites[@]}"; do
    base="$(basename "$suite" .sky)"
    ran=$((ran + 1))
    echo "── $base ──────────────────────────────────────────"
    tail -30 "$WORK/$base.log" 2>/dev/null
    # Keep the per-suite log where it has always been, for anyone following the
    # old path from CI output or a runbook.
    cp -f "$WORK/$base.log" "/tmp/conf-$base.log" 2>/dev/null || true

    # A MISSING rc file is a failure, never a pass. The run was launched; if it
    # left no exit code it died in a way that bypassed `run_one`'s last line
    # (killed by the timeout racer, OOM, the runner reclaiming the process).
    # Defaulting that to success is exactly how a gate comes to certify a suite
    # that never ran.
    if [ -f "$WORK/$base.rc" ]; then
        rc="$(cat "$WORK/$base.rc")"
    else
        rc=1
        echo "!! $base: no exit code recorded — the run did not complete" >&2
    fi
    if [ "$rc" -ne 0 ]; then
        echo "!! $base: sky test exited non-zero (rc=$rc)" >&2
        fail=$((fail + 1))
    fi
    if [ -n "$JSON_OUT" ]; then
        printf '{"name":"%s","exit_code":%d,"report":"%s"},\n' \
            "$base" "$rc" "$REPORT_DIR/$base.json" >> "$JSON_OUT.entries"
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
