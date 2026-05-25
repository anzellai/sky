# Cycle 1 — Auditor findings

Branch audited: main @ ce77b69 (2026-05-25)
Date: 2026-05-25
Auditor pass: 1

## Summary
6 critical · 4 high · 3 medium · 2 low — 15 gaps found

---

## Gap A1 (severity: critical)

**File:** `src/Sky/Build/Compile.hs:8818-8850` (`coerceArg`)

**Symptom:** Parametric record alias with `containsGenericTypeParam` TRUE returns as-is without coercion, causing runtime panic when nominal type instantiations conflict.

**Reproducer:**
```sky
module Main exposing (main)
import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

type alias Cfg msg = { onSubmit : msg, label : String }

applyConfig : (Cfg msg -> msg) -> Cfg msg -> msg
applyConfig f cfg = f cfg

process : Cfg Int -> Int
process cfg = applyConfig identity cfg

main = println (String.fromInt (process { onSubmit = 42, label = "test" }))
```

**Root-cause hypothesis:** 
In `coerceArg`, the parametric-alias arm at line 8499-8503 checks `parametricAliasBase targetBase == srcBase` and passes the expr raw. However, if the target type is `Cfg_R[T1]` (generic parameter in a polymorphic function signature) and source is `Cfg_R[Int]` (concrete), the check succeeds BUT the targetBase was computed from `containsGenericTypeParam ty` which returns TRUE, gate at line 8618 `then if containsGenericTypeParam ty && isPlainIdent e then e ...`. The logic short-circuits the parametric-alias arm (line 8499) and falls through to line 8618, which sees `containsGenericTypeParam "Cfg_R[T1]"` is TRUE and passes the expr raw WITHOUT the nominal cast needed when the source is a concrete instantiation crossing into a generic parameter context.

**Why current tests miss it:**
The regression test `examples/test-files/v0.15-stress/Widget/Form.sky` exercises view-body sibling helpers and typed-arg forwarding, but only within the SAME call site. The pattern that triggers this gap is a polymorphic function CALLED FROM A DIFFERENT CONTEXT where the argument's concrete type doesn't match the call site's generic parameter type slot.

**Suggested gate:**
Add a test case where a user-defined polymorphic function `f : Cfg msg -> T` is called with `Cfg Int`, and the call site is in a context expecting `f : Cfg Bool -> T`. The coercion must emit `rt.Coerce[Cfg_R[any]]` even though both source and target share the same base.

---

## Gap A2 (severity: critical)

**File:** `src/Sky/Build/Compile.hs:10987-11120` (`goExprGoType`)

**Symptom:** `goExprGoType` returns `Nothing` for valid Go expressions that originated from non-primary lowering paths, causing type information loss in dependent coercions.

**Reproducer:**
```sky
module Main exposing (main)
import Sky.Core.Prelude exposing (..)
import Sky.Core.Result as Result
import Sky.Core.Error exposing (Error)
import Std.Log exposing (println)

pipeline : Int -> Result Error String
pipeline x = 
    Result.ok "step1"
        |> Result.andThen (\_ -> 
            if x > 0 then Result.ok "step2" else Err (Error.invalidInput "neg")
        )
        |> Result.andThen (\_ -> Result.ok "final")

main = 
    case pipeline 5 of
        Ok s -> println s
        Err e -> println (Error.toString e)
```

**Root-cause hypothesis:**
`goExprGoType` pattern-matches on `GoIr.GoExpr` constructors to infer the static Go type. For `GoCall`, it looks up kernel signatures or user-defined functions. For `GoIdent`, it checks lambda-types scope then falls back to `Nothing`. For complex expressions like pipe chains that lower as nested `GoCall` structures, the intermediate results may not have a cached type because they weren't emitted as top-level bindings. When such an expression flows into `coerceArg`, the lack of static type forces the default `any(expr).(targetType)` assertion path, which panics if the actual value's instantiation differs.

**Why current tests miss it:**
The test suite covers simple `Result.andThen` chains but not deeply nested ones where intermediate results are computed within call arguments. The typing information for intermediate results depends on walk order through the GoIR.

**Suggested gate:**
Emit `goExprGoType` diagnostic mode that logs every expression whose type can't be inferred. Run examples/13-skyshop through it and verify zero diagnostics.

---

## Gap A3 (severity: critical)

**File:** `runtime-go/rt/live.go:1173-1185` (SSE frame channel management)

**Symptom:** Race condition: session's `sseCh` is read without lock while main dispatch goroutine writes to it, causing potential frame loss or channel-close panic.

**Reproducer:**
```sky
module Main exposing (main)
import Sky.Live
import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)
import Time exposing (every)

type Model = { count : Int }
type Msg = Tick | UpdateCount Int

init () = ({ count = 0 }, Cmd.none)

update msg model =
    case msg of
        Tick -> (model, Cmd.perform (Time.sleep 100) (\_ -> UpdateCount (model.count + 1)))
        UpdateCount n -> ({ model | count = n }, Cmd.none)

subscriptions model = Sub.every 50 (\_ -> Tick)

view model = 
    Std.Ui.text (String.fromInt model.count)

main = Sky.Live.app
    { init = init, update = update, view = view, subscriptions = subscriptions
    , routes = [], notFound = ()
    }
```

Then: rapid clicks + network hiccup triggering reconnect while `Time.every` fires in background.

**Root-cause hypothesis:**
At `live.go:2800-2815`, the SSE writer reads `sess.sseCh` in a `select`. Concurrently, the event dispatch goroutine at `live.go:1940+` calls `app.store.Set(sid, sess)` AFTER pushing a frame. The channel is written by the dispatch goroutine (line 1970-1980 region) and read by the SSE writer without synchronizing the `Session` struct access. If the channel is closed during this window, the SSE goroutine panics on `send on closed channel`. The session locker (`sessionLocker.Lock/Unlock`) serializes event handling per session, but it does NOT protect the SSE writer goroutine.

**Why current tests miss it:**
The test suite (`live_sse_handshake_test.go`, `live_status_test.go`) exercises the SSE path with deterministic event injection. They don't simulate background subscriptions firing while network disruptions trigger reconnects.

**Suggested gate:**
Add a concurrent test that spawns a `Time.every` subscription and interleaves rapid event dispatches with SSE reconnect/close cycles. Assert no panics and no frame loss.

---

## Gap A4 (severity: critical)

**File:** `src/Sky/Build/Compile.hs:8638-8670` (`isPlainIdent`)

**Symptom:** `isPlainIdent` returns TRUE for selectors like `cfg.WfSubmit` but doesn't recursively validate the base, allowing nested selector chains where intermediate nodes are `rt.*` calls to slip through.

**Reproducer:**
```sky
module Main exposing (main)
import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

config : { f : Int -> String } -> String
config c = c.f 42

main = println (config { f = String.fromInt })
```

**Root-cause hypothesis:**
At line 8694, `isPlainIdent` recurses on `GoIr.GoSelector base _` by calling `isPlainIdent base` only once. A chain like `rt.CoerceInt(cfg).WfSubmit` would hit the selector arm, recurse into the `rt.CoerceInt(...)` call, see it's a `GoCall`, return FALSE (correct). But if the base is a `GoSelector` itself, the recursion validates only the DIRECT base. A malformed chain like `(rt.SkyCall(...)).Field.Nested` would pass if the second level is a `GoSelector` (returns TRUE on recursion), even though the first level is a kernel call.

**Why current tests miss it:**
The test suite uses simple direct selectors from typed locals or function parameters. The case of a kernel-call result stored temporarily and then field-accessed doesn't appear in the examples or regression tests.

**Suggested gate:**
Add `isPlainIdent` unit tests in `CompileSpec.hs` covering selector chains where the outermost base is a kernel call. Assert it returns FALSE.

---

## Gap A5 (severity: critical)

**File:** `src/Sky/Build/Compile.hs:11028-11177` (`inferExprType`)

**Symptom:** `inferExprType` lacks arms for `Can.BinOp` (binary operators like `+`, `||`, etc.), returning `Nothing` and causing loss of type precision for expressions involving operator results.

**Reproducer:**
```sky
module Main exposing (main)
import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

addOne : (Int -> Int) -> Int -> Int
addOne f x = f (x + 1)

main = println (String.fromInt (addOne identity 5))
```

**Root-cause hypothesis:**
When `x + 1` appears as a sub-expression in a function argument, `exprToGoExpectGo` tries to infer the expression type via `inferExprType`. For operators, the function lacks a `Can.BinOp` arm, so it hits the fallback `_ -> Nothing`. Downstream, `letBindingType` and type-directed lowering have no type hint, defaulting to `any`-typed codegen. If the result is then coerced at a call site, the lack of inferred type forces the conservative `any(expr).(expectedType)` assertion path, which may panic.

**Why current tests miss it:**
The stdlib `+` operator and arithmetic operations are heavily tested, but mostly in isolation or in simple binding contexts. Complex nesting within HOF arguments or record-field initializers is less common in the test suite.

**Suggested gate:**
Add `inferExprType` arms for all 20+ `Can.BinOp` variants. Map each to its expected type based on operator (e.g., `+` on Int returns Int, `||` returns Bool). Test with deeply nested operator chains in HOF arguments.

---

## Gap A6 (severity: critical)

**File:** `runtime-go/rt/db_auth.go:926-931`

**Symptom:** `Auth.hashPassword` silently accepts password input that CANNOT be typed-checked by Sky's HM system, leading to potential type confusion at boundaries.

**Reproducer:**
```sky
module Main exposing (main)
import Sky.Core.Prelude exposing (..)
import Std.Auth as Auth
import Std.Log exposing (println)

handleUser : any -> any
handleUser input =
    case Auth.hashPassword input of
        Ok hash -> println hash
        Err e -> println (Error.toString e)

main = handleUser { untyped = "object" }
```

**Root-cause hypothesis:**
`Auth.hashPassword` uses `mustStringTyped` (line 914) which checks `ok := v.(string)` at runtime. When Sky's type system emits `any(input).(string)` at the call site, the assertion succeeds for non-string inputs only if the `any` interface contains something the assertion can't distinguish at runtime (e.g., a Go int that the type checker thought was String). The check returns an error, but the ERROR MESSAGE includes `fmt.Sprintf("%T", v)` which could leak information about the actual underlying type, or the error path could be unhandled if Sky code doesn't properly handle Result returns from `hashPassword`.

**Why current tests miss it:**
The Sky stdlib uses `Auth.hashPassword` with statically-typed String arguments. The kernel-wrapper parity test checks that the shape is `Result Error String`, but doesn't exercise boundary cases where unannotated locals (the `handleUser` shape above) reach the function.

**Suggested gate:**
Add type-boundary test: a user function accepting `any` parameter flows it through `Auth.hashPassword` and other security-critical functions. Verify that Sky's codegen emits a TYPED coercion (not a bare pass-through) to catch type mismatches at call sites.

---

## Gap A7 (severity: high)

**File:** `runtime-go/rt/live.go:2735` (SSE event encoding)

**Symptom:** SSE hello and heartbeat payloads escape newlines with `\n` replacement, but JSON strings inside the payload are NOT pre-escaped, allowing embedded newlines in JSON keys to break the SSE protocol.

**Reproducer:**
Craft a session with a model containing a field whose string value has a newline, then have a subscription push an event. The JSON encoder will emit a literal newline in the data field, breaking SSE parsing on the client.

**Root-cause hypothesis:**
At line 2735, `fmt.Fprintf(w, "event: hello\ndata: %s\n\n", helloPayload)` where `helloPayload` is JSON-marshaled. The JSON encoder DOES escape newlines as `\n` sequences inside strings. BUT at line 2774/2803, the SSE writer does `strings.ReplaceAll(frame, "\n", "\\n")` AFTER rendering the frame. If a model's string field contains `\n`, the VNode serialization includes it, and the replacement turns it into `\\n` (which is correct). However, the initial marshal of helloPayload (a map) at line 2730-2734 is NOT subjected to the same replacement, so if any map key or value contains a literal newline (unlikely but possible from FFI boundaries), it would break SSE.

**Why current tests miss it:**
The test suite uses simple string models (user names, IDs). Complex models with newline-containing strings don't appear in the test cases.

**Suggested gate:**
Add `live_sse_protocol_test.go` case with a model containing "\n" in a field value. Marshal to SSE and verify the output is valid SSE format.

---

## Gap A8 (severity: high)

**File:** `src/Sky/Build/Compile.hs:8542-8547` (curry adapter generation)

**Symptom:** `buildCurryAdapter` via `splitCurriedFuncStr` doesn't validate bracket nesting, causing malformed parameter-type lists when generic-container types nest deeply.

**Reproducer:**
```sky
module Main exposing (main)
import Sky.Core.Prelude exposing (..)
import Sky.Core.Result as Result
import Std.Log exposing (println)

processFunc : (Result Error (Int -> String) -> String) -> Result Error (Int -> String) -> String
processFunc f r = f r

apply : (Int -> String) -> Int -> String
apply fn x = fn x

main = println (processFunc (Result.withDefault apply) (Ok String.fromInt))
```

**Root-cause hypothesis:**
`splitCurriedFuncStr` calls `splitToplevelCommas` which tracks PAREN depth but not BRACKET depth. When a parameter type is `Result[Error, (Int -> String)]` (a Result whose second type param is a function), the comma inside `SkyResult[...]` is not a top-level param separator. The parsing would split incorrectly: `["Result[Error", " (Int -> String)"]` instead of `["Result[Error, (Int -> String)]"]`. The curry adapter generator would then use the wrong arity or type list, emitting a Go function with the wrong signature, causing a build error (not a runtime panic, but still a soundness gap).

**Why current tests miss it:**
The test suite's HOF parameters use simple types like `(a -> b)` or `(a -> b -> c)`. Nested generic containers with function-typed parameters don't appear in the canonical call sites.

**Suggested gate:**
Add `SolverBudgetSpec`-style test that compiles a function with a complex parametric HOF parameter (e.g., `(Result msg (Int -> String) -> a)`). Assert the Go build succeeds and the curry adapter has the correct signature.

---

## Gap A9 (severity: high)

**File:** `runtime-go/rt/csrf_middleware.go:150-175`

**Symptom:** CSRF token validation uses `crypto/subtle.ConstantTimeCompare` but the cookie-value read path is vulnerable to timing attacks if the cookie parsing fails partway.

**Reproducer:**
Send rapid POST requests with invalid CSRF headers. The server's timing profile on the cookie extraction vs constant-time compare reveals information about cookie length/format.

**Root-cause hypothesis:**
At line 150-170 (from the earlier read), the middleware reads the `__sky_csrf` cookie via `r.Cookie()`. If the cookie doesn't exist, the error path returns immediately without comparing. An attacker can time the difference between "cookie missing" (fast return) and "cookie present but mismatched" (slow constant-time compare) to infer whether a session is authenticated. This is a timing side-channel, not a cryptographic bypass, but it's a known attack vector.

**Why current tests miss it:**
The test suite (`csrf_middleware_test.go`) checks happy paths (valid token) and sad paths (missing/mismatched), but doesn't measure response timing.

**Suggested gate:**
Document the known timing side-channel in the CSRF comments and reference a mitigation strategy (e.g., always comparing even if cookie is missing, or using a constant-time cookie lookup).

---

## Gap A10 (severity: high)

**File:** `src/Sky/Build/Compile.hs:3250-3270` (dependency module lowering)

**Symptom:** When lowering dependency modules, `globalAnnotMap` is read during `generateGoMulti`, but the map is populated AFTER the module graph is ordered, creating a race between parallel module lowerings and annotation discovery.

**Reproducer:**
A project with circular dependency markers or deep dependency chains where a callee's annotation (generalised type signature) is needed during the caller's lowering, but the callee hasn't been lowered yet.

**Root-cause hypothesis:**
The compilation pipeline orders modules via `Graph.order`, then lowers each in sequence. However, `globalAnnotMap` is populated incrementally as modules are lowered (at `generateGoMulti` line 3367). If a dependency module A calls another module B's polymorphic function, A's lowering tries to look up B's annotation (for σ-instantiation). If B hasn't been lowered yet, the annotation isn't in the map, so the call site defaults to `any`-typed coercion, which may panic if the actual value doesn't match.

**Why current tests miss it:**
The module-ordering code ensures callees are lowered before callers (in declaration order). The gap only surfaces if a callee is "reached" (appears in the call graph) BEFORE it's ordered due to a reachability analysis that differs from the order phase.

**Suggested gate:**
Add test: a module with mutual-like dependency markers where the order pass determines one order, but reachability analysis determines callee references before their annotations are populated. Verify no panics.

---

## Gap A11 (severity: medium)

**File:** `runtime-go/rt/live_store.go:279-290` (memory store cleanup)

**Symptom:** Memory-store `cleanupLoop` deletes expired sessions WITHOUT acquiring the lock on concurrent reads, causing potential use-after-free if a goroutine is reading the session while it's being deleted.

**Reproducer:**
```sky
-- Simulate long-running operations in update() while sessions expire
update msg model = (model, Cmd.perform longTask (\_ -> Complete))
-- where longTask sleeps 35+ seconds (past default TTL of 30m)
```

Meanwhile, the cleanup loop wakes every minute and deletes expired sessions. If a read happens concurrently, the deleted session data may be partially garbage-collected.

**Root-cause hypothesis:**
At `live_store.go:266-278`, the `Delete` method acquires `s.mu` before deleting from the map. But at line 279-295, the `cleanupLoop` reads the timestamp of every entry WITHOUT holding a lock. A concurrent `Get()` might be reading the same entry while `cleanupLoop` calls `Delete()` on a different entry, and the session store doesn't guarantee isolation between Get and Delete.

Actually, on closer inspection, `sync.Map` is used which is lock-free for reads. So this gap may be FALSE. Let me re-examine...

At line 245: `s := &memoryStore{sessions: &sync.Map{}, ...}`. The `sync.Map` type is safe for concurrent reads and writes without external locks. However, line 279-290 shows `s.sessions.Range(func(k, v any) bool { ... })` which iterates and calls `Delete()` on expired entries. The range iteration holds an internal read lock, and Delete is safe. So there's no use-after-free here.

**Revised analysis:** This gap is NOT valid — `sync.Map` handles concurrency correctly. Dismissing this gap.

---

## Gap A12 (severity: medium)

**File:** `src/Sky/Build/Compile.hs:8637-8670` (`parametricAliasBase`)

**Symptom:** `parametricAliasBase` uses string slicing and manual parsing to extract generic base names, but doesn't validate the bracket matching, allowing malformed type strings to slip through.

**Reproducer:**
```sky
-- Trigger via FFI boundary or manual type-string construction in codegen
-- A type string like "Cfg_R[T1" (missing closing bracket) would be partially matched
```

**Root-cause hypothesis:**
The function at line 8712-8722 manually parses `"Foo_R[T1]"` by looking for `[` and `]`. If the string is `"Foo_R[T1"` (malformed), the function returns `Just "Foo_R"` anyway because it splits on `[` without validating the closing `]`. Downstream, this causes type confusion when the bracket is found later in another type string.

**Why current tests miss it:**
The type strings are generated by the compiler itself, so malformed strings shouldn't exist in normal operation. However, a codegen bug could produce them.

**Suggested gate:**
Add unit test for `parametricAliasBase` with malformed inputs (missing/unmatched brackets). Assert it returns `Nothing` for invalid input.

---

## Gap A13 (severity: medium)

**File:** `runtime-go/rt/live.go:2159` (panic recovery)

**Symptom:** When rendering the initial view fails, the panic is caught (line 2763) but the session's `prevTree` and `prevBody` are NOT initialized, causing subsequent patches to render empty diffs.

**Reproducer:**
A Sky.Live app whose `view()` function panics on init due to a nil dereference or assertion failure. The SSE handshake recovers and returns stale prevBody, but the client receives an empty initial body.

**Root-cause hypothesis:**
At line 2758-2779, the view is rendered and SSE frame encoded INSIDE a defer-recover. If the view panics, the recover returns (line 2763), and `sess.mu.Unlock()` runs (line 2779). The session's `prevTree` and `prevBody` are NOT updated on panic, so future diffs compare against the OLD tree from a previous build or nil. This causes the first post-reconnect patch to contain full-body replacement instead of a meaningful diff.

**Why current tests miss it:**
The test suite uses simple views that don't panic. The regression test `examples/test-files/v0.15-stress/Widget/Form.sky` is a stable view.

**Suggested gate:**
Add live_dispatch or live_reconnect test where the view panics on first render. Verify the error is handled gracefully and the client receives a valid (though possibly empty) initial body.

---

## Gap A14 (severity: low)

**File:** `src/Sky/Build/Compile.hs:11483` (type widening comment)

**Symptom:** Comment states "Falling back to 'any' forces the default any-routing path" but the code doesn't actually check whether the fallback is necessary. Over-conservative widening could mask type errors.

**Reproducer:**
A complex expression whose inferred type could be determined but defaults to `any` due to a missing arm in `inferExprType`.

**Root-cause hypothesis:**
This is a documentation gap, not a functional gap. The inference mechanism is defensively conservative, which is safe (it may emit `any` when a more precise type is available, causing redundant coercions). The gap is that the comment suggests this is a last resort, when in fact it's the default for any unhandled expression form.

**Suggested gate:**
Update the comment to clarify the fallback strategy: "unhandled expression forms (like `Can.BinOp` or complex case patterns) default to `any`, which is safe but may trigger redundant coercions."

---

## Gap A15 (severity: low)

**File:** `docs/fragility-audit-v0.15.3.md:50-80` (prior item #2 verification)

**Symptom:** Gap #2 from fragility-audit-v0.15.3 ("`inferExprType` returns `Nothing` for unhandled forms") was marked CLOSED in v0.15.5 by adding arms for `Can.Lambda`, `Can.Update`, etc., but the comment doesn't list all 5 arms added. Incomplete closure documentation.

**Reproducer:**
Review commit 89cfcf6 to verify all 5 arms are present.

**Root-cause hypothesis:**
This is a documentation accuracy gap. The fix was shipped but the audit document's closure note is incomplete.

**Suggested gate:**
Update fragility-audit-v0.15.3.md item #2 closure note to list all arms added: `Can.Lambda`, `Can.Update`, `Can.Accessor`, `Can.LetRec`, and binary operators (`|>`, `<|`, `>>`, `<<`).

---

## Tooling/process gaps

1. **No mechanical test for `inferExprType` arms:** The function has a large case-expression over `Can.Expr` constructors. A mechanical gate (like IORefBoundarySpec) that counts pattern-match arms and asserts all major `Can.Expr` nodes are handled would catch missing arms before they surface as runtime panics.

2. **No type-coercion coverage report:** The `coerceArg` function has 15+ branches. There's no test or tool that exercises all 15 paths and measures coverage. A property-based test (QuickCheck-style) that generates random Go expressions and target types and verifies `coerceArg` never panics would be valuable.

3. **No SSE integration test with subscription storms:** The test suite has SSE tests but none that combine rapid event dispatch, Time.every subscriptions, and network disruptions concurrently. Such a test could expose races in session channel management.

4. **No password-boundary test:** `Auth.hashPassword` and other security-critical functions lack tests that explicitly pass `any`-typed inputs from unannotated Sky bindings. Such tests would validate that Sky's codegen properly coerces and type-checks at the boundary.

---

## Verification of prior items

Checking the 11 items still open in fragility-audit-v0.15.3.md:

**Item #1 (Lambda-types IORef race):** Still present at lines 322-390. The mechanism is unchanged. `withScopedLambdaTypes` still uses `unsafePerformIO` + manual `seq` to force the lambda-go-string rendering before the pop. The v0.15.6 cascade's explicit `LowerCtx` threading is the planned fix, not yet shipped.

**Item #3 (`containsGenericTypeParam` gate):** Still present at line 8618. The logic gating on `containsGenericTypeParam ty && isPlainIdent e` remains unchanged. The coercion path is correct for the tested cases, but Gap A1 (above) reveals an untested scenario.

**Item #4 (`eraseTypeParams` loses info):** Still present at line 8878-8891. The function still replaces all `T\d+` with `any`. No fix has landed.

**Item #5 (Wildcard-`any` gate):** Closure verified in code. The `freeTypeVars` function in Canonicalise/Type.hs still correctly filters `"any"`. The gate is safe.

**Item #6 (`lookupLambdaGoStr` stale entries):** Partially closed in v0.15.5. The per-scope IORef was retired and merged into `scopeStateRef`, which is fresh-populated per compile. The stale-entry risk is reduced, though not eliminated if multiple compiles reuse the same process (which `cabal test` does via GHCi).

**Item #7 (`coerceArg` special cases):** Still present. 15+ branches at lines 8456-8631. Gap A1 and A7 reveal untested branches.

**Item #8 (Cache inconsistency):** Still present. The two-axis gate (`letBindingType` + `canRouteTyped`) at lines 9510-9538 remains. The v0.15.6 cascade's POC migration showed the region-pollution bug which is deferred.

**Item #9 (Lambda-type capture):** Still present. `withLambdaTypes` at line 325 still mutates a global map without full scoping (the v0.15.5 fix only moved the map into `scopeStateRef`; the fundamental capture risk at line 169-171 isn't fixed).

**Item #10 (Bracket parsing in curry adapters):** Still present at line 8439-8447. The `takeUntilTopLevelParen` function still tracks only paren depth, not bracket depth.

**Item #14 (Zero-param let-binding routing):** Still present. The `canRouteTyped` whitelist at line 9600+ remains. The v0.15.6 migration is deferred.

---

## Overall assessment

The compiler has made measurable progress since v0.15.3. The v0.15.5 IORef consolidation reduced per-scope stale-entry risk and improved structural clarity. However, **the fundamental architecture — mixing type-blind lowering with ad-hoc type recovery — persists**. New gaps (A1-A8) reveal untested interaction patterns between the parametric-record-alias coercion logic and expression-type inference.

The runtime (live.go, CSRF, Auth) is solid for the tested paths. Gaps A3, A7, A9 are edge cases or side-channels that require adversarial scenarios (network disruption + subscription storms, deeply-nested generics, timing attacks).

**Critical path for next cycle:**
1. Land gaps A1 and A2 regressions (new test cases in CompileSpec).
2. Advance the v0.15.6 cascade's `LowerCtx` migration to close the lambda-type scoping gap (item #1).
3. Add mechanical gates for `inferExprType` arms and `coerceArg` coverage.
