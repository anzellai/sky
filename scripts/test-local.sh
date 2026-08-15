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

# `with_timeout <secs> <cmd...>` — the one time bound. See the header of
# scripts/lib/with-timeout.sh for what a bare `timeout` did when it went missing.
source "$ROOT/scripts/lib/with-timeout.sh"
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
    echo "--- phase 1/6: test-ci (workspace + gates) ---"
    local t0; t0=$(date +%s)
    bash "$ROOT/scripts/test-ci.sh"
    local rc=$?
    local t1; t1=$(date +%s)
    echo "  $(( t1 - t0 ))s (exit $rc)"
    return $rc
}

phase_behavioral() {
    echo
    echo "--- phase 2/6: runtime-correctness golden + behavioral conformance ---"
    # The heavier runtime-correctness gates, kept OUT of the fast pre-push
    # test-ci.sh: `golden` (emitted-Go runtime output matches committed goldens
    # for the CLI subset) + `conformance` (the adversarial Sky-source stdlib
    # suites — the int64-class "compiles-clean, behaves-wrong" gate). Also run on
    # both platforms in CI's codegen-build / macos-determinism jobs.
    local t0; t0=$(date +%s)
    ( cd "$ROOT/rust" && with_timeout 1500 bash -c '
        cargo run -q -p xtask -- build-run --shape cli --run --golden || exit 1
        cargo build --release -p sky --locked || exit 1
        SKY_BIN="$PWD/target/release/sky" ../scripts/conformance.sh || exit 1
    ' )
    local rc=$?
    local t1; t1=$(date +%s)
    echo "  $(( t1 - t0 ))s (exit $rc)"
    return $rc
}

phase_web_verify() {
    echo
    echo "--- phase 3/6: Playwright web verify ---"
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
    echo "--- phase 4/6: CLI / Tui / Webview verify ---"
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
    echo "--- phase 5/6: UI showcase visual regression ---"
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

phase_release_parity() {
    echo
    echo "--- phase 6/6: well-typed differential fuzzer (Rust ⇄ Haskell oracle) ---"
    # Generates bounded, deterministic well-typed Sky programs and asserts the
    # Rust compiler and the Haskell oracle AGREE (accept/reject) on every one —
    # the WellTypedFuzzerSpec analog, inference parity on inputs beyond the fixed
    # corpus. Needs the oracle (NOT available in CI); the gate self-skips (exit 0)
    # when the oracle binary is absent, so this phase is safe everywhere.
    local t0; t0=$(date +%s)
    ( cd "$ROOT/rust" && with_timeout 900 cargo run -q -p xtask -- welltyped )
    local rc=$?
    local t1; t1=$(date +%s)
    echo "  $(( t1 - t0 ))s (exit $rc)"
    return $rc
}

main() {
    local t_start; t_start=$(date +%s)
    local any_fail=0

    phase_test_ci    || any_fail=1
    phase_behavioral || any_fail=1
    phase_web_verify || any_fail=1
    phase_cli_verify || any_fail=1
    phase_ui_showcase || any_fail=1
    phase_release_parity || any_fail=1

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
