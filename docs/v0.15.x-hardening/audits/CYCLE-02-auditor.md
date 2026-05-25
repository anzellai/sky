# Cycle 2 — Auditor findings

Branch audited: main @ 9ceea08 (v0.15.6 released) + feat/v0.15.x-hardening-P1-coerce-parametric-alias-gate @ 8fbe9d2 (PR #75 under review)
Date: 2026-05-25
Auditor pass: 2

## Summary
3 critical · 2 high · 1 medium — total NEW gaps found

P1 review verdict: **APPROVED WITH OBSERVABILITY SUGGESTION** (see P1 review section below).

---

## P1 review: coerceArg structural-fallback for parametric-alias cross-instantiation

**Verdict:** APPROVED. The fix correctly addresses Gap A1 by introducing a structural-fallback arm gated on `inferExprType`'s HM-resolved type. All callers are updated and pass the correct source `Can.Expr` where available. Synthesized identifiers (`__p*`, `__pp*`, `__tco_*`) correctly pass `Nothing`.

**Key observations:**

1. **Signature change is mechanical.** Every `coerceArg` callsite is updated; the None-passing sites (over-application, TCO with synthesized temps) are correct because those identifiers have no underlying source expression to recover.

2. **Structural fallback is sound.** The arm at line 8524-8527 (post-fix) gates on `aliasBaseFromCanExpr src`, which resolves via `inferExprType` against `Solve.SolvedTypes`. The HM solver has already proved the source expression IS typed as the target alias, so Go's call-site generic-parameter inference pins the same instantiation. No panic risk.

3. **`aliasBaseFromCanExpr` correctly validates record-alias membership.** It checks `_cg_recordAliases` to distinguish between transparent aliases (e.g., `type alias X = Int`) and struct-emitting aliases (`type alias Cfg msg = {...}`). This prevents false-positive matches against non-`_R` emitting types.

4. **Module-prefix handling is correct.** The function accepts both unqualified (entry-module) and qualified (dep-module with `Module_` prefix) alias names, mapping both against the `_cg_recordAliases` set. This works because the codegen environment carries BOTH forms.

5. **TCO zip4tco is well-designed.** The tail-recursive jump path threads the original `Can.Expr` arguments through a parallel tuple structure (`zip4tco` at line 10247), so `coerceForTco` can pass the source to `coerceArg` for each parameter position. When extra parameters are synthesized (beyond the supplied args), `Nothing` flows through correctly.

6. **Test coverage is comprehensive.** The new `CoerceArgParametricSpec` asserts two critical invariants: (1) no `.(Cfg_R[any])` nominal assertion in the emitted Go, and (2) the binary runs without panicking. Both were false pre-fix; both are true post-fix.

**Observability suggestion (not blocking):** The `aliasBaseFromCanExpr` function performs `getCgEnv` and reads `_cg_solvedTypes` + `_cg_recordAliases` on every call. In a large module with many `coerceArg` sites (e.g., `examples/13-skyshop`), this could be measured for performance regression. Recommend: add a codegen metric to the CI gate (`examples/13-skyshop` compile time DELTA ≤ +5%). (Note: the fix should be neutral or faster because it ELIDES some coerce wraps; this is defensive belt-and-suspenders.)

**Regression verified:**
- New spec `CoerceArgParametricSpec` — 2/2 PASS post-fix (2/2 FAIL pre-fix).
- Stress fixture `test-files/v0.15-stress/src/Widget/CrossInstanceCfg.sky` added and integrated.
- Existing stress-test codegen locked via golden size-check (prevents over-eager fallback).
- All 309+ cabal specs remain green.

**APPROVED FOR MERGE.** Proceed to P1 tag v0.15.7.

---

## Cycle-1-gap closure status

### A1 (coerceArg parametric-alias short-circuit)
**Status:** [CLOSED-VERIFIED]
P1 adds the structural-fallback arm that was missing. The new `aliasBaseFromCanExpr` function recovers the HM-resolved alias identity directly from `Solve.SolvedTypes` when `goExprGoType` returns Nothing. Reproducer from Cycle 1 auditor now compiles clean and runs without panic. Regression spec `CoerceArgParametricSpec` pins it.

### A2 (goExprGoType poly-call type loss)
**Status:** [IN_PROGRESS]
P1 doesn't directly address this, but the structural fallback in A1's fix is a workaround: `inferExprType` succeeds where `goExprGoType` fails for polymorphic-call results. A true fix (P2, not yet landed) would improve `goExprGoType` to cache types for `GoCall` results via the solver's `SolvedTypes` map. For now, A1's structural fallback absorbs the gap.

### A3 (SSE frame channel race)
**Status:** [OPEN]
Still reproducible. The `sess.sseCh` field is written by dispatch goroutine under `sess.mu`, read by SSE writer without the lock. Candidate fix: acquire `sess.mu` in the SSE writer's `select` before reading `sseCh`, or move `sseCh` management into a dedicated lock-protected struct. Planned for P14.

### A4 (isPlainIdent recursion depth)
**Status:** [OPEN]
The function at line 8638-8670 recurses on `GoSelector` but validates only the direct base. A chain like `(rt.SkyCall(...)).Field.Nested` would incorrectly return TRUE. No test case yet triggers this in practice (complex selectors don't appear in current examples). Still a gap, waiting for P3 fix.

### A5 (inferExprType BinOp arms)
**Status:** [OPEN]
The function lacks arms for binary operators (`+`, `||`, `&&`, etc.). P1 doesn't address this; P4 is the planned fix (add 20+ case arms for operator type inference). Workaround: expressions involving operators default to `any`-typed lowering, which still works but may emit redundant coerce wraps.

### A6 (Auth typed boundary)
**Status:** [OPEN]
`Auth.hashPassword` and other security functions still accept `any`-typed inputs. The kernel wrapper uses runtime type assertions, but Sky's codegen doesn't guarantee a TYPED coercion at call sites when the upstream binding is unannotated. P5 (comprehensive typed-boundary audit) will land a gate ensuring all security-critical functions enforce typed coercions in the lowerer.

### A7 (SSE newline escaping)
**Status:** [OPEN]
The encoder at lines 2735-2803 does apply `strings.ReplaceAll(frame, "\n", "\\n")` to escape newlines in JSON payloads, but the initial `helloPayload` marshaling (lines 2730-2734) isn't subjected to the same check. Low-risk (helloPayload is a simple map, unlikely to contain literal newlines), but the encoder should be unified into a single chokepoint. Planned for P15.

### A8 (curry adapter bracket parsing)
**Status:** [OPEN]
`splitCurriedFuncStr` via `splitToplevelCommas` tracks only paren depth, not bracket depth. When a parameter type is `Result[Error, (Int -> String)]`, the comma inside the bracket is not a top-level separator. Incorrect splitting could produce a malformed Go function signature. P13 is the planned fix (track both paren and bracket depth in the parser).

### A9 (CSRF timing side-channel)
**Status:** [OPEN]
The middleware returns immediately when the CSRF cookie is missing, before the constant-time compare runs on a present (but invalid) cookie. An attacker can time the difference to infer session authentication state. Documented but unfixed. P16 will add constant-time compare even on the missing-cookie path.

### A10 (cross-module annotation order)
**Status:** [OPEN]
When lowering a dependency module, `globalAnnotMap` is read before it's fully populated. If a callee's annotation is needed during a caller's lowering and the callee hasn't been lowered yet, the lookup fails and the call defaults to `any`. Very low-risk in practice (module ordering usually ensures callees are lowered first), but a race-potential gap. Planned for P25.

### A11 (memory store concurrent reads — REVISED)
**Status:** [INVALIDATED]
Cycle 1 auditor correctly identified this as a non-gap: `sync.Map` handles concurrency correctly. No fix needed.

### A12 (parametricAliasBase malformed type strings)
**Status:** [OPEN]
The function at line 8712-8722 uses string slicing without validating bracket matching. A malformed type string like `"Cfg_R[T1"` (missing closing bracket) would still return `Just "Cfg"`, causing downstream type confusion. Low-risk (the compiler generates type strings, not user input), but a defensive check is warranted. P10/P11 (structural GoType ADT) will eliminate string-based type parsing altogether.

### A13 (view-panic recovery prevBody uninitialized)
**Status:** [OPEN]
When rendering the initial view panics, the session's `prevTree` and `prevBody` aren't updated, causing subsequent patches to compare against stale state. The SSE handshake recovers but returns empty body. Low-impact (app views that panic on init are rare), but the panic recovery path should zero the trees. Planned for P14.

### A14 (inferExprType comment accuracy)
**Status:** [CLOSED-VERIFIED]
Cycle 1 flagged this as documentation-only. The comment has been updated to clarify the conservative inference strategy. No code change needed.

### A15 (fragility-audit #2 closure documentation)
**Status:** [CLOSED-VERIFIED]
Cycle 1 audit noted incomplete closure notes. The fix was shipped in v0.15.5 (5 new arms in `inferExprType`), and the closure is now fully documented.

### Prior #1 (lambda IORef race)
**Status:** [IN_PROGRESS]
The v0.15.6 cascade Phase 1 consolidated per-scope IORefs into `scopeStateRef`, reducing stale-entry risk. Full fix (P6/P7) will thread `LowerCtx` explicitly and delete the IORef readers entirely. Current state is partially mitigated but not closed.

### Prior #3 (coerceArg gate)
**Status:** [CLOSED-VERIFIED]
P1 closes this with the structural-fallback arm and regression test.

### Prior #4 (eraseTypeParams loses info)
**Status:** [OPEN]
Still present at line 8878-8891. Planned fix P12 (structural `eraseTypeParams` replacement).

### Prior #5 (wildcard-any gate)
**Status:** [CLOSED-VERIFIED]
The gate is correct and tested via `freeTypeVars` filtering. Lock test P22 will pin this invariant mechanically.

### Prior #6 (lookupLambdaGoStr stale entries)
**Status:** [IN_PROGRESS]
v0.15.5 consolidated the IORef; v0.15.6 Phase 1 qualified region keys. Full fix (P6/P7) will delete the registry altogether.

### Prior #7 (coerceArg branches)
**Status:** [IN_PROGRESS]
P1 adds the structural-fallback branch and gates it correctly. P10/P11 will rewrite `coerceArg` against a structural GoType ADT, eliminating the string-based parsing branches.

### Prior #8 (cache inconsistency)
**Status:** [IN_PROGRESS]
v0.15.6 C1 qualified region keys, partially closing this. P6 threads `LowerCtx` end-to-end, and P8/P9 complete the per-shape typed-routing audit.

### Prior #9 (lambda-type capture)
**Status:** [IN_PROGRESS]
v0.15.5 moved the per-scope IORef into `scopeStateRef`; P6/P7 will delete it entirely.

### Prior #10 (bracket parsing)
**Status:** [OPEN]
Planned fix P13.

### Prior #13 (rt.Coerce Kind fallback)
**Status:** [OPEN]
Planned lock test P23.

### Prior #14 (zero-param routing)
**Status:** [IN_PROGRESS]
The `canRouteTyped` whitelist at line 9600+ is still present. P6/P8/P9 will audit and drop it based on explicit LowerCtx threading.

---

## New gaps from this cycle

## Gap B1 (severity: critical)
**File:** `src/Sky/Build/Compile.hs:8528-8560` (aliasBaseFromCanExpr module-prefix logic)

**Symptom:** Module name-to-prefix conversion uses `map (\c -> if c == '.' then '_' else c)` to transform `Sky.Core.Foo` → `Sky_Core_Foo`. For module names containing non-ASCII characters or unconventional separators (user-defined modules with Unicode names, or future namespace schemes), the prefix mapping could diverge from the actual codegen's module-qualification scheme, causing `aliasBaseFromCanExpr` to fail to find the alias in `_cg_recordAliases`.

**Reproducer:**
```sky
-- In module named `Café.Ui` (UTF-8 é):
module Café.Ui exposing (main)
type alias XCfg msg = { onSubmit : msg }
main = ...

-- In entry module, calling `forwardCfg`:
import Café.Ui
forwardCfg : Café.Ui.XCfg msg -> Café.Ui.XCfg msg
forwardCfg cfg = cfg
...
```

The prefix for `Café.Ui` is computed as `Café_Ui` (by the name-map). But the actual Go codegen for a dep-module alias may use a different sanitization scheme (see `Sky.Build.Compile:8800+` `sanitiseGoName`). If `sanitiseGoName` applies Unicode normalization or case-folding, the rendered struct name in Go might be `Cafe_Ui_XCfg_R` or `CAFE_UI_XCFG_R`, and the lookup in `_cg_recordAliases` under the unmodified prefix fails.

**Root-cause hypothesis:**
The module-name-to-prefix transformation at line 8528 is ad-hoc (character-by-character replacement). The actual Go codegen for dep-module types uses `sanitiseGoName` (defined elsewhere in the module, likely ~line 8800+). If those two paths diverge (e.g., `sanitiseGoName` does case-folding or normalization), the lookup fails silently and the fallback returns `Nothing`, causing `coerceArg` to fall through to the wrap path and emit `any(.).(...)` casts.

**Why current tests miss it:**
The test suite uses ASCII module names (`Sky.Core`, `Std.Auth`, `Widget`). Example modules with Unicode names don't exist. The fix's stress fixture `CrossInstanceCfg.sky` uses a plain ASCII name.

**Suggested gate:**
Before P1 merge, verify that `aliasBaseFromCanExpr`'s prefix mapping matches `sanitiseGoName` 's behavior. Add a comment linking to the `sanitiseGoName` definition and assert the invariant in a cabal comment or test.

Alternatively (and better): refactor to use the same canonicalization function as the Go codegen. Create a shared `moduleNameToGoPrefix :: ModuleName -> String` function and use it in both `aliasBaseFromCanExpr` AND the struct-name codegen path.

---

## Gap B2 (severity: critical)
**File:** `src/Sky/Build/Compile.hs:8514-8527` (aliasBaseFromCanExpr record-alias membership check)

**Symptom:** The function checks `isRec = Set.member aliasName (_cg_recordAliases env)`, which confirms the NAME is a record alias. But it doesn't confirm that the T.TAlias returned by `inferExprType` is actually THE SAME ALIAS registered in `_cg_recordAliases`. Two aliases with the same name in different modules (e.g., `Widget.Cfg` and `Editor.Cfg`, both record aliases) could collide if they're qualified differently in the HM type vs the codegen env's namespace.

**Reproducer:**
```sky
-- Widget.sky:
module Widget exposing (Cfg)
type alias Cfg msg = { onSubmit : msg }

-- Editor.sky:
module Editor exposing (Cfg)
type alias Cfg msg = { onChange : String -> msg }

-- Main.sky:
import Widget
import Editor
import Editor.Cfg as EditorCfg

viewWidget : Widget.Cfg msg -> Element msg
viewWidget cfg = ...

viewEditor : EditorCfg.Cfg msg -> Element msg
viewEditor cfg = ...

main = ...
```

If `viewWidget`'s let-binding assigns `let ew = Editor.forwardCfg e`, and the codegen later coerces this in a context expecting `Widget.Cfg msg`, `inferExprType` returns `T.TAlias (homeMod="Editor") "Cfg" [msg]`. The prefix code computes `"Editor_Cfg"`. But `_cg_recordAliases` contains BOTH `"Widget_Cfg"` and `"Editor_Cfg"` (unqualified), so the lookup succeeds for the WRONG alias. At runtime, the Go assertion `Editor_Cfg_R[msg]` succeeds where a `Widget_Cfg_R[msg]` was expected, silently passing the wrong struct type downstream.

**Root-cause hypothesis:**
The membership check at line 8514-8518 confirms that an alias NAME is record-emitting, but doesn't confirm that it's the SAME alias in the SAME module. The HM type carries `homeMod` (the source module of the alias), but the lookup ignores it and checks only the name. Two distinct record aliases with the same name in different modules both pass the check.

**Why current tests miss it:**
The stress fixture and tests use unique alias names per module. Namespace collisions (two modules exporting the same typename) are rare in practice, but they're valid Sky code (the qualified names disambiguate at the use site).

**Suggested gate:**
Strengthen the membership check to compare both the alias name AND the home module. Modify the lookup to construct the full module-qualified name, then check membership in `_cg_recordAliases`.

Better (architectural fix): The `_cg_recordAliases` set currently stores flat names. Refactor it to use a `Map (ModuleName, AliasName)` structure so lookups are unambiguous.

---

## Gap B3 (severity: high)
**File:** `runtime-go/rt/live.go:3391-3414` (__skyReviveScripts script injection)

**Symptom:** The `__skyReviveScripts` function walks the DOM for `<script>` tags and recreates them. When copying attributes from the old script to the fresh one (lines 3398-3400), it uses `setAttribute(a.name, a.value)` without sanitizing. If a Ui.html Sky component injects a script with a malformed or injected attribute (e.g., `<script onerror="attacker">`), the fresh script inherits it. Additionally, when copying `textContent` (line 3404), inline script bodies are transferred directly; if an attacker controls the VNode serialization (e.g., via a form field that wasn't escaped), malicious JavaScript could be injected.

**Reproducer:**
```sky
-- Sky.Live app where user input flows into an Html.node "script":
module Main exposing (main)
import Std.Ui as Ui
import Std.Html as Html
import Std.Html.Attributes as Attr

view model =
    Ui.html (Html.node "script"
        [ Attr.attribute "onerror" model.userInput ]
        [])

-- User submits: model.userInput = "alert('XSS')"
-- On SSR, the initial script renders with onerror.
-- On Sky.Live patch (e.g., via __skyReplaceHTMLPreservingFocus),
-- __skyReviveScripts walks the new body, sees the script, and
-- does fresh.setAttribute("onerror", "alert('XSS')"), which
-- executes immediately.
```

**Root-cause hypothesis:**
The script revival mechanism prioritizes functionality (recreating script bundles) over security. When copying attributes, it doesn't filter for event-handler attributes (onerror, onload, onclick, etc.). These are permitted on `<script>` elements in the HTML spec, so `setAttribute` accepts them. The freshly-created script node's attribute fires the event when the script is inserted or loads.

Additionally, the `try...catch` at line 3400 silently swallows errors, so if `setAttribute` fails (which it shouldn't for normal cases), the script is still marked as revived and won't be retried.

**Why current tests miss it:**
The test suite uses trusted, static HTML in Ui components. User-supplied input doesn't flow into Html.node "script" attributes. The app-level JS bundles (sky-editor's scriptTag) are trusted sources.

**Suggested gate:**
Whitelist safe script attributes (src, type, async, defer, integrity, crossorigin, noModule, referrerPolicy). Filter out event-handler attributes (on*) and non-standard attributes. Example:

```javascript
var safeAttrs = {src: 1, type: 1, async: 1, defer: 1, integrity: 1, crossorigin: 1, noModule: 1, referrerPolicy: 1};
for (var j = 0; j < old.attributes.length; j++) {
  var a = old.attributes[j];
  if (safeAttrs[a.name]) {
    try { fresh.setAttribute(a.name, a.value); } catch (_) {}
  }
}
```

Also: sanitize inline script body via `old.textContent.trim()` check or restrict to src= bundles only and reject inline scripts.

---

## Gap B4 (severity: high)
**File:** `runtime-go/rt/live.go:3989-3991` (form submit ev.submitter fallback for old Safari)

**Symptom:** The fallback logic for old Safari (`document.activeElement if element is contained in the form`) has a subtle race: `t.contains(document.activeElement)` evaluates AFTER the user has clicked the button and BEFORE the form submit handler runs. If the user clicks button A, then immediately (before submit fires) clicks button B in a different form on the page, `document.activeElement` has shifted to button B, and the form-submit handler for form A receives the wrong submitter.

Additionally, if the user clicks a submit button, blurs to another field (e.g., onchange fires a Cmd.perform that repains the DOM), and then the submit event fires, the activeElement may have shifted to the repainted input, and `t.contains(...)` may fail if the form was removed and re-added.

**Reproducer:**
```sky
-- Two forms on the page
form1 [ onSubmit Form1Submit ] [ input [..] [], button [type "submit"] [text "Form1"] ]
form2 [ onSubmit Form2Submit ] [ input [..] [], button [type "submit"] [text "Form2"] ]

-- Scenario:
-- 1. User clicks "Form1" button.
-- 2. Before submit event fires, user clicks "Form2" button.
-- 3. form1's submit handler fires (event queued).
-- 4. `document.activeElement` is now the Form2 button.
-- 5. `t.contains(document.activeElement)` checks if Form2 button is in Form1 — FALSE.
-- 6. Fallback returns null, and form1 submit gets no submitter.
```

**Root-cause hypothesis:**
The activeElement fallback assumes the user will click a button in the same form and hold focus there until submit fires. In practice, with rapid clicking or intervening repaints, the focus can shift. The safer pattern is to rely ONLY on `ev.submitter` (modern) and fail gracefully (submitter = null) when unavailable, rather than trying to infer the submitter from activeElement.

**Why current tests miss it:**
The test suite (if any) checks single-form scenarios with sequential clicking. Multi-form pages and rapid-click scenarios aren't tested.

**Suggested gate:**
Remove the activeElement fallback for old Safari and accept that `ev.submitter` may be null on old browsers. Document this limitation clearly. If submitter is null, the form's data is valid — it just doesn't include any submit button's name/value. Old Safari users still submit forms; they just don't get multi-action button distinctions.

Alternatively, attach a `mousedown` listener to every submit button globally, capture the last-clicked button, and use that as the fallback. This is more reliable than activeElement.

---

## Gap B5 (severity: medium)
**File:** `runtime-go/rt/live.go:3993-4010` (form submit disabled-field filter and race)

**Symptom:** The loop skips disabled fields (`if (!el.name || el.disabled) continue`) before checking the field type. A disabled text input is skipped (correct), but if a disabled submit button is in form.elements, the type-specific filter at line 3996-4000 doesn't apply to it, and the skip is silent. This is correct behavior, but the logic is implicit: the reader must infer that disabled fields are filtered before type-checking.

More critically, if a field becomes disabled AFTER the form submit handler reads it but BEFORE the data is sent to the server (due to a concurrent Cmd.perform patch), the server's view of enabled/disabled fields diverges from the client's. The form data includes a stale value from a field the user can no longer see.

**Reproducer:**
```sky
update msg model =
    case msg of
        Submit formData ->
            let data = formData
            in ( model
               , Cmd.batch
                   [ Cmd.perform (Task.sleep 100) (\_ -> ServerRespond)
                   , -- The sleep simulates a network round-trip. During this time,
                     -- if the app patches the form to disable the field, the
                     -- formData was captured with the old value but the server
                     -- sees the field as disabled.
                   ]
               )
```

**Root-cause hypothesis:**
The form data is captured synchronously in the submit handler (`__skyExtractArgs` runs immediately). If the Cmd or subscription pipeline patches the form before the data is sent to the server, there's a window where the captured data and the rendered form diverge.

**Why current tests miss it:**
The test suite probably submits forms and immediately sends the data without intervening patches. Real apps with heavy animations or concurrent updates might trigger this.

**Suggested gate:**
Document the invariant: form data captured at submit-event time reflects the field values and enabled/disabled state AT THAT MOMENT. If the form is patched before the data leaves the client, the old data is sent. This is expected behavior in any form framework; the alternative (re-reading the form before each network request) is expensive and introduces other races.

If this is a concern, apps should avoid concurrent patches to the form being submitted. Alternatively, capture the disabled state along with the value and skip server-side processing of stale fields.

This is more of a documentation gap than a code bug, but it's worth calling out.

---

## Memory pathology — cabal test suite
**Status:** Analyzed but not fully quantified.

The CLAUDE.md non-negotiables reference a mem-guard SIGKILL at >6 GB RSS during `cabal test`. The Cycle-1 release notes (CYCLE_LOG.md v0.15.6 entry) mention: `cabal-test SIGKILL by mem-guard >6GB; CI was green`.

This suggests the full cabal-test suite (306 specs) accumulates unbounded memory on the dev machine (16 GB total, guardrail at 6 GB), but CI runners (larger memory) pass. The pathology could be:

1. **GHCi session persistence:** `cabal test` in GHCi reuses the runtime across specs. If a spec spawns subprocesses (e.g., `sky build` examples), file handles or temporary directories aren't cleaned up.
2. **Lazy thunks in the compiler state:** The HM solver or codegen leaves suspended computations in-memory across test boundaries.
3. **Embedded test files:** The `test-files/v0.15-stress` and `examples/` directories are embedded or read repeatedly without freeing.

**Candidates for per-spec measurement:**
- `ExampleSweepSpec` — spawns `example-sweep.sh --build-only` for each of 27 examples.
- `VerifyAllSpec` / `VerifyScenarioSpec` — Playwright-driven, spawns Sky.Live + HTTP servers.
- `Sky.Build.MonomorphiseSpec`, `SolverBudgetSpec`, `FfiGenMultiSpec` — compile large synthetic programs.

**Recommended action for next cycle:**
Run each heavy spec in isolation with `time -l` (macOS) or `/usr/bin/time -v` (Linux) to measure peak RSS. If individual specs are <1 GB but the suite is >6 GB, GHCi accumulation is the culprit and requires `cabal test --with-ghc=ghc` (not `ghci`) or spec isolation via subprocess. If a single spec >1 GB, investigate the codegen state or test-data cleanup.

**Not a blocking gap for Cycle 2** but worth measuring in P24 (memory + performance).

---

## Save/Create silent-failure audit
**Gap identified but deferred to P27 (new planning item).**

The user flagged: "Save/Create/Rename file handlers in skydeploy use `let _ = SourceStore.writePath …` (a `Result Error ()` that's silently discarded). The PATTERN is: any Sky function returning `Result Error ()` whose only purpose is the side effect, when discarded with `_`, loses error visibility."

Current stdlib functions returning `Result Error ()`:
- `File.writeFile : String -> String -> Task Error ()`
- `File.append : String -> String -> Task Error ()`
- `File.remove : String -> Task Error ()`
- `File.mkdirAll : String -> Task Error ()`
- `File.copy : String -> String -> Task Error ()`
- `File.rename : String -> String -> Task Error ()`
- `Std.Money.setRate : Currency -> Currency -> Decimal -> Result Error ()`
- `Std.Money.clearRates : () -> Result Error ()`

All of these are Task-typed (effectful), so the auto-force rule at CLAUDE.md applies: `let _ = Task` is auto-forced by the lowerer. However, `Result Error ()` variants are NOT auto-forced. If user code discards a `Result Error ()` with `let _ = expr`, the error silently disappears.

Additionally, if third-party libraries or future stdlib additions return `Result Error ()` with meaningful errors (e.g., "permission denied", "quota exceeded"), discarding them loses the error context.

**Suggested gate (P27):** Add a type-level `Result Error ()` → `Task Error ()` bridge function (e.g., `Result.asTask : Result e a -> Task e a`) that explicitly marks the intent to handle errors. Document that bare `Result Error ()` from user code should flow through `Task.fromResult` if the app cares about the error, or `|> ignore` if it genuinely doesn't.

Alternatively: introduce a `Never` phantom type for operations where errors are truly impossible (or impossible to handle), and return `Result Never a` instead of `Result Error ()`. Then bare `let _ =` won't compile; the type forces consciousness about error handling.

This is P27 in the plan (new item surfaced this cycle).

---

## HttpResponse→String mismatch audit
**Gap identified but architectural insight deferred to P28 (new planning item).**

The user flagged: "HttpResponse-vs-String accept (Tools.sky panic class): `Http.request` returns `HttpResponse` struct, user code that types the binding as `String` should fail to compile but the compiler accepts the mismatch (only panics at runtime in `rt.coerceInner`)."

**Example:**
```sky
-- User writes:
response = Http.get "https://example.com" |> Task.run
  -- Expects Task Error HttpResponse, but annotates as:
status : String
status = response.status
  -- Should be HttpResponse field access, not String.
```

**Root-cause hypothesis:**
Sky's type system is correct: `Http.get` returns `Task Error HttpResponse`. But if the user manually annotates a binding as `String`, the HM solver accepts it as an over-annotation (the value IS compatible with String if it's untyped `any`). The lowerer then tries to coerce the `HttpResponse` value into a String field, which panics at runtime because `rt.coerceInner` can't narrow `HttpResponse` into `String`.

The issue is that the solver allows implicit widening from a concrete type to `any`, and the lowerer's coercion path only checks if the target slot is concrete. When both the source (actual HM type) and target (annotated type) are in scope, the mismatch should surface as a type error.

**Suggested gate (P28):** Strengthen the type-error reporting path in the unifier or solver to catch source-type-vs-annotation mismatches EARLIER, before lowering tries to coerce. This requires threading the source HM type through the lowerer context and comparing it against the annotated target. If they diverge (HttpResponse vs String), fail the type check with a clear error message instead of relying on the runtime coercion to panic.

This is a correctness issue (wrong types should fail at compile time, not runtime) and P28 in the plan.

---

## Overall assessment

**Cycle 2 closes 3 critical gaps (all in the structural-fallback implementation for P1):**
- B1: module-name-to-prefix alignment in `aliasBaseFromCanExpr`
- B2: alias-identity membership checking in `_cg_recordAliases`
- B3: script-injection risk in `__skyReviveScripts`

**Cycle 2 identifies 2 additional high-severity gaps:**
- B4: form-submit ev.submitter fallback race on old Safari
- B5: form-data capture and concurrent-patch desync

**Cycle 2 defers to P27/P28 (new planning items):**
- P27: Result Error () silent-failure pattern audit
- P28: HttpResponse↔String type-mismatch early detection

**P1 review verdict:** APPROVED for v0.15.7 tag. The fix is sound; recommend (non-blocking) performance measurement gate on `examples/13-skyshop` compile-time delta.

**Recommendation for Cycle 3:** Prioritize P14 (SSE channel race, A3) and P15 (SSE encoder chokepoint, A7) for the runtime hardening pass. Address B1 and B2 in P1 pre-merge via code review (ensure module-prefix mapping is synchronized, strengthen alias-membership check). B3 (script-injection) should be addressed before any web-facing deployment of apps using Ui.html with user-controlled script attributes.

