#!/usr/bin/env bash
# sky-suites.sh — run the root `tests/` Sky.Test suites.
#
# `tests/` is ONE Sky project (tests/sky.toml) holding the *Test.sky suites in
# subdirectories: tests/Auth/, tests/Core/, tests/Db/, tests/Json/, tests/Lang/,
# tests/Live/, tests/Server/, tests/Sky/Core/, tests/Std/. They assert the pure
# seams of the stdlib + language (pattern matching, TEA update loops, route
# matching, Std.Db row/result shapes, Std.Ui element shapes).
#
# They had NO runner and had NEVER been executed. `scripts/conformance.sh` looks
# like it covers them, but its PROJ is `tests/conformance` and its loop globs
# `tests/*Test.sky` RELATIVE to that — i.e. only tests/conformance/tests/. The
# root suites live one directory deeper than a flat glob can see, so discovery
# here is RECURSIVE. A flat glob is the exact bug that hid them.
#
# Usage: scripts/sky-suites.sh                 # run all suites
#        scripts/sky-suites.sh Ui              # run only *Ui* suites
#        scripts/sky-suites.sh --json <path>   # + a machine-readable manifest
#
# --json writes a manifest of (suite, exit_code, per-suite Sky.Test report).
# Like conformance.sh it deliberately does NOT aggregate: this script emits only
# values it controls (a slug, an integer, a path) and never parses JSON, because
# a shell that parses its own output is how `grep -qE "0 fail"` came to match
# inside "10 fail". The aggregation and the verdict belong to the caller, which
# has a real JSON parser — see `xtask harness`'s `sky-suites` gate.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# `with_timeout <secs> <cmd...>` — the one time bound. See the header of
# scripts/lib/with-timeout.sh for what a bare `timeout` did when it went missing.
source "$ROOT/scripts/lib/with-timeout.sh"
# `require_fresh_compiler <bin>` — see the header of scripts/lib/fresh-compiler.sh.
source "$ROOT/scripts/lib/fresh-compiler.sh"
PROJ="$ROOT/tests"

FILTER=""
JSON_OUT=""
while [ $# -gt 0 ]; do
    case "$1" in
        --json)
            [ $# -ge 2 ] || { echo "sky-suites: --json requires a path" >&2; exit 2; }
            JSON_OUT="$2"; shift 2 ;;
        --*)
            echo "sky-suites: unknown option $1" >&2; exit 2 ;;
        *)
            FILTER="$1"; shift ;;
    esac
done

# Prefer the compiler in THIS tree over whatever `sky` is installed on PATH — a
# pass from a months-old installed binary says nothing about the tree under
# test. conformance.sh and doc-examples.sh resolve in this same order.
SKY="${SKY_BIN:-}"
if [ -z "$SKY" ]; then
    if [ -x "$ROOT/sky-out/sky" ]; then SKY="$ROOT/sky-out/sky"; else SKY="sky"; fi
fi
# …and only if it is CURRENT. "Months-old installed binary" and "compiler from
# before the change under test" are the same defect at two intervals; the
# resolution order above fixed the first, this fixes the second.
require_fresh_compiler "$SKY" "$ROOT"


if [ ! -f "$PROJ/sky.toml" ]; then
    echo "sky-suites: no Sky project at $PROJ" >&2
    exit 2
fi

cd "$PROJ"
fail=0
ran=0

# Per-suite Sky.Test JSON reports live here; the manifest points at them.
REPORT_DIR="$ROOT/.skycache/sky-suite-reports"
if [ -n "$JSON_OUT" ]; then
    rm -rf "$REPORT_DIR"
    mkdir -p "$REPORT_DIR"
    : >| "$JSON_OUT.entries"
fi

# RECURSIVE discovery, excluding tests/conformance/ (owned by conformance.sh)
# and any sky-out* build directories a previous run left behind. Sorted so the
# manifest order is deterministic across platforms.
SUITES="$(find . -name '*Test.sky' -type f \
    -not -path './conformance/*' \
    -not -path './sky-out*' \
    | sed 's|^\./||' | LC_ALL=C sort)"

for suite in $SUITES; do
    # Slug from the FULL relative path, not `basename`: two suites in different
    # directories can share a basename, and a basename-keyed report path would
    # let one silently overwrite the other's result.
    slug="$(printf '%s' "${suite%.sky}" | tr '/' '_')"
    if [ -n "$FILTER" ] && [[ "$slug" != *"$FILTER"* ]]; then
        continue
    fi
    ran=$((ran + 1))
    echo "── $slug ──────────────────────────────────────────"
    # unique out dir per suite so parallel/repeat runs don't clobber
    out="sky-out-suite-$slug"
    report=""
    if [ -n "$JSON_OUT" ]; then
        report="$REPORT_DIR/$slug.json"
        rm -f "$report"
    fi
    rc=0
    # Exported explicitly rather than as an assignment prefix: the prefix form
    # in front of a shell FUNCTION has shell-dependent export semantics, and a
    # silently-unset SKY_TEST_JSON would produce an empty manifest that the
    # caller would have to treat as a failure.
    export SKY_TEST_JSON="$report"
    with_timeout 300 "$SKY" test "$suite" --out "$out" 2>&1 \
        | tee "/tmp/sky-suite-$slug.log" | tail -30
    rc=${PIPESTATUS[0]}
    unset SKY_TEST_JSON
    if [ "$rc" -ne 0 ]; then
        echo "!! $slug: sky test exited non-zero (rc=$rc)" >&2
        fail=$((fail + 1))
    fi
    if [ -n "$JSON_OUT" ]; then
        printf '{"name":"%s","path":"%s","exit_code":%d,"report":"%s"},\n' \
            "$slug" "$suite" "$rc" "$report" >> "$JSON_OUT.entries"
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
# Zero suites discovered is a FAILURE, never a pass. `total=0 … PASS` is a
# defect class this repo has already shipped once (doc-examples.sh): a discovery
# bug that finds nothing must be loud, because "nothing ran" and "everything
# passed" are indistinguishable to any caller that only reads the exit code.
if [ "$ran" -eq 0 ]; then
    if [ -n "$FILTER" ]; then
        echo "sky-suites: no suites matched filter '${FILTER}'" >&2
    else
        echo "sky-suites: NO suites discovered under $PROJ — discovery is broken" >&2
    fi
    exit 2
fi
if [ "$fail" -eq 0 ]; then
    echo "sky-suites: ALL $ran suite(s) passed"
    exit 0
else
    echo "sky-suites: $fail of $ran suite(s) FAILED"
    exit 1
fi
