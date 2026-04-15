# Sky Adversarial Audit — Remediation Plan

**Authored 2026-04-15 on branch `feat/sky-haskell-compiler`.** This is the
single source of truth for executing the fixes identified in the
2026-04-15 adversarial audit. It supersedes ad-hoc bugfixing until every
P0 item below is ticked.

## The guiding principle this plan is defending

> **"If it compiles, it works."**

The audit showed this is currently **FALSE**. Five concrete counterexamples
were produced in a single session (fibonacci `.(int)` panic, skychess
Piece-ctor field-order panic, http-server handler Task-coercion panic,
skychess `Db.getField` wrapper panic, skyvote double-init). Silent
coercers (`AsInt`/`AsBool`/`AsFloat` returning `0`) actively violate
the principle by producing wrong answers instead of errors. This plan
closes the gap.

## How to use this doc

On session resume:
1. Read **Progress tracker** (bottom). Pick the lowest-numbered unchecked
   item in the highest-priority unfinished block (P0 first, then P1, etc.).
2. Read that item's full section — every item is self-contained with
   acceptance criteria and verification commands.
3. Before starting: run the **Regression fence** (§ Fence) to confirm a
   green baseline.
4. Fix the item. Do **not** introduce silent escape hatches (see
   § Anti-regression rules).
5. Add/update a test that specifically exercises the class of bug fixed.
6. Commit with the item label: `[audit/Pn-M] <short description>`.
7. Tick the checkbox in the tracker and update the verification note.
8. Move to the next item.

If an item turns out to be wrong in practice, amend this doc first, commit
the amendment, then continue. Never let the plan drift from reality.

---

## Anti-regression rules (non-negotiable)

These are enforced by the audit remediation plan. Violating one is a
reason to revert the change.

1. **No silent coercion.** `AsInt` / `AsBool` / `AsFloat` and their cousins
   must fail loudly (return `Err Error` or propagate through the task
   boundary) on type mismatch. No `return 0` / `return false` / `return ""`
   fallbacks for a type that failed to match.
2. **No `.(T)` on untyped thunks.** Typed codegen that declares return
   type `T` must route the body through a runtime `Coerce` helper that
   either succeeds or returns `Err Error`. No raw `any(body).(T)`.
3. **`sky check` ≥ `sky build`.** `sky check` must fail if `sky build`
   would fail. Any codegen-stage failure is a check-stage failure.
4. **Record field order = declaration order.** Every `Map.toList fields`
   enumeration in codegen must sort by `_fieldIndex` before emission.
5. **No `fmt.Sprintf("%v", x)` stringification of secrets / types.**
   Secrets are `String`, not `any`. Auth secrets reject non-String at
   the Sky type layer.
6. **No new `any`-only FFI boundaries.** Every new FFI binding generator
   output must have `Result Error a` at every Go-error seam and never
   `any` where a concrete Go type is known.
7. **Every fix has a regression test** — Cabal Hspec spec or
   `runtime-go/rt/*_test.go` — that fails before the fix and passes after.
   Don't land a fix without the test.
8. **Comments survive `sky fmt`.** The safety guard refuses to write
   output with fewer comments than input. Any fmt change must preserve
   this invariant.

---

## Fence — regression gate

Before starting any P0/P1 item, confirm green:

```bash
bash scripts/build.sh --self-tests        # 67/67 pass
cabal test --test-show-details=direct     # 26 examples, 0 failures
bash scripts/example-sweep.sh --build-only  # 18/18 pass
```

If any of these is red on `main` / this branch, fix that first as a
non-audit emergency.

---

## P0 — Type-soundness & safety floor

The principle is false until every P0 item is green.

### [ ] P0-1 `sky check` runs the Go emitter
**Problem.** `sky check` reports "No errors found" on input that `sky build`
refuses (concrete case: self-recursive `Int -> Int` emitted
`fibonacci(n-1).(int)` which Go rejects; C1 in the audit).

**Fix.** After HM inference succeeds in `sky check`, run the Go IR emitter
and call `go build -o /dev/null` on the result. If the Go compiler reports
errors, `sky check` fails with the Go errors mapped back to Sky-level
positions where possible, otherwise reported verbatim.

**Files.** `app/Main.hs` (Check branch), `src/Sky/Build/Compile.hs` (expose
a `checkWithGoBuild` entry point).

**Test.** Add `test/Sky/Build/CheckIsBuildSpec.hs`: for each file in
`test-files/*.sky` that `sky build` accepts, `sky check` must also accept.
For a seeded broken fixture, both must reject.

**Acceptance.** For every file in `test-files/`, `sky check` and
`sky build` agree on accept/reject. The broken-fixture regression test
fails without this change.

### [ ] P0-2 Silent numeric coercion → fallible coercion
**Problem.** `rt.AsInt(v any) int` returns `0` for any non-numeric input.
`rt.Add`/`Sub`/`Mul`/`Div` pipe every operand through `AsInt`. Well-typed
Sky that happens to pass a non-int through an `any`-typed path produces
wrong answers silently (C2 in the audit).

**Fix.** Introduce `rt.asIntChecked(v any) (int, bool)` and variants for
Bool/Float. Replace every call-site in the runtime:
- Arithmetic primitives (`Add`, `Sub`, `Mul`, `Div`, `Mod`, `Lt`, `Le`,
  `Gt`, `Ge`) panic-recover into `Err (InvalidInput "...")` on mismatch.
- Display / logging paths keep the lenient form but with a distinct name
  (`rt.AsIntOrZero`) so the lenient use is visible at every call site.

**Files.** `runtime-go/rt/rt.go` (AsInt, AsBool, AsFloat, all arithmetic
primitives).

**Test.** `runtime-go/rt/arith_panic_test.go`: assert that `Add("x", 1)`
returns a SkyResult Err, not `1`.

**Acceptance.** Passing a wrong-typed value through the arithmetic primitives
surfaces as `Err Error` at the nearest Task boundary. No arithmetic path
returns `0` for a type mismatch.

### [ ] P0-3 Typed-return coercion generalised
**Problem.** Today `wrapTypedReturn` handles `rt.SkyResult[...]`,
`rt.SkyMaybe[...]`, and (since today) `rt.SkyTask[...]` via runtime
helpers; every other concrete Go type falls through to raw
`any(body).(T)`. That path panicked on http-server handlers before today's
`TaskCoerce` fix (C1 in the audit).

**Fix.** Every concrete Go return type gets a `rt.Coerce<TypeName>`
runtime helper, or a generic `rt.Coerce[T any](v any) T` that uses
reflect-safe conversion and returns a zero value + recovers into a Sky
`Err` when the value is the wrong shape. Codegen replaces every
`any(body).(T)` with a `Coerce` call.

Acceptable shortcuts: primitive types (`string`, `int`, `bool`, `float64`)
can use a single generic helper; struct types go through a reflect path.

**Files.** `src/Sky/Build/Compile.hs` (`wrapTypedReturn`,
`exprToGoTyped` VarLocal case), `runtime-go/rt/rt.go` (new Coerce
helpers).

**Test.** `runtime-go/rt/coerce_safety_test.go`: assert that `rt.CoerceString(42)`
returns a distinguishable Err/default rather than panicking out of the
call site.

**Acceptance.** `grep -nE '\.(rt\.Sky|any).*\.\([A-Za-z]+\)' sky-out/main.go`
on every example returns zero naked assertions.

### [ ] P0-4 Record field declaration order
**Problem.** `Map.toList fields` in three codegen sites returns alphabetical
order. Auto-generated record constructors had swapped param orders,
panicking at `.(T)` assertions (C1: skychess Piece case).

**Fix.** All three call sites (`Record.classifyAlias`,
`Compile.generateAlias`, `Compile.generateAliasForDep`) sort by
`_fieldIndex` — already done this session. Add a
test to prevent regression.

**Files.** `src/Sky/Generate/Go/Record.hs`, `src/Sky/Build/Compile.hs`.

**Test.** `test/Sky/Build/RecordFieldOrderSpec.hs`: parse a module with
`type alias R = { b : Int, a : String, c : Bool }` and assert the
generated Go struct field order is `b, a, c`, not `a, b, c`.

**Acceptance.** Regression test passes with current code; a change that
reintroduces alphabetical order fails the test.

### [ ] P0-5 Codegen `unreachable case arm` panics → non-fatal
**Problem.** 89 `panic("sky: internal — codegen reached unreachable case
arm (compiler bug)")` sites in `examples/12-skyvote/sky-out/main.go`
alone. These are a crash class if the checker's exhaustiveness proof
ever disagrees with codegen's case flattening.

**Fix.** Introduce `rt.Unreachable(site string) any` that logs via
`Slog.Error` and returns a zero-`Ok` / `Err` based on expected shape.
Codegen emits `rt.Unreachable("<module>.<function>.<line>")` in place of
the raw `panic(...)`. Panic recovery still catches but the error is
distinguishable.

**Files.** `src/Sky/Build/Compile.hs` (panic site at line ~3294),
`runtime-go/rt/rt.go` (new `Unreachable` helper).

**Test.** `runtime-go/rt/unreachable_test.go`: direct call to
`rt.Unreachable` doesn't panic the process; returns a tagged Err.

**Acceptance.** No `panic(` in generated `main.go` files after this
change — all replaced with `rt.Unreachable(...)`.

### [ ] P0-6 FFI boundary: remove silent type-conversion fallback
**Problem.** `skyCallDirect` in `runtime-go/rt/rt.go` has:
```go
} else {
    vals[i] = av   // silent fallback — wrong type, passes anyway
}
```
This throws the type mismatch inside `reflect.Call`, which panics.
`SkyFfiRecover` catches it and returns `Err`, which is safe but the
fast-path silently passes wrong types (C4 in the audit).

**Fix.** `skyCallDirect` returns `Err (Ffi "…")` directly when it cannot
type-match an argument to the callee's parameter type. Reflect.Call is
entered only when all arguments match.

**Files.** `runtime-go/rt/rt.go` (skyCallDirect, SkyCall).

**Test.** `runtime-go/rt/ffi_type_mismatch_test.go`: pass a `string` to
a function expecting `*sql.DB`; assert a clean `Err` comes back, not a
panic-then-recover.

**Acceptance.** No test result in the mismatch class goes through
`recover()` — it's rejected before `reflect.Call`.

### [ ] P0-7 `Test.equal` deep structural equality
**Problem.** `rt.Eq` does not deep-compare lists/records (admitted in
`tests/Core/CoreTest.sky` and elsewhere). `Test.equal Nothing x` across
generic-instantiated `SkyMaybe[any]` vs `SkyMaybe[string]` returns false.
Tests pass that shouldn't.

**Fix.** `rt.Eq` uses `reflect.DeepEqual` for composite types, with
special cases for `SkyMaybe[*]` / `SkyResult[*, *]` / `SkyTuple[*]`
(structural match by Tag + OkValue/ErrValue/Just payload) so generic
instantiation doesn't matter.

**Files.** `runtime-go/rt/rt.go` (`Eq`).

**Test.** `runtime-go/rt/eq_deep_test.go`: list equality, nested map
equality, Maybe across instantiations.

**Acceptance.** Remove the "extract a scalar first" workaround from
`tests/Core/CoreTest.sky` and verify the test still passes.

---

## P1 — Security hardening

### [ ] P1-1 Sky.Http.Server CSRF support
**Problem.** No CSRF token infrastructure. POST routes are forgeable.

**Fix.** `Sky.Http.Server.csrfToken : Request -> String` + companion
`verifyCsrf : Request -> Bool` using double-submit cookie pattern.
Middleware helper that rejects unmatched POST/PUT/DELETE.

**Files.** `runtime-go/rt/rt.go`, `sky-stdlib/Sky/Http/Server.sky` (if
materialised), `examples/15-http-server/src/Main.sky` adds a CSRF'd
form to demonstrate.

**Test.** `runtime-go/rt/csrf_test.go` covers token issuance + verify.

**Acceptance.** `sky verify 15-http-server` exercises a CSRF'd form path.

### [ ] P1-2 Rate limiting middleware
**Problem.** `MaxBytesReader` caps body size but not request rate.
A single curl loop can exhaust the server.

**Fix.** `Sky.Http.Server.rateLimit : Int -> Handler -> Handler` takes
req-per-minute cap. In-memory sliding-window counter keyed by client IP.

**Files.** `runtime-go/rt/rt.go` (new middleware + test).

**Acceptance.** `rateLimit 60 handler` returns 429 after 60 requests in
60s from the same IP.

### [ ] P1-3 `Db.findWhere` / raw SQL: prepared statements only
**Problem.** `Db_findWhere` documents "never splice untrusted input" but
the type allows it. Classic SQLi surface.

**Fix.** Sky-level API: deprecate the string-WHERE form. Introduce
`Db.findOneByField`, `Db.findManyByField`, `Db.findByConditions` that
take typed predicates. Raw string WHERE requires explicit
`Db.unsafeRawWhere` with a doc link to the injection risk.

**Files.** `runtime-go/rt/db_auth.go`, `sky-stdlib/**/Db.sky`.

**Test.** `runtime-go/rt/db_sqli_test.go`: attempting `findWhere` with
a payload containing `; DROP TABLE` on a column value is handled by
the parameter binding, not the query string.

**Acceptance.** `Db.findWhere` grep gate: no example uses the raw form
after this change (only `unsafeRawWhere` with a justifying comment).

### [ ] P1-4 Auth secrets: typed, minimum length
**Problem.** `Auth_signToken(secret any, ...)` stringifies via
`fmt.Sprintf("%v", secret)`. Accidentally passing a non-String compiles
and produces a wrong HMAC (C6 in the audit).

**Fix.** Sky-level type is `String -> Dict -> Int -> Result Error String`.
The Go wrapper asserts `.(string)` explicitly (not `%v`) and returns
`Err (InvalidInput "secret too short")` when `len(secret) < 32`.

**Files.** `runtime-go/rt/db_auth.go` (Auth_signToken, Auth_verifyToken).

**Test.** `runtime-go/rt/auth_secret_test.go`: non-string secret
produces `Err`. Short secret produces `Err InvalidInput`.

**Acceptance.** P1-4 test green.

### [ ] P1-5 Cookie defaults: `Secure` + production-mode panic logs
**Problem.** Server cookies default to `HttpOnly; SameSite=Lax` without
`Secure`. Panic logs include absolute repo paths.

**Fix.**
(a) `Secure` added when `SKY_ENV=prod` or the request was HTTPS.
(b) Panic recovery respects `SKY_ENV`:
    - `dev`: full stack trace (current behaviour).
    - `prod`: method + path + error kind only. Stack trace goes to a
      `.skylog/` rotated file, not stderr.

**Files.** `runtime-go/rt/rt.go` (Server_withCookie, panic-recovery
defer).

**Acceptance.** `SKY_ENV=prod ./sky-out/app` shows only method + path +
error kind on a triggered panic. Stack trace written to disk.

---

## P2 — Soundness work

### [ ] P2-1 AST-level comment preservation
**Problem.** `sky fmt` preserves comments via a post-pass in
`app/Main.hs` that's position-heuristic and can't handle comments
mid-expression (inside `|>` pipelines, between pattern arms, etc.).

**Fix.** Extend `Sky.AST.Source`: `Module` gains
`_comments :: [A.Located String]`. Parser records each `--`/`{- -}`
with its source region. Formatter interleaves comments at emit time.
Retire `preserveTopLevelComments` from `app/Main.hs`.

**Files.** `src/Sky/AST/Source.hs`, `src/Sky/Parse/Space.hs` (capture
instead of skip), `src/Sky/Format/Format.hs` (emit), `app/Main.hs`
(remove post-pass).

**Test.** `test/Sky/Format/CommentPreservationSpec.hs`: round-trip
fixtures with comments in every position (module header, above decl,
between type annotation and body, inside let-body, inside case-arm,
inside pipeline, trailing).

**Acceptance.** Every fixture round-trips byte-identically. The
safety-guard refusal in `app/Main.hs` is gone.

### [ ] P2-2 LSP local-binding types keyed by region
**Problem.** `idxLocalTypes` is `Map FilePath (Map String Type)` — name
collisions across scopes resolved by insertion order, not scope.
Shadowing produces wrong hover.

**Fix.** Extend the solver's `_locals` to accumulate
`[(Region, Name, Type)]`. LSP `lookupLocal` matches by smallest
enclosing region AND name.

**Files.** `src/Sky/Type/Solve.hs`, `src/Sky/Type/Constrain/Expression.hs`
(pass real regions into CLet headers), `src/Sky/Lsp/Index.hs`.

**Test.** `test/Sky/Lsp/HoverShadowingSpec.hs`: shadowed `x` in nested
lets returns the correct inner type on hover.

**Acceptance.** Shadowing test green.

### [ ] P2-3 LSP global TVar renaming
**Problem.** `showType` renames TVars per render. Two hovers in the
same file can show the same internal `t108` as different letters.

**Fix.** `showType` takes a stable renaming context keyed at the module
level. LSP index building computes one renaming per module and caches.

**Files.** `src/Sky/Type/Solve.hs`, `src/Sky/Lsp/Index.hs`.

**Test.** `test/Sky/Lsp/HoverConsistentNamesSpec.hs`: hover two bindings
that share a polymorphic var; assert both render as the same letter.

**Acceptance.** Test green.

### [ ] P2-4 `sky verify` expanded scenarios
**Problem.** Checks HTTP 200 + panic detection. Broken handler that
returns 200 empty passes.

**Fix.** Per-example scenario file (`examples/<n>/verify.json` or
inline in `sky.toml`) declaring a sequence of requests with expected
response substrings / status / header constraints. `sky verify` runs
the script.

**Files.** `src/Sky/Build/Compile.hs` (runVerify), each example's
scenario file.

**Acceptance.** A regression that changes a handler response silently
fails `sky verify`.

### [ ] P2-5 `Sky.Live` session store types round-trip
**Problem.** Non-memory session stores (SQLite/Postgres/Redis) serialise
via gob. Function values, FFI opaque handles, reflect.Values don't
gob-register; first cross-instance deployment corrupts state.

**Fix.** Session store serialises only a whitelist of Go types: numeric
primitives, strings, bools, `SkyResult`/`SkyMaybe`/`SkyTuple`, records
built from these, ADT tags with SkyADT shape. Anything else becomes a
registration error at session-write time, surfaced as `Err`.

**Files.** `runtime-go/rt/live_store.go`.

**Test.** `runtime-go/rt/live_store_roundtrip_test.go`: round-trip
through each backend for the same model; fail fast on unsupported
values.

**Acceptance.** Test covers all four session-store backends.

---

## P3 — Tooling polish

### [ ] P3-1 CI: `sky verify` as canonical runtime check
**Problem.** ci.yml cherry-picks a handful of examples with ad-hoc curl
checks. A broken example can rot unseen (this session: 15-http-server
was broken for an unknown period).

**Fix.** Replace the individual example test steps with a single
`sky verify` invocation. Fyne skip on Linux moves into `sky verify`
itself (not the sweep script).

**Files.** `.github/workflows/ci.yml`, `src/Sky/Build/Compile.hs`
(runVerify respects a skip list).

**Acceptance.** CI passes only when `sky verify` passes every
non-skipped example.

### [ ] P3-2 LSP integration test harness
**Problem.** No tests for `sky lsp`. Hover regressions ship silently.

**Fix.** `test/Sky/Lsp/ProtocolSpec.hs`: spawns `sky lsp`, sends
JSON-RPC init + hover requests for fixture files, asserts the
response payload.

**Files.** New test file, cabal file update to register it.

**Acceptance.** Spec covers: hover on top-level, local let binding,
lambda param, imported name, FFI binding, nested-scope shadowed name.

### [ ] P3-3 Remove TH mtime dance; real runtime dependency
**Problem.** `scripts/build.sh` touches `src/Sky/Build/EmbeddedRuntime.hs`
if any `runtime-go/*` file is newer. `cabal build` doesn't automatically
invoke this, so plain `cabal build` ships a stale runtime.

**Fix.** `EmbeddedRuntime.hs` uses `TemplateHaskell` with an
`addDependentFile` call for every file in `runtime-go/rt/` so cabal
invalidates the TH splice correctly.

**Files.** `src/Sky/Build/EmbeddedRuntime.hs`.

**Acceptance.** Plain `cabal build` after editing `runtime-go/rt/rt.go`
rebuilds and re-embeds without the scripts/build.sh dance.

### [ ] P3-4 `fmt.Sprintf("%v", x)` audit
**Problem.** Multiple runtime sites use `%v`-stringification on
`any` values. Each is a potential silent coercion (secrets, IDs,
query parameters, JSON keys).

**Fix.** Catalogue every occurrence; replace with explicit
type-assert-then-stringify or typed casts. Retain only for genuine
display paths (println, error messages).

**Files.** `runtime-go/rt/*.go`.

**Acceptance.** `grep -n 'fmt\.Sprintf.*%v' runtime-go/rt/` results
are justified by a preceding comment or gated by a test.

---

## P4 — Architecture

### [ ] P4-1 Fully-typed codegen v1.0
**Problem.** `any`-typed function signatures throughout emitted Go.
Runtime catches mismatches late. The original CLAUDE.md "v1.0 goal"
(TODO section) hasn't landed.

**Fix.** See `docs/PRODUCTION_READINESS.md` phases P7/P8 — the typed
dispatch plan. This item is a pointer to that doc; do not duplicate
its content.

**Acceptance.** `grep -nE ' any\b' sky-out/main.go` on every example
drops below the current total by ≥ 90%.

### [ ] P4-2 Haskell-compiler test harness for `tests/**/*Test.sky`
**Problem.** `docs/tooling/testing.md` describes a `sky test` framework
for `tests/**/*Test.sky`. These are self-hosted-compiler-era fixtures
that don't typecheck on the Haskell compiler.

**Fix.** Port or replace `Sky.Test` library so `sky test tests/Core/CoreTest.sky`
runs on the Haskell compiler. Wire into `cabal test` as a new suite.

**Files.** `sky-stdlib/Sky/Test.sky` (materialise from embedded
runtime), `src/Sky/Build/Compile.hs` (cmdTest wires to cabal-test
harness).

**Acceptance.** `cabal test` runs every `tests/**/*Test.sky` file as
its own test case.

---

## Progress tracker

Legend: ☐ not started · ◐ in progress · ☑ done.

| ID | Title | Status | Verified (date, commit) |
|----|-------|--------|-------------------------|
| P0-1 | sky check runs Go emitter | ☑ | 2026-04-15, 660cc66 |
| P0-2 | Silent numeric coercion → fallible | ☑ | 2026-04-15, f56d85c |
| P0-3 | Typed-return coercion generalised | ☑ | 2026-04-15, 96def08 |
| P0-4 | Record field declaration order | ☑ | 2026-04-15, 88bbbf2 |
| P0-5 | Unreachable panic → Err | ☑ | 2026-04-15, 9c3836f |
| P0-6 | FFI skyCallDirect type-safe | ☑ | 2026-04-15, 202f691 |
| P0-7 | Test.equal deep equality | ☑ | 2026-04-15, a85bb70 |
| P1-1 | CSRF support | ☑ | 2026-04-15, 16ba414 |
| P1-2 | Rate limit middleware | ☑ | 2026-04-15, b05c664 |
| P1-3 | Db.findWhere safe API | ☑ | 2026-04-15, ce5ae6b |
| P1-4 | Auth secrets typed + min-length | ☑ | 2026-04-15, ebba5c5 |
| P1-5 | Cookie Secure + prod-mode logs | ☑ | 2026-04-15, 30d2706 |
| P2-1 | AST comment preservation | ☑ | 2026-04-15, c544fe9 |
| P2-2 | LSP local types by region | ☑ | 2026-04-15, cae934c |
| P2-3 | LSP TVar global renaming | ☑ | 2026-04-15, 170b77e |
| P2-4 | sky verify scenarios | ☐ | |
| P2-5 | Session store type safety | ☐ | |
| P3-1 | CI uses sky verify | ☐ | |
| P3-2 | LSP integration tests | ☐ | |
| P3-3 | No-TH-dance runtime dep | ☐ | |
| P3-4 | %v audit | ☐ | |
| P4-1 | Typed codegen v1.0 | ☐ | refers to PRODUCTION_READINESS.md |
| P4-2 | sky test on Haskell compiler | ☐ | |

Last verified green baseline: **2026-04-15** at commit f5f3351.

---

## Completion signal

When every P0–P3 item is ticked and this line is appended below, the
stop-hook stops blocking:

**`## Audit remediation complete`**

Do not add that line prematurely. The stop-hook grep is authoritative.
P4 items continue in their own doc and are out of scope for the
stop-hook gate.
