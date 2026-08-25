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

## Judge verdict (2026-08-25, branch @ 6b0dc094, independent adversarial)

- **SHIPPED-SCOPE = ACHIEVED + VERIFIED**: one `App` value feeds all 5 runners
  (cargo 4/4); web/terminal:tui/terminal:cli/desktop dispatch + build + DCE-prune;
  mix-safety rejects bad combos with did-you-mean; the 5 kernels are byte-identical
  to main (non-breaking); census gates + docs + templates green; client boundary
  honest and never claimed done. Also verified separately: example-sweep 29/0,
  harness 24/0, sky-suites 26/26, doc-examples 14/14.
- **LITERAL (7 criteria incl. client targets subsumed by Std.App + build-time
  capability model) = NOT ACHIEVED — 2 gaps**:
  1. Client-wasm (web:app/mobile/tablet) NOT subsumed by Std.App — still needs a
     Std.Spa entry. Wiring requires `spa_partition` to trace through the `App.app`
     indirection (empirically: "no resolvable case msg of") — a substantial,
     risky change ADJACENT TO the proven auto-split path the user has repeatedly
     + explicitly forbidden risking for live-traffic apps (darraghstudio).
  2. Capability validation is RUNTIME (runLive Task.fail), not build-time/
     construction-level; `App.targets [...]` unbuilt. Build-time would need
     HIR-level analysis of the App.app builder chain.

**ESCALATION (per §0 rule 4 — ambiguous user-decision on scope + risk):** both
remaining gaps need substantial HIR-level analysis of the App builder, and gap 1
is adjacent to the fenced-off split path. Shipped scope = the agreed design
(web:app = Sky.Spa, established in design §6). Awaiting user direction: pursue the
larger client-subsumption + build-time-capability work (accepting the effort +
split-path risk), or accept the documented boundary as the merge scope.

## Update (2026-08-25) — GAP 2 CLOSED, user-designed capability model

User directed the capability model (their design: minimal core + uniform withX +
mandatory config enforced by the ADT). Implemented a phantom `fallback` flag →
`withNotFound` mandatory for web at COMPILE time (Phase 3, commit 00520e55).
Second independent Judge: **GAP 2 = ACHIEVED + VERIFIED** (runLive rejects
NoFallback, runTui accepts; terminal-only not forced; clean error; 6/6 tests;
kernels byte-identical; census green; zero annotation burden).

**LITERAL goal now = 1 gap remaining: gap 1 (client-wasm subsumed by Std.App).**
That is the fenced-off spa_partition work the user forbids risking for live apps;
it remains the documented Sky.Spa boundary. Everything else (criteria 1,3,4,5,6,7)
holds and is verified. Branch is mergeable for the shipped scope.

## Update 2 (2026-08-25) — GAP 1 CLOSED (user chose Spa subsumption)

User directed: App.run explicit entry + optional --target (done, 26125c33), then
Spa subsumption (fcaa596e). Client targets (web:app/mobile/tablet) now build from
the ONE Std.App source: the build SYNTHESISES a Spa.app from the App.app value
(extracts init/update/view/subs + withRoutes/withNotFound from the fmt'd form,
references the user's fns DIRECTLY, view wrapped in Ui.layout []) and feeds the
EXISTING UNCHANGED auto-split — so spa_partition sees `update`, the proven split
path is NOT modified (the fenced-off risk avoided). Verified: build web:app →
backend+wasm; run web:app → HTTP 200 SPA shell. std_app_flow 7/7.

Confirmed taxonomy (delivery=family, native=platform): web/tablet→Live,
desktop→Live+native-window, web:app/desktop:os/tablet:os/mobile:os→Spa,
terminal:tui|cli→Tui/Cli. Std.Ui→Std.App (cross-platform); Std.Html→Sky.Live.

**Still remaining (roadmap, not yet done):** taxonomy remap (desktop bare→
Live-in-webview [NEW runtime mode], desktop:os/tablet:os→Spa, tablet bare→Live);
App.withRequest (portable init seed). Both additive. LITERAL goal now essentially
met (Std.App covers every target from one source); pending re-Judge + the remap.
