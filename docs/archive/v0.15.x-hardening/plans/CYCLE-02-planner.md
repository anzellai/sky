# Cycle 2 — Planner integration of B-gaps + tag renumber

Plan written for: main @ 0171096
Date: 2026-05-25
Planner pass: 2

## Architectural diagnosis (cycle-2 delta)

Cycle 1 closed three of the six critical compiler items (P1 → v0.15.7, P3 → v0.15.8, P2-followup → v0.15.9) and surfaced a **standing direction** binding every future `coerceArg`/`goExprGoType` change to the three-way σ-recovery / TVar-erasure / skip-check consensus (per `HEAD-CYCLE-01-P2.md`). Cycle 2 Auditor's six new gaps cluster cleanly into three families:

1. **B1/B2 — structural-fallback identity hygiene (compiler, critical).** Direct followups to the P1 codegen path; they don't exist as panic classes today (Sky doesn't yet accept non-ASCII module names; the audit's two-module collision is structurally valid Sky but no current example hits it), but the fix-as-discovered-artefact rule applies: a regression test that fails on `main` once the user writes the colliding code is the contract. These are compiler items that must respect the cycle-1 σ-consensus direction.
2. **B3 — `__skyReviveScripts` event-handler attribute passthrough (runtime, high).** Pure JS-runtime hardening; isomorphic to A3/A7/A13's "writer/reader of a runtime invariant don't agree" pattern. A whitelist of safe `<script>` attributes closes it. Independent of all compiler items; interleaves with P14-P19.
3. **B4/B5 — form-submit submitter race + concurrent-patch desync (runtime, high/medium).** Both are JS DOM ordering invariants on the Sky.Live wire. B4 has a deterministic fix (drop the activeElement fallback OR install a global mousedown listener); B5 is mostly a documentation gap with an optional capture-state-snapshot hardening.
4. **P34 — cabal-test memory pathology (test infrastructure, measurement).** The CLAUDE.md non-negotiable says mem-guard SIGKILL'd cabal-test at >6 GB RSS during cycle 1; CI passed because runners have more RAM, but the floor for `cabal test` on a 16 GB dev Mac is now broken. Per-spec measurement work; subsumes the original P24 "skyshop RSS budget" item's local-dev concern.

The strategic ordering remains: cycle-1 items P4-P28 still ship in their planned order. The new B-gaps slot in as follows:

- **B2 (P30)** lands after the cycle-1 backlog because it gates P1's structural-fallback correctness (interim mitigation: dev-mode collision-warn log at v0.15.10 from P4 onward).
- **B1 (P29)** lands alongside P30. No runtime panic today, but lock prevents B1 from re-opening when Unicode module names eventually ship.
- **B3 (P31)** is the most urgent runtime item. Its fix is a 4-line attribute whitelist with a new Playwright assertion.
- **B4 (P32)** depends on B3's runtime test infra.
- **B5 (P33)** is documentation + optional snapshot; interleaved with cycle-1 P14.
- **P34** is independent measurement work; ships last.

## Tag renumber (cycle 1 plan items)

| Item | Old tag | New tag | Status |
|---|---|---|---|
| P1 | v0.15.7 | v0.15.7 | SHIPPED (PR #75, commit 11c11b7) |
| P3 | v0.15.9 | v0.15.8 | SHIPPED (PR #76, commit 5dca12c) |
| P2 + P2-followup | v0.15.8 | v0.15.9 | SHIPPED (PR #78, commit 359a795) |
| P4 | v0.15.10 | v0.15.10 | IN PROGRESS |
| P5 | v0.15.11 | v0.15.11 | PENDING |
| P6 | v0.15.12 | v0.15.12 | PENDING |
| P7 | v0.15.13 | v0.15.13 | PENDING |
| P8 | v0.15.14 | v0.15.14 | PENDING |
| P9 | v0.15.15 | v0.15.15 | PENDING |
| P10 | v0.15.16 | v0.15.16 | PENDING |
| P11 | v0.15.17 | v0.15.17 | PENDING |
| P12 | v0.15.18 | v0.15.18 | PENDING |
| P13 | v0.15.19 | v0.15.19 | PENDING |
| P14 (folds P17/P18) | v0.15.20 | v0.15.20 | PENDING |
| P15 | v0.15.21 | v0.15.21 | PENDING |
| P16 | v0.15.22 | v0.15.22 | PENDING |
| P19 | v0.15.23 | v0.15.23 | PENDING |
| P20 | v0.15.24 | v0.15.24 | PENDING |
| P21 | v0.15.25 | v0.15.25 | PENDING |
| P22 | v0.15.26 | v0.15.26 | PENDING |
| P23 | v0.15.27 | v0.15.27 | PENDING |
| P24 | v0.15.28 | v0.15.28 | ABSORBED INTO P34 |
| P25 | v0.15.29 | v0.15.29 | PENDING |
| P26 | v0.15.30 | v0.15.30 | PENDING |
| P27 | v0.15.31 | v0.15.31 | PENDING |
| P28 | v0.15.32 | v0.15.32 | PENDING |

Cycle-1 plan items keep their original tags; cycle-2 B-gap items take the tail (v0.15.33-v0.15.38).

## New items (P29-P34 from B-gaps)

### Item P29: Close Gap B1 — module-prefix alignment in `aliasBaseFromCanExpr`
- Closes: B1 (latent — no current panic, activates if Sky parses Unicode module names).
- Severity: critical (latent).
- Branch: `feat/v0.15.x-hardening-P29-alias-base-prefix-shared-helper`
- Patch tag: v0.15.33
- Fix: extract `moduleNameToGoPrefix :: ModuleName -> String` helper, use in BOTH `aliasBaseFromCanExpr` AND every codegen site building dep-module prefixes.
- New tests: `AliasBasePrefixSpec.hs` property test across 27 examples + 1 pending Unicode fixture.
- Risk: low; mechanical refactor.
- Session cost: 3-4 hours.

### Item P30: Close Gap B2 — alias-identity validation by (module, name) pair
- Closes: B2 — strengthens cycle-1 P1's structural-fallback against namespace collisions.
- Severity: critical.
- Branch: `feat/v0.15.x-hardening-P30-alias-identity-module-qualified`
- Patch tag: v0.15.34
- Fix: replace `_cg_recordAliases :: Set String` with `Set (ModuleName.Raw, String)`. Every populator constructs `(homeMod, name)` keys; consumers lookup against the typed key.
- New tests: `AliasIdentityCollisionSpec.hs` — two modules with same alias name; assert correct module-qualified resolution.
- Risk: medium; type-change ripple touches ~10 call sites.
- Session cost: 5-7 hours.

### Item P31: Close Gap B3 — script-injection whitelist in `__skyReviveScripts`
- Closes: B3 — XSS surface in Sky.Live patch path.
- Severity: high (security).
- Branch: `feat/v0.15.x-hardening-P31-script-revive-attr-whitelist`
- Patch tag: v0.15.35
- Fix: strict allowlist of safe `<script>` attrs (src, type, async, defer, integrity, crossorigin, nomodule, referrerpolicy, data-sky-script-revived). Drop event-handler attrs (onload, onerror, etc.). Drop inline script bodies.
- New tests: Playwright XSS canary (negative) + legit-revival (positive).
- Risk: low; defensive allowlist on a known-narrow input surface.
- Session cost: 3-4 hours.

### Item P32: Close Gap B4 — form-submit submitter via global mousedown capture
- Closes: B4 — Sky.Live multi-form submit-button identification race.
- Severity: high.
- Branch: `feat/v0.15.x-hardening-P32-form-submitter-mousedown-capture`
- Patch tag: v0.15.36
- Fix: install global `mousedown` capture listener; track `lastClickedSubmitButton` + timestamp; use as fallback when `ev.submitter` is undefined AND captured button's closest form === submitting form. Robust against repaints/rapid clicking.
- New tests: Playwright multi-form race; modern-browser regression; old-Safari simulation.
- Risk: low.
- Session cost: 4-5 hours.

### Item P33: Close Gap B5 — form-data concurrent-patch desync documentation + optional snapshot
- Closes: B5 — Sky.Live concurrent-patch form-data desync.
- Severity: medium (mostly documentation; optional behavioural hardening).
- Branch: `feat/v0.15.x-hardening-P33-form-data-concurrent-patch-doc`
- Patch tag: v0.15.37
- Fix: (1) docs section in `docs/skylive/overview.md` explaining snapshot timing + race window + recommended pattern. (2) Optional `WithFormStaleFilter()` decoder opt-in dropping disabled-at-snapshot fields, surfacing `FormStaleField` error.
- New tests: Go unit test for stale-filter + Playwright integration test.
- Risk: minimal; opt-in only.
- Session cost: 3-4 hours.

### Item P34: Cabal-test memory pathology — per-spec RSS measurement + isolation fix
- Closes: cabal-test mem-guard SIGKILL >6 GB during cycle 1 (CLAUDE.md non-negotiable §1).
- Severity: high (non-negotiable broken on local dev machine).
- Branch: `feat/v0.15.x-hardening-P34-cabal-test-rss-measurement`
- Patch tag: v0.15.38
- ABSORBS cycle-1 P24 (skyshop RSS budget).
- Fix: measure first via `scripts/measure-cabal-test-rss.sh`; identify top-5 RSS-heavy specs; apply fix per cause (deepseq force, split-to-subprocess, or test-suite stanza split). Add CI gate enforcing <1.5 GB per spec + <5 GB total.
- New tests: measurement script as the test; CI gate.
- Risk: medium; per-spec investigation depth unknown until measurement lands.
- Session cost: 6-8 hours.

## Updated total

- **Original 28 items + 6 new = 34 items.**
- 31 distinct PRs (P17/P18 fold into P14; cycle-1 P24 absorbed into cycle-2 P34).
- Patch tags: v0.15.10 (P4) → v0.15.38 (P34). v0.15.7-v0.15.9 SHIPPED.
- Cumulative session-cost: ~154-217 hours (5-7 weeks at 30h/week).
- Cumulative new tests vs cycle-1: +5 cabal specs + 4 Playwright scenarios + 2 Go unit tests.

## Standing directions added this cycle

1. **Cycle 1 P6-P9 (LowerCtx cascade) MUST consume the `RecordAliasKey` type** introduced in P30, not reintroduce flat-string lookups against `_cg_recordAliases`.
2. **No raising `MEM_GUARD_PROC_MB`** as a "fix" for P34. CLAUDE.md §1 is absolute.
3. **B3-B5 PRs MUST exercise their assertions via Playwright in `scripts/verify-all-web.sh`** (not solely Go unit tests).

## Sign-off checklist additions (cycle-2 only)

13. For B3/B4/B5: Playwright scenario evidence pasted in implementation report.
14. For compiler items P29/P30: σ-consensus voter audit included in PR description.
15. For P34: measurement output committed to `docs/v0.15.x-hardening/measurements/`.

Full per-item content (architectural diagnosis, sequenced steps, file lists, tests, gates, risks) is preserved verbatim in the Planner agent's task transcript at `/private/tmp/claude-501/-Users-anzel-works-playground-sky/f0f468dc-466c-4c96-bd19-85dfb01a1908/tasks/afe107451a0fcc897.output`. The Developer agent for each cycle-2 Item copies the corresponding item's full content into its own working file before starting.
