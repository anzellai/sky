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
( cd rust && cargo build --release --locked -p sky-cli ) 2>&1 | tail -5
mkdir -p ./sky-out
cp rust/target/release/sky ./sky-out/sky
[ -x ./sky-out/sky ] || fail "compiler binary missing after cargo build"

step "2/6 — Smoke-test binary"
ver=$(./sky-out/sky --version 2>&1)
echo "  version output: $ver"
echo "$ver" | grep -qE "^sky " || fail "sky --version did not print 'sky' line"

step "3/6 — rust gate suite"
# CLAUDE.md §2.3 — long-running commands must be timeout-bounded.
# 60 min ceiling; if real runs need more, that's a flaky test.
( cd rust && timeout 3600 bash -c 'cargo test --workspace --locked && for g in roundtrip resolve infer reject fuzz coerce-floor repro; do cargo run -q -p xtask -- "$g" || exit 1; done && cargo run -q -p xtask -- build-run --all && cargo run -q -p xtask -- build-run --shape cli --run --golden' ) || fail "rust gate suite had failures"

step "4/6 — Example sweep (build-only, all 19+ examples)"
scripts/example-sweep.sh --build-only 2>&1 | tail -5
tail=$(scripts/example-sweep.sh --build-only 2>&1 | tail -1)
echo "$tail" | grep -qE "^sweep: [0-9]+ passed, 0 failed$" || \
    fail "example-sweep failed: $tail"

if [ $SKIP_WEB -eq 0 ]; then
    step "5/6 — Runtime verification (Playwright; web apps)"
    out=$(scripts/verify-all-web.sh 2>&1)
    echo "$out" | tail -3
    echo "$out" | grep -qE "0 fail" || fail "verify-all-web reported failures"
else
    echo ""
    echo "⚠ SKIPPED step 5 (--skip-web). This is ONLY acceptable in"
    echo "  headless CI environments without browsers. NEVER skip on"
    echo "  the release host."
fi

if [ $SKIP_CLI -eq 0 ]; then
    step "6/6 — Runtime verification (CLI / Sky.Tui / Sky.Cli)"
    if [ -x scripts/verify-cli.sh ]; then
        out=$(scripts/verify-cli.sh 2>&1)
        echo "$out" | tail -3
        echo "$out" | grep -qE "0 fail" || fail "verify-cli reported failures"
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

# Stamp success so the pre-push hook permits the tag push.
touch "$REPO_ROOT/.git/last-preflight-pass"
echo "  stamp: $REPO_ROOT/.git/last-preflight-pass updated"
