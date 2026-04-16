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
#   3. End-to-end testing harness — behavioural contracts per example
#      to catch logic/CLI/DB regressions that HTTP-200 probes miss.
#      Brief: .claude/prompts/end-to-end-testing.md
#      Marker: `.claude/e2e-harness-complete` exists.
#   4. Compiler + CLI + LSP audit — every command and language
#      construct gets a regression spec; skychess AI investigated;
#      docs reconciled with reality.
#      Brief: .claude/prompts/compiler-cli-lsp-audit.md
#      Marker: `.claude/compiler-cli-lsp-audit-complete` exists.
#   5. Known-limitations resolution — convert each entry in
#      docs/KNOWN_LIMITATIONS.md into a shipped fix or a
#      deliberate-deferral with stated justification.
#      Brief: .claude/prompts/known-limitations-resolution.md
#      Marker: `.claude/known-limitations-resolved` exists.
#   6. Compiler soundness + LSP diagnostic parity — canonicaliser
#      catches every source-level error (not Go as a fallback);
#      LSP surfaces every compile-time error as publishDiagnostics.
#      Brief: .claude/prompts/soundness-and-lsp-diagnostics.md
#      Marker: `.claude/soundness-lsp-complete` exists.
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

# ── Gate 3: end-to-end test harness
E2E_MARKER=".claude/e2e-harness-complete"
if [[ ! -f "$E2E_MARKER" ]]; then
    cat <<'EOF'
{
  "decision": "block",
  "reason": "End-to-end test harness NOT complete. Current CI catches compile-time and HTTP-200 smoke regressions but MISSES logic/CLI/DB regressions. User caught three manually (todo-cli args broken, skychess AI regressed to nonsense moves, skyvote comment insert hits UNIQUE constraint). Read .claude/prompts/end-to-end-testing.md — it's a 6-item self-contained brief: add e2e.json contract per example, extend `sky verify --e2e` runner, fix the three known bugs (each gated by its contract), wire into CI. Done condition: touch .claude/e2e-harness-complete after all six items land. Do NOT change example semantics to pass tests — the bugs are real and must be fixed at the code level. To pause: touch .claude/allow-stop."
}
EOF
    exit 0
fi

# ── Gate 4: compiler + CLI + LSP audit
AUDIT_MARKER=".claude/compiler-cli-lsp-audit-complete"
if [[ ! -f "$AUDIT_MARKER" ]]; then
    cat <<'EOF'
{
  "decision": "block",
  "reason": "Compiler + CLI + LSP audit NOT complete. Multiple gaps suspected: skychess AI plays nonsense moves despite algorithmically-correct negamax (likely Move.applyMove or eval indexing bug); CLI commands lack per-subcommand specs (any silent exit-code regression goes undetected); LSP capabilities documented in docs/tooling/lsp.md may be aspirational. Read .claude/prompts/compiler-cli-lsp-audit.md — it's a 8-item brief covering: (1) skychess AI Sky-level tests + fix, (2) compiler regression specs for every language construct + error message, (3) test/Sky/Cli/*Spec.hs for every sky subcommand, (4) LSP per-capability specs, (5) port the bash e2e harness into `sky verify --e2e` (Haskell), (6) reconcile docs with reality, (7) push + watch CI green, (8) touch .claude/compiler-cli-lsp-audit-complete. Do NOT relax test expectations — if a behaviour is hard to assert, the behaviour is the bug, pin it down. Do NOT add v1.0 references. To pause: touch .claude/allow-stop."
}
EOF
    exit 0
fi

# ── Gate 5: known-limitations resolution
KL_MARKER=".claude/known-limitations-resolved"
if [[ ! -f "$KL_MARKER" ]]; then
    cat <<'EOF'
{
  "decision": "block",
  "reason": "Known limitations NOT resolved. docs/KNOWN_LIMITATIONS.md still lists: skychess AI sub-optimality (root cause unknown), 7 missing CLI subcommand specs, 8 missing LSP capability specs, bash→Haskell e2e port pending, plus categorical 'compiles-it-works' gaps (no Sky-test runner in cabal-test, no concurrency fixtures, no external-service-skip semantics). Read .claude/prompts/known-limitations-resolution.md — it's an 8-item brief with diagnostic-first methodology: write the failing test FIRST, then fix, then doc-update. Each session lands at minimum one item end-to-end. Done condition: every entry in KNOWN_LIMITATIONS.md is either fixed (entry purged) or has explicit 'won't fix in v0.9' justification, then `touch .claude/known-limitations-resolved`. Do NOT ship spec-without-fix limbo — if a spec lands, the underlying fix lands in the same session. Do NOT add v1.0 references. To pause: touch .claude/allow-stop."
}
EOF
    exit 0
fi

# ── Gate 6: compiler soundness + LSP diagnostic parity
SOUND_MARKER=".claude/soundness-lsp-complete"
if [[ ! -f "$SOUND_MARKER" ]]; then
    cat <<'EOF'
{
  "decision": "block",
  "reason": "Compiler soundness + LSP diagnostic parity NOT complete. Empirically verified gaps: (1) canonicaliser doesn't catch undefined variables — typos pass through to Go which errors with 'compiler-side bug'; not user-friendly, no position. (2) LSP's computeDiagnostics pipeline skips the exhaustiveness pass — users see 'case does not cover: Blue' at sky-build time but the editor stays silent. Dev experience is top priority; these are the biggest regressions in the compile→error→edit loop. Read .claude/prompts/soundness-and-lsp-diagnostics.md — 7-item brief: fix Gap 1 (canonicaliser), DRY the LSP test harness, fix Gap 2a (LSP exhaustiveness), 2b (LSP unbound), broader audit of diagnostic quality, CI parity, touch .claude/soundness-lsp-complete. Tests first (failing-at-HEAD-passing-post-fix pattern). No spec-without-fix limbo. Realistically 2-4 sessions. To pause: touch .claude/allow-stop."
}
EOF
    exit 0
fi

# All gates green — let the turn end.
exit 0
