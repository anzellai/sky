# Autonomous mandate — config/withX overhaul + perf (2026-08-18)

VERBATIM USER DIRECTIVE:
"we need the sky.toml + withX overhaul fully closed + perf done."
Sliding session: "the sliding session should expand to non Auth sessions too";
auth-token intent: "bump the TTL whenever a user interacts, so auth'd sessions
continue working if user has interactions, rather than hard cut off from the
first interaction."
Mode: "fully autonomous + agents + grill + PIV mode."

(Supersedes the completed v0.17 compiler-close mandate; git preserves that history.)

## Scope (from .claude/TELEMETRY_STORAGE_PLAN.md follow-up list)

HEADLINE (user-emphasised "fully closed"):
  A. sky.toml + withX config overhaul — stages 1-3 + withX builders SHIPPED on
     main; MISSING: legacy->new format migration/converter, full withX as a
     complete user-facing sky.toml replacement, upgrade path printed on
     sky run/build when legacy config detected. "phase 1 of N" -> close it.
  B. Perf — render-path / model-diff selective render. Diff the MODEL, static
     map of which view subtrees read which fields, re-render only dirty regions
     (sound because view is pure). The framework benchmark identified this as
     THE lever: Django took the interaction ~2x because it renders 1 fragment
     where Sky re-renders 30 posts + diffs.

SECONDARY:
  C. Sliding Std.Auth JWT re-issue (rolling, ABSOLUTE-CAP) + 30-day(csrf)/
     30-min(live) TTL default reconciliation. Non-auth sky_sid ALREADY slides
     (verified live_store.go:499/507/554/715) — add the regression gate that an
     interacting session never expires.
  D. Telemetry storage P1-P3 (size report, metric aggregation; the 2 live bugs
     already merged on main).
  E. runPerform concurrency bound (live.go:5786) — dependent-perform deadlock +
     must-not-serialise-parallel-fetch constraints; NOT a drop-in.
  F. Cosmetic: coerce-floor --bless drops golden header comments; freshness gate
     mtime for Rust sources; --verify-falsifiers T2-T4; gc_tuning.go:101 number.

## Method (INVIOLABLE per CLAUDE.md §0)
- PIV per phase: Architecture-Consult -> adversarial grill -> implement ->
  Judge (fresh context, verifies the LITERAL claim). No "done" without Judge PASS.
- Phase boundaries commit + verify. Milestone verification MUST include
  coerce-floor (the gap that slipped CI last wave). Local green != CI green until
  build.sh + workspace + sweep + conformance + harness --tier t1 + coerce-floor
  all pass, on a log confirmed to be THIS run (noclobber/case-insensitive trap).
- Don't bloat one branch: merge coherent phases; split a branch if it grows.
- Grill every phase. The recurring class this session found is "the check
  measures a proxy for the thing." Assume each phase breaches.
