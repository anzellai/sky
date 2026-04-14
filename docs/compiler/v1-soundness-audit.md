# v1 soundness audit

**Scope:** every path where Sky source type-checks but generated output can still panic or misbehave. Every claim in the documentation and tooling was checked against the actual implementation.

**Honest verdict:** *"if it compiles, it works"* is now **mostly true**, with three explicitly-named exceptions (listed below). Every source-to-runtime shape mismatch discovered during the audit has been closed with a test fixture in `runtime-go/rt/*_test.go` or `test/Sky/**Spec.hs`.

---

## 1. Compiler typing / soundness — fixes

### 1.1 Nested Sky container shape mismatch (`ResultCoerce` / `MaybeCoerce`)

**Before:** when a function's return type was something like `Result Error (Maybe (Dict String String))`, the body constructed `SkyResult[any, any]` of a `SkyMaybe[any]`, but the signature declared `SkyResult[any, SkyMaybe[map[string]any]]`. `coerceInner` panicked with:

```
interface conversion: interface {} is rt.SkyMaybe[interface {}],
not rt.SkyMaybe[map[string]interface {}]
```

**After:** `coerceInner` has a reflect fallback that rebuilds the target struct/slice/map shape element-wise. Handles:

- `SkyMaybe[any]` → `SkyMaybe[ConcreteT]`
- `[]any` → `[]ConcreteT` (recursive)
- `map[K]any` → `map[K]ConcreteT` (recursive)
- `SkyResult[any, any]` → `SkyResult[E, A]`
- Nested combinations of the above

**Regression tests:** `runtime-go/rt/coerce_test.go` — seven fixtures covering Just+map, Nothing, []row, dict, dict-of-dict, MaybeCoerce, error-side.

### 1.2 ADT struct type-identity mismatch

**Before:** rt-side `ErrIo`/`ErrNetwork`/etc produced `skyErrorAdt` values. User Sky code matching `case e of Error kind info -> ...` lowered to `any(e).(Sky_Core_Error_Error)` — a distinct Go type with identical layout. The type assertion panicked with:

```
interface conversion: interface {} is rt.SkyADT,
not main.Sky_Core_Error_Error
```

**After:** every Sky-emitted non-enum ADT now emits as `type <ModQualName> = rt.SkyADT` — a Go type alias. `rt.skyErrorAdt` and `rt.skyMaybeAdt` are internal aliases of `rt.SkyADT`. rt-side builders' values flow through user case expressions with no type-assertion panic possible.

**Regression tests:** `runtime-go/rt/error_adt_shape_test.go` — asserts `ErrIo` produces `SkyADT` and round-trips through a `SkyADT`-aliased user type.

### 1.3 User-source error wrapping

**Before:** examples' `Lib.Db` / `Lib.Auth` wrappers did `Err e -> Err (Error.io e)`, asserting `e` as `String`. Since `rt.Db_exec` / `rt.Http_get` / similar now return structured `Error` values in the Err slot, this asserted a struct as `string` and panicked.

**After:** seven example files (Lib/Db in 08, 12, 16; Auth/Alerts/Monitors/Metrics/SafeQuery in 17; Db in 13) rewritten to pass the Error through: `Err e -> Err e`. The stdlib contract is now uniform: every fallible kernel returns a Sky-side `Error`, callers that want to reshape use `Error.withMessage` / `Error.withDetails`.

---

## 2. Error system — audit outcome

- No public `Result String` in `src/`, `sky-stdlib/`, or any `examples/*/src/` — enforced by `Sky.ErrorUnificationSpec` (forbidden-grep gate in `test/`).
- No public `Task String` — same enforcement.
- `Std.IoError` (the old error type) deleted — no dangling imports.
- `RemoteData` (the old async-state type) deleted — no dangling imports.
- rt-side `Err*` builders (`ErrIo`, `ErrNetwork`, `ErrFfi`, `ErrDecode`, `ErrTimeout`, `ErrNotFound`, `ErrPermissionDenied`, `ErrInvalidInput`, `ErrConflict`, `ErrUnavailable`, `ErrUnexpected`) all produce `rt.SkyADT` values shaped identically to user-defined `Sky.Core.Error.Error`.
- `Error.toString`, `Error.isRetryable`, `Error.withMessage`, `Error.withDetails` all work on the canonical ADT.

---

## 3. Sky.Live — audit outcome

### What was verified

- Session round-trip: `encoding/gob` serialises model + VNode tree. `gobRegisterAll` walks every value reachable from the session at encode time, registering struct types by their runtime identity. With the ADT-alias fix above, rt-side error values and user-side error values register under the same type (`rt.SkyADT`) so they round-trip correctly across session boundaries.
- View handler invocation: `sky_call(app.view, model).(VNode)` at `live.go:1406` is guaranteed well-typed by `sky check` enforcing that `view : Model -> Html Msg` returns `VNode`.
- Command dispatch: `cmd.(cmdT)` at `live.go:1602` is guarded with `ok` — a bad command produces a no-op log line, not a panic.
- Subscription dispatch: `subResult.(subT)` at `live.go:1645` is guarded.
- Event payload decode: `json.Unmarshal` at `live.go:1451` returns an error, not a panic.

### What is explicit debt

- Reflection in session store's `gobRegisterAll` walker remains — required because Go structs have no schema.
- `sky_call(app.view, model).(VNode)` is an unguarded assertion. Well-typed Sky source cannot reach it with a non-VNode, but a runtime that accepts an external model (via session store cross-version) can. Acceptable for single-version deployments; document.

---

## 4. LSP — audit outcome

The LSP declares capabilities that were previously under-documented:

- `hoverProvider`, `definitionProvider`, `declarationProvider`, `documentSymbolProvider`, `documentFormattingProvider`, `referencesProvider`, `renameProvider` (with `prepareProvider`), `signatureHelpProvider`, `codeActionProvider` (`quickfix`, `source.organizeImports`), `semanticTokensProvider` (full), `completionProvider` (triggered on `.`).

**Truth audit outcome:** `docs/tooling/lsp.md` rewritten with an honest capability matrix (feature × symbol-class grid). Known limitations explicitly listed: unqualified completion not surfaced, single-project workspaces only, rename doesn't touch dependencies, no code lens / inlay hints.

No LSP bugs were fixed in this audit — the documentation simply stopped understating the implementation.

---

## 5. Formatter — audit outcome

### What was broken

1. **String literal escape drop.** `fmtExpr (Src.Str s) = "\"" ++ s ++ "\""` didn't re-escape embedded `"`, `\n`, `\t`, `\r`, or `\\`. First-pass format dropped the escapes; second-pass reparsed as multiple tokens. Hit in `examples/15-http-server/src/Main.sky` on a JSON literal.

2. **Scientific-notation float parse gap.** `Sky.Parse.Number` handled `123.456` but not `5.0e-2` or `1e6`. Haskell's `show` emits scientific notation for small floats, so `show 0.05 == "5.0e-2"` — the formatter produced output the lexer couldn't read. Hit in `examples/08-notes-app/src/Lib/View.sky` and `examples/12-skyvote/src/Ui/Styles.sky` via `Css.rgba ... 0.05`.

### What was fixed

- `escapeStringLit` and `escapeMultilineLit` in `Sky.Format.Format` now escape `\\`, `\"`, newlines, tabs, and carriage returns. Triple-quoted strings also escape embedded `"""` runs.
- `Sky.Parse.Number` now parses an optional `[eE][+-]?[0-9]+` exponent after the mantissa, and accepts integer-mantissa-with-exponent as a Float (`1e6 → 1000000.0`).
- Idempotency verified on all 90 `.sky` source files across `examples/*/src/**/*.sky`.

**Regression tests:** `test/Sky/Format/FormatSpec.hs` — six fixtures covering JSON strings, scientific floats, multiline interpolation, record updates, nested case, long pipelines. Each asserts byte-identical output after two passes.

---

## 6. `sky build` / `sky check` — audit outcome

- Both now auto-regenerate missing Go FFI bindings from declared `[go.dependencies]` before running. Clean-slate builds no longer require the user to run `sky install` first.
- `sky clean` removes `sky-out/`, `.skycache/`, `.skydeps/`, `dist/` — explicit and stated.
- Behaviour gap hunt turned up no case where `sky check` passes but `sky build`/`run` panics due to type-representation reasons — every such case found in this audit has been fixed at the root-cause layer (coerceInner, ADT alias, error pass-through).

---

## Remaining explicit debt

Three areas where the implementation is intentionally not as strong as a reader might expect:

### A. Reflection in runtime boundary helpers

`ResultCoerce` / `MaybeCoerce` / `coerceInner` use reflection to reconstruct parametric Sky containers across Go's generic-type-identity boundary. This is the cost of Sky's any-boxed-by-default calling convention at the function-return boundary. Typed FFI + typed kernel dispatch routes around reflection where possible (~900 typed call sites land per sweep run); reflection only fires at the narrow function-return wrap point.

**Status:** acceptable by design for v1. A future rewrite could eliminate reflection entirely by threading concrete HM types through codegen at construction sites — that's documented as the long-term typed-body direction.

### B. View return type assumption

`sky_call(app.view, model).(VNode)` in `runtime-go/rt/live.go` is an unguarded type assertion. If an externally-provided `app.view` returns something other than `VNode`, this panics. Well-typed Sky source enforces `view : Model -> Html Msg` via `sky check`, so this is unreachable in supported code paths. A `ok`-guarded assertion would trade one panic for a different failure mode (silent wrong-type behaviour); leave as-is.

**Status:** acceptable by design for v1. Requires trust that `sky check` is used.

### C. Session store version drift

`encoding/gob` registers concrete Go types at encode time. A session written by binary V1 and decoded by binary V2 (where V2 has different struct layouts for the same Sky types) will fail to decode. This is a deployment concern, not a soundness concern: operators are expected to invalidate sessions across schema-breaking deploys.

**Status:** acceptable by design for v1. Document.

---

## Verification

- `cabal test` — all suites green (ExampleSweep 18/18, TypedFfi, ErrorUnification, Exhaustiveness, Exposing, Pattern, Compile, Format).
- `scripts/example-sweep.sh --build-only` — 18/18.
- `runtime-go/rt/go test ./rt/` — 9/9 (coerce + error ADT regression tests).
- `find . -type d -name ffi` — returns nothing outside `.skycache/`.
- Formatter idempotency — all 90 example `.sky` source files byte-identical after two passes.

---

## Honest summary

> **"If it compiles, it works."**

This is now **true for every example project in the sweep and every path exercised by the test suite**. The three residual areas above are documented rather than hidden. The residual reflection helpers are load-bearing for Sky's calling convention at a specific boundary — they don't lie about types, they bridge between concrete Go generic instantiations that Go's type system treats as distinct.

No panic was observed during runtime validation of the skyvote / notes-app / skymon paths that previously failed. If a new panic is reported, the workflow is:

1. Reproduce with a minimal Sky source fixture.
2. Add a failing test in `runtime-go/rt/*_test.go` or `test/Sky/**Spec.hs`.
3. Fix at the root cause (codegen, runtime helper, or compile-time checker), not by adding another reflect fallback.
4. Land the fix + test together.
