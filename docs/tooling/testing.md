# Testing Sky projects

> **Status**: the Rust compiler (`rust/`, `cargo build --release -p sky`)
> is the primary Sky compiler; the Haskell compiler is preserved under
> `legacy-haskell-compiler/`. Verified by the example sweep + compiler test
> suite (`cargo test` + xtask gates). See
> [`../../CHANGELOG.md`](../../CHANGELOG.md) for the changelog. (This link
pointed at `../compiler/versions.md`; there is no `docs/compiler/` directory.)


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

## Machine-readable output (`SKY_TEST_JSON`)

Set `SKY_TEST_JSON` to a path and the run additionally writes a per-case JSON
report. The human output above is byte-identical either way, so turning this on
never changes what you read in the terminal.

```bash
SKY_TEST_JSON=/tmp/report.json sky test tests/MyTest.sky
```

```json
{
  "schema": "sky-test/v1",
  "cases": [
    { "name": "String.trim", "outcome": "pass", "assertions": 1, "message": "" },
    { "name": "String.split non-empty", "outcome": "fail", "assertions": 1,
      "message": "expected True, got False" }
  ],
  "total": 6, "passed": 5, "failed": 1, "assertions": 6
}
```

`name` is the fully-qualified leaf name, so `suite` labels appear exactly as the
human summary prints them. This is what lets a CI gate attribute a failure to a
**specific** test case rather than to a whole suite, and what lets it assert an
exact case count so a suite that silently stops running cases fails instead of
passing with fewer.

Two limits, so they are not mistaken for bugs:

- **`assertions` is 1 per case.** A `Test` leaf is `() -> TestResult` and yields
  exactly one result. The count that detects a shrinking suite is the
  suite-level `total`.
- **There is no file/line.** `Test.test` carries a name and a thunk; Sky has no
  source-location intrinsic, so the name is a case's only stable identity.

`Sky.Test.jsonReport` (pure, `List (String, TestResult) -> String`) and
`Sky.Test.writeJsonReport` (a `Task`, no-op on an empty path) are exposed for
callers that drive `Sky.Test.run` themselves.

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

## Test mode: offline effects and mock-by-default

A scenario test needs the app's effects to run without a live world — no real
network, no shared database, a fixed clock. Sky owns its effect boundary, so it
supplies that world itself. Test mode is **opt-in per project**: a project that
has a `.env.test` file runs `sky test` in test mode; a project without one runs
exactly as before.

In test mode the runner:

- sets `SKY_TEST_MODE=1`;
- loads `.env.test`, then `.env.test.local` (override) into the app's
  environment. `.env.test` is the committed, non-secret config and mock toggles;
  `.env.test.local` is gitignored, for sandbox credentials.

### Mock-by-default outbound HTTP

With `SKY_TEST_MODE` on, the shared HTTP client's transport intercepts **every**
outbound request, so no test ever touches the real network:

- a request that matches a fixture returns that fixture's canned status and body;
- an **unmatched request fails closed** with a transport error. That surfaces to
  the app as an `Http` error, so the app's failure path runs automatically. The
  default in test mode is therefore deterministic failure-mode coverage, with no
  per-test code.

A fixture is one JSON file under `tests/mocks/` (or the dir named by
`SKY_TEST_MOCKS_DIR`). The shape:

```json
{
  "match": { "method": "POST", "urlContains": "/v1/checkout/sessions" },
  "status": 200,
  "body": "{\"id\":\"cs_test_123\",\"payment_status\":\"paid\"}"
}
```

- **`match.method`** — optional. Empty or absent matches any method
  (case-insensitive otherwise).
- **`match.urlContains`** — optional substring of the full URL. Empty or absent
  matches any URL, so one fixture can cover a whole host or path prefix.
- **`status`** — the response status; `0` or absent means `200`.
- **`body`** — the response body, verbatim. Paste a real captured payload here.

Fixtures load once per run and the **first match wins, in filename-alphabetical
order**. A broad fixture (`urlContains: ""`) shadows the ones after it, so name a
specific fixture to sort before a general one (`00-charge-ok.json` before
`99-catch-all.json`).

You do not have to memorise the shape: an unmatched request prints it. The
fail-closed error names the method, the URL, the mocks dir, and the exact
`{match:{method,urlContains},status,body}` skeleton to add.

### Success, pending, failure — several outcomes for one call

- **Different outcomes on different URLs** — one fixture each. This is the common
  case (a `create` call and a `retrieve` call have different URLs).
- **The failure outcome comes for free** — omit the fixture (fail-closed), or
  give it a `status` of `402`/`500` with an error `body`.
- **The same URL, different outcomes** — keep separate mock directories
  (`tests/mocks/success/`, `tests/mocks/pending/`, `tests/mocks/failure/`) and
  select one per run with `SKY_TEST_MOCKS_DIR`. Fixtures load once per process,
  so this switch is per-run.

Two shapes are **not** expressible today, and are worth knowing before you design
around them: two responses for the **same** method+URL told apart by request
**body**/query/headers (the matcher does not read the body), and a **sequenced**
response (first call `pending`, second `succeeded`). Use distinct URLs or
separate directories until the matcher grows those fields.

### Determinism (opt-in, decoupled from test mode)

- **`SKY_TEST_SEED=<int>`** seeds `Random` and `Uuid`, so a generated code, token
  or id is reproducible — a test can predict or read back what the app produced.
- **`SKY_TEST_CLOCK_MS=<ms>`** pins `Time.now` to a fixed instant.

These are **decoupled** from `SKY_TEST_MODE` on purpose: without a seed,
`Data.newId ()` stays unique across runs, so a DB test does not collide with
itself. Opt into a seed only when you want reproducibility.

### Ephemeral database

A project that declares a `[database]` but is given no DSN gets an offline
database for the run, thrown away at the end. The engine decides how: a
**Postgres** app gets a throwaway embedded cluster; a **SQLite** app has its path
redirected to a scratch file (it is already offline). So a scenario needs no live
database. A DSN in the environment (`DATABASE_URL`) opts back out — the test then
targets that database.

### Log capture

Set **`SKY_TEST_LOG_CAPTURE=<path>`** and the app's `Std.Log` output is written
to that file. A scenario reads it back with `File.readFile` to assert on what the
app logged — or to recover a value the app would otherwise only have emailed, for
example a verification code, which makes the "check your email" step readable
in-process.

### A verification-flow scenario, offline

These features compose into a real account-creation + code-verification test with
no external world. Signup runs under a fixed `SKY_TEST_SEED`, so the generated
code is reproducible; the outbound "send email" call is intercepted by a fixture
(or read back from the ephemeral DB / the log capture); the test then submits the
code and asserts the account is active. The only thing that stays mocked is a
real message actually delivered by a third party — automated tests prove the
app's handling of the flow, never a provider's delivery.

## Property-based fuzzing — `sky fuzz [--target <t>]`

One command derives its inputs from the app's own types, so it needs no
hand-written oracle. It runs one or two nets, chosen by `--target`.

```bash
sky fuzz src/Main.sky --iters 500 --seed 42          # model no-panic net
sky fuzz src/Main.sky --target web:app --iters 500   # + the differential split oracle
```

### The model no-panic net (always, any TEA app)

`sky fuzz` derives a `Msg` generator from the app's own `Msg` union, folds random
`Msg` sequences from `init ()` through the real `update`, and asserts no
**unclassified** panic. Every sequence is a valid input by construction — a
client can send any `Msg`, in any order — so it drives exactly the hostile,
out-of-order input a real client can (an `EnterCode` before any signup, a
`SubmitCode` while anonymous). It works on **any** TEA app, Spa or Live, because
it needs only `(Model, Msg, update)`, and it runs under test mode with the
offline database above, so a DB-backed app fuzzes offline. It exits `0` on PASS
and non-zero on the first sequence that panics, reproducible with the same
`--seed`.

### The differential split oracle (Sky.Spa client targets)

When `--target` selects a client/server split (a Sky.Spa wasm client: `web:app`,
`mobile*`, `desktop:<os>`, `tablet:<os>`; the default is the project's
`sky.toml [app] target`), `sky fuzz` ALSO runs each random `(Model, Msg)` two
ways — directly, and through the client/server split plumbing (build the request
from the read-set, reconstruct the server model, apply the write-set delta) — and
asserts the two results agree. A dropped read or a `Msg`-argument collision
diverges the two paths and is caught mechanically, with no oracle and no real
credentials. A non-split target (or none) runs the model net alone; a `whole
update` app with no resolvable `case msg of` skips the oracle with a note rather
than failing. (This replaces the former `sky spa-diff-fuzz` verb.)

## Scaffolding the fixtures — `sky test --scaffold-mocks`

You do not have to hand-write the fixtures. The compiler knows which outbound
calls the app makes, and to which URLs, because that is in the typed IR. Run:

```bash
sky test --scaffold-mocks
```

It walks the project's own modules for outbound `Sky.Core.Http` calls and writes
one skeleton per distinct call under `tests/mocks/`, with `match.method` and
`match.urlContains` pre-filled. It recovers the URL from the shapes real code
uses, not only a bare literal: a direct `Http.get "https://…"`, a piped
`… |> Http.request` builder, and a `base ++ "/path"` concat (whose literal path
suffix becomes a host-independent `urlContains`). It never overwrites an existing
fixture, and a call whose URL is fully computed gets a blank `urlContains` for
you to narrow.

You fill one thing: the response `body`. Paste a captured real payload there. A
captured payload is the most faithful body, because it proves the app's decoder
handles the shape the provider actually sends, not only the shape the app models.

The fixture schema above plus the self-documenting fail-closed error are enough
to hand-write or adjust a mock by hand when you prefer.

## Regression discipline

Every bug that reaches production gets a permanent regression test:

1. Reproduce with a minimal Sky fixture.
2. Add the failing test under `tests/` (or as a Rust `cargo test` spec / `runtime-go/rt/*_test.go` if the bug lives in the compiler or runtime).
3. Fix the root cause.
4. Verify the regression test passes with the fix and fails without it.

Current permanent regressions:

- `legacy-haskell-compiler/test/Sky/Build/NestedPatternSpec.hs` — nested `Ok (Just x)` / `Ok True` discrimination.
- `runtime-go/rt/coerce_test.go` — nested `SkyMaybe[X]` / `[]T` / `map[K]T` shape-mismatch via `ResultCoerce`.
- `runtime-go/rt/error_adt_shape_test.go` — rt `ErrIo` values are type-compatible with user-side `Sky_Core_Error_Error`.
- `legacy-haskell-compiler/test/Sky/Format/FormatSpec.hs` — formatter idempotency (string escapes, scientific-notation floats, nested case, long pipelines, record updates).
- `legacy-haskell-compiler/test/Sky/ErrorUnificationSpec.hs` — forbidden-pattern greps: `Result String`, `Task String`, `IoError`, `RemoteData`.
- `tests/Core/CoreTest.sky` — **30** stdlib semantic tests (String / List / Dict / Maybe / Result). (Said 22; `grep -o 'Test\.test' tests/Core/CoreTest.sky | wc -l` → 30. The other seven counts in this list match exactly, so this one had genuinely drifted.)
- `tests/Lang/PatternTest.sky` — 10 pattern-matching tests (nested Result/Maybe, enum ADT, Bool-inside-Ok).
- `tests/Live/CounterTest.sky` — 19 Sky.Live TEA loop tests (init / update / model invariants / event dispatch).
- `tests/Live/FormTest.sky` — 20 Sky.Live form-handling tests (validation / state machine transitions / sign-out).
- `tests/Live/SessionTest.sky` — 18 Sky.Live subscription + session round-trip tests.
- `tests/Server/HttpServerTest.sky` — 43 Sky.Http.Server pure-seam tests (route matching, path params, response builders, request record shape, status classification).
- `tests/Auth/AuthTest.sky` — 28 Sky.Auth state-machine tests (sign-in success/failure, sign-out, session resume, error classification, authenticated/unauthenticated invariants).
- `tests/Db/DbTest.sky` — 28 Std.Db pure-seam tests (row building, field extraction, exec/query simulation, not-found vs. error, structured-error mapping).

## Known limits

- **Nested `Test.suite`** — currently hits a `SkyCall` shape issue when the outer list is walked via `List.map` over an ADT-pattern-match closure. Use a flat `List Test` until fixed.
- **`Test.equal` is deep-structural.** `==` / `Test.equal` go through `rt.sky_equal`, which recurses into ADTs / records / lists / dicts — `runtime-go/rt/eq_deep_test.go` exercises the deep-list / deep-map / cross-instantiation paths. (Older versions of this page warned that collections needed scalar extraction; that workaround is no longer required.)
