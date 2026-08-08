# G1 — Reactive initial-render self-deadlock: implementable fix design

**Status**: DESIGN (not implemented). Author: architect agent.
**Repo**: `/Users/anzel/works/playground/sky` · **Branch**: `feat/bluedb` @ `0242154e`
**Severity**: every initial page load of every reactive Sky.Live app hangs forever.
**Regressing change**: Phase-4b `b3a062a1` ("rt reactive pump") added
`reactiveEnsureStartedHook` inside `setupSubscriptions`.

---

## 0. Executive summary

| | |
|---|---|
| **Root cause** | `setupSubscriptions` has an **undocumented, unenforced, but universally-honoured** contract: *callers hold `sess.mu`*. Phase-4b hooked `ensureReactiveStarted` into it; that callee re-acquires `sess.mu` to read `sess.model`. Go mutexes are not reentrant → self-deadlock on the same goroutine. |
| **Chosen fix** | **(b) + (a): thread the caller's `model` through the hook, and promote the implicit "caller holds `sess.mu`" rule to a written, machine-checked contract.** |
| **Verified** | The deadlock reproduces in a 45-line `rt` test (measured, §5.1); the fix makes it pass and keeps `go test ./rt/ -race -tags pebblegozstd` green (measured, §6). |
| **Structural guard** | A real runtime test at the `handleInitial` layer (primary) + an always-compiled, test-enabled `assertSessMuHeld` lock-contract assertion (secondary) + a **non-vacuous** grep tripwire on `ensureReactiveStarted` (tertiary). |
| **Cost** | 4 production lines + 1 new test file (the fix). The lock-contract assertion additionally requires 51 mechanical lock-wraps across 27 tests in 12 test files (enumerated in §4.2 — every one is test-side; **zero** production callers violate the contract). |

---

## 1. Full lock-discipline audit

All line numbers are `feat/bluedb` @ `0242154e`, verified by direct read.

### 1.1 The deadlock chain

```
live.go:4176   handleInitial            sess.mu.Lock()
live.go:4183     app.setupSubscriptions(sess)          ← sess.mu HELD
live.go:5492       reactiveEnsureStartedHook(app, sess)
bluedb_reactive.go:41  → app.ensureReactiveStarted(sess)
bluedb_reactive.go:149     sess.mu.Lock()             ← SELF-DEADLOCK
live.go:4214   (never reached) sess.mu.Unlock()
```

### 1.2 Callers of `setupSubscriptions` — production

| # | Call site | Function | `sess.mu` at call | Evidence |
|---|---|---|---|---|
| P1 | `live.go:4183` | `handleInitial` (`live.go:4011`) | **HELD** | acquired `live.go:4176`, released `live.go:4214` |
| P2 | `live.go:4991` | `dispatch` (`live.go:4814`) | **HELD** (by every caller — see 1.3) | `dispatch` itself never locks; all 7 callers lock first |

**There are exactly two production call sites.** Both hold `sess.mu`.

### 1.3 Callers of `dispatch` (which transitively calls `setupSubscriptions`)

| # | Call site | Function | `sess.mu` at call | Lock acquired at |
|---|---|---|---|---|
| D1 | `live.go:4552` | `handleEvent` (`live.go:4355`) | **HELD** | `live.go:4446` (and `4423` on the rebuild branch) |
| D2 | `live.go:4737` | `dispatchBatched` (`live.go:4680`) | **HELD** | `live.go:4681` |
| D3 | `live.go:5372` | `runPerformBody` (`live.go:5351`) | **HELD** | `live.go:5361` |
| D4 | `live.go:5601` | `Time.every` tick goroutine (spawned in `setupSubscriptions`, `live.go:5571`) | **HELD** | `live.go:5581` |
| D5 | `live.go:5845` | `runSubscriberDispatch` (`live.go:5825`) | **HELD** | `live.go:5842` |
| D6 | `live.go:6065` | `runStreamSubscriberDispatch` (`live.go:6036`) | **HELD** | `live.go:6062` |
| D7 | `websocket.go:926` | `dispatchOneWsSub` (`websocket.go:887`) | **HELD** | `websocket.go:923` |

**Verdict: production is 100% consistent.** Every path that reaches
`setupSubscriptions` holds `sess.mu`. The contract is real; it was simply never
written down or enforced.

### 1.4 Callers of `setupSubscriptions` — tests (the inconsistency)

| Call site | `sess.mu` at call |
|---|---|
| `live_pubsub_dispatch_test.go` :206, :249, :320, :341, :420, :430, :445, :477, :522, :592, :596, :638 | **NOT held** |
| `live_store_delete_test.go` :185, :258 | **NOT held** |

Plus 37 further violations that reach `setupSubscriptions` via an unlocked
`app.dispatch(...)` call — full list in §4.2. **All 51 are test-side.**

> **This inconsistency is the root cause of the class**, not merely of the
> instance. Because the test fixtures run `setupSubscriptions` *without*
> `sess.mu`, a callee that acquires `sess.mu` looks perfectly correct under
> `go test` — which is precisely why Phase-4b shipped green while every real
> app hangs. The design must make the tests obey the production contract, not
> paper over the divergence.

### 1.5 Callers of `reactiveEnsureStartedHook` / `ensureReactiveStarted`

| Call site | `sess.mu` at call | Note |
|---|---|---|
| `live.go:5492` (`setupSubscriptions`) | **HELD** on both production paths, **NOT held** on the 51 test paths | the sole call site |
| `bluedb_reactive.go:41` (`init` wiring) | n/a | hook wiring only |

`reactiveTeardownHook` (`live.go:2442`, from `markDone` at `live.go:2368`) is
called with `sess.mu` **NOT** held, and `reactiveTeardown`
(`bluedb_reactive.go:79`) takes only `reactiveStateMu`. **No hazard; unchanged
by this fix.**

### 1.6 Everything `setupSubscriptions` transitively touches

| Callee | Lock taken | Re-enters `sess.mu`? | Verdict |
|---|---|---|---|
| `sess.cancelSubMu` block, `live.go:5484-5487` | `cancelSubMu` (leaf) | no | safe |
| `reactiveEnsureStartedHook`, `live.go:5492` | **`sess.mu`** (via `bluedb_reactive.go:149`) | **YES** | **THE BUG** |
| `sky_call(app.subscriptions, sess.model)`, `live.go:5501` | none | no — but **reads `sess.model` unlocked** | *proof the contract already exists*: this read is only race-free because the caller holds `sess.mu` |
| `applyTopicSubsDiff`, `live.go:5680` | `sess.activeSubsMu` (leaf) | no — the doc comment at `live.go:5669-5672` explicitly says "NOT `sess.mu`, so a concurrent subscriber dispatch holding `sess.mu` + calling `setupSubscriptions` doesn't recurse" | safe, and **states the contract in prose** |
| `applyStreamSubsDiff` / `applyWsSubsDiff`, `live.go:5540-5541` | `activeStreamSubsMu` / `activeWsSubsMu` (leaf) | no | safe |
| `sess.cancelSubMu` read, `live.go:5558-5560` | `cancelSubMu` (leaf) | no | safe |
| `Time.every` goroutine, `live.go:5571` | `sess.mu` at `live.go:5581` | yes, **but on a new goroutine** | safe (this is the documented "deferred first dispatch") |

`live.go:5501` is the smoking gun: `setupSubscriptions` **already** reads
`sess.model` with no lock of its own. The function has *always* required the
caller to hold `sess.mu`. Phase-4b's mistake was not "adding a lock where none
was needed" — it was "adding a lock in a function that was already inside the
critical section".

---

## 2. Fix strategy — evaluation and choice

### 2.1 Candidate (a) — hard contract + lock-free callee

Document "`setupSubscriptions` MUST be called with `sess.mu` held"; make
`ensureReactiveStarted` read `sess.model` without locking.

* **Pro**: matches reality (§1.3); closes the class, not the instance; makes the
  pre-existing unlocked read at `live.go:5501` *correct by contract* instead of
  correct by luck.
* **Con**: `ensureReactiveStarted` still *has* `sess` in hand and could re-lock;
  the contract is prose unless enforced.

### 2.2 Candidate (b) — thread `model` through the hook ✅ (chosen, with (a))

`reactiveEnsureStartedHook(app, sess, model)` /
`ensureReactiveStarted(sess *liveSession, model any)`; delete
`bluedb_reactive.go:149-151`.

* **Pro**: the callee has **no reason** to touch `sess.mu` — the only field it
  wanted is handed to it. Removes a redundant lock hop on the hot dispatch path.
  Strictly *more* correct than today: the model is now read at the same instant
  the caller committed it, so there is no window in which `ensureReactiveStarted`
  could observe a model from a *later* dispatch than the one that started it.
  Preserves the hook indirection (`live.go` still never names `bluedb`).
* **Con**: signature churn across the seam (3 files, 4 lines). Does not by itself
  stop a *future* hook from locking.
* **Why (b) needs (a)**: (b) fixes the instance; (a) + the guards in §4 fix the
  class. They are complements, not alternatives — ship both.

### 2.3 Candidate (c) — start reactive loops after `sess.mu` is released ❌

* **Where would it go?** `handleInitial` releases at `live.go:4214`, but
  `dispatch` (P2) does **not** release at all — its 7 callers release at 7
  different points (`live.go:4533/4552…`, `4737…`, `5372…`, `5601…`, `5845…`,
  `6065…`, `websocket.go:926…`). A post-unlock start would have to be added at
  **8 sites**, and each would need its own idempotence re-check. That is a worse
  version of exactly the 6-site band-aid that ADR-001 / Phase-5d already
  dissolved into a funnel.
* **Ordering hazard**: between `sess.mu.Unlock()` and the deferred start there is
  a real window in which a Change can be committed by another session and missed.
  The reactive loop's initial fill (`bluedb_reactive.go:205`) would still
  self-heal it, but the window is a new correctness surface for no benefit.
* **Paint-then-fill**: unchanged either way — under (b) the loops are *spawned*
  under `sess.mu`, and their first `reactiveRefreshOnce` blocks on
  `sess.mu.Lock()` (`bluedb_reactive.go:265`) until the initial render commits.
  That is **precisely** the semantics `live.go:4168-4175` documents ("holding
  `sess.mu` here simply defers those goroutines' first dispatch until the initial
  render has committed"). (c) buys nothing here.
* **Rejected.**

### 2.4 Candidate (d) — reentrant mutex / `TryLock` / state machine ❌

* A reentrant `sess.mu` would silently legalise re-entry everywhere, destroying
  the one invariant that keeps `renderVNode`'s `sess.handlers` map write safe
  (`live.go:4160-4166`: two writers under different locks = fatal "concurrent map
  writes"). A reentrant lock cannot distinguish "the same goroutine, same
  critical section" from "the same goroutine, nested render".
* `TryLock`-then-skip in `ensureReactiveStarted` would make the model read
  *silently skipped* on the initial-render path — the reactive loops would start
  against a nil model, and `assertReactiveCapabilityOrExit` would classify the
  backend as `""` and never gate. Silent wrong behaviour is worse than a hang.
* A state machine (defer the start to a "post-render" queue) is (c) with extra
  machinery and the same 8-site problem.
* **Rejected.** (`TryLock` *is* used in §4.1 — but only as a one-way
  **assertion**, never as control flow.)

### 2.5 The chosen fix — exact diff

**Do NOT touch `live.go:4176`.** That lock predates the mandate (`main`
`0ce26000`) and is the sole protection against the fatal concurrent-map-write in
`renderVNode` documented at `live.go:4154-4175`. This design does not narrow,
move, or remove it.

**1. `runtime-go/rt/live_reactive_hooks.go:12`**

```go
-	reactiveEnsureStartedHook = func(app *liveApp, sess *liveSession) {}
+	// CONTRACT: called with sess.mu HELD (see setupSubscriptions). The caller's
+	// committed model is passed in precisely so the implementation never needs —
+	// and must never take — sess.mu.
+	reactiveEnsureStartedHook = func(app *liveApp, sess *liveSession, model any) {}
```

**2. `runtime-go/rt/bluedb_reactive.go:41`**

```go
-	reactiveEnsureStartedHook = func(app *liveApp, sess *liveSession) { app.ensureReactiveStarted(sess) }
+	reactiveEnsureStartedHook = func(app *liveApp, sess *liveSession, model any) {
+		app.ensureReactiveStarted(sess, model)
+	}
```

**3. `runtime-go/rt/bluedb_reactive.go:132-159`**

```go
-func (app *liveApp) ensureReactiveStarted(sess *liveSession) {
+// LOCK CONTRACT: MUST be called with sess.mu HELD (setupSubscriptions' contract,
+// live.go:5472). `model` is the caller's already-committed sess.model — this
+// function must NEVER acquire sess.mu (Go mutexes are not reentrant; doing so
+// self-deadlocks the initial render, see docs/bluedb/g1-reactive-deadlock-fix-design.md).
+func (app *liveApp) ensureReactiveStarted(sess *liveSession, model any) {
 	if app.reactiveBindings == nil {
 		return
 	}
 	... reactiveStateMu once-only gate unchanged (bluedb_reactive.go:136-147) ...
-
-	sess.mu.Lock()
-	model := sess.model
-	sess.mu.Unlock()
-
-	app.assertReactiveCapabilityOrExit(model)
-
-	for _, b := range app.reactiveBindingsFor(model) {
+	// Evaluate the Sky `reactiveBindings model` accessor ONCE (it ran twice
+	// before: once inside the gate's reactiveDataBackendKind, once here). It is
+	// now on the sess.mu critical path, so paying for it twice is not free.
+	bindings := app.reactiveBindingsFor(model)
+	app.assertReactiveCapabilityOrExit(model)
+
+	for _, b := range bindings {
```

**4. `runtime-go/rt/live.go:5492`**

```go
-	reactiveEnsureStartedHook(app, sess)
+	// sess.model read under the CALLER's sess.mu (setupSubscriptions' contract,
+	// see the function doc) — the hook must not lock, so we hand it the model.
+	reactiveEnsureStartedHook(app, sess, sess.model)
```

**5. `runtime-go/rt/live.go:5472`** — write the contract into the
`setupSubscriptions` doc comment:

```go
// LOCK CONTRACT (INVIOLABLE): setupSubscriptions MUST be called with sess.mu
// HELD, and MUST NOT itself acquire sess.mu — synchronously or via any hook.
// It already reads sess.model unlocked (line ~5501); that read is race-free
// ONLY because the caller holds sess.mu. Every production caller complies:
// handleInitial (live.go:4183, lock at 4176) and dispatch (live.go:4991, whose
// 7 callers all lock first). Any callee that re-acquires sess.mu SELF-DEADLOCKS
// the initial render on the same goroutine — that is exactly what Phase-4b's
// reactive hook did. Enforced by assertSessMuHeld (below) + the runtime test
// TestHandleInitial_ReactiveApp_DoesNotDeadlock.
```

**Preservation check** (every invariant the mandate names):

| Invariant | Preserved? | Why |
|---|---|---|
| `renderVNode` concurrent-map-write protection (`live.go:4154-4175`) | ✅ | `live.go:4176` lock untouched; its span is unchanged |
| paint-then-fill of `reactiveLoop` | ✅ | loops still spawned under `sess.mu`; first `reactiveRefreshOnce` (`bluedb_reactive.go:265`) blocks until `live.go:4214` — the documented "deferred first dispatch" |
| once-only idempotence (`reactiveStateMu` + `st.started`) | ✅ | `bluedb_reactive.go:136-147` untouched |
| tenant/identity stamping of spawned goroutines | ✅ | `reactiveLoop` still does `setGoroutineLiveSession(sess)` (`bluedb_reactive.go:178`); `currentSessionTenant()` (`bluedb_reactive.go:50-60`) reads `sess.identity`, never `sess.model` — the fix touches neither |
| `live.go` never imports `bluedb` | ✅ | hook indirection retained; only the signature widens by one `any` |
| `bluedb` never imports `rt` | ✅ | no `bluedb/` file is touched |

---

## 3. `assertReactiveCapabilityOrExit` under the session lock

**Under the chosen fix, YES — it is called with `sess.mu` held** (and with
`app.locker(sid)` held, on the `handleInitial` path).

What runs inside (`bluedb_reactive_gate.go:152-186`):

| Step | Cost under lock | Blocking? |
|---|---|---|
| `reactiveGateOnce.Do` | once per **process** | no |
| `reactiveDataBackendKind(model)` → `reactiveBindingsFor(model)` → `sky_call` into the Sky accessor + `embeddedBackend` registry lookups | O(#bindings), pure Sky + map lookups | no I/O, no network, no DB |
| `productionFromEnv()` / `os.Getenv` | trivial | no |
| FATAL arm: `fmt.Fprintf(os.Stderr, …)` + **`os.Exit(1)`** | one stderr write | no |
| WARN arms: `logStructured(...)` | one log emit | no |

**Verdict — acceptable, keep it inline, with one change.**

* `os.Exit(1)` while holding `sess.mu` is fine: the process is terminating on an
  operator misconfiguration, no deferred unlock matters, and this mirrors
  `AssertConsoleInvariantOrExit`. Crucially, exiting **before** the initial
  render commits is *better* than exiting after — a fail-closed gate must not
  serve one page that appears to work.
* No step blocks. The only real cost is the Sky accessor evaluation, which ran
  **twice** (once via `reactiveDataBackendKind`, once at
  `bluedb_reactive.go:159`). §2.5 change 3 hoists it to **one** evaluation and
  passes the result to both. Net effect: the first request of the process pays
  *less* work under `sess.mu` after the fix than the (deadlocked) code intended.
* It must **not** move after the loop spawn: the gate exists to refuse boot
  *before* a silently-stale reactive loop starts serving. Moving it post-unlock
  would let one render + one fill escape the gate.

**Rule to write down**: nothing added to this path may perform I/O, acquire a
non-leaf lock, or block. The gate stays because it is O(#bindings) and
once-per-process.

---

## 4. Preventing the class

Ranked by strength. Ship **G-1** and **G-2**; **G-3** is cheap and additive.

### 4.1 G-1 (primary, a real test): `TestHandleInitial_ReactiveApp_DoesNotDeadlock`

See §5. This is the layer the Judge identified as missing. Preferred over any
grep because it exercises the actual path under a timeout.

### 4.2 G-2 (secondary): `assertSessMuHeld` — always compiled, test-enabled

New file `runtime-go/rt/live_lock_contract.go`:

```go
package rt

// sessMuContractChecks enables the sess.mu lock-contract assertions. OFF in
// production (one predictable bool load per call — no atomic, no lock); the rt
// test suite turns it on via live_lock_contract_enable_test.go, so EVERY rt test
// that reaches a contract-annotated function is checked.
var sessMuContractChecks = false

// assertSessMuHeld panics if the caller does NOT hold sess.mu.
//
// Soundness: TryLock on a mutex we already hold returns false (it never blocks,
// never deadlocks). So a SUCCESSFUL TryLock proves nobody held the lock at that
// instant — including us — which is a contract violation. False positives are
// impossible. False negatives are possible (another goroutine happens to hold
// it while we do not); that is an acceptable one-way assertion.
func assertSessMuHeld(sess *liveSession, site string) {
	if !sessMuContractChecks || sess == nil {
		return
	}
	if sess.mu.TryLock() {
		sess.mu.Unlock()
		panic("sess.mu lock contract violated: " + site +
			" must be called with sess.mu held (see docs/bluedb/g1-reactive-deadlock-fix-design.md)")
	}
}
```

`runtime-go/rt/live_lock_contract_enable_test.go`:

```go
package rt

func init() { sessMuContractChecks = true }
```

Call sites: `assertSessMuHeld(sess, "setupSubscriptions")` at the top of
`live.go:5472`, and `assertSessMuHeld(sess, "dispatch")` at the top of
`live.go:4814`.

**Measured cost of adopting this** (I ran it, see §6.4): **51 violations across
27 tests in 12 test files — all test-side, zero production.** Every one is
mechanically fixed by wrapping the call in `sess.mu.Lock()` / `defer
sess.mu.Unlock()` (which is what production does, so the fixtures become *more*
faithful):

```
live_commit_render_test.go:217,247,263        live_perform_suppression_test.go:81,115,165,205,261,276,329
live_dispatch_noop_test.go:46,54,78,79        live_pubsub_dispatch_test.go:206,249,320,341,420,430,445,477,522,592,596,638
live_marshal_outside_lock_test.go:260,359     live_sse_buffer_test.go:184,227
live_perform_persist_test.go:53,90,128        live_sse_diff_producer_test.go:181
                                              live_store_delete_test.go:185,258
```

Owning tests: `Test_CommitRender_DispatchEndToEnd`,
`Test_CommitRender_DispatchPanicRollsBackViaHelper`,
`TestDispatch_returnsBodyForIdenticalView`, `TestDispatch_emitsWhenViewChanges`,
`TestDispatch_PanicPreservesPrevTreeAndLastComputed`,
`TestPerformBody_NoFrameNoRequiredPersist`, `TestPerformBody_PersistsBeforeAck`,
`TestRunPerformBody_IdenticalView_SuppressesFrame`,
`TestRunPerformBody_LastShippedAdvancesCoherently`,
`TestRunPerformBody_SuppressedDispatch_OnlyComputedAdvances`,
`TestRunPerformBody_ViewChange_QueuesFrame`,
`TestRunPerformBody_DropIncrementsCounter`,
`TestDispatchBatched_DropIncrementsCounter`,
`TestSubscriberDispatch_PersistsBeforeAck`,
`Test_SubscriberDispatch_DecoderPanic_Recovered`,
`Test_TwoSession_FanOut_DispatchesMsgToSubscriber`,
`Test_EchoToPublisher_ViaDispatchPath`,
`Test_SetupSubscriptions_BatchEveryAndSubscribeTopic_Coexist`,
`Test_SetupSubscriptions_DiffMode_NoSpuriousChurn`,
`Test_SetupSubscriptions_DiffMode_RemovedDropsRegistration`,
`Test_Cleanup_MarkDone_ReleasesAllSetupSubs`,
`Test_Cleanup_NoGoroutineLeak_AfterDiffSwap`,
`Test_MarshalOutsideLock_DispatchBatched_Snapshot`,
`Test_MarshalOutsideLock_PreservesSeqMonotonicity`,
`Test_RunPerformBody_SmallDelta_ShipsEventPatches`,
`TestEveryGoroutine_exitsOnDelete`, `TestEveryGoroutine_exitsOnCleanupExpiry`.

This is the honest resolution of §1.4: it makes the divergence between test and
production *impossible to reintroduce*, and it is mechanical. It ships as its own
commit (§8, commit 4) so it is independently revertable if the churn is judged
too large in review — but shipping the fix **without** it leaves the class open,
which is the same mistake Phase-4b made.

### 4.3 G-3 (tertiary): a **non-vacuous** tripwire on `ensureReactiveStarted`

New test in `runtime-go/rt/live_reactive_lock_tripwire_test.go`:

```go
func TestEnsureReactiveStarted_NeverTakesSessMu(t *testing.T) {
	src, err := os.ReadFile("bluedb_reactive.go")
	if err != nil { t.Fatalf("read: %v", err) }
	s := string(src)
	const sig = "func (app *liveApp) ensureReactiveStarted("
	i := strings.Index(s, sig)
	if i < 0 { t.Fatal("ensureReactiveStarted not found — update this tripwire") }
	// Body = from the signature to the next top-level func declaration.
	rest := s[i+len(sig):]
	if j := strings.Index(rest, "\nfunc "); j >= 0 { rest = rest[:j] }
	if strings.Contains(rest, "sess.mu.") {
		t.Fatalf(`ensureReactiveStarted touches sess.mu.

INVARIANT: setupSubscriptions is called with sess.mu HELD (live.go:4183 and
live.go:4991). ensureReactiveStarted runs inside that critical section, so any
sess.mu acquisition here SELF-DEADLOCKS every initial page load (Go mutexes are
not reentrant). The caller's model is already a parameter — use it.`)
	}
}
```

**Non-vacuity proof (required by the mandate).** The assertion is a predicate
over the *body of the function being protected*, not over an unrelated token
count:

* Re-insert today's `bluedb_reactive.go:149-151` (`sess.mu.Lock(); model :=
  sess.model; sess.mu.Unlock()`) → `strings.Contains(rest, "sess.mu.")` is true →
  **test fails**. The exact regression is caught.
* Add *any* new `sess.mu.Lock()` / `sess.mu.RLock()` anywhere in the function →
  **test fails**.
* Delete the whole function → `strings.Index(sig)` returns −1 → **test fails**
  with "update this tripwire". No silent pass on removal.

**Contrast with the existing vacuous tripwire.**
`live_persist_invariant_test.go::TestPersistBeforeAck_FunnelIsSoleSender` asserts
(i) `strings.Count(s, "case sess.sseCh <- frame:") == 1`, (ii) that occurrence is
positioned after the funnel's signature, (iii)
`strings.Count(s, ".fanOutFrame(") == 3`. **None of these three predicates
mentions the persist.** Deleting `app.store.Set(sess.sid, sess)` at
`live.go:5340-5342` leaves all three true → the test passes while the invariant
it names is gone. Two further defects found during this audit, reported for the
no-deferral pipeline (out of scope for this fix, tracked in §7):

* **Scope vacuity**: it reads only `live.go`, but the package has **three** raw
  `sseCh <- frame` sends — `live.go:5344`, **`bluedb_reactive.go:311`**, and
  **`websocket.go:945`**. The stated invariant ("exactly ONE raw async send") is
  already false at HEAD.
* **A real hole behind it**: `reactiveRefreshOnce`
  (`bluedb_reactive.go:294-317`) mutates `sess.model` and ships an SSE frame
  **without** routing through `persistAndShipFrame` — i.e. the reactive path
  acks a Model mutation before persisting it, the exact grill-A1 failure the
  Phase-5d funnel was built to close.

A minimally non-vacuous rewrite of that test would assert the funnel body
contains `app.store.Set(` **and** count raw sends across all three files.

---

## 5. The missing test layer

### 5.1 T1 (the crux) — `TestHandleInitial_ReactiveApp_DoesNotDeadlock`

**File**: `runtime-go/rt/live_reactive_initial_render_test.go` (new).

**Mechanism**: drive the real HTTP entry point `app.handleInitial(w, r)` via
`httptest` on a `liveApp` whose **only** difference from the existing
`newMirrorTestApp()` fixture (`live_nav_mirror_test.go:30`) is a non-nil
`reactiveBindings`. Run the call in a goroutine; `select` on a `time.After`
timeout so a deadlock **fails** the test in 5 s instead of hanging the suite.

```go
package rt

// The initial-render layer. live_reactive_test.go / live_reactive_delivery_test.go
// drive reactiveLoop DIRECTLY — BELOW handleInitial → setupSubscriptions →
// ensureReactiveStarted. That gap is why Phase-4b shipped a green suite and a
// hanging app. This test closes it: it drives the real HTTP entry with an app
// that declares reactive bindings, under a timeout, so a re-introduced
// sess.mu re-entry FAILS the test instead of hanging the suite.

func TestHandleInitial_ReactiveApp_DoesNotDeadlock(t *testing.T) {
	app := &liveApp{
		init:   func(req any) any { return SkyTuple2{V0: "model", V1: cmdT{kind: "none"}} },
		update: func(msg, model any) any { return SkyTuple2{V0: model, V1: cmdT{kind: "none"}} },
		view:   func(model any) any { return velement("div", nil, []any{vtext("page")}) },
		subscriptions: func(model any) any { return nil },
		store:         newMemoryStore(30 * time.Minute),
		locker:        newSessionLocker(),
		msgTags:       map[string]int{},
		// THE one difference from newMirrorTestApp: the app declares reactive
		// bindings (Live.withReactive / Persist.liveInto). An EMPTY binding list
		// keeps the test hermetic — no pebble, no engine — because the deadlock
		// is upstream of any binding being read.
		reactiveBindings: func(model any) any { return []any{} },
	}

	done := make(chan int, 1)
	go func() {
		rr := httptest.NewRecorder()
		app.handleInitial(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		done <- rr.Code
	}()

	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Fatalf("GET / returned %d, want 200", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DEADLOCK: handleInitial did not return within 5s — " +
			"a callee below setupSubscriptions re-acquired sess.mu " +
			"(Go mutexes are not reentrant). See docs/bluedb/g1-reactive-deadlock-fix-design.md")
	}
}
```

**Why an empty binding list is the right choice**: the deadlock is at
`bluedb_reactive.go:149`, *upstream* of `reactiveBindingsFor`, so an empty list
still reproduces it — while keeping the test hermetic (no Pebble temp dir, no
`embeddedRegister`) and keeping `assertReactiveCapabilityOrExit` on its
`backend == ""` → `nil` branch (`bluedb_reactive_gate.go:106-108`), so the test
can never `os.Exit` the test binary.

**"Fails today" — verified by measurement, not by reasoning.** I wrote exactly
this test to an untracked scratch file
`runtime-go/rt/zz_scratch_deadlock_test.go`, ran it, and deleted it (`git status`
is clean):

```
$ cd runtime-go && timeout 900 go test ./rt/ \
    -run TestScratch_ReactiveHandleInitialDeadlock -count=1 -tags pebblegozstd -v
=== RUN   TestScratch_ReactiveHandleInitialDeadlock
    zz_scratch_deadlock_test.go:46: DEADLOCK: handleInitial did not return within 5s
--- FAIL: TestScratch_ReactiveHandleInitialDeadlock (5.00s)
FAIL	sky-app/rt	5.029s
```

**"Passes after the fix" — also verified by measurement.** I applied the §2.5
diff, re-ran, then reverted every tracked file (§6.4):

```
=== RUN   TestScratch_ReactiveHandleInitialDeadlock
--- PASS: TestScratch_ReactiveHandleInitialDeadlock (0.00s)
```

### 5.2 T2 (integration) — `TestHandleInitial_ReactiveApp_LiveDeliversAfterInitialRender`

Same file. Composes T1's entry point with the existing engine fixtures
(`openReactiveBackend`, `schemaFor`, `putRowAs`, `awaitFrameContaining` from
`live_reactive_test.go` / `live_reactive_delivery_test.go`) to prove the *whole*
path works, not merely that it returns:

1. `openReactiveBackend(t)` → `(backend, storeID)`; `b.Register(schemaFor(t))`.
2. Build the app as in T1 but with a **real** binding list (`store: storeID`,
   `schema: reactiveTestSchema`, `run:` the re-query closure from
   `live_reactive_delivery_test.go:78-86`), and a `view` that renders the model's
   id list.
3. `app.handleInitial(rr, req)` in a goroutine with the same 5 s deadlock guard;
   assert `rr.Code == 200` and the body is the *pre-fill* paint (paint-then-fill).
4. Recover the session from `app.store.Get(sidFromSetCookie(...))`
   (`live_nav_mirror_test.go:19`), register an SSE conn, `putRowAs(sess, …)` a row.
5. `awaitFrameContaining(sess.sseCh, "<id>", 3*time.Second)` — the write reaches
   the client **through the initial-render-started loop**.

This is the rt-level analogue of the Phase-4 "2-browser live demo" gate, and it
is the test that gate should have been. **Note for the implementer**: because the
app has always deadlocked, the "initial fill frame is buffered in `sess.sseCh`
until the client's SSE relay attaches" hop has *never* executed in a real app.
T2 is the first thing to exercise it — treat a failure here as a genuine second
bug, not as test-fixture noise (see §7, R6).

---

## 6. Three-leg verification plan (CLAUDE.md §0.4)

All commands timeout-bounded. `cd /Users/anzel/works/playground/sky`.

### 6.1 Leg (a) — unit, `-race`, in `runtime-go/rt`

```bash
cd /Users/anzel/works/playground/sky/runtime-go
timeout 900 go test ./rt/ -run 'TestHandleInitial_ReactiveApp' \
  -count=1 -race -tags pebblegozstd -v
timeout 900 go test ./rt/ -run 'TestEnsureReactiveStarted_NeverTakesSessMu' \
  -count=1 -tags pebblegozstd -v
```

Expected: T1 + T2 + the tripwire PASS. Before the fix, T1 FAILs in 5 s.

### 6.2 Leg (b) — integration, whole runtime

```bash
cd /Users/anzel/works/playground/sky/runtime-go
timeout 3600 go test ./rt/... ./bluedb/... -count=1 -race -tags pebblegozstd
```

Must stay green — in particular these, which pin the reactive delivery path
end to end:

* `TestPhase4b_WriteTimeTenantTag`, `TestPhase4b_TwoTenantIsolation`,
  `TestPhase4b_SameTenantLiveDelivery` (`live_reactive_test.go`)
* `TestPhase4c_HeadlessLiveDelivery`, `TestPhase4c_CrossTenantNoLiveDelivery`
  (`live_reactive_delivery_test.go`)
* `TestHandleInitial_MirrorsNavigationToOtherTabs` (`live_nav_mirror_test.go`) —
  the other `handleInitial` consumer
* `TestPersistBeforeAck_FunnelIsSoleSender` (`live_persist_invariant_test.go`)
* the 27 tests listed in §4.2 (they change in commit 4 and must stay green after)
* the whole `live_pubsub_dispatch_test.go` / `live_store_delete_test.go` /
  `live_sse_buffer_test.go` sets (subscription lifecycle)

### 6.3 Leg (c) — real use, `examples/59-persist-live`

```bash
cd /Users/anzel/works/playground/sky
export CARGO_TARGET_DIR=/Users/anzel/.cargo/bin
timeout 1800 cargo build --release -p sky --manifest-path rust/Cargo.toml 2>&1 | tail -20
#   ↑ CONFIRM "Compiling project" / "Compiling sky" appears, else you are testing
#     a stale binary (the Go runtime is //go:embed'd into the compiler).
command cp -f /Users/anzel/.cargo/bin/release/sky sky-out/sky

cd /Users/anzel/works/playground/sky/examples/59-persist-live
rm -rf sky-out .skycache .skydeps
timeout 900 ../../sky-out/sky build src/Main.sky

SKY_LIVE_PORT=8063 ./sky-out/app > /tmp/ex59.log 2>&1 &
APP_PID=$!
timeout 60 bash -c 'until curl -sf -m 2 -o /dev/null http://localhost:8063/; do sleep 1; done'

# Assertion 1 — the page serves 200 with a body (this is what hangs today).
timeout 30 curl -s -m 25 -o /tmp/ex59.html -w '%{http_code} %{size_download}\n' \
  -c /tmp/ex59.cookies http://localhost:8063/
#   EXPECT: "200 <n>" with n > 0.  TODAY: curl exits 28 (timeout), 0 bytes.

# Assertion 2 — two-tab reactive update. Tab B holds an SSE connection; a write
# performed through tab A must reach tab B's stream.
timeout 20 curl -sN -m 15 -b /tmp/ex59.cookies -H 'X-Sky-Tab: tabB' \
  'http://localhost:8063/_sky/sse' >| /tmp/ex59.sse &
SSE_PID=$!
sleep 2
# (drive the app's add-item action from tab A — the exact handler id / Msg name
#  comes from /tmp/ex59.html; POST it to /_sky/event with the sky_sid cookie and
#  X-Sky-Tab: tabA)
sleep 3
grep -c 'event: patch' /tmp/ex59.sse    # EXPECT: >= 1

# Cleanup — MANDATORY.
kill -9 $SSE_PID $APP_PID 2>/dev/null
pkill -f 'examples/59-persist-live/sky-out/app' 2>/dev/null
rm -f /tmp/ex59.html /tmp/ex59.cookies /tmp/ex59.sse
```

Also run the standard gates once at the milestone:
`timeout 3600 scripts/example-sweep.sh` and
`timeout 900 cargo run -p xtask --manifest-path rust/Cargo.toml -- build-run`.

Ensure `scripts/mem-guard.sh` is running first (CLAUDE.md §1):

```bash
pgrep -f mem-guard.sh >/dev/null || \
  (nohup /Users/anzel/works/playground/sky/scripts/mem-guard.sh > /tmp/mem-guard.out 2>&1 & disown)
```

### 6.4 What was already measured for this design

Run on `feat/bluedb` @ `0242154e`, with all tracked files restored afterwards
(`git status` clean apart from `.claude/settings.local.json`, which was already
modified at session start, and this design doc):

| Measurement | Result |
|---|---|
| T1 (scratch) on unmodified HEAD | **FAIL** — "DEADLOCK: handleInitial did not return within 5s" (5.00 s) |
| T1 + `TestPhase4b_*` + `TestPhase4c_*` with the §2.5 fix applied, `-race` | **all PASS** (4.5 s) |
| Full `go test ./rt/ -count=1 -tags pebblegozstd` with the fix applied | **ok — 20.9 s** |
| Full `rt` suite with a prototype `assertSessMuHeld` on `setupSubscriptions` | **51 violations, 27 tests, 12 files — all test-side, 0 production** |

Scratch files (`zz_scratch_deadlock_test.go`, `zz_scratch_lockcontract.go`,
`zz_scratch_lockcontract_enable_test.go`) were deleted and the three touched
tracked files reverted with `git checkout --`.

---

## 7. Blast-radius / risk register

| # | Risk | Why it could bite | Gate that catches it |
|---|---|---|---|
| R1 | **SSE reconnect-resync** re-renders under `sess.mu` (`live.go:4174` names it) — if it ever gained a `setupSubscriptions` call it would inherit the contract | it does *not* call it today (only 2 production call sites, §1.2) | `live_desync_resync_test.go`, `live_sse_handshake_test.go` in leg (b); G-2 assertion would panic if a future resync path called it unlocked |
| R2 | **`dispatchBatched`** (`live.go:4680`) — batched events now run `ensureReactiveStarted` with a model parameter | model is read at `live.go:5492` under the caller's lock at `live.go:4681`; strictly tighter than the old post-unlock read | `TestDispatchBatched_*`, `Test_MarshalOutsideLock_DispatchBatched_Snapshot` |
| R3 | **`handleEvent`** (`live.go:4355`) | same as R2, lock at `live.go:4446` | `live_protocol_test.go`, `live_dispatch_noop_test.go` |
| R4 | **`Time.every` tick** (`live.go:5571-5653`) calls `dispatch` → `setupSubscriptions` → the hook every interval, now under `sess.mu` | the once-only gate (`bluedb_reactive.go:136-147`) short-circuits after the first tick, so steady-state cost is one map lookup under `reactiveStateMu` | `TestEveryGoroutine_exitsOnDelete`, `TestEveryGoroutine_exitsOnCleanupExpiry`, `live_sse_buffer_test.go` |
| R5 | **topic / stream / ws delivery** (`live.go:5845`, `live.go:6065`, `websocket.go:926`) | all already lock before `dispatch`; unchanged | `live_pubsub_dispatch_test.go`, `server_stream_test.go`, `websocket_test.go` |
| R6 | **The initial fill frame has never been delivered in a real app** — `reactiveRefreshOnce` pushes into `sess.sseCh` (cap `sseChanBuffer`, default 16) before the browser's SSE relay attaches | this hop is newly reachable; if the buffer is drained/reset at relay attach, the first fill is silently lost | T2 (§5.2) + leg (c) assertion 2. Treat a failure as a real second bug. |
| R7 | **`os.Exit(1)` from the boot gate now fires under `sess.mu` + `app.locker(sid)`** | a misconfigured prod deploy dies mid-request instead of mid-goroutine | intended (§3); `bluedb_reactive_gate_test.go` covers the pure decision matrix |
| R8 | **Reactive path bypasses `persistAndShipFrame`** (`bluedb_reactive.go:311`) — pre-existing, surfaced by this audit | a reactive Model mutation is acked to the client before the session is persisted (grill A1) | **not covered by any gate today**; the existing tripwire is vacuous (§4.3). Enters the no-deferral pipeline as a separate item. |
| R9 | **G-2 churn** — 51 test edits could mask a real behaviour change | a fixture wrapped in `sess.mu` might now serialise something it previously raced | ship G-2 as its own commit (§8 commit 4), diff-review each wrap, and require leg (b) green **before and after** |

**Out of scope for this fix, reported for the pipeline**: R8, and the two
tripwire defects in §4.3 (vacuous predicates + `live.go`-only scope while three
raw sends exist).

---

## 8. Commit plan

Additive, dependency-ordered, each independently revertable. Per AGENTS.md, the
failing test is the discovery artefact and lands **first**.

| # | Commit | Contents | Gate before moving on |
|---|---|---|---|
| **1** | `test(rt): regression — reactive handleInitial self-deadlock (G1)` | `runtime-go/rt/live_reactive_initial_render_test.go` with **T1 only**. **Lands RED.** | `timeout 900 go test ./rt/ -run TestHandleInitial_ReactiveApp_DoesNotDeadlock -count=1 -tags pebblegozstd -v` → **FAIL in 5 s** with the deadlock message. Confirms the artefact is real. |
| **2** | `fix(rt): thread model through the reactive hook — no sess.mu re-entry (G1)` | the §2.5 diff: `live_reactive_hooks.go:12`, `bluedb_reactive.go:41`, `bluedb_reactive.go:132-159` (incl. the single-evaluation hoist), `live.go:5492`, and the `setupSubscriptions` lock-contract doc block at `live.go:5472`. **`live.go:4176` untouched.** | leg (a) + leg (b) green (§6.1, §6.2). |
| **3** | `test(rt): reactive live delivery through the real initial render (G1)` | **T2** added to the same file; the §4.3 tripwire in `live_reactive_lock_tripwire_test.go`. | leg (a) green; leg (c) run once here (§6.3) — this is the "2-browser demo" gate Phase-4 never actually met. |
| **4** | `test(rt): enforce the sess.mu lock contract (assertSessMuHeld)` | `live_lock_contract.go` + `live_lock_contract_enable_test.go`; `assertSessMuHeld` calls at `live.go:5472` and `live.go:4814`; the 51 mechanical lock-wraps across the 12 test files in §4.2. | full leg (b) green. Independently revertable if review judges the churn too large — but see R9/§4.2 on why it should ship. |
| **5** | `docs(bluedb): G1 reactive-deadlock design + Phase-4 gate postmortem` | this file, plus a `docs/bluedb/phase4-grill-findings.md` note that the Phase-4 "2-browser live demo" gate was never met on this code and is now discharged by T2 + leg (c). | — |

**Push discipline** (CLAUDE.md §0.1): commits 1-5 land locally; push **once**
after a Judge verifies commit 3's leg (c) — that is the milestone. Commit 4 may
ride the same push or a second one if its churn needs its own review.

**Answer to "what test would have caught it"**: **T1** — the moment any test
drove `handleInitial` (or `dispatchRoot`) on an app with
`reactiveBindings != nil` under a timeout, Phase-4b could not have shipped
green. The existing `live_reactive_test.go` / `live_reactive_delivery_test.go`
construct `liveSession` structs by hand and call `app.reactiveLoop` directly,
entirely below the layer that deadlocks; and `live_nav_mirror_test.go` drives
`handleInitial` correctly but with `reactiveBindings == nil`. The bug lives in
the intersection of the two fixtures, and no test occupied it.
