# v0.27.0 Phase 7 — typed Result payload patterns, and the Task trampoline

Branch `phase7/task-trampoline`, cut from `release/v0.27.0` at `c651aafb`.
This file is the Phase 0 record required by `CLAUDE.md` §0.3: the architecture
consult, the plan, the invariants and the self-grill (G1–G5 of §0.4). It was
written before any code changed. Two parts, independent of each other.

---

## Part A — read a typed `OkValue` / `ErrValue` / `JustValue` directly

### What the compiler emits today

A `case` on a Result whose Go type is already typed re-narrows the payload
field it reads (measured on `c651aafb`, `sky build` of a two-function program):

```go
func Main_describe(v_0 string) string {
	_subj := Main_parse(v_0)                 // rt.SkyResult[Sky_Core_Error_Error, int]
	if (_subj.Tag == 0) {
		v_1 := /* generic erase */ rt.AsInt(_subj.OkValue)                        // _subj.OkValue is already int
	if (_subj.Tag == 1) {
		v_2 := /* generic erase */ rt.Coerce[Sky_Core_Error_Error](_subj.ErrValue) // already Sky_Core_Error_Error
```

and the same for `Just` on an `rt.SkyMaybe[string]` (`rt.AsString(_subj.JustValue)`).
Each is a box of a typed value into `any` followed by an assertion back to the
type it already had. Phase 3 re-blessed `narrow` +32 for exactly this shape
(`03` 4→5, `05` 10→11, `13-skyshop` 1479→1509).

### Architecture consult (doc 14 §10, all four citations)

1. **Origin — R8** (`docs/rust-rewrite/14-runtime-narrowing-taxonomy.md` §3):
   "record / `Maybe` field pattern on an erased named field — closeable where
   the nominal is known". The site is `bind_field_pat` in
   `rust/crates/lower/src/lower.rs` (the `CoerceReason::GenericErase` Coerce at
   `lower.rs:8436-8444`, reached from `ctor_pattern` at `lower.rs:8147-8161`).
   The doc's line numbers for R8 (`:6674-6683`) predate later growth of the
   file; the site is the same one (the only `GenericErase` Coerce that reads a
   container payload `Selector`).
   The defect is narrower than R8's general case: `bind_field_pat` builds the
   raw selector with `GoTy::Any` *unconditionally* (`lower.rs:8425-8428`), so
   even when the payload's Go type is known it wraps an `Any → T` Coerce around
   a field whose static Go type is already `T`.
2. **Lever — §5.2** (slot-typed construction: take the statically known type as
   authoritative rather than narrowing back into it), realised through the
   identity row of §2.2 ("identity (`from == to`) — elided entirely"). The
   selector is typed with the field's real Go type; the Coerce becomes an
   identity and is not emitted.
3. **Floor check — §1, applied.** The value's Go shape: `_subj` is bound with
   `:=` to exactly the lowered subject's Go type (`lower.rs:7650-7660` picks
   `subj.ty` when it is not `any`, and coerces the subject up to `subj_ty`
   otherwise, `lower.rs:7666-7695`), so `_subj.ErrValue` has static Go type
   `ts[0]` of `rt.SkyResult[ts…]` at emit time. The slot's Go shape: the
   sub-pattern's binder type, which is that same `ts[0]` (`sub_ty = ty` when
   `ty != Any`, `lower.rs:8418-8423`). Both shapes are known at emit time and
   are equal → **closeable, not floor**. No R3/R4/wire/TEA/stdlib-ADT
   representation is touched, so no user authorisation is needed (§0.3 rule 5).
   When `ty == Any` (an element-erased container) the narrowing is genuine and
   stays.
4. **Verification — `xtask coerce-floor`.** A regression re-emits the Coerce
   and raises `narrow` on every row that pattern-matches a typed Result/Maybe;
   `coerce-floor` fails on any increase. Plus a new emitted-Go test
   (`rust/crates/sky/tests/typed_result_payload_pattern.rs`) that fails when
   `rt.AsInt(_subj.OkValue)`, `rt.Coerce[Sky_Core_Error_Error](_subj.ErrValue)`
   or `rt.AsString(_subj.JustValue)` is emitted, and runs the program.

### Change

In `bind_field_pat`: when the payload type `ty` is not `GoTy::Any`, the field
expression is the selector typed `ty`, with no Coerce. When `ty` is `Any` the
existing behaviour (narrow to the sub-pattern's nominal, or keep `any`) is
unchanged. Expected effect: `narrow` falls on every row with such a pattern;
the drop is blessed only with a per-row table (see the report).

### Invariants and tests (Part A)

| Invariant | Test |
|---|---|
| A typed payload field is read without a narrowing token | `typed_result_payload_pattern.rs` emitted-Go leg |
| The program still builds and computes the same values | same file, run leg (`ok 42 / err InvalidInput… / ada`) |
| No row's `narrow` rises; `adapter` stays 0 | `xtask coerce-floor` |
| Oracle-compared emission for the corpus is unchanged where the oracle keys | `xtask roundtrip` / `infer` |

---

## Part B — the Task trampoline

### Why

`Task.andThen` forces its source and then forces the continuation's task
*inside the same Go frame* (`AnyTaskAndThen` → `anyTaskInvoke` →
`anyTaskInvoke(SkyCall(fn, v))`, `runtime-go/rt/rt.go:6589-6597`), and every
typed boundary adds a closure frame (`TaskCoerceT`, `rt.go:6089-6097`). Plain
recursion

```elm
step n = work |> Task.andThen (\_ -> step (n + 1))
```

therefore grows the goroutine stack per step. Measured on `c651aafb`: a Sky
program recursing 2,000,000 steps dies with `fatal error: stack overflow`
(Go's 1 GB limit), and so does 1,000,000 in a Go benchmark. Phase 2 shipped
`Task.loop` / `Task.forever` as the stack-safe form; Phase 7 makes the
ordinary recursion safe too.

### Architecture consult

This is a runtime change to how Task values are represented and forced. It
is not a runtime-narrowing tactic, so doc 14's R-catalogue does not name it;
the doc-14 relevance is the reverse — the change must not ADD narrowing:
`TaskCoerceT` is emitted by `narrow_call` (`rust/crates/codegen/src/lib.rs:572`)
for every `rt.SkyTask[E, A]` slot and must not become a per-step cost. The
compiler emits no direct call of a Task value: it forces only through runtime
kernels (`rt.AnyTaskRun`, `lower/src/kernel.rs:268,273`, `lower.rs:8810-8820`)
and converts only through `rt.TaskCoerceT`; `rt.SkyTask` appears in
`lower`/`codegen` only as a type name (`goty.rs:804`, `codegen/src/lib.rs:572`).
So the representation is a runtime-internal decision and needs no compiler
change. It is not floor-touching under §0.3 rule 5 (no FFI, wire decoder, TEA
`sky_call` contract or stdlib-ADT representation changes; the TEA boundary
keeps forcing tasks through `sky_call(task, nil)`, which now recognises the
new representation).

### Design

1. **Representation.** `type SkyTask[E, A any] struct{ n *taskNode }`. E and A
   are phantom: every instantiation has the same underlying type, so a
   conversion between two instantiations is a Go conversion of a one-pointer
   struct — no allocation, no closure. The struct is pointer-shaped, so boxing
   it into `any` does not allocate either.
2. **Nodes.** A `taskNode` is immutable data: `kind`, `val`, `src`, `fn`.
   Kinds: `pure` (Ok val), `fail` (Err val), `leaf` (an opaque thunk or value
   in `val`, forced by `forceLeaf`), `bind` (andThen), `map`, `mapErr`,
   `catch` (onError), `bindResult` (andThenResult), `fromResult`, `lazy`,
   `seq` (sequence). Every Task combinator in `rt` builds a node:
   `AnyTaskSucceed/Fail`, `AnyTaskAndThen`, `Task_map`, `Task_mapError`,
   `Task_onError`, `Task_fromResult`, `Task_andThenResult`,
   `Result_andThenTask`, `Task_lazy`, `Task_sequence`, the typed companions
   (`Task_succeed/fail/andThen/mapT/sequenceT/run`, `Time_nowT`, `Random_*T`),
   and `Task_loop` / `Task_forever` (leaf nodes over a Go loop). Kernel thunks
   (`func() any` returning a Result) stay funcs; they are leaves.
3. **One interpreter.** `runTask(t any) SkyResult[any, any]` is the only place a
   Task is forced. It keeps an explicit continuation stack (a slice of 16-byte
   frames, first eight on the Go stack). `bind`/`map`/`mapErr`/`catch`/
   `bindResult`/`seq` push a frame and descend into `src`; a result unwinds
   frames. A `bind` continuation's returned task replaces the current task
   *after its frame is popped*, so tail recursion through `andThen` runs at a
   constant Go stack and a constant frame-stack depth. Left-nested chains keep
   their pending frames on the heap stack, never the Go stack.
   `anyTaskInvoke`, `AnyTaskRun`, `SkyTask.RunAny` and the typed
   `Task_run` all call `runTask`.
4. **`Task.sequence` and `Task.map` are node folds** (review correction 4):
   `seq` runs element *i* inside the same interpreter loop through a frame that
   holds its index and accumulator; `map` is a frame. Neither builds nested
   closures, and neither re-enters `runTask`.
5. **`TaskCoerceT[E, A]` is a free phantom conversion** (review correction 1).
   A `SkyTask` of any instantiation converts by copying its node pointer
   (`skyTaskNode()` interface method, no allocation). Only a non-SkyTask input
   (a kernel `func() any` thunk) is wrapped in a leaf node — one allocation at
   the boundary, never per step of a loop that already holds a SkyTask.
6. **One `isTaskLike` classifier at every force / reflect site** (review
   correction 2): `taskNodeOf(v) (*taskNode, bool)` recognises every SkyTask
   instantiation; `isTaskValue(v)` is true for a SkyTask or a zero-argument
   thunk. It is applied at: `AnyTaskRun`, `anyTaskInvoke` (and its reflect
   fallback, now `forceLeaf`), `SkyCall` (zero-arg force, and a Task applied to
   arguments), `skyCallOne`, `sky_call`, `Task_lazy`, `narrowReflectValue`,
   `Coerce[T]`, `narrowSkyContainer`, `coerceReflectArg` and `skyCallDirect`
   (argument into a typed `SkyTask` parameter), and the session-value
   validator (`walkValidateGob`: a Task in a model is rejected as before).
7. **Fallbacks panic, classified** (review correction 3). A site that cannot
   force what it was given panics with an `rt.Coerce: expected a Task …`
   message, which `classifyPanic` files as `CoerceFailure`; it never returns
   the unforced value. Concretely: a func that is not a zero-argument thunk
   reaching `forceLeaf`; a zero `SkyTask{}`; a Task applied to arguments in
   `SkyCall`/`sky_call`; a non-Task narrowed to a `SkyTask` type in
   `Coerce`/`narrowReflectValue`; a Task handed to `Task.lazy` as its thunk.
   A bare non-func value reaching `forceLeaf` keeps its documented meaning
   (`Ok value`, the kernel trust boundary `AnyTaskRun` states), and an
   already-resolved `SkyResult` of any instantiation is returned as that
   result (read with `anyResultView`, which also fixes the old `func() any`
   path that wrapped a non-`[any, any]` Result inside `Ok`).
8. **Fixture runtime copies** (review correction 5). No tracked file embeds a
   copy of `rt` (`git grep -l 'type SkyTask\['` lists `runtime-go/rt/rt.go`
   and the retired Haskell compiler only). The copies under `**/sky-out/rt/`,
   `**/sky-out-rust/rt/`, `.skyapp/` and `rust/target/**/embedded-assets` are
   gitignored build outputs (82 found). `./scripts/build.sh` re-stages the
   embedded tree and every `sky build` rewrites its `sky-out/rt`; the stale
   fixture outputs under `rust/crates/*/tests/fixtures/**/sky-out` are deleted
   so no test can read a pre-change runtime.

### Invariants and how each is tested

| # | Invariant | Test |
|---|---|---|
| I1 | 2,000,000 steps of `andThen` recursion finish at a 1 MB goroutine stack | `TestTaskAndThenRecursion_TwoMillionSteps` (child process; red before: `exit status 2`, stack overflow) |
| I2 | Same for recursion through `onError` | `TestTaskOnErrorRecursion_TwoMillionSteps` (red before) |
| I3 | A 2,000,000-deep left-nested `map`/`andThen` chain finishes at 1 MB stack | `TestTaskLeftNestedChain_TwoMillionSteps` (red before) |
| I4 | `Task.forever` and a recursive `andThen` service loop hold a constant live heap | `TestTaskForever_ConstantHeap`, `TestTaskRecursiveForever_ConstantHeap` (`SKY_TASK_SOAK_SECONDS=10` for the full soak) |
| I5 | The same holds for a real Sky program, end to end through the emitted `TaskCoerceT` shape | `rust/crates/sky/tests/task_trampoline_flow.rs` (build + run, 2,000,000 steps) and a case in `tests/conformance/tests/TaskLoopConformanceTest.sky` |
| I6 | `TaskCoerceT` between SkyTask instantiations allocates nothing | `TestTaskCoerceT_IsFree` (`testing.AllocsPerRun == 0`) |
| I7 | Every force/reflect site recognises a SkyTask, and fallbacks panic classified | `TestTaskLike_*` table tests over `SkyCall`, `sky_call`, `AnyTaskRun`, `anyTaskInvoke`, `Task_lazy`, `Coerce`, `narrowReflectValue`, `coerceReflectArg`, `walkValidateGob` |
| I8 | Semantics are unchanged: order of effects, short-circuit, error mapping, sequence order and first-error stop, lazy re-run per force | existing `task_*_test.go`, `TaskLoopConformanceTest`, the whole conformance suite, `xtask harness --only corpus` |
| I9 | The Sky.Spa client (`GOOS=js`) still builds; no new reflect on the client path | `GOOS=js GOARCH=wasm go build ./rt/` and `go vet` |
| I10 | No race | `go test -race ./rt/ -run Task` |

### Grill (CLAUDE.md §0.4)

**G1 — false negatives (a gap the tests would not catch).** The dangerous
class is a site that holds a Task as `any` and decides by `reflect.Func` or a
func type switch whether to force it: with a struct Task it would take the
"not a function, return it as a value" branch silently. Mitigation: (a) the
typed sites cannot be missed — changing `SkyTask` from a func to a struct
makes every `t()` on a typed task a compile error; (b) the untyped sites were
enumerated by grep (`reflect.Func` — 42 sites, `SkyCall(x)` with no
arguments, `sky_call(task, …)`, `.(func() any)`) and by an independent
read-only audit agent, and each Task-reachable one is listed in design item 6;
(c) the fallbacks at those sites now panic instead of returning the value, so
a missed site fails loudly in the conformance suite and the corpus rather
than producing a wrong answer. Residual risk: a Task reaching a site that is
not Task-reachable today (for example a Task stored in a Sky.Live model); the
session validator keeps rejecting it.

**G2 — false positives (over-eager rejection).** The classifier accepts every
shape the old `anyTaskInvoke` accepted (SkyTask, `func() any`, `func()
SkyResult[any, any]`, an unnamed typed `func() SkyResult[E, A]`, a resolved
Result, a bare value). The only newly rejected inputs are ones that used to be
silently wrong: a func with parameters forced as a Task (it used to become
`Ok <func>`), a zero SkyTask, and a Task applied to arguments. A bare value
keeps `Ok value`, because `AnyTaskRun` documents that contract for entry
points (`main = …` that is not a Task).

**G3 — cost.** Per `andThen` step the old path allocated a closure for the
bind, a closure for each `TaskCoerceT` wrap, and grew the stack; the new path
allocates one node per combinator and nothing for `TaskCoerceT`. Frames reuse
one slice per `runTask`. Measured before on `c651aafb` (`go test ./rt/ -run
'^$' -bench 'BenchmarkTask' -benchmem -count=3`, darwin/arm64 M1):
`AndThenRecursion1e5` ≈ 29–31 ms/op, 32.8 MB/op, 1,099,494 allocs/op;
`AndThenRecursion1e6` fatal stack overflow; `Loop1e6` ≈ 126–128 ms/op,
184 MB/op, 6,999,497 allocs/op; `Sequence1e5` ≈ 2.6 ms/op, 6.4 MB/op, 100,450
allocs/op. After-numbers are recorded in the CHANGELOG entry and the phase
report; a regression in any of them is a reason to stop, not to bless.
Time budget: one phase; the runtime change is contained to `rt`.

**G4 — layering.** The change is confined to `runtime-go/rt` (one new file,
`task_trampoline.go`, plus edits at the listed force sites). The compiler is
untouched for Part B; the emitted `rt.SkyTask[E, A]` / `rt.TaskCoerceT[E, A]`
spellings keep their meaning. Dependency direction is unchanged. Part A is a
`lower` change only.

**G5 — does it close the criterion?** The criterion (Phase 7 of the plan, and
the AGENTS.md rule it rewrites) is "ordinary recursive Task code no longer
grows the Go stack". I1–I5 test exactly that claim at the 2,000,000-step size
where it failed, at a 1 MB stack cap, in Go and through a compiled Sky
program. What it does not claim: recursion whose recursive call is *not*
behind a Task continuation (`step n = step (n - 1) |> Task.map f` evaluates
`step (n - 1)` eagerly while building the task) still recurses in Go at
construction time — that is ordinary strict recursion, the same as in Elm, and
the docs say so. Left-nested chains cost heap proportional to their pending
frames, which the semantics require.
