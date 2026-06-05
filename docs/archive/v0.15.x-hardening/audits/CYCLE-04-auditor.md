# Cycle 4 — Auditor findings

Branch audited: main @ f3dd0cd7c49373683c1948a171a8dc6b0bbd9146 (post v0.15.21
release; CYCLE-03-P49 dev-log committed)
Date: 2026-05-27
Auditor pass: 4
Input: 6 user-surfaced bugs from working on another Sky project; each had an
in-app workaround but the compiler / runtime needs a proper fix.

## Summary

6 NEW gaps — D1 D2 D3 D4 D5 D6
Severity breakdown: 2 high · 3 medium · 1 low

All six reproducers confirmed against a fresh sky binary built from this
worktree (`cabal install … exe:sky`, then per-fixture
`rm -rf sky-out .skycache .skydeps && sky build`).

| ID | Subject                                            | Severity | Stage broken         |
|----|----------------------------------------------------|----------|----------------------|
| D1 | `Time.year` / `String.toList` undefined kernels    | high     | codegen → validator  |
| D2 | `case _ -> Ok ()` emits unused `__subject` var     | medium   | codegen (caseToGo)   |
| D3 | `{{NAME}}` interpolation can't be escaped          | medium   | parser / canonicaliser |
| D4 | Multi-line `case\n    subject\n    of` won't parse | medium   | parser (exprCase)    |
| D5 | Dual-import qualifier collision misroutes types    | high     | canonicaliser env    |
| D6 | Row-polymorphic record annotation not parsed       | low      | parser (type ann)    |

The cycle-3 work-stream (LowerCtx cascade, runtime hardening, observability)
is unrelated to all six bugs. None of these are regressions from v0.15.x; D1
and D5 are latent bugs from the kernel-registry / canonicaliser-environment
shape that pre-date this cycle. D6 is a long-standing parser limitation
(the AST + type checker already support row polymorphism).

---

## Gap D1 (severity: high)
**Files:** `src/Sky/Canonicalise/Environment.hs:351,379-380` +
`src/Sky/Generate/Go/Kernel.hs` (missing entries) +
`src/Sky/Build/Compile.hs:7555-7561` (kernelToGo default)

**Symptom (user-facing):** Calling `Time.year` (or any of `Time.month`,
`Time.day`, `Time.dayOfWeek`, `Time.dayOfYear`, `Time.weekOfYear`,
`Time.isWeekend`, `Time.startOfYear`, `Time.endOfYear`, `Time.inZone`,
`Time.formatInZone`, `Time.addYears`, …) — and separately `String.toList`,
`String.fromList`, `String.casefold`, `String.equalFold`, `String.isEmail`,
`String.isUrl`, `String.words`, `String.lines` — fails compilation with:

```
-- CODEGEN ERROR ────────────────────────────────── src/Main.sky:N:M [E4005]

Codegen emitted a reference to `rt.Time_year`
but the runtime does not export a function or value
named `Time_year`. `go build` would reject this
as `undefined: rt.Time_year`.
```

**Minimal reproducer (asserts the codegen error):**

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String
import Std.Log exposing (println)

main =
    let chars = String.toList "abc"
    in println (toString chars)
```

`sky build` ⇒ `E4005 Codegen emitted a reference to rt.String_toList … the
runtime does not export …`. Same fixture with `Sky.Core.Time.year` or
`Std.Time.year` triggers the analogous `rt.Time_year` error. Reproducer ran
against `/tmp/d1-fixture/src/Main.sky` with the fresh worktree binary at
17:38 UTC; FAILS as expected.

**Root cause (the under-handled "Layer 3 stdlib via Ffi.kernel" routing):**

`src/Sky/Canonicalise/Environment.hs:staticKernelModules` maps both
`Sky.Core.Time` AND `Std.Time` to the kernel pseudo-module `"Time"`
(lines 379-380). Similarly `Sky.Core.String` maps to `"String"`. This
causes the canonicaliser at `Sky/Canonicalise/Expression.hs:179` to emit
`Can.VarKernel "Time" "year"` for ANY `Time.year` reference — bypassing
the Sky-source binding's `Ffi.callPure "Time_year" […]` body.

The `Can.VarKernel` then reaches codegen's `kernelToGo`
(`src/Sky/Build/Compile.hs:7545-7561`). Because `("Time", "year")` and
`("String", "toList")` are NOT in `Kernel.lookup`, the catch-all at
line 7561 emits `rt.<Mod>_<Fn>` as a direct Go symbol reference. But the
runtime registers these via `RegisterPure("Time_year", …)` (a runtime
KERNEL DISPATCH callback, lookup-keyed by STRING) — there is no Go
`func Time_year` exported from the rt package, so the validator
(`src/Sky/Build/Validator.hs:206-244` `patternUndefinedKernel`) correctly
catches the dangling reference.

Verification of the asymmetry against HEAD:

- `runtime-go/rt/time_zones.go:241`: `RegisterPure("Time_year", …)` — exists.
- `grep -rn "func Time_year" runtime-go/rt/` → zero matches.
- `Kernel.hs:246-268` (Time block) lists 13 Time entries — `year`, `month`,
  `day`, `dayOfWeek`, etc. all ABSENT.
- The Layer-3 stdlib `sky-stdlib/Std/Time.sky:123-150` defines
  `year zone ms = Ffi.callPure "Time_year" [Ffi.toAny zone, Ffi.toAny ms]`
  — which would emit `rt.Ffi_callPure("Time_year", …)` correctly, but the
  canonicaliser never sees this body because it short-circuits the import to
  the kernel pseudo-module first.

The user's workaround was almost certainly: route through the FFI directly,
e.g. `Ffi.callPure "Time_year" [Ffi.toAny "UTC", Ffi.toAny ms]` inside their
own module — bypassing the canonicaliser's kernel-pseudo-module shortcut.

**Affected function families (from grep against `sky-stdlib` and
`runtime-go/rt/`):**

- Std.Time calendar queries: `year`, `month`, `day`, `dayOfWeek`,
  `dayOfYear`, `weekOfYear`, `isWeekend`.
- Std.Time arithmetic: `addYears`, `addMonths`, `addDays`, `addHours`,
  `addMinutes`, `addSeconds`, `diffDays`, `diffHours`, `diffMinutes`,
  `diffSeconds`.
- Std.Time boundaries: `startOfDay`, `endOfDay`, `startOfWeek`,
  `startOfMonth`, `endOfMonth`, `startOfYear`, `endOfYear`.
- Std.Time zones: `inZone`, `formatInZone`, `zoneOffset`, `zoneName`,
  `fromParts`, `utc`, `daysInMonth`, `isLeapYear`.
- Sky.Core.String layer-3 extras: `toList`, `fromList`, `casefold`,
  `equalFold`, `isEmail`, `isUrl`, `words`, `lines`. (Stdlib source defines
  each via `Ffi.kernel "String_*"` but the corresponding kernel registry
  entries + runtime registrations may be missing — needs per-name sweep.)

Single user invocation hits one missing kernel; a user porting an existing
app can hit a half-dozen in succession.

**Suggested fix (root-cause):**

Two coordinated changes:

1. **Add the missing kernel-registry entries in `Kernel.hs`.** Each Layer-3
   stdlib function defined via `Ffi.kernel "K"` MUST appear in
   `_kernelTable` with `KernelInfo "rt.<K>" <arity> <typed?>`. This is the
   audit pass: walk every `Ffi.kernel "Name"` declaration in
   `sky-stdlib/{Sky/Core,Std,Sky/Http}/*.sky` and verify a matching
   `Kernel.hs` entry. The current Time block stops at `addMillis` /
   `diffMillis` / `every` — every Std.Time entry (year, month, day,
   addYears, etc.) is missing.

2. **OR: make `kernelToGo`'s default emit `rt.Ffi_callPure("Name", …)`
   instead of `rt.<Name>` direct symbol.** This pushes through the runtime
   dispatch table (the SAME path `Ffi.callPure "Name"` from Sky source
   takes today), so missing-from-Kernel.hs entries gracefully route through
   the kernel-registry instead of failing the validator. Trade-off: lose
   the static typed-call shape that Kernel.hs entries can carry
   (`_ki_typed`, arity, generic params). Probably a layered fix — typed
   path via Kernel.hs entry where available; otherwise dispatch through
   `Ffi_callPure`.

3. **Add an integration test** that walks every `Ffi.kernel "Name"` in the
   stdlib + every `Ffi.callPure "Name"` and asserts EITHER Kernel.hs has
   an entry OR the runtime has a `RegisterPure("Name", …)`. This is a
   build-time invariant the current build doesn't enforce — the validator
   only catches the FIRST `rt.<X>` that's emitted but the bug exists for
   any function the user happens not to call.

**Why current tests miss it:** the 27-example sweep exercises a fixed set
of stdlib calls. `Std.Time.year` / `String.toList` are not used in any
example, so no codegen happens to hit them in CI. The 120 Sky.Test
assertions in `examples/00-standard-libs` exercise the `String` /
`Maybe` / `Result` / `List` / `Dict` / `Set` / `Time` (just `now` and
`format`) basics — calendar queries are not covered.

**Dependency:** none — fix is isolated additions in Kernel.hs +
optionally a Compile.hs default-emit change. No prior cycle item blocks
this.

---

## Gap D2 (severity: medium)
**File:** `src/Sky/Build/Compile.hs:10752-10773` (`caseToGo` subject decl)
+ `10800-10825` (`caseBranchToStmtsWith` catchall arm)

**Symptom:** Sky code `case foo of _ -> Ok ()` emits Go where `__subject`
is declared but never read, tripping Go's `declared but not used` build
error:

```
-- GO BUILD ERROR
./main.go:87:63: declared and not used: __subject
```

**Minimal reproducer (asserts the go-build error):**

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

checkValue : Int -> Result Error ()
checkValue n =
    case n of
        _ -> Ok ()

main =
    case checkValue 5 of
        Ok () -> println "ok"
        Err _ -> println "err"
```

`sky build` ⇒ `E5001 ./main.go:87:63: declared and not used: __subject`.
Verified against `/tmp/d1-fixture/src/Main.sky` at 17:30 UTC; FAILS.

The emitted Go for `checkValue` is:

```go
func checkValue(n int) rt.SkyResult[Sky_Core_Error_Error, struct{}] {
    return func() rt.SkyResult[Sky_Core_Error_Error, struct{}] {
        __subject := n;
        return rt.ResultCoerce[Sky_Core_Error_Error, struct{}](
            rt.Ok[any, any](struct{}{}));
        _ = rt.Unreachable("case/__subject");
        return rt.SkyResult[Sky_Core_Error_Error, struct{}]{}
    }()
}
```

`__subject := n` is declared on the first inner-fn line; the second line
returns unconditionally (the `_` catchall pattern needs no condition);
`__subject` is never read.

**Root cause:** `caseToGo` unconditionally emits
`subjectDecl = GoShortDecl "__subject" goSubject` (line 10754/10756).
`caseBranchToStmtsWith` with a `PAnything` head returns `bodyStmts`
directly (line 10824 `Nothing -> bodyStmts`) without inserting a
condition that USES `__subject`. When ALL branches are catchalls
(`PAnything` / bare `PVar`) — the case is effectively
`let __subject = goSubject in body` where `body` never references
`__subject`.

**Suggested fix:**

Insert `_ = <subjectName>` (a Go blank-identifier assignment) as the
SECOND statement after `subjectDecl` whenever the case has NO branch
that would actually read `__subject`. Quickest test: if EVERY branch
returned `Nothing` from `patternCondition` AND EVERY branch returned
`[]` from `patternBindings`, then `__subject` is unused — emit
`_ = __subject` to satisfy Go.

Simpler and more conservative: always emit `_ = __subject` immediately
after `subjectDecl`. The Go compiler is happy to see `_ = x` followed
by other reads, and the runtime cost is zero. Three-line patch at
`caseToGo`'s `subjectDecl` site.

**Why it matters beyond the immediate compile failure:** the bug also
exposes a semantic-hygiene issue — if the subject HAD side effects
(impossible in pure Sky today, but conceivable for a future Task-typed
case subject), dropping the discard would mean the side effect doesn't
fire. The `_ = __subject` discard is the canonical Go pattern for
"keep evaluated, ignore result" and matches Sky's `let _ = TaskExpr`
auto-force convention (CLAUDE.md "Effect boundary" section).

**Why current tests miss it:** every existing example's `case ... of`
either has a useful branch (PCtor / PInt / PStr / PBool) that emits a
condition reading `__subject`, OR has a bare-variable head (`PVar n`)
that emits `n := __subject` (the binding USES the subject). The
exact shape "case with ONLY `_ -> body` branches AND body doesn't
mention the subject" isn't covered in the cabal sweep.

**Dependency:** none. Standalone fix in `caseToGo`.

---

## Gap D3 (severity: medium)
**File:** `src/Sky/Canonicalise/Expression.hs:557-572` (`splitInterpolation`)
+ `594-622` (`resolveInterpolationRef`)

**Symptom:** A Sky multiline string `"""Hello {{NAME}}"""` intended as a
template for ANOTHER templating system (Handlebars, Mustache, env-var
substitution, etc. — `{{NAME}}` is widely used as a placeholder
convention) is hijacked by Sky's interpolation desugarer.

The desugarer parses `NAME` as a Sky identifier (resolved via
`Env.lookupVar`); since no Sky variable named `NAME` exists, it falls
through to `Can.VarLocal "NAME"` at line 613. The type checker may
either error (if it tracks unbound names) or accept the unbound var
silently; codegen then emits `Go_NAME` or similar, and `go build`
rejects with `undefined: NAME`.

**Minimal reproducer (asserts the go-build error):**

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

template : String
template = """Hello {{NAME}},
your account {{ACCOUNT_ID}} has balance {{BALANCE}}.
"""

main =
    println template
```

`sky build` ⇒ `E5001 ./main.go:87:113: undefined: NAME`. Verified against
`/tmp/d1-fixture/src/Main.sky` at 17:31 UTC; FAILS.

The user's workaround was almost certainly: drop the triple-quote, use
single-quoted strings with `\n` escapes + `++` concatenation (cumbersome
but avoids the interpolation pass).

**Root cause:** `splitInterpolation` at line 558 unconditionally treats
`{{` as the interpolation opening — it has NO escape mechanism. The
syntax has no documented way to emit a literal `{{NAME}}`. The CLAUDE.md
documentation says "Single `{` is literal" — but a single `{` followed
by `{` is `{{` and is greedily consumed as interpolation start.

**Severity rationale:** medium, not high. The bug fires only when:

1. Sky source contains a triple-quoted multiline string AND
2. The string contains `{{Word}}` (uppercase starts especially likely to
   reach codegen because `Word` is parsed as a constructor/type if dotted,
   or as `VarLocal` if bare) AND
3. The user intended the `{{...}}` as a literal placeholder for
   downstream tooling.

But the failure mode (`undefined: <NAME>`) is opaque — the user has to
read the emitted Go to see what went wrong. A poor first impression for
anyone writing HTML email templates / config-file generators / shell
scripts via Sky's heredoc support.

**Suggested fix (two complementary changes):**

1. **Add a parser-level escape.** Recognise `\{{` (or `{{{` doubled, or
   any well-documented prefix) inside the multiline lexer as the literal
   `{{`. Compare Elm's approach (no triple-quoted interpolation; uses
   `++`) or Roc's (`\(expr)` with explicit `\` escape).

2. **Tighten the desugarer's fallback.** When `splitInterpolation`'s
   `(inside, after)` (line 563) `inside` body fails `resolveInterpolationRef`
   (i.e. yields the literal-string fallback at line 622 — for cases the
   simple parser can't handle), the resulting fallback string IS the
   correct behaviour. But the current "uppercase bare identifier" case
   (`first` is uppercase, no dot at line 615) routes to the fallback only
   when `lookupImportAlias` returns Nothing. Bare uppercase like `NAME` 
   probably DOES return Nothing from `lookupImportAlias` (it's not a
   module alias), so this path is already handled.

   Re-verify: bare lowercase like `{{name}}` is the silently-wrong case
   (resolves to `VarLocal "name"` even when no such var exists). For
   ANY identifier the desugarer resolves, the canonicaliser should
   reject the reference if no binding exists — but `Env.lookupVar` is
   "lookup or fall through to VarLocal" (line 613). Make that fall-through
   only fire for KNOWN locals (lambda params, let-bindings); for unknown
   names, emit a clear error: "Multiline-string interpolation referenced
   unbound identifier `NAME`. If you meant the literal text `{{NAME}}`,
   escape with `\{{NAME}}` or use single-quoted concatenation."

**Why current tests miss it:** every example's triple-quoted string uses
`{{` only for genuine interpolation. No test exercises the escape case.
The Format/Format.hs `escapeMultilineLit` (line 296) escapes some
characters but not `{`, suggesting the formatter also doesn't know
about the literal-`{{` problem.

**Dependency:** none. Lexer change is local to `Sky.Parse.String`;
canonicaliser change is local to `Sky.Canonicalise.Expression`.

---

## Gap D4 (severity: medium)
**File:** `src/Sky/Parse/Expression.hs:360-362` (case-of dispatch) +
`569-588` (`exprCase`)

**Symptom:** Multi-line `case` subject:

```elm
classify a =
    case
        a
    of
        0 -> "zero"
        _ -> "other"
```

Parser fails with the misleading "Top-level declaration expected here"
error pointing at the `case` keyword's column.

**Minimal reproducer (asserts the parse error):**

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

classify : Int -> Int -> Int -> String
classify a b c =
    case
        ( a
        , b
        , c
        )
    of
        (0, 0, 0) -> "zeros"
        _ -> "other"

main =
    println (classify 1 2 3)
```

`sky build` ⇒ `E0001 src/Main.sky:10:9 Top-level declaration expected
here.` Verified against `/tmp/d1-fixture/src/Main.sky` at 17:32 UTC; FAILS.

The single-line variant `case (a, b, c) of` works. The user's workaround
was almost certainly: collapse the tuple onto a single line, or extract
the tuple to a let-binding first.

**Root cause:** `Sky.Parse.Expression.hs:exprCase` (line 569-588) starts
with `subject <- expression mkError` (line 571) but the dispatch at
line 360-362 only calls `spaces` (no newline) between the `case`
keyword and `exprCase`. The `expression` parser itself doesn't `freshLine`
at its head. Compare `exprLet` (line 466-475) which starts with
`freshLine mkError` (line 468) — that's why `let\n    x = …` parses.

The parse FAILS at line 10 col 9 — the first non-whitespace character
AFTER the `case` keyword and the newline. The parser bottoms out
trying to find an expression on the SAME line as `case`, finds the
newline, the layout filter treats column 9 as a fresh decl start, and
the error message blames the wrong position.

**Suggested fix:**

Insert `freshLine mkError` at the head of `exprCase` (between line 569's
`do` and line 571's `subject`). Three-line patch. Mirror the `exprLet`
pattern; the `of` keyword later (after `freshLine mkError` at line 583)
already proves the multi-line subject form was INTENDED to work — only
the keyword-to-subject transition was missed.

**Cascade implication:** the `let` body's `case` subject can be multi-
line for the same reason — try `let x = case\n   y\n   of …` — likely
also broken. Audit other `freshLine mkError` placements at expression-
opening keywords: `if`, `then`, `else`, `let`, `in`, `case`, `of`.

**Why current tests miss it:** every example's `case` keyword is
followed by either a same-line subject or a multi-line subject that
parses because it starts with `case x of` (subject ON the case line)
THEN the branches are multi-line. The shape "case\n    subject\n    of"
isn't in the test corpus.

**Dependency:** none. Isolated parser fix.

---

## Gap D5 (severity: high)
**File:** `src/Sky/Canonicalise/Environment.hs:140-145`
(`addQualifiedImport`) + `Sky/Canonicalise/Module.hs:280-290`
(`buildImportAliasMap`)

**Symptom:** When two modules whose LAST PATH SEGMENT is the same (e.g.
`import State` and `import App.State`, both deriving qualifier `State`),
the canonicaliser's qualifier registry has a contract mismatch:

- `_qualVars` and `_qualCtors` are UNIONED via `Map.insertWith Map.union`
  (line 142-143) — so `State.initial` (from `State` module) AND
  `State.defaultModel` (from `App.State`) both resolve under qualifier
  `State`.
- `_importAliases` uses bare `Map.insert` (line 144) — LAST-WINS. The
  module resolved for qualifier `State` is the LAST imported (here
  `App.State`).
- `_qualTypes` (Environment.hs:21) is declared but never populated;
  qualified TYPE references fall back to `_importAliases` resolution.

When the user writes `useFn : State.Model`, the canonicaliser resolves
`State` → `App.State` (last wins from `_importAliases`), then looks up
`Model` in `App.State`'s alias table. Meanwhile `State.initial` (a value)
correctly resolves to the original `State` module's binding via the
unioned `_qualVars`. The result: `State.initial` returns `State.Model`
but the annotation `State.Model` resolves to `App.State.Model` — type
checker reports the dishonest error `Foreign 'State.initial': Model vs
Model` (same alias name from two different homes).

**Minimal reproducer (asserts the type error):**

```elm
-- src/State.sky
module State exposing (Model, initial)

type alias Model = { count : Int, label : String }

initial : Model
initial = { count = 0, label = "init" }
```

```elm
-- src/App/State.sky
module App.State exposing (Model, defaultModel)

type alias Model = { foo : String, bar : Int }

defaultModel : Model
defaultModel = { foo = "x", bar = 99 }
```

```elm
-- src/Main.sky
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)
import State
import App.State

useFn : State.Model
useFn = State.initial

main = println (toString useFn.count)
```

`sky build` ⇒ `E2001 Foreign 'State.initial': Model vs Model`. Verified
against `/tmp/d5-fixture/src/Main.sky` at 17:33 UTC; FAILS.

**Worse failure modes:**

- The error message **lies about identity** ("Model vs Model" — both
  names print identically; the user can't tell the two aliases apart
  without reading the canonicaliser source).
- If the second-imported module ALSO exposes `initial` (different shape),
  `State.initial` silently resolves to the LATER one's value via
  `_qualVars` union — but the type annotation `State.X` resolves via
  `_importAliases` last-wins. The user gets type confusion errors that
  point at the call site rather than the import site.
- Unaliased imports of distinct deep paths that happen to share a last
  segment (e.g. `import Lib.Internal.Foo` and `import Lib.External.Foo`
  — both qualifier `Foo`) silently collide. No diagnostic warns about
  this.

The user's workaround was almost certainly: alias one or both imports
explicitly (`import State as Local`).

**Root cause:** the qualifier-to-module mapping uses two DIFFERENT
combine semantics for related fields (union vs last-wins) — the
contract isn't enforced. The qualifier "State" lands in three maps but
each map has a different policy. There is no validation step that
detects a SAME-qualifier conflict from two imports and rejects it (the
way Haskell's compiler does with "ambiguous occurrence" errors).

**Suggested fix (two layers):**

1. **Detect the collision at import-time.** In
   `Sky.Canonicalise.Module.processImportWith` (or earlier in
   `buildImportAliasMap`), check whether the qualifier already exists in
   `_importAliases` AND resolves to a DIFFERENT canonical module. If so,
   reject with:
   `Import error: two imports both use qualifier 'State' (State and
   App.State). Disambiguate with 'import State as ...' or 'import
   App.State as ...'.`

2. **Replace `_importAliases :: Map.Map String ModuleName.Canonical` with
   `Map.Map String [ModuleName.Canonical]`** AND consult the list when
   resolving qualified TYPE references — try every candidate module and
   succeed if EXACTLY ONE has the named type. If 0 → undefined-type
   error; if >1 → ambiguous-qualified-name error with the candidate list.
   Same for qualified VALUE references (today's `_qualVars` union is
   nearly that, but it merges entries and the resolution fails silently
   on collisions).

The simpler v0 fix (#1) ships in one PR; the deeper fix (#2) is the
right long-term design.

**Why current tests miss it:** all 27 examples use distinct module
hierarchies — no two share a last segment. The cabal test suite tests
SINGLE-module imports against direct dep references. The exact "two
modules, same last segment, both imported" shape isn't tested. This
would only surface in real-world apps that adopt a flat
`src/<feature>/State.sky` convention (very common in Std.Ui apps —
CLAUDE.md "Three idioms" #3 names `State.sky` explicitly as one of the
canonical split files).

**Dependency:** none. Standalone canonicaliser fix.

---

## Gap D6 (severity: low)
**File:** `src/Sky/Parse/Type.hs:91-103` (record type parser); the AST
support already exists at `src/Sky/AST/Source.hs:223`
(`TRecord [...] (Maybe String)`); the type-system support already exists
at `src/Sky/Type/Unify.hs:283-347` (`unifyRecords` already handles open
records) and `src/Sky/Type/Instantiate.hs:118-127`
(`typeToVariable T.TRecord` already creates a flex-var for `mExt = Just
name`).

**Symptom:** Sky's HM type system already supports row polymorphism
(open records are created internally by `Can.Access` so `expr.field` can
unify with any record carrying `field`), but the SURFACE SYNTAX has no
way to write a row-polymorphic record annotation:

```elm
greet : { r | name : String } -> String
greet rec = "Hello, " ++ rec.name
```

`sky build` ⇒ `E0001 src/Main.sky:9:13 Top-level declaration expected
here.` (the `|` after `r` confuses the type-annotation parser).
Verified against `/tmp/d6-fixture/src/Main.sky` at 17:34 UTC; FAILS.

The user's workaround was almost certainly: pin to a concrete record
shape `greet : { name : String } -> String` — which then rejects any
record with EXTRA fields (the closed-vs-open distinction unifyRecords
enforces post-2026 fix at Unify.hs:307-340).

**Severity rationale: LOW.** Row polymorphism in annotations is a
relatively rare power-user feature; most Sky code uses concrete record
shapes via type aliases. AI tooling rarely needs it. But the gap is
load-bearing for any library author who wants to expose a function
that operates on "any record carrying field X" — today they MUST take
a concrete record or a function-extractor parameter.

**Root cause:** the record-type parser at `Sky.Parse.Type.hs:91-103`
unconditionally calls `typeRecordFields` (which expects `name : Type,
…`). It never tries to parse a leading lowercase identifier followed
by `|`. The AST slot for `mExt :: Maybe String` is hard-coded to
`Nothing` at line 98 and 103.

**Suggested fix:**

Extend the record-type parser to recognise `{ <lower>` followed by `|`
as the row-variable form. Pseudo-shape:

```haskell
-- After consuming the `{`, peek for: lowercase identifier + `|`
case peekVarPipe of
    Just rowVar -> do
        char mkError '|'
        spaces
        fields <- typeRecordFields mkError
        freshLine mkError
        char mkError '}'
        return (Src.TRecord fields (Just rowVar))
    Nothing -> ... -- existing branch
```

The canonicaliser at `Sky.Canonicalise.Type.hs:75-81` already passes
`mExt` through unchanged. `Instantiate.hs:118-127` already wires the
row var into the type variable env. So the parser fix is the ONLY
required change to ship D6.

**Why this WORKS through the rest of the pipeline (auditor-verified):**

- `Sky.AST.Source.TRecord [(...)] (Maybe String)` — slot exists.
- `Sky.Canonicalise.Type.canonicaliseTypeAnnotation`'s
  `Src.TRecord fields mExt` arm at line 75-81 passes `mExt` through
  unchanged.
- `Sky.AST.Canonical.TRecord fields (Maybe String)` — slot exists.
- `Sky.Type.Instantiate.typeToVariable T.TRecord` already handles the
  `Just name` case at line 121-126 — looks up `name` in the type-var
  env (so multiple uses of `r` in the same annotation share the row
  var), or creates a fresh flex-var if not bound.
- `Sky.Type.Unify.unifyRecords` already handles open records — its
  `closed/closed/open` branching logic is exactly what row polymorphism
  needs.

The whole HM machine is in place; only the surface syntax is missing.

**Why current tests miss it:** no test fixture writes a row-polymorphic
annotation — they'd all fail at parse. The Instantiate.hs code path for
`mExt = Just name` is exercised today ONLY for the internally-generated
open records from `Can.Access` lowering, not for user-written annotations.

**Dependency:** none. Parser-only patch. The deeper question of whether
to expose this in the language is a docs/design call (CLAUDE.md
Limitation #1 says "No higher-kinded types. HM only" — row polymorphism
is orthogonal to HKT).

---

## Tooling / process notes from this cycle

1. **Kernel-name registry contract is under-enforced.** D1 exposes the
   missing-from-Kernel.hs class. A build-time test that walks every
   `Ffi.kernel "Name"` declaration in `sky-stdlib/**/*.sky` and asserts
   either Kernel.hs has an entry OR runtime has a `RegisterPure("Name")`
   would prevent this regression class entirely. The validator catches
   it AFTER codegen emits a dangling reference — preferable to catch
   it at install time so the user gets a clean "module X function Y
   has no kernel binding" error.

2. **Canonicaliser environment fields use different combine semantics.**
   D5's `_qualVars` (insertWith union) vs `_importAliases` (insert,
   last-wins) is the visible case. Audit every Env field combine policy
   — pick a single rule (union with collision-report, OR explicit
   first-wins with collision-error) and apply uniformly.

3. **Multi-line layout-sensitive parsers need a `freshLine` audit.** D4
   exposes one site (`exprCase`). The `exprLet` site already has it.
   Every other "keyword expecting an expression next" site (if / then /
   else / case / of / in / `=` / `->`) needs the same `freshLine` at
   the head of the continuation parser.

4. **Multiline string escape is undocumented.** D3 — there's no
   documented way to emit a literal `{{NAME}}`. Either add an escape
   (`\{{NAME}}`) or document the recommended workaround
   (single-quoted concat) in CLAUDE.md + docs/stdlib.md "Multiline
   strings" section.

---

## Closure-of-prior verification (Cycle 3 → Cycle 4)

None of the six cycle-4 user bugs intersect with cycle-3's planned work
(LowerCtx P7, dispatchBatched suppression, SSE channel race, sub-app
observability). Cycle 3's gaps C1-C14 remain at the status documented
in CYCLE-03-auditor.md. The v0.15.21 batched tag (P40 + P49 — case
subject region coercion + scopeStateRef snapshotCallerCtx helper) shipped
between cycle-3 audit and this run; both relate to compiler typed-codegen
routing and don't intersect with D1-D6.

---

## Overall assessment

The six cycle-4 bugs cleave cleanly into two categories:

- **Hygiene/contract gaps in the compiler frontend** (D1: kernel-registry
  vs Layer-3 stdlib mismatch; D3: missing multiline-string escape;
  D4: missing freshLine; D5: canonicaliser env combine-policy mismatch).
  Each is a small, surgical fix at the listed file:line. Total estimate
  6-10 hours across all four.

- **Surface coverage gaps** (D2: caseToGo subject discard;
  D6: row-poly annotation parser). Both are ~1-line fixes; D6 surfaces
  fully-implemented HM machinery to user syntax.

None of them are architecturally hard. All six are good candidates for
a single batched dev cycle. The highest-impact items are D1 (blocks
calendar/string Std functions across the board) and D5 (silent
miscompile from a common module-layout pattern). D6 is the lowest impact
but the highest "value-per-line-changed" — full row polymorphism in
annotations for ~20 lines of parser change.

**Cycle 4 critical path:**

1. **Land D1 + the Kernel.hs sweep** as the first batched item — closes
   `Std.Time.year` / `String.toList` / siblings. The kernel-registry
   walk + integration test together prevent recurrence.
2. **Land D5 collision detection** as the second item — silent
   miscompile risk is highest among the six. The minimal-fix version
   (reject same-qualifier collision at import time) ships in one PR.
3. **Land D2 + D4 + D6** as a parser/codegen hygiene batch — three
   small fixes, one PR each (or batched if convenient).
4. **Land D3 as the docs/escape addition** — last because the
   workaround is well-defined.

---

## Cross-reference

- Reproducer fixtures: `/tmp/d1-fixture/`, `/tmp/d5-fixture/`,
  `/tmp/d6-fixture/` (this worktree, lost on reboot — recreate from
  this audit's "Minimal reproducer" code blocks).
- Prior cycle audits: docs/v0.15.x-hardening/audits/CYCLE-0{1,2,3}-auditor.md
- Cycle log: docs/v0.15.x-hardening/CYCLE_LOG.md
- Tag history: v0.15.7 → v0.15.21 (no tag this cycle; cycle 4 dev
  pending).
