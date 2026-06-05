#!/usr/bin/env bash
# scripts/test-ci.sh — CI release gate (headless, deterministic).
#
# Sky's two-suite test architecture (v0.16.5, #494):
#
#   scripts/test-ci.sh     headless gate; runs in GitHub Actions and
#                          before `git push`. Target <12 min on M1.
#   scripts/test-local.sh  full e2e with browser; runs before `git
#                          tag`. Includes Suite 1 + Playwright +
#                          CLI/Tui drive. Target <25 min.
#
# This script is Suite 1. It does NOT spawn a browser; it does NOT
# need a display. It WILL exercise `sky verify` over every example
# (via SKY_RUN_FULL_VERIFY=1 → VerifyAll's second `it` block).
#
# Suite components:
#
#   1. cabal test (full hspec suite). VerifyAll's full per-example
#      `sky verify` runs because SKY_RUN_FULL_VERIFY=1 is set.
#   2. The cabal test includes ExampleSweep which delegates to
#      scripts/example-sweep.sh — parallel, CPU/mem-aware via the
#      concurrency helper.
#   3. Hub / receiver / bridge Go tests (run as part of `go test
#      ./runtime-go/...` inside cabal-test where applicable, or
#      explicitly here for hub-only PRs).
#
# Concurrency: every parallel step reads MAX_WORKERS from the shared
# helper scripts/lib/concurrency.sh. Operators can pin via
# MAX_TEST_WORKERS=N env var.
#
# Timings: each run emits per-describe CSV to /tmp/sky-cabal-timings.csv
# (or SKY_TIMINGS_FILE if set). Use this to spot regressions over time
# and to identify the next optimisation target.
#
# Exit code: 0 on full pass, non-zero on any failure.

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# shellcheck source=lib/concurrency.sh
source "$ROOT/scripts/lib/concurrency.sh"

# Defaults that map "CI mode" semantically. Operators can override
# anything by exporting before invoking the script.
export SKY_RUN_FULL_VERIFY="${SKY_RUN_FULL_VERIFY:-1}"
export SKY_SKIP_SWEEP="${SKY_SKIP_SWEEP:-}"  # leave unset → run sweep
export SKY_TIMINGS_FILE="${SKY_TIMINGS_FILE:-/tmp/sky-cabal-timings.csv}"
# Clear the timings CSV at the START so a fresh run is the only data.
: > "$SKY_TIMINGS_FILE" 2>/dev/null || true

echo "=== Sky test-ci ============================================="
echo "  $(date '+%Y-%m-%d %H:%M:%S')"
describe_concurrency | sed 's/^/  /'
echo "  SKY_RUN_FULL_VERIFY=$SKY_RUN_FULL_VERIFY"
echo "  SKY_TIMINGS_FILE=$SKY_TIMINGS_FILE"
echo "==========================================================="
echo

# Phase 1: ensure the compiler binary is fresh (TH re-embeds the
# runtime-go tree; stale binary builds wrong artifacts).
phase_compiler_build() {
    echo "--- phase: compiler build ---"
    local t0; t0=$(date +%s)
    if [ ! -x "$ROOT/sky-out/sky" ] || [ -n "${SKY_REBUILD:-}" ]; then
        timeout 600 cabal install --overwrite-policy=always \
            --installdir="$ROOT/sky-out" --install-method=copy exe:sky
    else
        echo "  sky-out/sky exists (set SKY_REBUILD=1 to force rebuild)"
    fi
    local t1; t1=$(date +%s)
    echo "  $(( t1 - t0 ))s"
    echo
}

# Phase 2: cabal test with budget + the watcher.
phase_cabal_test() {
    echo "--- phase: cabal test ---"
    local t0; t0=$(date +%s)
    # 1800 s budget — Suite 1's target is <12 min (720 s). Doubling
    # the budget tolerates first-run-cache misses without being so
    # loose that a real hang goes undiagnosed.
    timeout 1800 bash "$ROOT/scripts/cabal-test.sh"
    local rc=$?
    local t1; t1=$(date +%s)
    echo "  $(( t1 - t0 ))s (exit $rc)"
    return $rc
}

# Phase 3: optional — summarise top time consumers from the
# timings CSV. Useful for the operator to spot regressions.
phase_summary() {
    echo
    echo "--- top 10 slowest describes (this run) ---"
    if [ -s "$SKY_TIMINGS_FILE" ]; then
        sort -t, -k4 -nr "$SKY_TIMINGS_FILE" | head -10 \
            | awk -F, '{printf "  %6.1f s  %s\n", $4, $1}'
    else
        echo "  (no timing data — $SKY_TIMINGS_FILE empty)"
    fi
}

main() {
    local t_start; t_start=$(date +%s)
    phase_compiler_build
    if ! phase_cabal_test; then
        phase_summary
        echo
        echo "FAIL: test-ci did not pass cleanly"
        exit 1
    fi
    phase_summary
    local t_end; t_end=$(date +%s)
    echo
    echo "=== PASS in $(( t_end - t_start )) s ==="
}

main "$@"
