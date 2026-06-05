# Cycle 3 — Auditor findings (regen)

Branch audited: main @ e4656f1 (v0.15.15 released; v0.15.14 + v0.15.15 release-log entries committed)
Date: 2026-05-26
Auditor pass: 3
Regenerated after the prior cycle-3 output file was lost in conflict resolution.

## Summary
3 critical · 5 high · 4 medium · 2 low — 14 NEW or RESTATED gaps

Source-of-truth verifications against current `main`:

- v0.15.14 (PR #85, commit 3de5810) shipped: `diffNodes` now diffs `VNode.Events` AND `runPerformBody` suppresses byte-identical view (closes Gap C1 at patch layer; transport overhaul remains C11).
- v0.15.15 (PR #84, commit 38c50e4) shipped: LowerCtx cascade Phase 2 — `lowerExpr`/`lowerExprExpectGo` wrappers now ctx-install; lambda body + call arg slots migrated. Record-field init, list element, let-body deliberately reverted (blackhole on real fixtures).
- Cycle-1 backlog P14-P28 + cycle-2 B1-B5 → all still OPEN.
- Cycle-1 P1-P5 (A1-A6) → all closed and locked.

---

## P6 review verdict (LowerCtx cascade Phase 2 / v0.15.15)

**Verdict:** APPROVED-WITH-CAVEAT. The 2-of-4 partial migration matches the safe ceiling the deferred-thunk class permits. The deferred slots (record-field init, list element, let-body) require structural surgery (P7) before they can route through `lowerExpr*` ctx-installers.

Key observations:

1. **Migrated slots compile byte-identical to main.** 13-skyshop main.go md5 unchanged (39b18368…); 12-skyvote / 19-skyforum / 01-hello-world clean build. Zero behaviour change confirmed across the example sweep.

2. **`lowerExpr` / `lowerExprExpectGo` use `seq`-then-restore correctly.** Mirrors `withScopedLambdaTypes` — the rendered string is forced before `writeIORef scopeStateRef prev` runs, so deferred thunks observe the installed ctx. Confirmed by reading Compile.hs:6465-6485.

3. **`ctxFromIORef ()` is NOINLINE.** Each call snapshots `scopeStateRef`; matches the IORef-implicit semantics of `lookupLambdaType` et al. Compile.hs:6498-6500.

4. **Reverts are documented in-line** (Compile.hs:6576-6583 for `Can.List`; 6730-6739 for `lowerRecordLiteralTo`; 10054-10064 for `letToGo`). Each comment names the reproducer (examples/16-skychess; CoerceArgParametricSpec; skyshop) and the failure mode (deferred lazy thunk × ctx-aware write/force seam → GHC blackhole).

5. **`LowerCtxCascadeSpec` (4/4) + `IORefBoundarySpec` (positive surface)** lock the integration shape: `LC.lookupRegionType`, `LC.withLambdaTypes`, `letBindingType :: LC.LowerCtx` all remain wired in.

**Caveat (Gap C5, restated below):** the `letToGo` shared-seam pattern documented at Compile.hs:9996 (`let ctx = unsafePerformIO (readIORef scopeStateRef)`) remains the load-bearing seam. P7's framing must be "make `letBindingType`'s region lookup pure end-to-end" (i.e. SolvedTypes must carry the region map) — NOT "delete `scopeStateRef`" mechanically. The IORef is a SYMPTOM; the disease is `letBindingType ctx solved name body` thunks deferred inside `bodyGo = lowerBody body` while the ctx-aware wrapper writes `scopeStateRef` for the body's duration. Closing the symptom without breaking the chain re-opens the blackhole class.

---

## Cycle-1 + Cycle-2 closure status (current HEAD)

| ID | Subject | Status | Notes |
|---|---|---|---|
| A1 | coerceArg parametric-alias short-circuit | CLOSED | v0.15.7 (P1) |
| A2 | goExprGoType poly-call type loss | CLOSED | v0.15.9 (P2-followup) |
| A3 | SSE channel race (sseCh write/read no lock) | OPEN | P14 not yet shipped |
| A4 | isPlainIdent recursion depth | CLOSED | v0.15.8 (P3) |
| A5 | inferExprType BinOp arms | CLOSED | v0.15.10 (P4) |
| A6 | Auth typed boundary | CLOSED | v0.15.12 (P5) |
| A7 | SSE newline encoding chokepoint | OPEN | P15 not yet shipped |
| A8 | Curry adapter bracket parsing | OPEN | P13 not yet shipped |
| A9 | CSRF timing side-channel | OPEN | P16 not yet shipped; csrf_middleware.go:156 still short-circuits on missing cookie |
| A10 | Cross-module annot order | OPEN | P25 not yet shipped |
| A11 | Memory store concurrent reads | INVALIDATED | sync-protected as designed |
| A12 | parametricAliasBase malformed strings | OPEN | P10/P11 (structural GoType ADT) not yet shipped |
| A13 | View-panic recovery prevBody | OPEN | P14 fold-in pending; verified at live.go:2540-2549 (sets body="" but prevTree may be partial) |
| B1 | Module-name-to-prefix alignment | OPEN | aliasBaseFromCanExpr at Compile.hs:9322-9326 still uses ad-hoc `map (\c -> ...)` |
| B2 | Alias-identity membership by (mod, name) | OPEN | `_cg_recordAliases :: Set String` unchanged |
| B3 | __skyReviveScripts attribute whitelist | OPEN | live.go:3548-3572 still copies ALL attributes including event handlers |
| B4 | Form-submit submitter race | OPEN | live.go:4146-4148 still uses activeElement fallback |
| B5 | Form-data concurrent-patch desync | OPEN | live.go:4152-4164 captures synchronously without snapshot of disabled state |
| Prior #1/#6/#9 | Lambda IORef race | IN_PROGRESS | v0.15.15 migrated 2/4 slots; 3 remain (record/list/let-body) |
| Prior #4 | eraseTypeParams loses info | OPEN | P12 |
| Prior #7 | coerceArg branches | IN_PROGRESS | P1 added structural-fallback arm; P10/P11 await |
| Prior #8 + #14 | letBindingType whitelist | IN_PROGRESS | LowerCtx threading partial; whitelist remains at Compile.hs:10259-10266 |
| Prior #13 | rt.Coerce Kind fallback | OPEN | P23 lock |

---

## NEW gaps from this cycle

## Gap C1 (severity: critical — superseded but RESIDUAL)
**File:** `runtime-go/rt/live.go:2697-2723` (`runPerformBody`)

**Symptom (original):** byte-equality suppression at the SSE producer was previously absent, so every `Cmd.perform` completion that produced an identical view still pushed a full-body SSE frame. Combined with the `diffNodes` event-attr gap, this produced two symptoms: (i) idle flicker as Time.every-driven completions shipped redundant HTML; (ii) the silent-keypress regression on sky-diagram canvas-wrap.

**Status:** SUPERSEDED IN PART by v0.15.14 patch (commit 3de5810). The patch is correct as far as it goes:

```go
sess.mu.Lock()
prevBody := sess.prevBody       // captured BEFORE dispatch
body := app.dispatch(sess, msg) // dispatch writes sess.prevBody = body
var frame string
if body != "" && body != prevBody {
    frame = encodeSSEFrame(sess, body)
}
sess.mu.Unlock()
```

The same shape lands at the Time.every callsite (live.go:2767-2772). The Events-diff fix at `diffNodes` (live.go:678-739) closes the canvas-wrap regression by ensuring stale event handler attrs cannot survive a tree change.

**Residual (still OPEN, severity: high):**

1. **`dispatchBatched` does NOT apply prevBody suppression** (live.go:2480-2486). A beacon-driven batched dispatch (tab-unload path) that produces a byte-identical view still pushes a redundant frame to `sess.sseCh`. Low traffic in practice but the asymmetry across the three dispatch paths is fragile.

2. **No regression test pins `runPerformBody`'s suppression behaviour.** `grep -rn "TestRunPerform\|TestPerformIdentical" runtime-go/rt/` returns zero matches. The v0.15.14 patch shipped without a `_test.go` mate. A future refactor that re-introduces the unconditional push (or removes the `body != prevBody` check) will not trip cabal/Go test; only the original Playwright probe on sky-diagram would catch it.

3. **`sess.prevBody` is updated unconditionally even on dispatch panic.** dispatch's recover at live.go:2540-2549 sets `body = ""`. Line 2618 then writes `sess.prevBody = body` (empty string). Next dispatch's suppression check `body != prevBody` compares against `""`, so suppression never fires on the FIRST post-panic dispatch — harmless but obscures the intent.

**Suggested fix:**
- Mirror suppression into `dispatchBatched` (3-line patch).
- Add `runtime-go/rt/live_runperform_suppress_test.go` with three cases: (a) identical-view dispatch produces no SSE frame; (b) view-change dispatch DOES produce a frame; (c) post-panic dispatch's first-update produces a frame even if it equals the post-panic-zeroed prevBody.

**Why current tests miss it:** `TestDispatch_returnsBodyForIdenticalView` (live_dispatch_noop_test.go:38) locks dispatch's return contract (always returns body), not the SSE producer's suppression contract.

---

## Gap C2 (severity: high)
**File:** `runtime-go/rt/live.go:2618` (dispatch unconditionally writes `sess.prevBody = body`)

**Symptom:** `dispatch` writes `sess.prevBody = body` AFTER computing the body, regardless of caller. This means the suppression mechanism is FUSED to dispatch's contract: any caller that reads prevBody BEFORE dispatch and compares AFTER dispatch sees an apparent equality because dispatch already updated it.

The current suppression code captures `prevBody := sess.prevBody` BEFORE dispatch, then compares `body != prevBody` — which works ONLY because dispatch unconditionally re-writes it post-render. If a future change made dispatch skip the write (e.g. "only update prevBody when shipping a frame"), every suppression check would read the SAME value before and after, breaking suppression.

**Reproducer (hypothetical refactor that would silently break this):**

```go
// "Cleanup": only update prevBody when we ship a frame
if body != "" {
    body = renderVNode(vn, sess.handlers)
    sess.prevTree = &vn
    // sess.prevBody = body  // commented out — only update on actual ship
}
```

Then `runPerformBody`'s `body != prevBody` check would compare `body` against the LAST-SHIPPED prevBody — but body would be NEWLY rendered. The comparison still works for the byte-identical case, but the semantics drift: prevBody no longer tracks "last computed body" — it tracks "last shipped body". This is the more-correct semantics, but the current code path doesn't enforce which interpretation is canonical.

**Root cause:** the prevBody field is overloaded — both "last computed body" (dispatch's invariant) AND "last shipped body" (suppression's invariant). They coincide today but the dual meaning is undocumented.

**Suggested fix:** rename `prevBody` → `lastComputedBody` and add `lastShippedBody`; suppression compares against `lastShippedBody` and is updated only when a frame is enqueued to `sseCh`. This makes the contract explicit and resilient to future cleanup.

---

## Gap C3 (severity: critical, strategic)
**File:** `runtime-go/rt/live.go` (no broadcast primitive); `runtime-go/rt/live_store.go` (Store interface)

**Symptom:** Sky.Live has no multi-session coordination primitive. Today every session runs an isolated TEA loop with its own `sess.sseCh`. There is no `Subscribe(topic) <-chan T` or `Broadcast(topic, payload)` API. Multi-session apps (chat, collaborative editor, presence-aware UIs — Phase 3g per issue #259) cannot be expressed in Sky source without writing Go FFI.

**Prerequisites for a `broadcast` primitive (audit-restated from prior cycle 3 run):**

1. **Store-level `Subscribe(filter) (<-chan SessionEvent, cancel func())` API.** Each store backend (memory / sqlite / redis / postgres / firestore) needs a fanout primitive. Redis pub/sub is native; the others need polling or notification triggers. The interface MUST be defined at the `liveStore` level (live_store.go:250-260) so the backend abstraction holds.

2. **Global + per-session seq split.** Today `sess.outSeq` is per-session (live.go:1289-1292). A broadcast-induced patch lands on N sessions; the client's `__skyLastAppliedSeq` is per-session, so cross-session ordering is implicit. But broadcast events themselves need a global monotonic counter so observers can detect drops. New field: `app.globalSeq atomic.Int64` (renamed `sess.outSeq → sess.localSeq` for clarity).

3. **Handler rebuild on broadcast-induced patch.** When session A's broadcast triggers session B's view to change, session B's `runCmd` did not run; its `sess.handlers` map is stale relative to the new render. The renderVNode-populates-handlers cycle must run on EVERY view-changing event, not just user-msg-driven dispatches. (Today this works because dispatch always re-renders; broadcast-as-Msg would route through dispatch too. Open question: should broadcast bypass dispatch for read-only state diffusion?)

4. **prevTree divergence detection.** Two sessions can race on shared model state (e.g. shared chat history). Session A applies broadcast event 100; session B applies user event 99 then broadcast 100. If event 99 modifies a field broadcast 100 reads, A and B diverge. Either (a) all broadcast events are total replacement (overwrite local state), or (b) broadcast events are message-shape (each session's `update` applies them deterministically). The Elm-style answer is (b) — Msg-shape — but Sky.Live's wire protocol has no precedent for "broadcast Msg".

5. **Memory bound on subscription registries.** A naive `map[topic][]chan` grows unbounded under churn (sessions opening/closing). Need ref-counted topics + cleanup on session.Delete.

**Why this is critical-strategic:** every implementation Sky.Live ever lands MUST satisfy all 5 prereqs OR introduce a runtime panic class. Adding broadcast without (3) means user clicks are silently dropped on broadcast-recipient sessions. Adding broadcast without (4) means apps silently desync without any error signal. The architectural decision needs to land BEFORE the API surface; today neither exists.

**Suggested fix:** issue #259 (Phase 3g) needs a design doc PRIOR to any code. The doc establishes:
- Store interface for broadcast (with backend-by-backend feasibility analysis).
- Msg-shape broadcast (option b above).
- Seq numbering rule (global counter alongside session counter).
- Memory bound (ref-counted topic registry).
- Test plan (multi-session Playwright in `scripts/verify-all-web.sh` exercising N parallel sessions).

---

## Gap C4 (severity: high)
**File:** `runtime-go/rt/live_store.go:300-330` (memoryStore Delete + cleanupLoop); `runtime-go/rt/live.go:1271` (sseCh field)

**Symptom:** TTL-deletion never closes `sess.sseCh`. The session map is the only owner of the session struct; `delete(s.sessions, id)` (line 303 / line 324) makes it GC-eligible BUT goroutines holding `sess.sseCh` references via Time.every subscriptions or in-flight `runPerformBody` continue to push to the channel. The channel is buffered (16); pushes succeed until the buffer fills, then the `select { case sess.sseCh <- frame: default: }` (live.go:2719/2781) silently drops. No goroutine ever exits — `cancelSub` (live.go:1273) is only closed on next-`setupSubscriptions`, not on session deletion.

**Reproducer:**
1. Open a Sky.Live session with `subscriptions = Sub.every 100ms Tick`.
2. Disconnect the browser; let TTL expire (`SKY_LIVE_TTL=30s` for fast repro).
3. `cleanupLoop` deletes the session entry at the 60-second tick.
4. The Time.every goroutine is still alive (its `cancelSub` channel was not closed). It continues to dispatch ticks. Each tick:
   - Acquires `sess.mu` (still valid pointer; session struct not yet GC'd).
   - Runs `app.dispatch(sess, msg)` (updates model — harmless, no observer).
   - Tries to push to `sess.sseCh` — drops via `default:`.
5. Goroutine leaks for the lifetime of the process. Multiplied by every expired session over the process's uptime.

**Root-cause hypothesis:** the Store API treats sessions as data, not as resources with a lifecycle. The implicit contract is "Delete removes the entry; goroutines holding the reference are someone else's problem". But the SSE goroutine + Time.every goroutine have NO other reference holder — they ARE the session's lifecycle.

**Suggested fix:**
1. `Delete(sid)` must `close(sess.cancelSub)` before removing from the map. This signals every subscription goroutine to exit.
2. `Delete(sid)` should also `close(sess.sseCh)` — but only AFTER all goroutines that write to it have exited. Use a `sess.done atomic.Bool` flag; writers check it before pushing (`if sess.done.Load() { return }`); after closing cancelSub, wait briefly for goroutines to flush, then close sseCh.
3. Add `runtime-go/rt/live_store_delete_test.go` that sets a 50-ms TTL, opens a session with Time.every, sleeps past TTL + cleanup interval, then checks `runtime.NumGoroutine()` returned to baseline.

**Why current tests miss it:** the test suite uses long TTLs (default 30 min) and short test runtimes. The leak only surfaces under production runtime + churn. The cabal/Go-test runner finishes before any leak is observable.

---

## Gap C5 (severity: critical — RESTATED from cycle 3 prior pass)
**File:** `src/Sky/Build/Compile.hs:9980-10090` (`letToGo`); 9996, 10357, 10163 (`unsafePerformIO (readIORef scopeStateRef)` sites)

**Symptom:** The `scopeStateRef` IORef is NOT just a "race-prone scope state holder" — it is a SHARED SEAM around which `letToGo`'s body lowering and `letBindingType`'s region lookup form a deferred-thunk cycle. The cycle:

```
letToGo def body =
    let ctx     = unsafePerformIO (readIORef scopeStateRef)
        solved  = ...
        defStmts = case def of
            ... | Just dt <- letBindingType ctx solved dn valExpr ->
                    letBindStmts dn (exprToGoExpect dt valExpr)
            _ -> defToStmts def         -- defToStmts ALSO snapshots scopeStateRef
        bodyGo = lowerBody body          -- body lowering may install fresh ctx
        raw = GoIr.GoBlock defStmts bodyGo
    in ...
```

The hazard: a naive P7 ("delete `scopeStateRef`, pass ctx purely") would still keep `letToGo`'s body lowering as a SUSPENDED THUNK on `bodyGo`. The thunk forces during GoBuilder's `renderExpr` traversal of the GoBlock. Meanwhile, `letBindingType ctx solved dn valExpr` is ALSO a suspended thunk inside `defStmts`. GHC evaluates these thunks in dependency order. If both depend on the SAME ctx-shaped value but observe it at DIFFERENT evaluation points (because one is inside `defStmts`, the other inside `bodyGo`), and the value is constructed via a chain of let-bindings on the call stack of the OUTER expression, GHC blackholes.

Verified: the comment block at Compile.hs:10054-10064 explicitly names "GHC blackholes (verified under skyshop, May 2026)" — the developer agent encountered this during P6's POC migration.

**Reframed P7 plan (replaces "delete scopeStateRef" with):**

1. **Move the region-types map OUT of `scopeStateRef`.** `Solve.SolvedTypes` already carries `Map Var T.Type`; extend it to also carry `Map A.Region T.Type` (the per-region map). This is a Solver-side change, not a lowering-side one.

2. **`letBindingType` becomes `Solve.SolvedTypes -> String -> Can.Expr -> Maybe T.Type`** — drop the `LC.LowerCtx` parameter; the function never needed lambda-types, only region-types and solved-types. SolvedTypes now carries both.

3. **Once `letBindingType` is pure** (no IORef reads), the `defStmts` thunk no longer participates in the ctx-aware-wrapper write/force seam. `letToGo`'s body can then route through `lowerExprExpectGo` ctx-installer safely.

4. **Delete `scopeStateRef`'s `_lc_regionTypes` field** (and the `lookupRegionType` IORef-backed helper that reads it). Anything else using `scopeStateRef` (lambda-types, lambda-Go-strings) can stay until its own deferred-thunk seam is closed independently.

**This is a 2-3-PR sequence, not a 1-PR deletion.** The P7 plan in CYCLE-01-planner.md says "3-4 hours, branch `feat/v0.15.x-hardening-P7-delete-scope-state-ref`". That estimate is WRONG by ~3× — closing C5 requires Solver-side surgery to extend SolvedTypes with the region map, plus call-site migration. Estimate: 8-12 hours, 2 PRs (P7a: extend SolvedTypes with regions; P7b: migrate letBindingType + delete `_lc_regionTypes`).

---

## Gap C6 (severity: high)
**File:** `runtime-go/rt/live.go:2618` + `2683` (dispatch + runPerformBody share `sess.mu` but the I/O side of SSE write is locked too)

**Symptom:** `runPerformBody` holds `sess.mu` while calling `encodeSSEFrame(sess, body)` (live.go:2712). encodeSSEFrame is a pure function over `sess`, but it does JSON marshalling of the (potentially large) body. The lock is held during this CPU-bound work, blocking other dispatchers and the SSE writer goroutine.

For a moderately complex view (~50 KB rendered HTML), JSON marshal of `{seq, body, ackInputs}` is ~200µs on M1. Under steady-state Time.every load + Cmd.perform completions + browser-driven events, this stacks up: every tick × every session × every Cmd-completion serializes through the same mutex. A 100-session app with 1-second polling sees 100 × 0.0002s = 20ms of lock contention per second — 2% of one core just on JSON marshal.

**Root-cause hypothesis:** the lock-around-marshal pattern was correct when the marshal was trivial, but as observability metadata (ackInputs map, multi-key seq, future trace IDs) has grown, the marshal cost has grown too. The lock's job is to make seq + state mutation atomic; JSON serialization can happen unlocked because the marshalled bytes carry their own snapshot.

**Suggested fix:**
1. Hoist `respSeq = sess.nextOutSeq()` (and snapshot `body`, `ackInputs`) under the lock.
2. Release the lock.
3. Marshal outside the lock — the marshal sees consistent values because they were snapshotted.

3-call-site fix (dispatchBatched, runPerformBody, Time.every setupSubscriptions). Single shared helper `prepareFrame(sess) (snapshot, error)` cleans it up.

**Why current tests miss it:** Go's race detector doesn't flag held-lock contention as an error; only data races. The behavioural symptom is increased p99 latency under high concurrency, which the cabal test suite never exercises.

---

## Gap C7 (severity: medium)
**File:** `runtime-go/rt/live.go:2078-2091` (handleInitial) + `2624-2630` (renderView) + `2895` (handleSSE rebuild)

**Symptom:** `sess.prevBody` and `sess.prevTree` are written at FIVE separate sites: dispatch (2594/2618), handleInitial (2090/2091), renderView (2629), dispatchBatched-handlers-rebuild (2438), handleSSE-reconnect-resync (2894/2895). Each site has slightly different invariants:
- dispatch writes both BEFORE runCmd (line 2594 prevTree, 2618 prevBody — runCmd spawned goroutines see the new tree).
- handleInitial writes both AFTER runCmd (lines 2087-2091 — runCmd's goroutines may push frames built against the OLD tree, none yet, OK on init).
- renderView writes ONLY prevTree (line 2629 — used by guard-rejected dispatch; prevBody unchanged from prior, so any subsequent suppression check uses stale value).
- dispatchBatched-handlers-rebuild (line 2438) writes prevTree but NOT prevBody — symmetry with renderView.
- handleSSE-resync writes both inside the recover defer (lines 2894-2895), but the encoded body is also kept locally (2896); a panic between 2895 and 2896 leaves prevBody-as-frame-source desynced from the actual written SSE bytes.

This is a 5-site write fan-out for a 2-field invariant. Any new feature that re-renders (e.g. the future broadcast handler from Gap C3) needs to update both fields; missing one site silently corrupts subsequent diff/suppression decisions.

**Suggested fix:** extract `func (sess *liveSession) commitRender(vn *VNode, body string)` that writes both fields atomically. All 5 sites call it. The function's signature documents the invariant: prevTree and prevBody MUST be consistent.

---

## Gap C8 (severity: medium)
**File:** `src/Sky/Build/Compile.hs:9322-9332` (`aliasBaseFromCanExpr` module prefix logic — B1 superset)

**Symptom (restatement of B1 with current verification):** the module-name → Go-prefix conversion is hardcoded:

```haskell
let prefix = case ModuleName.toString homeMod of
        ""  -> ""
        nm  -> map (\c -> if c == '.' then '_' else c) nm ++ "_"
```

But the actual Go codegen for dep-module types uses `sanitiseGoName` (defined separately in Compile.hs). If those two paths diverge (e.g. `sanitiseGoName` adds Unicode normalization in a future PR), this lookup silently fails, dropping into the wrap path that emits `any(.).(...)` — the bug class P1 closed for the ASCII case but didn't lock against drift.

**Verification against HEAD:** identical to cycle-2 audit B1. No fix landed. Compile.hs:9322-9326 unchanged.

**Restated suggested fix (per cycle-2 plan P29):** extract `moduleNameToGoPrefix :: ModuleName -> String` helper, use in BOTH `aliasBaseFromCanExpr` AND every codegen site building dep-module prefixes. Add a property test asserting the prefix matches `sanitiseGoName`'s convention across the 27 examples.

---

## Gap C9 (severity: medium)
**File:** `runtime-go/rt/live.go:3548-3572` (`__skyReviveScripts` — B3 restated)

**Symptom (restatement of B3 with current verification):** script-tag attribute copy is unfiltered. `<script onerror="alert(1)" src="...">` revival re-emits `onerror` verbatim. The attribute fires when the script element loads (or fails to load).

**Verification against HEAD:** live.go:3554-3557 still uses unconditional `fresh.setAttribute(a.name, a.value)`. No whitelist applied.

**Restated suggested fix (per cycle-2 plan P31):** strict allowlist of safe `<script>` attrs (src, type, async, defer, integrity, crossorigin, nomodule, referrerpolicy, data-sky-script-revived). Drop event-handler attrs. Drop inline script bodies unless `src` is also present (or only allow inline for a SAME-ORIGIN list of known bundles).

**Note:** Sky's `Ui.html` accepts arbitrary HTML; user-supplied content flowing into `Html.node "script" [...] []` is the attack surface. The current threat model is "user owns the source", but a Sky.Live app accepting WYSIWYG content + rendering it back into `Ui.html` is exactly the kind of misuse Sky should be hardened against.

---

## Gap C10 (severity: high — RESTATED from cycle 3 prior pass)
**File:** `src/Sky/Build/Compile.hs:9996, 10357, 10163` (3 sites of `unsafePerformIO (readIORef scopeStateRef)` outside the wrappers)

**Symptom:** Three SEPARATE `unsafePerformIO (readIORef scopeStateRef)` reads at the function head of `letToGo`, `defToStmts`, and `registerMainLetBindingType`. Each is a "POC for the v0.15.6 cascade" per the in-line comments (PR 3 iteration 3). Each captures a snapshot of scope state ONCE per function entry and threads it via `ctx`.

This is GOOD pattern — single snapshot vs N implicit reads. But it's also LATENT: the three sites form a chain. `defToStmts` is called from `letToGo`'s `defStmts` thunk. If `letToGo`'s caller has installed ctx via the lowerExpr wrapper, AND `defToStmts` runs INSIDE the body's `lowerBody body` evaluation, then `defToStmts`'s `unsafePerformIO` snapshots the WRAPPER-INSTALLED ctx, not the original caller's ctx. This is the intended behaviour FOR LAMBDA BODY (ctx propagation), but `defToStmts` is also called from the LET DEF, where the wrapper ctx isn't relevant.

The current code happens to work because the wrappers are only installed on FOUR slots (lambda body, call arg, plus the reverted record/list/let-body sites). `defToStmts` doesn't sit inside any of them today. But if P7 migrates the let-body to use a wrapper, `defToStmts` would silently start observing the wrapper-installed ctx — and the developer might not notice because the ctx fields it reads (region-types) overlap correctly across the boundary.

**Hazard for P7:** the symptom would be a region-types lookup returning the WRONG region's type because the wrapper installed a ctx whose region map was extended for the body, and `defToStmts` reads regions intended for the let's RHS. Subtle, intermittent, only triggered when the let RHS has a region whose Go type the body lowering rewrites.

**Suggested fix:** as part of C5's reframed P7 plan, also unify the three `unsafePerformIO (readIORef scopeStateRef)` sites into a single helper that documents the snapshot semantics explicitly:

```haskell
-- | Snapshot scope-state ctx at function entry. Returned ctx is
-- the value installed at the CALL site, NOT any inner-wrapper
-- installed value. Use for functions whose body should observe
-- the caller's scope, not any nested wrapper's.
snapshotCallerCtx :: () -> LC.LowerCtx
snapshotCallerCtx () = unsafePerformIO (readIORef scopeStateRef)
```

The mere act of giving the read a name + Haddock comment makes the semantic explicit; future migrations will think twice before assuming "ctx" means "wrapper-installed ctx".

---

## Gap C11 (severity: critical, strategic — RESTATED from cycle 3 prior pass)
**File:** `runtime-go/rt/live.go:2926-2934` (handleSSE for-select; sseCh transports full bodies, not patches)

**Symptom:** The SSE channel `sess.sseCh` carries full RENDERED HTML BODIES, not structural patches. Every Cmd.perform completion, every Time.every tick, every reconnect-resync sends a complete `<sky-root>...</sky-root>` blob (typically 10-50 KB). The HTTP /_sky/event POST path (line 2375-2381) computes structural patches via `diffTrees`, but the SSE path does not.

This forces THREE costs:
1. Bandwidth: every SSE frame ships the full body even when only one attribute changed.
2. Client re-render: `__skyPatch`'s `innerHTML` replacement loses uncontrolled-input state unless every focused-element rule fires correctly (live.go:3245-3370 area). v0.15.14's Events-diff fix CANNOT prevent this for SSE-driven changes — diffTrees only runs on the HTTP path.
3. Suppression: the byte-equality check in `runPerformBody` and Time.every (v0.15.14) only catches WHOLE-BODY identity. A view with a single timestamp ticking would never suppress.

**Status:** the v0.15.14 patch addresses the SUPPRESSION cost at the byte-equality boundary. It does NOT address the BANDWIDTH or RE-RENDER costs. Gap C1 closes the patch-layer regression; this gap (C11) closes the structural cost.

**Suggested fix:** SSE diff-then-patch.
1. When a Cmd.perform completion / Time.every tick renders a new view, compute `patches := diffTrees(sess.prevTree, newTree, ...)` BEFORE encoding the SSE frame.
2. If `patches` is non-empty AND not `patchesAreFullReplace(patches)`, encode the patches into the SSE frame (`event: patches` + JSON body) instead of `event: patch` + full HTML.
3. Client adds an `event: patches` listener that calls `__skyApplyPatches(patches)` (existing function at live.go:3989+).
4. Fall back to full-body SSE for the first session (no prevTree) and for full-replace cases.

This closes:
- Gap C1 fully (suppression becomes "patches.length === 0").
- Gap C2 fully (prevBody dual-meaning goes away — patches transport doesn't need it).
- A part of Gap A7 (newline-in-JSON-key class) by routing structured payloads through a single JSON-marshalling chokepoint.
- The Sky.Live multi-session story (C3) becomes simpler: broadcast events ship patches that every recipient session applies independently.

**Estimated effort:** 12-20 hours, 2-3 PRs:
- (i) wire prevTree through runPerformBody + Time.every; compute diffTrees.
- (ii) add `event: patches` SSE event type + client `__skyApplyPatches` adapter for SSE.
- (iii) update reconnect-resync to ship full-body as today (the only "no prevTree yet" case).

This is C11 = the larger architectural follow-up the v0.15.14 commit message acknowledges ("closes Cycle 3 audit Gap C1 at patch layer; C11 transport overhaul remains").

---

## Gap C12 (severity: medium)
**File:** `runtime-go/rt/live.go:2856-2860` (handleSSE helloPayload JSON marshal)

**Symptom (restatement of A7 with current verification):** `helloPayload` is JSON-marshalled via `json.Marshal` (line 2856). The marshal output contains escaped newlines (`\n` → `\\n`) for any string values that contain literal newlines. The SSE writer's `strings.ReplaceAll(frame, "\n", "\\n")` (lines 2899/2928) double-escapes — first the JSON layer escapes, then the SSE layer replaces the surviving `\n`. For the hello map (simple int + string values), this is a no-op. But the asymmetry between the hello path (single marshal + no SSE escape) and the patch path (renderVNode → marshal → SSE escape) means a future field added to helloPayload that contains newlines would NOT be SSE-escaped, breaking the protocol.

**Verification against HEAD:** unchanged from cycle-1 audit A7. No chokepoint encoder exists; `strings.ReplaceAll(frame, "\n", "\\n")` appears at lines 2899 and 2928 but not at the hello site.

**Restated suggested fix (per cycle-1 plan P15):** consolidate every SSE write into a single `writeSSEFrame(w, eventType, payload)` helper that handles JSON marshal + newline escape + headers. Three sites become one.

---

## Gap C13 (severity: low)
**File:** `src/Sky/Build/Compile.hs:10259-10266` (`letBindingType`'s `canRouteTyped` whitelist)

**Symptom:** The body-shape whitelist for typed routing — Record / Lambda / If / Case / Let / LetRec — is unchanged since v0.15.3. The comment block (Compile.hs:10233-10256) explicitly documents the regression that broadening to other shapes triggered:

> Concrete regression that fired in baseline sweep:
>   `decimals = Ffi.callPure "Money_allocate" […]` — typed gate emitted `rt.AsListT[Decimal](rt.Ffi_callPure(…))` which stripped the Result-Ok wrap incorrectly and yielded an empty slice; downstream `List.map` returned [] and `Money.allocate` silently produced no parts.

This is a SYMPTOM of `coerceArg`'s string-based type parsing (Gap A12 / Prior #7) — the routing whitelist is a workaround. The whitelist will be droppable once P10/P11 land (structural GoType ADT replaces string parsing).

**Status:** the whitelist is currently CORRECT for the typed-routing slots it covers. The gap is the structural-fix dependency: dropping it without P11 re-opens the Ffi.callPure regression.

**Suggested action:** add a comment cross-referencing P10/P11 + the regression test that locks the workaround. Already partially present at Compile.hs:10218-10230 — strengthen by naming the specific path (`coerceArg → coerceToFieldType → rt.AsListT`) that misbehaves on FFI results.

---

## Gap C14 (severity: low)
**File:** `runtime-go/rt/live.go:1271` (`sseCh chan string`); cycle-2 standing direction (P34)

**Symptom:** `sseCh` capacity is hardcoded at 16 (live.go:2066). For a session under heavy event load (rapid keystrokes + Time.every + Cmd.perform completions), the channel can fill and frames drop silently via the `default:` arm at 2719/2781. The drop is a correctness loss (the client never sees the missed view), masked by the next-event re-render (which overrides the missed frame anyway).

But: under a broadcast scenario (Gap C3), the missed frame might be the ONLY representation of a transient event (e.g. "user X is typing"). Drops become user-visible.

**Suggested fix:** parameterize sseCh capacity via env (`SKY_LIVE_SSE_BUF`, default 16). Add metric `sky_live_sse_drops_total` so operators can observe drop rate. The 16-buffer is a reasonable default for steady state but a 5-second burst at 100 ms/tick fills it.

**Related to:** P34 (cabal-test memory pathology) — not directly, but the buffer-pressure question is similar (bounded buffers vs unbounded growth).

---

## Tooling / process gaps (this cycle)

1. **No `runtime-go/rt/` test for runPerformBody suppression.** v0.15.14 patch landed without a `_test.go` mate. A Go-level unit test exercising the byte-identical-view → no-SSE-frame contract should land next cycle (Cycle 4 Item).

2. **No multi-session Playwright fixture.** `scripts/verify-all-web.sh` exercises single-session Sky.Live apps. Phase 3g (Gap C3) cannot land without a multi-tab Playwright probe — the existing infra has zero coverage for cross-session coordination.

3. **No measurement of SSE bandwidth.** Gap C11 (transport overhaul) is justified by bandwidth + re-render cost, but no metric exists today. A `sky_live_sse_bytes_total{session}` counter would gate the C11 fix on demonstrable cost.

4. **`scopeStateRef` write/restore semantics undocumented.** The `lowerExpr` / `lowerExprExpectGo` wrappers' contract ("force to WHNF before restore") is documented in-line but not in a separate design note. Future maintainers reading the wrapper code may miss the subtle force-then-restore ordering. A `docs/v0.15.x-hardening/lowerctx-cascade-design.md` write-up summarizing the 3 Phase 2 wrappers + the 3 deferred-thunk reverts would prevent regressions during P7.

---

## Closure-of-prior verification

The 3 cycle-3 critical items the prior summary highlighted:

- **C1 (runPerformBody suppression):** PARTIALLY CLOSED by v0.15.14 patch (3de5810). Residuals (this audit): dispatchBatched omission + no Go test + post-panic-zero edge case.

- **C3 (multi-session pub-sub prerequisites):** OPEN. None of 5 prereqs (Subscribe API, seq split, handler rebuild, divergence detection, memory bound) exist in current main. Phase 3g cannot ship without a design doc first.

- **C5 + C10 (LowerCtx blackhole class):** OPEN. P6 (v0.15.15) closed the 2/4 safe slots; the remaining 3 require Solver-side surgery (extend SolvedTypes with region map) before they can migrate. The "delete scopeStateRef" framing is WRONG; the right framing is "make letBindingType pure".

- **C4 (TTL-deletion race):** OPEN. Verified at live_store.go:300-330 and live.go:1271. Goroutine leak class.

- **C11 (SSE diff-then-patch transport):** OPEN. v0.15.14 patch addresses ONLY the byte-equality suppression boundary; the transport itself still ships full HTML bodies.

---

## Overall assessment

The v0.15.14 + v0.15.15 patches close the most acute symptoms (canvas-wrap keypress drop; LowerCtx Phase 2 zero-behaviour-change cascade). The underlying architecture remains:
- SSE ships full bodies, not patches (C11).
- `scopeStateRef` is a shared seam that prevents the safe migration of 3/4 ctx-cascade slots without Solver-side surgery (C5).
- No multi-session coordination primitive exists; Phase 3g blocked on design (C3).
- TTL-deletion leaks goroutines (C4).

The runtime hardening backlog (Cycle 1 P14-P19) and the structural-fallback hygiene gaps (Cycle 2 B1-B5) are ALL still open. None of these are regressions from the v0.15.14/v0.15.15 work; they remain as previously-scoped Cycle 1 / Cycle 2 items waiting for their dev slot.

**Critical path for Cycle 4:**

1. **Add the missing runPerformBody Go test** (closes Gap C1 residual; ~1 hour).
2. **Mirror suppression into dispatchBatched** (closes Gap C1 residual; ~1 hour).
3. **Land P7-as-reframed**: Solver-side SolvedTypes extension to carry region map; make `letBindingType` pure; THEN delete `_lc_regionTypes` from `scopeStateRef` (closes C5; 8-12 hours, 2 PRs).
4. **Land P14 (SSE channel race / TTL-deletion goroutine leak)** (closes A3 + C4 + A13 together; 6-8 hours).
5. **Draft a Phase 3g design doc** before any code (gates C3; 4-6 hours design work).
6. **Address C11 transport overhaul** as the cycle-4 strategic item — requires (3) and (5) to be settled first; 12-20 hours.

---

## Cross-reference

- Prior cycle 3 audit output: LOST (regenerated in this file).
- Implementation reports: docs/v0.15.x-hardening/implementations/CYCLE-01-{developer,P2-followup-developer,P3-developer,P4-developer,P5-developer}.md
- Planner outputs: docs/v0.15.x-hardening/plans/CYCLE-{01,02}-planner.md
- Tag history: docs/v0.15.x-hardening/CYCLE_LOG.md (v0.15.7 → v0.15.15)
- Release artefacts: v0.15.14 (PR #85, 3de5810); v0.15.15 (PR #84, 38c50e4).
