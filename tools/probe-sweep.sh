#!/usr/bin/env bash
# tools/probe-sweep.sh — typed-codegen invariant probe sweep.
#
# Drop-in safety net for the v0.17 fully-typed-codegen refactor.
# Runs every fixture under tools/probe-fixtures/ through `sky build`,
# captures emitted Go, and asserts type-presence invariants.
#
# Fixture shape: each fixture lives under tools/probe-fixtures/<name>/
#   - sky.toml
#   - src/Main.sky       — the Sky source under test
#   - expectations.txt   — one ASSERTION per line (see syntax below)
#   - README.md          — what this fixture probes (root cause A-J)
#
# Expectations syntax (one per line, blank lines + #-comments ignored):
#   MUST_CONTAIN <go-substring>          — main.go must contain substring
#   MUST_NOT_CONTAIN <go-substring>      — main.go must NOT contain substring
#   MUST_BUILD                            — sky build must exit 0
#   MUST_FAIL_BUILD                       — sky build must exit non-zero
#   MUST_NOT_PANIC_AT_RUNTIME             — built binary runs without panic
#   GREP_COUNT <n> <go-pattern>           — go grep matches exactly n times
#
# Each fixture also produces a one-line result in $RESULTS_DIR/<name>.result:
#   OK
#   FAIL <message>
#
# Exit 0 on full pass; non-zero with a failure summary on any failure.
#
# Designed to run after EVERY commit on feat/v0.17-fully-typed-codegen.

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

SKY="$ROOT/sky-out/sky"
[[ -x "$SKY" ]] || { echo "missing $SKY — run scripts/build.sh first" >&2; exit 2; }

export SKY_RUNTIME_DIR="$ROOT/runtime-go"

FIXTURES_DIR="$ROOT/tools/probe-fixtures"
[[ -d "$FIXTURES_DIR" ]] || { echo "missing $FIXTURES_DIR" >&2; exit 2; }

RESULTS_DIR="$(mktemp -d -t probe-sweep-XXXXXX)"
trap 'rm -rf "$RESULTS_DIR"' EXIT

VERBOSE=0
ONLY=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        --verbose|-v) VERBOSE=1; shift ;;
        --only)
            shift
            [[ $# -gt 0 ]] || { echo "--only requires a fixture name" >&2; exit 2; }
            ONLY="$1"
            shift ;;
        --only=*)
            ONLY="${1#--only=}"
            shift ;;
        --help|-h)
            sed -n '2,35p' "$0"; exit 0 ;;
        *) echo "unknown flag: $1" >&2; exit 2 ;;
    esac
done

run_fixture() {
    local fixture_dir="$1"
    local name
    name="$(basename "$fixture_dir")"

    local exp_file="$fixture_dir/expectations.txt"
    if [[ ! -f "$exp_file" ]]; then
        echo "SKIP $name (no expectations.txt)" > "$RESULTS_DIR/$name.result"
        return 0
    fi

    # Determine build expectation
    local must_build=1
    if grep -qx 'MUST_FAIL_BUILD' "$exp_file"; then
        must_build=0
    fi

    # Clean state per run
    rm -rf "$fixture_dir/sky-out" "$fixture_dir/.skycache" "$fixture_dir/.skydeps"

    local log
    log="$RESULTS_DIR/$name.log"
    local rc=0
    (cd "$fixture_dir" && "$SKY" build src/Main.sky) > "$log" 2>&1 || rc=$?

    if (( must_build == 1 )) && (( rc != 0 )); then
        local msg
        msg=$(tail -3 "$log" | tr '\n' ' ')
        echo "FAIL $name — build failed: $msg" > "$RESULTS_DIR/$name.result"
        return 0
    fi
    if (( must_build == 0 )) && (( rc == 0 )); then
        echo "FAIL $name — build succeeded but MUST_FAIL_BUILD" > "$RESULTS_DIR/$name.result"
        return 0
    fi
    if (( must_build == 0 )); then
        # We expected failure and got it — pass without main.go checks
        echo "OK" > "$RESULTS_DIR/$name.result"
        return 0
    fi

    local main_go="$fixture_dir/sky-out/main.go"
    if [[ ! -f "$main_go" ]]; then
        echo "FAIL $name — sky-out/main.go missing after build" > "$RESULTS_DIR/$name.result"
        return 0
    fi

    local failures=()
    while IFS= read -r line; do
        # Strip leading whitespace and skip blanks/comments
        local trimmed="${line#"${line%%[![:space:]]*}"}"
        [[ -z "$trimmed" || "$trimmed" == \#* ]] && continue

        case "$trimmed" in
            MUST_BUILD|MUST_FAIL_BUILD)
                ;;  # Already handled
            MUST_CONTAIN\ *)
                local needle="${trimmed#MUST_CONTAIN }"
                if ! grep -qF -- "$needle" "$main_go"; then
                    failures+=("missing substring: $needle")
                fi ;;
            MUST_NOT_CONTAIN\ *)
                local needle="${trimmed#MUST_NOT_CONTAIN }"
                if grep -qF -- "$needle" "$main_go"; then
                    failures+=("unwanted substring present: $needle")
                fi ;;
            GREP_COUNT\ *)
                local rest="${trimmed#GREP_COUNT }"
                local n="${rest%% *}"
                local pat="${rest#* }"
                local got
                got=$(grep -cF -- "$pat" "$main_go" || true)
                if [[ "$got" != "$n" ]]; then
                    failures+=("grep '$pat' want=$n got=$got")
                fi ;;
            MUST_NOT_PANIC_AT_RUNTIME)
                local bin="$fixture_dir/sky-out/app"
                if [[ ! -x "$bin" ]]; then
                    failures+=("binary missing: $bin")
                else
                    # CLAUDE.md non-negotiable #3 — every long-running
                    # command MUST be timeout-bounded.  A probe binary
                    # that infinite-loops (e.g. miscompiled TCO
                    # continue-block) would otherwise stall the sweep
                    # indefinitely.  10 s is generous for the
                    # fixtures' workload; exit code 124 = timed out.
                    local run_rc=0
                    timeout 10 "$bin" > /dev/null 2>&1 || run_rc=$?
                    if (( run_rc == 124 )); then
                        failures+=("runtime timed out — infinite loop / TCO miscompile")
                    elif (( run_rc != 0 )); then
                        failures+=("runtime exit $run_rc — likely panic")
                    fi
                fi ;;
            *)
                failures+=("unknown directive: $trimmed") ;;
        esac
    done < "$exp_file"

    if (( ${#failures[@]} > 0 )); then
        local msg
        msg=$(printf '%s; ' "${failures[@]}")
        echo "FAIL $name — ${msg%; }" > "$RESULTS_DIR/$name.result"
    else
        echo "OK" > "$RESULTS_DIR/$name.result"
    fi
}

# Discovery
FIXTURES=()
for d in "$FIXTURES_DIR"/*/; do
    [[ -d "$d" ]] || continue
    local_name="$(basename "$d")"
    if [[ -n "$ONLY" && "$local_name" != "$ONLY" ]]; then
        continue
    fi
    FIXTURES+=("$d")
done

if (( ${#FIXTURES[@]} == 0 )); then
    if [[ -n "$ONLY" ]]; then
        echo "no fixture matches --only=$ONLY" >&2
    else
        echo "no fixtures found in $FIXTURES_DIR" >&2
    fi
    exit 2
fi

echo "probe-sweep: ${#FIXTURES[@]} fixture(s)"

for d in "${FIXTURES[@]}"; do
    run_fixture "$d"
done

# Aggregate
pass=0
fail=0
fail_lines=()
for d in "${FIXTURES[@]}"; do
    name="$(basename "$d")"
    line=$(cat "$RESULTS_DIR/$name.result" 2>/dev/null || echo "FAIL $name — no result file")
    case "$line" in
        OK)
            pass=$((pass + 1))
            (( VERBOSE )) && echo "  OK    $name" ;;
        SKIP*)
            echo "  $line" ;;
        FAIL*)
            fail=$((fail + 1))
            fail_lines+=("$line")
            echo "  $line" ;;
        *)
            fail=$((fail + 1))
            fail_lines+=("UNKNOWN: $line") ;;
    esac
done

echo ""
echo "probe-sweep: $pass pass / $fail fail / ${#FIXTURES[@]} total"

if (( fail > 0 )); then
    exit 1
fi
exit 0
