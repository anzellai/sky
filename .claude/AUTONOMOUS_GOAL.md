# AUTONOMOUS GOAL — Sky.Spa de-reflection for prod web (branch `exp/spa`)

## Verbatim mandate (user, 2026-08-22)

> ok let's do it, dereflect, in same manner, fully unattended + autonomous

Context (the approved plan): the prod-web exploration (`docs/skyspa/prod-web.md`)
measured that a **reflection-free** Sky.Spa client compiles under **TinyGo** to
~65 KB (counter) / ~103 KB (todos-scale surrogate) gzip — web-viable, ~23× smaller
than the current reflect-heavy 2.4 MB. The blocker is that the real client core is
reflection-native (`live_core.go` `sky_call`/`sky_call2` → `reflect.Value.Call`;
`codec_auto.go` 85 reflect sites; `adt_shape.go`). **De-reflect the client core so
a REAL Sky-emitted client compiles under TinyGo to a web-viable bundle.**

## What "done" means (the Judge verifies the LITERAL claims)

Start with the SMALLEST real client and expand:

1. **Real Sky-emitted `spa-counter` client compiles under TinyGo** (`tinygo build
   -target wasm`) with **no `reflect.Value.Call`/`reflect.MakeFunc` in the client
   dispatch path**, and the produced wasm **renders + runs the TEA loop** —
   headless-verified functionally equivalent to the reflect build (init→0, +1×3→3,
   Reset→0, −1→−1). Real measured bundle recorded (target: web-viable, ~100 KB gz).
2. **Isolation — Sky.Live is UNTOUCHED.** The de-reflection lives in a
   **client-only runtime** (a `//go:build js`/`spa` variant, e.g. `rt_spa`), so
   the server-side reflection path (`sky_call`, `Codec.auto`, `adt_shape`) that
   Sky.Live/Tui/CLI rely on is byte-unchanged and unregressed. Full §0.2.1 green
   (workspace + harness T1/T2 + census + example-sweep + conformance); 09/19
   build+run.
3. **Dispatch de-reflected** — finish the `perMsgTypedDispatch` Stage 6 (typed
   dispatch table keyed on the Msg ADT; thread the ADT name from the call site) OR
   an equivalent reflection-free client dispatch; codegen emits what it needs.
4. **Codec + ADT de-reflected for the client** (needed for the todos client, which
   decodes `data`): a reflection-free client codec path (typed, not `Codec.auto`
   reflection) + reflection-free ADT shape. THEN the real **todos client compiles
   under TinyGo, renders, and its e2e (run_roundtrip.sh) still passes** — real
   todos web bundle recorded.
5. **No regressions** — all prior Sky.Spa acceptance tests (spa-counter/input/
   perform/sub/http/router/boundary + todos e2e + the DB e2e) still pass on the
   standard build; the CI (PR #189) is green.
6. **Honesty** — TinyGo stdlib gaps surfaced (not just reflect); real bundle
   numbers measured, not projected; nothing marked shipped/released (still
   experimental, exp/spa). Forbidden in a PASS verdict: but/except/however/caveat/
   mostly/essentially/for-the-scope-of/modulo.

Staged: spa-counter first (dispatch + minimal), then codec/adt, then todos. Each
phase: Architecture-Consult (cite files; §0.3) → grill → implement (isolated to
the client runtime) → verify (TinyGo build + render + Sky.Live untouched).

## Autonomy scope (carried from the v1 build)

Full local autonomy + push `exp/spa` to origin at milestones (PR #189 CI).
Merge-to-main REMAINS gated — ask first. TinyGo installed at /opt/homebrew/bin.

## Agent-stall mitigation (learned this session)

4 agents stalled hanging on a background build/test they `run_in_background` +
waited on. Instruct every agent: run builds/gates in the FOREGROUND (bounded via
`timeout`/`with_timeout`), COMMIT before any long wait, and do NOT spawn a
background task then block on a monitor. Coordinator verifies each phase itself
(foreground) rather than trusting a stalled agent's report.

## Durable state / loop

Progress tracker: `docs/skyspa/dereflect-progress.md` (create; update every phase).
Verified baseline: Sky.Spa v1 @ `exp/spa` (all gates green). Design refs:
`docs/skyspa/prod-web.md`, `auto-split.md`, `design.md`. Drive phase-by-phase with
worktree-isolated agents + grill + fresh-context Judge at close; ScheduleWakeup is
the safety-net heartbeat, agent-completions are the primary signal. Continue until
the Judge verifies the DONE list.
