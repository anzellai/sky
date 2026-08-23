# AUTONOMOUS GOAL — Sky.Spa auto-split, e2e #1–#3 (branch `exp/spa`)

## Verbatim mandate (user, 2026-08-23)

> ok agreed, please proceed in fully unattended + autonomous mode for e2e 1-3

Referring to the three items I proposed and the user agreed to:

1. **Generalize the generator to a real app** — Msg-arg typed Req fields,
   whole-model fallback record, multi-module apps, non-primitive field codecs —
   so `sky spa-split` handles a todos-in-one-project, not just the skeleton.
2. **Fail-closed effect-family guard** — an unrecognized effect kernel becomes a
   build failure (the analysis/generator must never default an unknown effect to
   client/pure), closing the last soundness seam.
3. **Server-push / subscription channel** — SSE or WebSocket for server→client
   push, closing clojj's #168 WebSocket case and cross-client `Cmd.publish`.

Order: **#2 → #1 → #3** (lock the soundness guarantee, then make it eat real
apps, then add push). Each phase: build → verify (myself, on generated output) →
commit. Security spine throughout: an effectful value/function never reaches
client code, and it is a BUILD FAILURE if it could.

## Done means

`sky spa-split` handles a real one-project app (todos-in-one-project) end to end:
generates a wasm frontend + native backend + shared contract that BUILD and
round-trip; the generated frontend is provably free of effectful code
(leak-check + the fail-closed guard); and the app supports server→client push so
a WebSocket-style / cross-client-fanout app works. Verified on generated output.

## Decisions captured upfront (fully autonomous, no check-ins)

- Fully autonomous + unattended. No permission/continuity asks. Halt ONLY on a
  genuine blocker (a real ambiguity I cannot resolve from code/goal, an external
  wall). Describe it, then continue on direction.
- Branch `exp/spa`. NO merge to `main`, NO release/tag unless the user says so.
- Verify continuously; nothing is "done" until I've run the generated output
  myself (build both + round-trip + leak-check).

## State at mandate start

Auto-split B0–B4 complete + verified: `sky spa-partition` (inference/report),
secure-by-default rule (any effect → server), B0 Msg-constant precision, B1
read/write sets, B2 walking skeleton (proven), B3/B4 `sky spa-split` generator
(e2e on the skeleton: leak-check clean, round-trip verified). Design +
progress: `docs/skyspa/auto-split.md` (§11–§15).

## Loop / durable state

Drive #2 → #1 → #3, each verified + committed on `exp/spa`. Progress tracker:
`docs/skyspa/auto-split.md`. Continue until all three work e2e on a real app,
then report. Genuine blocker → describe + await.
