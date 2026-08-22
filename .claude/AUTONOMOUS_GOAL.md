# AUTONOMOUS GOAL — compiler-wide reflection-free codegen (branch `exp/spa`)

## Verbatim mandate (user, 2026-08-22)

> ok please do the de reflection whole site, and coercion work. in fact I wanted
> fully non-reflected codegen anyway so the work we will do eventually benefits
> what sky compiler is. fully autonomous+ unattended + ask perms & continuity
> upfront so you can carry out e2e without any of my inputs or decisions

## Decisions captured upfront (so no further input is needed)

1. **Scope = COMPILER-WIDE reflection-free codegen.** Build the de-reflection
   mechanism GENERALLY so emitted Go eliminates `reflect.Value.Call` /
   `reflect.MakeFunc` (and the reflect-based coercion) across targets — a general
   Sky-compiler improvement, not only the SPA client. Land INCREMENTALLY with the
   **todos client running under TinyGo (web-viable)** as the first e2e proof.
2. **Merge to `main` = GATED to the user.** Land everything verified on `exp/spa`
   (PR #189, kept green). Do NOT merge to `main`.
3. **Release = NONE.** Do NOT bump version / tag / cut a release. Leave to the user.

## What "done" means (Judge verifies the LITERAL list)

1. **The real `examples/60-spa-todos` client runs under TinyGo** — compiles,
   renders, and passes its e2e (durable CRUD round-trip + rehydrate) with the
   TinyGo-built wasm; real bundle recorded (target ~100 KB gz once the size cliff
   trips).
2. **Reflection-free emission, generally.** Emitted Go for the client reaches
   ZERO reachable `reflect.Value.Call`/`reflect.MakeFunc` (proven), via a GENERAL
   codegen mechanism (typed coercion + typed dispatch/HOF + typed codec), not
   client-only hacks. The mechanism reduces the reflect surface for other targets
   too; document the compiler-wide reflect inventory + how much is eliminated vs
   residual (with each residual's reason).
3. **Coercion de-reflected at codegen** — the any→typed narrows (`Coerce` /
   `coerceInner` / `ResultCoerce` / `narrowReflectValue` / `mapToRecordStruct` /
   slice+struct+container narrows) emit reflection-free using the STATIC element/
   field types codegen knows (route slices through `AsListT[Elem]`, structs
   field-wise, containers via the typed fast-paths). This is the crux finding from
   `docs/skyspa/dereflect-progress.md`.
4. **Dispatch/HOF de-reflected** — `sky_call`/`sky_call2`/`SkyCall`/`pipelineApply`
   eliminated from the client graph (typed closures / typed HOF twins), extending
   D3a/D3b generally.
5. **NO regressions, everything green** — full §0.2.1 suite (workspace + harness
   T1/T2 + census + example-sweep + conformance) green; Sky.Live/Tui/CLI byte-
   behaviour unchanged (09/19 build+run); all prior Sky.Spa acceptance + DB e2e
   pass; PR #189 CI green. Server correctness is never traded for de-reflection.
6. **Honesty** — real bundle numbers measured (not projected); the reflect
   inventory + residuals stated plainly; nothing marked shipped/released.
   Forbidden in a PASS verdict: but/except/however/caveat/mostly/essentially/
   for-the-scope-of/modulo.

## Progress already landed (exp/spa @ 15354f12)
D1+D2 (spa-counter runs under TinyGo, CI-green, 521 KB), D3a (client dispatch),
D3b (codec applicative), D3c-partial (ResultCoerce). The wall: functional
record-app needs CODEGEN-level coercion de-reflection (element type known only at
emit). See `docs/skyspa/dereflect-progress.md` for the precise resume point.

## Autonomy scope
Full local + push `exp/spa` to origin at milestones (PR #189 CI). Agents +
worktrees allowed. TinyGo at /opt/homebrew/bin. NO merge-to-main, NO release.

## Agent-stall mitigation (5 agents stalled this session)
Do critical codegen surgery MYSELF, foreground + bounded. Use agents only for
read-heavy research (consults) + parallelizable verify, with explicit "no
run_in_background+wait" and "commit before any long step". Coordinator verifies
each phase itself, foreground.

## Loop / durable state
Progress tracker: `docs/skyspa/dereflect-progress.md` (update every phase).
Drive phase-by-phase; ScheduleWakeup is the safety-net heartbeat; agent-completions
+ my own foreground gates are the primary signals. Continue until the fresh-context
Judge verifies the DONE list on `exp/spa`, then STOP and report (user merges).
