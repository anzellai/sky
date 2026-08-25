# AUTONOMOUS MANDATE — Unified App Builder

## Verbatim goal (captured 2026-08-25)

> ok cool please proceed e2e until fully completed ready to review/merge.
>
> in fully unattended + autonomous + PIV mode

## Established context this mandate operates within

The goal is to complete the **unified app builder** on branch
`feat/unified-app-builder` (off `main` @ a2e31843) — the design agreed in
`docs/design/unified-app-builder.md` and the phase plan in the
`unified_app_builder` memory. "Fully completed ready to review/merge" means all
phases implemented, integrated, and verified, the branch in a mergeable state —
but **NOT merged/tagged/released** (that needs a separate explicit ask, per
standing feedback). PIV = Plan → Implement → Verify, grilling every phase with
its own adversary (feedback_grill_every_phase).

## Success criteria (what "done" requires — Judge verifies the LITERAL claim)

1. **`Std.App`** — a single unified builder (`App.app { init, update, view,
   subscriptions }` + `withX` capability builders) that wraps/subsumes the five
   app shapes (Sky.Live / Sky.Spa / Sky.Tui / Sky.Cli / Sky.Webview), on the
   correct `Std.*` namespace side.
2. **One extendible `--target family[:variant]`** axis fully wired end-to-end
   (Phase 1 landed the parser; the build must dispatch on it), including the
   `web` (server, = Sky.Live) vs `web:app` (client, = Sky.Spa) semantic split,
   with invalid combinations impossible by construction.
3. **Derived, never a flag**: renderer from family; split from the sandbox;
   backend-or-not from the effects used. No `--html`/`--wasm`/`--split`/`web:spa`.
4. **Mandatory per-target config = capability model**, validated per target with
   clear fix-it errors (optionally `App.targets [...]` at check time).
5. **Strictly non-breaking migration**: existing `Live.app` / `Spa.app` /
   `Tui.app` / `Cli.program` / `Webview.app` code keeps working (thin aliases).
6. **Docs + templates + sky-lang** updated in the same commit-spirit as the
   design (AGENTS.md app-shape matrix collapses; `sky doc`; templates).
7. **Green everywhere**: `cargo test --workspace`, the xtask gate suite (incl.
   census/ratchet gates for the new stdlib+CLI surface: denominators /
   coverage-ledger / config-surface / kernel_api if touched), the full example
   sweep, conformance — all green on a clean slate.

## Hard constraints (INVIOLABLE)

- No merge / tag / release without explicit user ask. Get it READY only.
- **darraghstudio — HARD HOLD**: do not touch/deploy/upgrade it (live traffic +
  orders). See darraghstudio_repo_hygiene.
- Root-cause fixes only; no deferral framings; every fix gets a regression test
  first. Only an independent adversarial Judge (fresh context, this verbatim
  goal) may return "100% achieved".
- Local commits are checkpoints; do not push per-iteration.
