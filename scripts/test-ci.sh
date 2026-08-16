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

# `with_timeout <secs> <cmd...>` — the one time bound. See the header of
# scripts/lib/with-timeout.sh for what a bare `timeout` did when it went missing.
source "$ROOT/scripts/lib/with-timeout.sh"
cd "$ROOT"

# shellcheck source=lib/concurrency.sh
source "$ROOT/scripts/lib/concurrency.sh"
# shellcheck source=lib/cargo-target.sh
source "$ROOT/scripts/lib/cargo-target.sh"
# `sky_compiler_freshness` / `require_fresh_compiler` — see the header of
# scripts/lib/fresh-compiler.sh.
source "$ROOT/scripts/lib/fresh-compiler.sh"

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
    # `[ ! -x … ]` was the whole condition, in the phase whose comment says its
    # purpose is freshness. Existence is not freshness: with a `sky-out/sky`
    # from any earlier run on disk, this phase printed "sky-out/sky exists" and
    # every later phase measured a compiler built before the change under test.
    # `sky_compiler_freshness` returns 1 for absent OR stale — see the header of
    # scripts/lib/fresh-compiler.sh — so both now rebuild.
    local fresh_rc=0
    sky_compiler_freshness "$ROOT/sky-out/sky" "$ROOT" || fresh_rc=$?
    if [ "$fresh_rc" = "2" ]; then
        echo "  FAIL — cannot establish compiler freshness: $SKY_FRESH_REASON" >&2
        return 1
    fi
    if [ "$fresh_rc" != "0" ] || [ -n "${SKY_REBUILD:-}" ]; then
        # Not `cp "$ROOT/rust/target/release/sky"` — cargo honours
        # CARGO_TARGET_DIR, so that path can name an older binary and this
        # phase's whole purpose is freshness. See scripts/lib/cargo-target.sh.
        # `cmd && install_binary` was the whole phase, under `set -uo pipefail`
        # with no `-e`. When `timeout` went missing from PATH the build
        # returned 127, `&&` short-circuited, the install was skipped, the
        # phase returned 0 — and every later phase ran against whatever stale
        # `sky-out/sky` happened to be on disk, or none at all. A build phase
        # that produces no binary has not succeeded.
        if ! ( cd "$ROOT/rust" && with_timeout 900 cargo build --release --locked -p sky ); then
            echo "  FAIL — cargo build did not complete (see above)" >&2
            return 1
        fi
        install_binary "$(cargo_bin_path "$ROOT/rust" sky --release)" "$ROOT/sky-out/sky" || return 1
        # And assert the claim this phase makes, rather than the weaker one it
        # used to check ("is executable").
        require_fresh_compiler "$ROOT/sky-out/sky" "$ROOT"
    else
        echo "  sky-out/sky is current with the tree (set SKY_REBUILD=1 to force rebuild)"
    fi
    local t1; t1=$(date +%s)
    echo "  $(( t1 - t0 ))s"
    echo
}

# Phase 2: rust gate suite (cargo test + xtask gates) with budget + watcher.
phase_rust_gates() {
    echo "--- phase: rust gate suite ---"
    local t0; t0=$(date +%s)
    # 2400 s budget — the FAST pre-push subset: cargo test + all parity gates
    # (incl. the fmt/s8/divergences/lsp additions that were CI-only) + build-run
    # --all. This mirrors the per-push jobs of .github/workflows/rust-ci.yml so a
    # fmt/nvim/parity regression surfaces LOCALLY before Actions. The heavier
    # runtime-correctness gates — `golden` + the behavioral `conformance` suites —
    # live in scripts/test-local.sh (the pre-tag gate) and CI's codegen-build job,
    # NOT here, to keep the pre-push gate from ballooning past its budget.
    ( cd "$ROOT/rust" && with_timeout 2400 bash -c '
        cargo test --workspace --locked || exit 1
        for g in roundtrip resolve infer reject fuzz coerce-floor repro fmt s8 divergences lsp; do
            cargo run -q -p xtask -- "$g" || exit 1
        done
        cargo run -q -p xtask -- build-run --all || exit 1
    ' )
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
    # Called bare, its status was discarded — so even after the phase learned
    # to return 1, `main` would have carried on into the gates with no
    # compiler. There is no useful gate run on a binary that was not built.
    if ! phase_compiler_build; then
        echo
        echo "FAIL: test-ci has no compiler to test with"
        exit 1
    fi
    if ! phase_rust_gates; then
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
