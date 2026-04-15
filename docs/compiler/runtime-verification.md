# Runtime verification

Build success is necessary but not sufficient. Sky's runtime verification layer ensures the contract "if it compiles, it works" is enforced end-to-end, not only at `sky build` time.

## Layers

### 1. Build-only sweep

```bash
scripts/example-sweep.sh --build-only
```

Runs `sky build` for every example from a clean slate. Fastest gate; catches codegen / typing regressions at compile time.

### 2. Runtime verification

```bash
scripts/verify-examples.sh               # all examples
scripts/verify-examples.sh 12-skyvote    # one example
scripts/verify-examples.sh --build-only  # skip runtime phase
```

Builds and RUNS every example. Fails if any of:

- `sky build` exits non-zero.
- Built CLI binary exits non-zero.
- stderr contains `panic:` / `runtime error:` / `[sky.live] panic`.
- Server examples: HTTP probe on the configured port returns non-2xx/3xx, or the process emits a panic within the probe window.
- CLI examples with an `expected.txt`: stdout differs from expected.

Classification:

- **cli** (01, 02, 03, 04, 06, 07, 14): run and expect graceful exit.
- **server** (05, 08, 09, 10, 12, 13, 15, 16, 17, 18): boot, probe the port, kill.
- **gui** (11): build-only (requires display).

### 3. Forbidden-pattern gate

```bash
scripts/check-forbidden.sh
```

Fails if any Sky source (under `src/`, `sky-stdlib/`, `examples/*/src/`) contains:

- `Result String …` / `Task String …` — pre-v1 stringly error types.
- `Std.IoError` — deleted pre-v1.
- `RemoteData` — deleted pre-v1.

Mirrored as a cabal test in `test/Sky/ErrorUnificationSpec.hs`.

### 4. Cabal test suite

```bash
cabal test
```

Runs eight spec modules:

| Spec | Scope |
|------|-------|
| `Sky.Build.CompileSpec` | Basic build correctness |
| `Sky.Parse.PatternSpec` | Parser regression (P1) |
| `Sky.Canonicalise.ExposingSpec` | Import validation (P2) |
| `Sky.Type.ExhaustivenessSpec` | Pattern exhaustiveness (P3) |
| `Sky.Format.FormatSpec` | Formatter idempotency fixtures |
| `Sky.Build.NestedPatternSpec` | Nested ctor-pattern discrimination (skyvote regression) |
| `Sky.Build.TypedFfiSpec` | Typed FFI wrapper + call-site migration |
| `Sky.ErrorUnification` | `Result String` / `Task String` / `IoError` / `RemoteData` forbidden gates |
| `Sky.Build.ExampleSweep` | Build-only sweep as a cabal check |

### 5. Runtime unit tests in `runtime-go/rt/`

```bash
cd runtime-go && go test ./rt/
```

Runs Go-side regression tests:

- `coerce_test.go` — ResultCoerce / MaybeCoerce with nested Sky shapes.
- `error_adt_shape_test.go` — rt-side `ErrIo` / `ErrNetwork` values are type-compatible with Sky-emitted `Sky_Core_Error_Error`.

## What each layer catches

| Bug class | Build-only | Verify | Cabal | rt go test |
|-----------|:----------:|:------:|:-----:|:----------:|
| Codegen produces invalid Go | ✔ | ✔ | ✔ | |
| Type error survives to runtime | | ✔ | regression-specific | |
| ADT shape mismatch (skyvote bug) | | ✔ | ✔ | ✔ |
| Formatter loses data | | | ✔ | |
| Forbidden-pattern regression | | | ✔ | |
| Panic in Sky.Http.Server handler | | ✔ | | |
| Sky.Live session round-trip | | ✔ partial | | |
| HM type-check regression | | | ✔ | |
| Exhaustiveness regression | | | ✔ | |

## Acceptance gates

Before landing a PR that touches codegen / runtime / stdlib:

```bash
# fastest signal (build-only): ~3 min
scripts/example-sweep.sh --build-only

# correctness (runtime): ~5 min
scripts/verify-examples.sh

# forbidden-pattern gate: <1 s
scripts/check-forbidden.sh

# full suite (includes above + cabal specs): ~20 min
cabal test

# rt regression tests
(cd runtime-go && go test ./rt/)
```

All five must pass. The v1 soundness audit at `docs/compiler/v1-soundness-audit.md` enumerates the debt items that remain acceptable by design.

## Known-gap runtime failures (non-regressions)

At the time of writing, `scripts/verify-examples.sh` reports four pre-existing runtime failures that are NOT compiler soundness bugs:

- **05-mux-server** — Sky handler emits `func(any, any) any` but gorilla/mux expects `func(http.ResponseWriter, *http.Request)`. FFI callback wrapping gap.
- **06-json** — JSON pipeline decoder invariant violation in `optionalExample`. User-code type mismatch.
- **08-notes-app / 15-http-server** — HTTP 500 from `Server.get` handler type mismatch: Sky source declares `handleLanding : a -> Request -> Task Error Response` (two params) but `Sky.Http.Server` expects `Request -> Task Error Response` (one param).
- **13-skyshop** — panic during Sky.Live session handling path.

These are tracked as separate backlog items, not v1 acceptance gates. The verify script surfaces them so they don't silently accumulate.
