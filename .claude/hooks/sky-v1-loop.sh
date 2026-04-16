#!/usr/bin/env bash
# Stop-hook gate for Sky.
#
# Fires every time the model tries to end a turn. Returns a JSON
# "block" decision unless one of the escape conditions is met, forcing
# the model to keep working until the active plans are marked complete.
#
# Active plans (checked in order):
#   1. Audit remediation (docs/AUDIT_REMEDIATION.md) — primary.
#   2. FFI boundary docs cleanup (.claude/prompts/ffi-boundary-docs.md) —
#      update all docs/samples/tests after P0-P3 FFI fixes landed.
#   3. v1.0 production readiness (docs/PRODUCTION_READINESS.md) — legacy,
#      only relevant once all above are complete.
#
# Escape conditions (any one allows the turn to end):
#   A. docs/AUDIT_REMEDIATION.md contains `## Audit remediation complete`
#      AND docs/PRODUCTION_READINESS.md contains
#      `## Current state snapshot — v1.0 complete`.
#   B. Manual pause: `.claude/allow-stop` exists. touch it to pause.
#
# The previous runaway-brake at 15+ commits-without-plan-amendment has
# been REMOVED by explicit user instruction: "no stopping sessions
# until all goals are reached." If you need to breathe for human
# review, use `touch .claude/allow-stop` — don't reintroduce an
# automated brake.

set -uo pipefail

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO"

AUDIT_DOC="docs/AUDIT_REMEDIATION.md"
V1_DOC="docs/PRODUCTION_READINESS.md"

audit_done=0
v1_done=0
if [[ -f "$AUDIT_DOC" ]] && grep -q '^## Audit remediation complete' "$AUDIT_DOC" 2>/dev/null; then
    audit_done=1
fi
if [[ -f "$V1_DOC" ]] && grep -q '^## Current state snapshot — v1\.0 complete' "$V1_DOC" 2>/dev/null; then
    v1_done=1
fi

# ── escape A: both plans complete
if [[ $audit_done -eq 1 && $v1_done -eq 1 ]]; then
    exit 0
fi

# ── escape B: manual pause marker
if [[ -f .claude/allow-stop ]]; then
    exit 0
fi

# ── otherwise: block with guidance. Pick the active plan's next step.
if [[ $audit_done -eq 0 ]]; then
    # Find the lowest-numbered ☐ item in the tracker.
    next_item="$(grep -E '^\| P[0-9]-[0-9]+ \|' "$AUDIT_DOC" 2>/dev/null \
        | grep -F '| ☐ |' \
        | head -1 \
        | awk -F'|' '{print $2 "—" $3}' \
        | sed 's/^ *//; s/ *$//')"
    if [[ -z "$next_item" ]]; then
        next_item="(tracker appears empty — read docs/AUDIT_REMEDIATION.md directly)"
    fi

    # Count remaining ☐ items for progress signal.
    remaining="$(grep -cF '| ☐ |' "$AUDIT_DOC" 2>/dev/null || echo 0)"
    done_count="$(grep -cF '| ☑ |' "$AUDIT_DOC" 2>/dev/null || echo 0)"

    cat <<EOF
{
  "decision": "block",
  "reason": "Audit remediation is the active plan and is NOT yet complete. Progress: ${done_count} done, ${remaining} remaining. Next unfinished item: ${next_item}. Authoritative docs: read .claude/prompts/audit-remediation.md (operating loop + non-negotiable rules) and docs/AUDIT_REMEDIATION.md (per-item acceptance criteria). Per-item loop: run the regression fence (pass=0;fail=0; for f in test-files/*.sky; do rm -rf .skycache; ./sky-out/sky build \$f >/dev/null 2>&1 && pass=\$((pass+1)) || fail=\$((fail+1)); done; echo \"self-tests: \$pass passed, \$fail failed\"; bash scripts/example-sweep.sh --build-only; cd runtime-go && go test ./rt/); write a regression test that FAILS against HEAD~1; implement the fix; re-run fence; tick the tracker checkbox (☐ → ☑) + record commit hash; commit with [audit/P<n>-<m>] label; push; loop. Non-negotiables: no silent coercion, no raw .(T) assertions, no any-escape hatches, no %v stringification of secrets, every fix has a failing-before test. There is NO automated runaway brake — to pause cleanly use \`touch .claude/allow-stop\`. To finish permanently: land every P0–P3 item then append '## Audit remediation complete' to docs/AUDIT_REMEDIATION.md. P4 items are out of scope for this loop."
}
EOF
    exit 0
fi

# ── FFI docs cleanup gate
FFI_MARKER="docs/ffi/boundary-philosophy.md"
if [[ ! -f "$FFI_MARKER" ]]; then
    cat <<'FFIEOF'
{
  "decision": "block",
  "reason": "FFI boundary docs cleanup is in progress. The P0-P3 fixes landed (e1faa21) but docs/samples/tests haven't been updated yet. Follow .claude/prompts/ffi-boundary-docs.md — update return-type mapping tables in docs/ffi/go-interop.md + CLAUDE.md + templates/CLAUDE.md, write docs/ffi/boundary-philosophy.md (trust boundary design), update code samples showing Result handling, run the full verification gate (18/18 sweep + go tests + cabal test + self-tests), then commit. To pause: touch .claude/allow-stop."
}
FFIEOF
    exit 0
fi

# Audit done + FFI docs done, v1.0 not done — fall back to the v1.0 guidance.
cat <<'EOF'
{
  "decision": "block",
  "reason": "Audit remediation is complete. v1.0 production readiness is still in progress per docs/PRODUCTION_READINESS.md and the technical brief at docs/NEXT_SESSION_BRIEF.md. Resume the P7/P8 typed-codegen work. Same rules as before: per-wrapper commits for P7 ([P7/partial] <wrapper>), per-module for P8 ([P8/<module>]), cabal test + example-sweep after every 5 commits. To pause: touch .claude/allow-stop. To finish: land the final v1.0 commit appending '## Current state snapshot — v1.0 complete' to the plan doc."
}
EOF
