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
#   3. Go runtime tests — `runtime-go/rt` (the embedded runtime, incl.
#      hub / receiver / bridge) AND `runtime-go/bluedb` (the BlueDB
#      ordered-storage + MVCC/SSI engine behind Std.Persist). These
#      mirror the `codegen-build` job's go-test steps in
#      .github/workflows/rust-ci.yml. `./rt/...` does NOT reach the
#      sibling bluedb package, so bluedb is named explicitly — it was
#      in no gate at all until this phase existed.
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
        ( cd "$ROOT/rust" && timeout 900 cargo build --release --locked -p sky ) \
            && cp "$ROOT/rust/target/release/sky" "$ROOT/sky-out/sky"
    else
        echo "  sky-out/sky exists (set SKY_REBUILD=1 to force rebuild)"
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
    ( cd "$ROOT/rust" && timeout 2400 bash -c '
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

# Phase 2b: Go runtime tests. Mirrors the `go test` steps of the
# codegen-build job in .github/workflows/rust-ci.yml so a runtime or
# BlueDB-engine regression surfaces LOCALLY before Actions.
#
# Two packages, deliberately named separately: `./rt/...` does not
# match the sibling `./bluedb/...` package, which is how ~40 SSI /
# contention / crash-sim tests sat in no gate whatsoever.
#
# bluedb runs under -race: the suite is deliberately concurrent, so the
# race detector is the point of it, and it costs ~25 s. -race requires
# cgo, hence no CGO_ENABLED=0 on that line.
phase_go_runtime_tests() {
    echo "--- phase: go runtime tests (rt + bluedb) ---"
    local t0; t0=$(date +%s)
    ( cd "$ROOT/runtime-go" && timeout 1800 bash -c '
        CGO_ENABLED=0 go test ./rt/... || exit 1
        go test ./bluedb/... -count=1 -race || exit 1
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
    phase_compiler_build
    if ! phase_rust_gates; then
        phase_summary
        echo
        echo "FAIL: test-ci did not pass cleanly (rust gate suite)"
        exit 1
    fi
    if ! phase_go_runtime_tests; then
        phase_summary
        echo
        echo "FAIL: test-ci did not pass cleanly (go runtime tests)"
        exit 1
    fi
    phase_summary
    local t_end; t_end=$(date +%s)
    echo
    echo "=== PASS in $(( t_end - t_start )) s ==="
}

main "$@"
