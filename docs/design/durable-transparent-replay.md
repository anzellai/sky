# Transparent Msg-replay for `Std.Durable` — an epic, not a patch

> Status: SCOPED, NOT STARTED. This is the v2 item from
> [`durable-execution.md`](durable-execution.md) that is *not* bounded. Worker
> versioning and history compaction shipped as pure-Sky additions; transparent
> replay needs new Go runtime kernels and touches every TEA loop, so it is written
> up here and left for an explicit decision rather than attempted in passing.

## What it would be

Today a durable workflow marks its effect boundaries by hand with
`Durable.step ctx codec "id" effect`. The author supplies the `stepId`, which is
the journal key. **Transparent replay** removes that annotation: an ordinary TEA
app (`init` / `update` / `view`) would become durable without any `step` calls —
the runtime would journal each effect result and each `Msg` automatically, and a
resumed run would replay them to rebuild state.

## Why it is a runtime epic, not a stdlib function

The bounded v2 items are pure Sky over `Std.Db` because they are state-machine
edits expressible with `Db.exec`. Transparent replay is not — the machinery it
needs does not exist at the Sky layer:

1. **There is no general effect-interception seam.** `runtime-go/rt/test_mode.go`
   states it directly: determinism is injected *inside the few non-deterministic
   kernels* (Time/Random/Uuid) plus outbound HTTP; there is no runtime
   effect-dispatch table to swap, because `Ffi.kernel` lowers to a direct Go call.
   The mock-by-default HTTP layer (`runtime-go/rt/test_http.go`) returns static
   fixtures and records nothing — it is not a journal. So a transparent journal
   has no existing substrate to sit on; it must be built into the `Cmd` dispatcher
   in the Go TEA loop (`runtime-go/rt/cli.go`, and the Live / Spa / Tui loops).

2. **Effect keying without a `stepId` is a causal-order problem.** The automatic
   key must be deterministic across a resume. `Cmd.batch` runs commands
   concurrently (goroutines), so *completion* order is nondeterministic —
   journalling by completion order is unsound. It needs a causal key computed at
   dispatch (`(msgSeq, cmdIndex)`), in Go.

3. **The determinism contract must be *enforced*, not documented.** Today "code
   between steps must be deterministic" is a docstring. Transparent replay must
   detect a divergent replay or it corrupts state silently — that is the
   differential/divergence subsystem (the same machinery behind the Sky.Spa split
   oracle), not a helper.

4. **Unbounded history needs a Model snapshot kernel.** Folding every `Msg` since
   `init` through `update` on each resume is unbounded; it needs a serializable
   Model snapshot (continue-as-new), which itself builds on history compaction.

5. **Four loops.** Live, Spa, Cli and Tui each have their own dispatch loop; a
   general seam touches all four.

## The work, if it is greenlit

- an effect-journal hook inside the Go `Cmd` dispatcher, per loop;
- a deterministic effect-ordinal key that survives `Cmd.batch` concurrency;
- a Model snapshot / serialize kernel (a new `Ffi.kernel`), tied to
  continue-as-new (which reuses the compaction work already shipped);
- reuse of the divergence checker to enforce the determinism contract on replay.

## Recommendation

The explicit-`step` model (v1) already delivers durable, exactly-once workflows
and is what `Std.Ai.Agent` runs on. Transparent replay is a large ergonomics win
but a multi-week runtime project that touches the floor (new kernels, the TEA
dispatch loops). It should be greenlit as its own effort with its own branch and
budget, not folded into a stdlib patch. Worker versioning (the per-run version
stamp) and history compaction — both shipped — are prerequisites it will build on.
