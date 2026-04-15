# Testing Sky projects

Sky ships with a first-class test framework: the `Sky.Test` stdlib module plus a `sky test` CLI command. Tests are plain Sky code and benefit from the same type checker, pattern exhaustiveness, and Error system as production code.

## Writing a test module

Every test module exposes a single `tests : List Test` value. Tests can be individual assertions or grouped into suites.

```elm
module StringTest exposing (tests)

import Sky.Core.Prelude exposing (..)
import Sky.Core.String as String
import Sky.Test as Test exposing (Test)


tests : List Test
tests =
    [ Test.test "trim removes outer spaces" (\_ ->
        Test.equal "hi" (String.trim "  hi  "))
    , Test.test "contains finds substring" (\_ ->
        Test.isTrue (String.contains "ell" "hello"))
    , Test.test "toInt rejects junk" (\_ ->
        Test.err (String.toInt "abc"))
    ]
```

The `(\_ -> ...)` thunk wraps each assertion so a panic in one test doesn't abort the rest of the suite.

## Assertions

From `Sky.Test`:

| Function | Use |
|----------|-----|
| `equal : a -> a -> TestResult` | strict equality on primitives / records / ADTs |
| `notEqual : a -> a -> TestResult` | negation |
| `ok : Result e a -> TestResult` | asserts `Ok _` |
| `err : Result e a -> TestResult` | asserts `Err _` |
| `expectErrorKind : ErrorKind -> Result Error a -> TestResult` | asserts specific kind |
| `isTrue : Bool -> TestResult` | asserts `True` |
| `isFalse : Bool -> TestResult` | asserts `False` |
| `fail : String -> TestResult` | unconditional failure with message |
| `pass : TestResult` | unconditional pass |

## Running tests

```bash
# From your project root (containing sky.toml):
sky test tests/MyTest.sky

# Or from any directory:
cd tests && sky test Core/CoreTest.sky
```

Exit code:

- `0` — every test passed.
- `1` — one or more tests failed.
- `2` — build failed before any test ran.

Output format:

```
  ok    String.trim
  ok    String.toUpper
  FAIL  String.split non-empty
          expected True, got False
5 passed, 1 failed (6 total)
```

## Module discovery

`sky test` synthesises an entry module that imports your test module and calls `Sky.Test.runMain tests`. The synthesis derives the module name from the path:

- `src/Foo/BarTest.sky` → `Foo.BarTest`
- `tests/Core/CoreTest.sky` (with `[source] root = "tests"`) → `Core.CoreTest`

The test file's module declaration must match this derived name. Directory segments are auto-capitalised (`tests/core/` → `Core.`).

## Testing `Result` / `Error`

```elm
Test.test "network errors carry retry hint" (\_ ->
    case Http.get "https://example.com/down" of
        Ok _ ->
            Test.fail "expected failure"

        Err e ->
            Test.isTrue (Error.isRetryable e)
    )
```

For `Result Error a` values, `expectErrorKind` is concise:

```elm
Test.test "unauthorised returns PermissionDenied" (\_ ->
    Test.expectErrorKind PermissionDenied
        (Auth.authenticateUser "bad@email" "wrong-password"))
```

## Example-level verification

For end-to-end verification of example projects (build + run + panic detection + HTTP probe), `sky verify` is the harness. Use `sky test` for unit-level stdlib and app logic; use `sky verify` for full-stack example regression.

## Regression discipline

Every bug that reaches production gets a permanent regression test:

1. Reproduce with a minimal Sky fixture.
2. Add the failing test under `tests/` (or as a cabal-level `Sky.Build.*Spec` / `runtime-go/rt/*_test.go` if the bug lives in the compiler or runtime).
3. Fix the root cause.
4. Verify the regression test passes with the fix and fails without it.

Current permanent regressions:

- `test/Sky/Build/NestedPatternSpec.hs` — nested `Ok (Just x)` / `Ok True` discrimination.
- `runtime-go/rt/coerce_test.go` — nested `SkyMaybe[X]` / `[]T` / `map[K]T` shape-mismatch via `ResultCoerce`.
- `runtime-go/rt/error_adt_shape_test.go` — rt `ErrIo` values are type-compatible with user-side `Sky_Core_Error_Error`.
- `test/Sky/Format/FormatSpec.hs` — formatter idempotency (string escapes, scientific-notation floats, nested case, long pipelines, record updates).
- `test/Sky/ErrorUnificationSpec.hs` — forbidden-pattern greps: `Result String`, `Task String`, `IoError`, `RemoteData`.
- `tests/Core/CoreTest.sky` — 22 stdlib semantic tests (String / List / Dict / Maybe / Result).
- `tests/Lang/PatternTest.sky` — 10 pattern-matching tests (nested Result/Maybe, enum ADT, Bool-inside-Ok).
- `tests/Live/CounterTest.sky` — 19 Sky.Live TEA loop tests (init / update / model invariants / event dispatch).
- `tests/Live/FormTest.sky` — 20 Sky.Live form-handling tests (validation / state machine transitions / sign-out).
- `tests/Live/SessionTest.sky` — 18 Sky.Live subscription + session round-trip tests.

## Known limits

- **Nested `Test.suite`** — currently hits a `SkyCall` shape issue when the outer list is walked via `List.map` over an ADT-pattern-match closure. Use a flat `List Test` until fixed.
- **`Test.equal` on lists/records** — Sky's `==` operator delegates to `rt.Eq` which doesn't deep-compare lists. For collections, extract a scalar (`List.length`, `List.head`) and assert on that.
