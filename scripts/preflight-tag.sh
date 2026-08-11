#!/usr/bin/env bash
# preflight-tag.sh — verify that a release is safe to tag.
#
# Runs the full CLAUDE.md release checklist in CI-style strict mode.
# Exits 0 when everything green; non-zero on the first failure.
#
# Designed to be called manually before `git tag`, OR wired into a
# pre-push hook on tag pushes. The script is intentionally noisy
# (each step prints a banner) so a glance tells you what stage we're at.
#
# Usage:
#   scripts/preflight-tag.sh              # full sweep
#   scripts/preflight-tag.sh --skip-web   # skip Playwright (CI envs without browsers)
#   scripts/preflight-tag.sh --skip-cli   # skip CLI sweep
#
# Why this exists: shipping v0.13.0 + v0.13.1 with the Std.Ui event-
# emission regression (AsListT[any] returned nil on typed slices,
# dropping every Sky.Live event) revealed that the prior workflow
# treated `cabal test` + `example-sweep --build-only` as sufficient.
# They are NOT. Runtime verification is the only check that catches
# the "click is a no-op" class.

set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

SKIP_WEB=0
SKIP_CLI=0
for arg in "$@"; do
    case "$arg" in
        --skip-web) SKIP_WEB=1 ;;
        --skip-cli) SKIP_CLI=1 ;;
        *) echo "unknown flag: $arg" >&2; exit 2 ;;
    esac
done

step() {
    echo ""
    echo "────────────────────────────────────────────────────────────────"
    echo "▶ $*"
    echo "────────────────────────────────────────────────────────────────"
}

fail() {
    echo ""
    echo "✗ FAIL: $*" >&2
    echo ""
    echo "Release is NOT safe to tag. Fix the failure above and re-run." >&2
    exit 1
}

step "1/6 — Rebuild compiler from clean state"
# `cp rust/target/release/sky` was wrong here for the same reason it was wrong
# in build.sh: cargo honours CARGO_TARGET_DIR and friends, so that path can name
# an older build. In a RELEASE gate that is the worst possible place for it —
# every check below would have certified a binary that is not the one just
# compiled, and the tag would ship the other one.
# shellcheck source=lib/cargo-target.sh
source "$REPO_ROOT/scripts/lib/cargo-target.sh"
( cd rust && cargo build --release --locked -p sky ) 2>&1 | tail -5
install_binary "$(cargo_bin_path "$REPO_ROOT/rust" sky --release)" "$REPO_ROOT/sky-out/sky" \
    || fail "could not install the compiler cargo just built"
[ -x ./sky-out/sky ] || fail "compiler binary missing after cargo build"

step "2/6 — Smoke-test binary"
ver=$(./sky-out/sky --version 2>&1)
echo "  version output: $ver"
echo "$ver" | grep -qE "^sky " || fail "sky --version did not print 'sky' line"

step "3/6 — rust gate suite"
# CLAUDE.md §2.3 — long-running commands must be timeout-bounded.
# 60 min ceiling; if real runs need more, that's a flaky test.
#
# The gates run --release. They are CPU-bound corpus walks (the checker over 63
# ill-typed programs, ~1000 fuzz inputs, an emit of every example), and an
# unoptimized xtask made them ~10x slower: `reject` alone measured 780s in debug
# vs 74s in release, for the identical 63/63 PASS. Debug-built gates could not
# finish inside the 60-minute ceiling at all — two release attempts died with no
# error text, just a kill, after every gate had actually passed. Optimizing the
# harness is the honest fix; raising the ceiling would only have hidden it.
( cd rust && timeout 3600 bash -c 'cargo test --workspace --locked && for g in roundtrip resolve infer reject fuzz coerce-floor repro; do cargo run --release -q -p xtask -- "$g" || exit 1; done && cargo run --release -q -p xtask -- build-run --all && cargo run --release -q -p xtask -- build-run --shape cli --run --golden' ) || fail "rust gate suite had failures"

step "4/6 — Example sweep (build-only, all 19+ examples)"
# Run the sweep ONCE. It used to run twice — once piped to `tail -5` for
# display and again captured for the check — which doubled the slowest step in
# the whole preflight for nothing.
#
# And grep the whole output for the summary, not `tail -1`: the sweep prints a
# `[hygiene] go-build cache …` line AFTER its summary, so the last line is
# never `sweep: N passed, 0 failed`. That mismatch failed a release whose sweep
# had actually passed 29/0, reporting the hygiene line as the failure message.
sweep_out=$(scripts/example-sweep.sh --build-only 2>&1)
echo "$sweep_out" | tail -5
echo "$sweep_out" | grep -qE "^sweep: [0-9]+ passed, 0 failed$" || \
    fail "example-sweep failed: $(echo "$sweep_out" | grep -E '^sweep: ' | tail -1)"

if [ $SKIP_WEB -eq 0 ]; then
    step "5/6 — Runtime verification (Playwright; web apps)"
    # Take the gate's EXIT STATUS as the verdict, and keep the anchored grep as
    # a second, independent witness.
    #
    # The anchor alone was not enough. verify-all-web.sh used to print a
    # "VERIFY: N pass / M fail" line after its example loop AND after each
    # later gate, so its output held several verdict lines and this grep
    # matched the FIRST one — a run whose examples all passed but whose
    # console-e2e / ui-showcase / resilience gate then failed still contained
    # " 0 fail" and was declared safe to tag. (The earlier form of this check
    # also matched the substring inside "10 fail": a run that ended
    # 0 pass / 12 fail, every Playwright check dead with ERR_MODULE_NOT_FOUND,
    # sailed through because it printed "0 pass / 10 fail" on the way there.)
    #
    # verify-all-web.sh now emits exactly one "VERIFY:" line, last, and exits
    # with its failure count. Both readings agree; require both.
    out=$(scripts/verify-all-web.sh 2>&1)
    web_rc=$?
    echo "$out" | tail -3
    [ "$web_rc" -eq 0 ] || fail "verify-all-web exited $web_rc"
    echo "$out" | grep -qE "(^|[^0-9])0 fail" || fail "verify-all-web reported failures"
else
    echo ""
    echo "⚠ SKIPPED step 5 (--skip-web). This is ONLY acceptable in"
    echo "  headless CI environments without browsers. NEVER skip on"
    echo "  the release host."
fi

if [ $SKIP_CLI -eq 0 ]; then
    step "6/6 — Runtime verification (CLI / Sky.Tui / Sky.Cli)"
    if [ -x scripts/verify-cli.sh ]; then
        # The CLI arm was left on the UNANCHORED pattern when the web arm above
        # was fixed. `grep -qE "0 fail"` matches the substring inside "10 fail",
        # and verify-cli.sh prints "VERIFY: $pass pass / $fail fail / $skip
        # skip" — so a run of 3 pass / 10 fail / 0 skip passed this gate.
        # Demonstrated:
        #   $ echo "VERIFY: 3 pass / 10 fail / 0 skip" | grep -qE "0 fail" \
        #       && echo PASS
        #   PASS
        # Use the exit status (verify-cli.sh ends in `exit $fail`) plus the
        # anchored field.
        out=$(scripts/verify-cli.sh 2>&1)
        cli_rc=$?
        echo "$out" | tail -3
        [ "$cli_rc" -eq 0 ] || fail "verify-cli exited $cli_rc"
        echo "$out" | grep -qE "(^|[^0-9])0 fail" || fail "verify-cli reported failures"
    else
        echo "  (verify-cli.sh not present; skipping)"
    fi
else
    echo ""
    echo "⚠ SKIPPED step 6 (--skip-cli)"
fi

echo ""
echo "════════════════════════════════════════════════════════════════"
echo "✓ All preflight checks passed. Safe to tag."
echo "════════════════════════════════════════════════════════════════"

# Stamp success so the pre-push hook permits the tag push. Use --git-common-dir,
# NOT "$REPO_ROOT/.git": inside a git worktree `.git` is a FILE, so the literal
# path names nothing, the touch fails, and the tag push is refused even though
# preflight passed — which is exactly what happened cutting v0.19.13.
STAMP_PATH="$(git rev-parse --git-common-dir)/last-preflight-pass"
touch "$STAMP_PATH"
echo "  stamp: $STAMP_PATH updated"
