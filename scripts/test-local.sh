#!/usr/bin/env bash
# scripts/test-local.sh — Local pre-tag gate (full e2e + browser).
#
# Sky's two-suite test architecture (v0.16.5, #494):
#
#   scripts/test-ci.sh     headless gate; runs in GitHub Actions and
#                          before `git push`. Target <12 min on M1.
#   scripts/test-local.sh  full e2e with browser; runs before `git
#                          tag`. Includes test-ci + Playwright +
#                          CLI/Tui drive. Target <25 min.
#
# This script is Suite 2. It assumes a desktop environment with a
# browser available (Playwright auto-detects). It additionally runs
# the CLI / Tui / Sky.Webview runtime drive.
#
# Suite components:
#
#   1. Everything in scripts/test-ci.sh (cabal test + example sweep
#      with SKY_RUN_FULL_VERIFY=1).
#   2. scripts/verify-all-web.sh — Playwright over Sky.Live +
#      Sky.Http.Server scenarios. Real browser, real SSE, real DOM
#      patches.
#   3. scripts/verify-cli.sh — Sky.Cli + Sky.Tui + Sky.Webview
#      runtime drive (sequential per-category due to TTY contention).
#   4. scripts/verify-ui-showcase.sh — visual regression on
#      examples/26-ui-showcase.
#   5. (deferred) Hub UI Playwright via examples/39-hub-demo —
#      ships when #493 lands.
#
# Concurrency: as test-ci.sh — every parallel step reads MAX_WORKERS
# from scripts/lib/concurrency.sh.
#
# Exit code: 0 on full pass; non-zero on any failure.

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# shellcheck source=lib/concurrency.sh
source "$ROOT/scripts/lib/concurrency.sh"

export SKY_TIMINGS_FILE="${SKY_TIMINGS_FILE:-/tmp/sky-cabal-timings.csv}"

echo "=== Sky test-local =========================================="
echo "  $(date '+%Y-%m-%d %H:%M:%S')"
describe_concurrency | sed 's/^/  /'
echo "==========================================================="
echo

phase_test_ci() {
    echo "--- phase 1/4: test-ci (cabal + sweep) ---"
    local t0; t0=$(date +%s)
    bash "$ROOT/scripts/test-ci.sh"
    local rc=$?
    local t1; t1=$(date +%s)
    echo "  $(( t1 - t0 ))s (exit $rc)"
    return $rc
}

phase_web_verify() {
    echo
    echo "--- phase 2/4: Playwright web verify ---"
    if [ ! -x "$ROOT/scripts/verify-all-web.sh" ]; then
        echo "  scripts/verify-all-web.sh missing or not executable — SKIP"
        return 0
    fi
    local t0; t0=$(date +%s)
    bash "$ROOT/scripts/verify-all-web.sh"
    local rc=$?
    local t1; t1=$(date +%s)
    echo "  $(( t1 - t0 ))s (exit $rc)"
    return $rc
}

phase_cli_verify() {
    echo
    echo "--- phase 3/4: CLI / Tui / Webview verify ---"
    if [ ! -x "$ROOT/scripts/verify-cli.sh" ]; then
        echo "  scripts/verify-cli.sh missing or not executable — SKIP"
        return 0
    fi
    local t0; t0=$(date +%s)
    bash "$ROOT/scripts/verify-cli.sh"
    local rc=$?
    local t1; t1=$(date +%s)
    echo "  $(( t1 - t0 ))s (exit $rc)"
    return $rc
}

phase_ui_showcase() {
    echo
    echo "--- phase 4/4: UI showcase visual regression ---"
    if [ ! -x "$ROOT/scripts/verify-ui-showcase.sh" ]; then
        echo "  scripts/verify-ui-showcase.sh missing or not executable — SKIP"
        return 0
    fi
    local t0; t0=$(date +%s)
    bash "$ROOT/scripts/verify-ui-showcase.sh"
    local rc=$?
    local t1; t1=$(date +%s)
    echo "  $(( t1 - t0 ))s (exit $rc)"
    return $rc
}

main() {
    local t_start; t_start=$(date +%s)
    local any_fail=0

    phase_test_ci    || any_fail=1
    phase_web_verify || any_fail=1
    phase_cli_verify || any_fail=1
    phase_ui_showcase || any_fail=1

    local t_end; t_end=$(date +%s)
    echo
    if [ $any_fail -eq 0 ]; then
        echo "=== PASS in $(( t_end - t_start )) s — ready to tag ==="
        exit 0
    else
        echo "=== FAIL after $(( t_end - t_start )) s ==="
        exit 1
    fi
}

main "$@"
