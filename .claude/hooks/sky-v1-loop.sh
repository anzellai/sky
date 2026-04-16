#!/usr/bin/env bash
# Stop-hook gate for Sky.
#
# Fires every time the model tries to end a turn. Returns a JSON
# "block" decision unless one of the escape conditions is met, forcing
# the model to keep working until the active plans are marked complete.
#
# Active gates (checked in order — first failing one blocks):
#   1. Audit remediation (docs/AUDIT_REMEDIATION.md) — soundness/security audit.
#      Marker: `## Audit remediation complete` heading.
#   2. FFI boundary docs cleanup — update docs/samples/tests after the
#      P0-P3 FFI fixes (commit e1faa21) so they match the new mapping.
#      Marker: `docs/ffi/boundary-philosophy.md` exists.
#
# Manual escape: `touch .claude/allow-stop` lets the turn end regardless
# of gate state. Remove the file (or it's removed by `git clean -fdx`)
# to re-engage the loop.
#
# v1.0 production-readiness was removed from this gate. Sky is at v0.9
# by explicit user decision; v1.0 is reserved for production-proven
# state and isn't a session-blocker.
#
# Per user instruction: NO automated runaway brake (no commit-count
# heuristic, no time-based break). Only the markers above and the
# manual pause file release the gate.

set -uo pipefail

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO"

# ── Manual pause always wins
if [[ -f .claude/allow-stop ]]; then
    exit 0
fi

# ── Gate 1: Audit remediation
AUDIT_DOC="docs/AUDIT_REMEDIATION.md"
audit_done=0
if [[ -f "$AUDIT_DOC" ]] && grep -q '^## Audit remediation complete' "$AUDIT_DOC" 2>/dev/null; then
    audit_done=1
fi

if [[ $audit_done -eq 0 ]]; then
    next_item="$(grep -E '^\| P[0-9]-[0-9]+ \|' "$AUDIT_DOC" 2>/dev/null \
        | grep -F '| ☐ |' \
        | head -1 \
        | awk -F'|' '{print $2 "—" $3}' \
        | sed 's/^ *//; s/ *$//')"
    [[ -z "$next_item" ]] && next_item="(tracker appears empty — read $AUDIT_DOC directly)"

    remaining="$(grep -cF '| ☐ |' "$AUDIT_DOC" 2>/dev/null || echo 0)"
    done_count="$(grep -cF '| ☑ |' "$AUDIT_DOC" 2>/dev/null || echo 0)"

    cat <<EOF
{
  "decision": "block",
  "reason": "Audit remediation NOT complete. Progress: ${done_count} done, ${remaining} remaining. Next item: ${next_item}. Read .claude/prompts/audit-remediation.md and docs/AUDIT_REMEDIATION.md. Per-item loop: write a failing-first test, implement the fix, run the regression fence (test-files self-tests, scripts/example-sweep.sh, runtime-go go test), tick the tracker, commit with [audit/P<n>-<m>] label. To finish: append '## Audit remediation complete' to docs/AUDIT_REMEDIATION.md. To pause: touch .claude/allow-stop."
}
EOF
    exit 0
fi

# ── Gate 2: FFI boundary docs cleanup
FFI_MARKER="docs/ffi/boundary-philosophy.md"
if [[ ! -f "$FFI_MARKER" ]]; then
    cat <<'EOF'
{
  "decision": "block",
  "reason": "FFI boundary docs cleanup NOT complete. The compiler fixes (P0-P3) landed in e1faa21 but the docs/samples/tests still reflect the OLD mapping. Read .claude/prompts/ffi-boundary-docs.md — it's a self-contained 7-item brief with verification gates. Done condition: docs/ffi/boundary-philosophy.md exists, doc commit landed, all six verification gates green (compiler builds, 67/67 self-tests, 18/18 example sweep, runtime go tests, cabal test, sky verify on key examples). Do NOT propose changing FFI from Result to Task — that's a settled design (Result for synchronous boundary, Task for deferred Sky effects). Do NOT add v1.0 references — v0.9 is the current version. To pause: touch .claude/allow-stop."
}
EOF
    exit 0
fi

# All gates green — let the turn end.
exit 0
