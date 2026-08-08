# v0.17 actual status audit — 2026-06-28

**Branch**: `feat/v0.17-pure-sound-codegen`
**Commit**: `7d09b428` (Fix v0.17 compiler soundness regression in RtFieldAdtBug342 by threading caller ctx to snapshotCallerCtx)
**Binary**: `sky-out/sky` reports `sky dev`
**Reframed goal (per session brief)**: "rock solid + ~100% sound, with documented surface for remaining rt.Coerce" — NOT literal-zero rt.Coerce.

---

## 1. rt.Coerce floor measurements (cold-build)

| Example | go build | `rt.Coerce` | Patterned (Maybe/Result/CoerceX/AsListT/AsMapT) |
|---|---|---|---|
| `examples/00-standard-libs` | **FAIL** (Go compile error) | 124 | 166 |
| `examples/26-ui-showcase` | PASS (`Build complete`) | **171** | 272 |
| `examples/13-skyshop` | **FAIL** (Go compile error) | 521 | 399 |

### 00-standard-libs failure (verbatim)
```
./main.go:1921: in call to Sky_Core_Maybe_map_, type rt.SkyMaybe[int] of rt.MaybeCoerce[int](rt.Just[any](2)) does not match inferred type rt.SkyMaybe[any] for rt.SkyMaybe[T1]
./main.go:1921: cannot use rt.MaybeCoerce[int](rt.Just[any](x + 5)) (value of struct type rt.SkyMaybe[int]) as rt.SkyMaybe[rt.SkyValue] value in return statement
./main.go:1979:181: cannot use rt.ResultCoerce[Sky_Core_Error_Error, Std_Decimal_Decimal](Std_Decimal_fromString("3.14")) … as rt.SkyResult[Sky_Core_Error_Error, rt.SkyValue] value in argument to Sky_Test_ok
./main.go:1985:150: in call to Sky_Core_Result_withDefault, type rt.SkyResult[Sky_Core_Error_Error, Std_Decimal_Decimal] of rt.ResultCoerce[Sky_Core_Error_Error, Std_Decimal_Decimal](Std_Decimal_fromString(s)) does not match inferred type rt.SkyResult[Sky_Core_Error_Error, any] for rt.SkyResult[Sky_Core_Error_Error, T1]
```
This is the SkyMaybe[T1]/SkyResult[E, T1] over-erasure regression: typed `MaybeCoerce[int]` wraps where the call site expects `rt.SkyValue` / `any` generic placeholder. Same root cause across the 4 lines.

### 13-skyshop failure (verbatim)
```
./main.go:541:116: undefined: rt.Go_Firestore_queryDocuments
```
Single missing FFI symbol. Not a soundness issue per se — looks like a stale FFI binding or missing generator pass.

### 26-ui-showcase status
171 rt.Coerce — matches "PARTIAL 317→209 (-34%)" trajectory in `iter87-closure.md`; this branch is below that floor (171 < 209), suggesting subsequent improvements landed since iter 87.

---

## 2. Stdlib gap status (G1-G5)

| Gap | Status | Evidence |
|---|---|---|
| **G1** Task.parallel early-cancel | **SHIPPED** | `runtime-go/rt/rt.go:5613-5660` uses `context.WithCancel` + select-based non-blocking send; first Err calls `cancel()` and returns. Docstring at `sky-stdlib/Sky/Core/Task.sky:124-128` documents short-circuit semantics. Sibling-goroutine results discarded via ctx.Done in sender select. |
| **G2** Sky.Tui tuiWarn markers | **SHIPPED** | `runtime-go/rt/tui_ui.go` emits tuiWarn for: pseudo-class (line 2164), media-query (2172), transition (2183), animation (2194), aspect-ratio (2086), explicit grid tracks (2080), raw CSS attrs (2090), AttrClass (2104), font size/family/letter-spacing/word-spacing (2210-2249). All deduped per the `tuiWarn` helper. |
| **G3** `Math.isNaN` export | **SHIPPED** | `sky-stdlib/Sky/Core/Math.sky:18` exposes `isNaN`; defined at line 211: `isNaN : Float -> Bool = Ffi.kernel "Math_isNaN"`. |
| **G4** `Db.migrate` tenant docs | **SHIPPED** | `sky-stdlib/Std/Db.sky:278-296` carries a "Tenant-gate bypass (by design)" header above `migrate`, explicitly warning never to call from per-tenant runtime path and listing every gate-enforced sibling (`Db.exec` / `query` / `queryDecode` / `findOneByField` / `findManyByField` / `findByConditions` / `unsafeFindWhere`). |
| **G5** Maybe/Result/Task laws | **SHIPPED** | All three spec files exist: `test/Sky/Stdlib/MaybeLawsSpec.hs`, `ResultLawsSpec.hs`, `TaskLawsSpec.hs`. |

All 5 stdlib gaps from `sky-stdlib-correctness.md` are shipped.

---

## 3. Roadmap docs review

### `docs/v0.17-roadmap/iter87-closure.md`

Banks closure state at iter 87 (base SHA `9e170314`). Audit summary table:

| # | Criterion | iter-87 status |
|---|---|---|
| 1 | `rt.Coerce` → 0 in 26-ui-showcase | **PARTIAL** (317 → 209, -34%) |
| 2 | `eraseUndeclaredTVarsInGoSource` DELETED | **CLOSED** (commit `04d6f707`) |
| 3 | `globalCgEnv` + `globalGoSigMap` DELETED | **PARTIAL** (both IORefs deleted, `getCgEnvFromScope` CAF + `scopeStateRef` survive) |
| 4 | `SKY_GOSIG_DIFF=1` zero `Anon_R_*` | **CLOSED** (`cde54107`) |
| 5 | 9 GoTypeAdt + GoTypeRoundTrip parity | **CLOSED** (72/72 passing) |
| 6 | Active limitations 4-10 closed | **CLOSED-IN-FACT** |
| 7 | Cycle 6 #383 credibility close | **PARTIAL** (ratchets on #1 hitting floor) |
| 8 | Property fuzzer ≥10k iters clean | **CLOSED** (`b6c9be6e`) |
| 9 | All umbrella tasks closed | **PARTIAL** (#383 #595 #644 #660 #664 #672 #677 in_progress) |
| 10 | Judge verdict 100% | **NOT YET RUN** |

### `docs/v0.17-roadmap/literal-zero-close-plan.md`

Authored 2026-06-23 as the **maximally-ambitious path**, not the reframe. Single reframe mention at line 26: "the user has committed to the MAXIMALLY-AMBITIOUS path: literal zero `rt.Coerce` … not the PRAGMATIC-V0.17-CLOSE reframe."

**Note**: This doc DOES NOT match the reframed goal in the session brief ("rock solid + ~100% sound, with documented surface for remaining rt.Coerce"). The plan doc lists "Zero `rt.Coerce` calls" as criterion #1 of done. If we are now shipping under the pragmatic reframe, this doc is stale and the v0.17 release notes must explicitly document the reframe.

Phase 1 work (G1-G5) listed in this doc is **all shipped** (verified §2 above).

### `docs/v0.17-roadmap/criterion-3-caf-deletion.md`

States: criterion #3 IORef-half **CLOSED** (`globalCgEnv`, `globalGoSigMap`, `getCgEnv` CAF all gone). CAF-half **PARTIAL** — `getCgEnvFromScope` CAF survives at `Compile.hs:860` with 57 reader sites (17 Class A / 25 Class B / 15 Class C). `scopeStateRef` IORef survives at `Compile.hs:508-510`.

Honest verdict from the doc: "criterion #3 is partially achieved — the IORef half is closed, the CAF half is still open via the renamed sibling."

---

## 4. Example sweep dry-run (12 examples)

| Example | Result |
|---|---|
| 01-hello-world | PASS |
| 02-go-stdlib | PASS |
| 05-mux-server | **FAIL** (go build error) |
| 07-todo-cli | PASS (Build complete) |
| 09-live-counter | PASS |
| 12-skyvote | **FAIL** — `./main.go:3989: rt.SkyResult[…, []map[string]string] does not match … rt.SkyResult[…, []any]` |
| 13-skyshop | **FAIL** — `undefined: rt.Go_Firestore_queryDocuments` |
| 14-task-demo | PASS |
| 18-job-queue | **FAIL** (Sky lowering OK, go build rejected) |
| 19-skyforum | **FAIL** — `./main.go:2879: rt.SkyMaybe[string] does not match rt.SkyMaybe[any] in Sky_Core_Maybe_withDefault on Sky_Core_List_head(rt.AsListAny(slash))` |
| 26-ui-showcase | PASS |
| 30-sse-server-demo | **FAIL** (go build error) |
| 00-standard-libs | **FAIL** (Maybe.map / Result over-erasure) |

**Score: 6 / 13 clean. 7 fail.** (Including 00-standard-libs from §1.)

The failing class shares a root cause across 00 / 12 / 19: typed `MaybeCoerce[T]` / `ResultCoerce[E,T]` returns the typed shape `rt.SkyMaybe[T]` / `rt.SkyResult[E,T]` but the consuming kernel expects `rt.SkyMaybe[any]` / `rt.SkyResult[E, []any]`. This is the "MaybeCoerce/ResultCoerce typed-emit edge case" — the wrap call site uses the typed payload but the receiver still has `T1`/`any` in its sig.

05-mux-server / 18-job-queue / 30-sse-server-demo failure detail wasn't captured in the tail; they fall under the same "Sky success / Go build rejected" pipeline-integrity class.

---

## 5. IORef status in `src/Sky/Build/Compile.hs`

- `unsafePerformIO` calls: **63**
- `newIORef` calls: **1**
- IORef declarations: **1** — `scopeStateRef :: IORef LC.LowerCtx` at line 519

**Surviving IORef**: `scopeStateRef` (the underlying CodegenEnv channel for `getCgEnvFromScope` CAF reads). This is the criterion-#3 CAF-half residue documented in `criterion-3-caf-deletion.md`.

The 63 `unsafePerformIO` calls are mostly `readIORefNoCse scopeStateRef` reads + sentinel CAFs documented as deleted-in-spirit (comments at lines 92-322 record the v0.17 IORef defusing batch — `globalCgEnv`, `globalGoSigMap`, `globalConsoleNeeded`, `globalIsInlineConsoleBuild`, `globalEntryPath`, `globalSourceFile`, `globalReachableSet`, `globalDceDisabled`, `globalAllAliases`, `globalAllFieldIdx`, `getCgEnv` CAF all deleted across iters).

---

## 6. Top-level recommendations

### Confirmed-shipped (proof in §2, §3, §5)

- G1 Task.parallel early-cancel (context.WithCancel + ctx.Done sender select)
- G2 Sky.Tui tuiWarn for all major unsupported markers
- G3 `Math.isNaN` exported
- G4 `Db.migrate` tenant-gate-bypass docstring
- G5 Maybe/Result/Task law specs
- Criterion #2: `eraseUndeclaredTVarsInGoSource` deleted
- Criterion #4: `SKY_GOSIG_DIFF=1` zero Anon_R_* errors
- Criterion #5: GoTypeAdt + GoTypeRoundTrip parity 72/72
- Criterion #6: CLAUDE.md Limitations #4-10 all closed
- Criterion #8: Property fuzzer ≥10k iters clean
- IORef cleanup: all named criterion-#3 IORefs (`globalCgEnv`, `globalGoSigMap`) deleted; only `scopeStateRef` survives as the documented bridge

### Verified clean

- Examples: 01-hello-world, 02-go-stdlib, 07-todo-cli, 09-live-counter, 14-task-demo, 26-ui-showcase (6/13)
- 26-ui-showcase rt.Coerce floor: 171 (below iter 87's documented 209 floor)

### Real remaining gaps

- **MaybeCoerce/ResultCoerce typed-emit over-erasure**: Sky lowering wraps with typed payload `[int]` / `[Decimal]`, but receiver kernel signature still has `any`/`T1`. Breaks 00-standard-libs (Maybe.map), 12-skyvote (Result/[]map[string]string), 19-skyforum (Maybe/Sky_Core_List_head). Root cause appears to be MaybeCoerce/ResultCoerce inserted at the call site when the kernel sig hasn't been rewritten for the typed payload, OR conversely the kernel got monomorphised but the wrap stayed `[any]`. Need to either: drop MaybeCoerce/ResultCoerce wraps where the kernel sig is generic, OR monomorphise the kernel sig at the call site.
- **13-skyshop missing FFI symbol**: `undefined: rt.Go_Firestore_queryDocuments` — investigate whether the Firestore FFI binding regenerated, or whether a kernel was renamed without updating the consumer.
- **05-mux-server / 18-job-queue / 30-sse-server-demo go-build failures**: tail was truncated; need re-check with full stderr to classify.
- **Criterion #3 CAF half**: `getCgEnvFromScope` CAF + `scopeStateRef` IORef survive. Closing the CAF half requires threading `LowerCtx` through 57 reader sites (17 Class A trivial + 25 Class B medium + 15 Class C high-effort) per `criterion-3-caf-deletion.md`.
- **Criterion #1 literal-zero rt.Coerce**: 171 in 26-ui-showcase, 521 in 13-skyshop. Pragmatic reframe accepts this; literal-zero plan does not.

### Known v0.17 limitations to document in release notes (under pragmatic reframe)

1. **Residual `rt.Coerce` in emitted Go**. Floor: ~171 calls in 26-ui-showcase. Concentrated in user-ADT typed-payload narrowings + collection-element coercion. Sealed-interface ADT emission (#677) is the architectural close path; deferred beyond v0.17.
2. **`scopeStateRef` IORef + `getCgEnvFromScope` CAF survive in `Compile.hs`**. Documented as a Reader-monad-style bridge with monotonic single-writer/57-reader contract; full thread-through requires LowerCtx parameter additions across 57 sites and is deferred.
3. **`rt.MaybeCoerce[T]` / `rt.ResultCoerce[E,T]` over-erasure edge case** — when a typed-wrap target's consuming kernel still uses generic `T1`/`any`, Go build rejects the call. Workaround in user code: avoid relying on inferred polymorphic Maybe/Result through stdlib HOFs when the inner type is concrete; ascribe locally. Tracked for v0.17.x patch.
4. **Sky.Live wire format** — not migrated to per-Msg typed dispatch (kept as `reflect.MakeFunc`-backed `func(any) any` per architectural floor §8). User-visible: `msgDisplayName` may surface "makeFuncStub" for some Msg shapes.
5. **FFI codegen**: Go FFI return remains within rt-floor (`any` boundary by design for opaque Go values); FFI shim per-symbol typing is a v0.18 path.

### Action items for v0.17 release

1. **CRITICAL**: Fix the MaybeCoerce/ResultCoerce over-erasure regression. 4 examples (00 / 12 / 19 + 00 standalone) fail go build with the same root cause. This is a release blocker even under pragmatic reframe — 00-standard-libs is the canonical stdlib smoke test.
2. **CRITICAL**: Investigate 13-skyshop `rt.Go_Firestore_queryDocuments` missing symbol — likely a regression from a recent FFI generator pass.
3. Capture full stderr for 05 / 18 / 30 to classify and triage.
4. Decide: ship under pragmatic reframe with documented limitations 1-5, OR continue to literal-zero close. The `literal-zero-close-plan.md` still states criterion #1 is "zero rt.Coerce" — this doc must be amended or marked superseded if shipping under the reframe.
5. Refresh `iter87-closure.md` audit (this branch is past iter 87 base SHA).
