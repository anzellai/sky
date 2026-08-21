# AUTONOMOUS GOAL — Sky.Spa v1 (desktop/mobile-first) — BUILD MANDATE

## Verbatim mandate (user, 2026-08-21→22)

> desktop/mobile first, go all-in in fully autonomous + agents + grill + PIV
> mode... fully unattended until building the greenlight v1 is fully done,
> working e2e. set loop & schedule accordingly. you know my preferences, and
> fully aligned, i will be away now, so please proceed.

Greenlit option (a) from the prior turn: build the **desktop/mobile-first
explicit-boundary Sky.Spa v1**, starting by productionizing the partition to
merge quality, then the client diff, then `Std.Spa` + the explicit boundary, to a
real e2e example. Web (TinyGo/JS) and the v2 auto-split are explicitly LATER bets,
NOT part of v1.

## Definition of DONE (the fresh-context Judge verifies this LITERAL list)

v1 is done only when ALL hold, verified e2e (not just built):

1. **Partition landed on `exp/spa` at merge quality.** `exp/spa-prototype`'s
   runtime partition is on `exp/spa`; full gate suite green per CLAUDE.md §0.2.1
   (workspace tests + xtask harness T1/T2 + census/coverage/config-surface/
   kernel-api + full example-sweep build-and-run + conformance); Sky.Live
   **unbroken** (09-live-counter + 19-skyforum e2e).
2. **Client-side diff renderer** — not full re-render; reuses `diffTrees` +
   `__skyApplyPatches` focus/cursor/dirty-input authority logic client-side. A
   form-input focus-preservation test passes.
3. **`interpretCmd` real effects** — client Task-running for `perform` (async,
   dispatch result), client effects (`Time`, `Http`/fetch), subscriptions
   (`Sub_every`/timers). `publish` bridged in-tab or explicitly documented as
   out-of-scope-for-v1.
4. **`Std.Spa` v1 surface** — `config { init, update, view, subscriptions,
   routes }` + `app`; client-side routing (History API); the **explicit
   author-declared server boundary** (client `Http` to a stateless Sky backend +
   shared `Std.Codec`). Kernel FFI registered (`kernel_api.rs` gate green); `sky
   doc Std.Spa` works.
5. **A real e2e example Sky.Spa app** — non-trivial (client UI state + explicit
   server persistence via Http + shared Codec against a stateless Sky backend).
   Verified e2e: renders, interacts client-side (zero round-trip on pure UI),
   persists/loads via the backend, reconnect-safe. Runs in a browser (served
   locally) and, if feasible, a Sky.Webview desktop shell. Bundle size recorded.
6. **Cross-platform `Element`** — the SAME view renders on the Spa client; no
   `Std.Html`-only lock-in beyond documented escape hatches.
7. **Docs + templates synced** — `design.md` status → v1 shipped; `Std.Spa` doc;
   app-shape matrix in `AGENTS.md` + `templates/{AGENTS,CLAUDE}.md`; `docs/skyspa/`
   overview. Live-docs gate green.
8. **The five pillars hold** (Judge checks each): DX (written like Sky.Live),
   scalability (stateless backend), maintenance (one lang/type/Element/Codec),
   performance (pure UI client-local; bundle recorded + honest), security
   (explicit boundary; untrusted-client rule; typed secrets; prod gate).

Forbidden in a PASS verdict: "but / except / however / caveat / mostly /
essentially / for the scope of / modulo". A genuine implementation blocker needing
a user decision is documented + surfaced, not glossed — but per the mandate,
proceed through everything I can decide myself.

## Phase plan (each: Architecture-Consult → grill → implement → verify; commit per phase)

- **P1 — Productionize + land the partition** onto `exp/spa`; full §0.2.1 gates
  green; Sky.Live unbroken. Handle census/coverage/kernel-api/config-surface
  ratchets the new module+kernels+build-tags trip.
- **P2 — Client-side diff renderer** (diffTrees + __skyApplyPatches reuse).
- **P3 — interpretCmd real effects** (perform async, Time/Http, subscriptions).
- **P4 — Std.Spa v1 surface + explicit server boundary** (routing, Http+Codec).
- **P5 — Real e2e example app** (client UI + stateless Sky backend) + browser e2e
  verify (+ Webview if feasible) + bundle number.
- **P6 — Judge + docs/templates sync + final full sweep.**

## Durable state (survives compaction / new session)

- Progress tracker: `docs/skyspa/v1-progress.md` — updated at every phase boundary
  (what's done + verified, what's next, how to resume). READ IT on resume.
- Verified prototype baseline: branch `exp/spa-prototype` (partition + render,
  independently verified 2026-08-22). Design: `design.md`, `auto-split.md` (v2),
  `prototype-results.md`.
- Work branch: `exp/spa`. Invasive runtime work → worktree-isolated agents.

## Loop protocol

Unattended: drive phase-by-phase with agents (worktree-isolated for invasive
runtime surgery), grill each phase's plan + result, fresh-context Judge at the
close against the DONE list. Agent completion notifications re-invoke the loop;
ScheduleWakeup is the heartbeat/safety-net (not a pacing mechanism). Checkpoint
with commits + the progress tracker at every boundary. Do NOT push to origin or
merge to `main` without explicit user ask (durable feedback). Continue until the
Judge returns 100% on the DONE list.
