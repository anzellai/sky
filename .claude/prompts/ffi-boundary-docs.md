# FFI boundary docs + samples + tests cleanup

**Goal:** after landing the P0–P3 FFI boundary fixes (commit e1faa21),
update every doc, code sample, test, and template so they accurately
reflect the new return-type mapping. Do not stop until every item
below is ticked and the verification gates pass.

---

## Items

### 1. Update return-type mapping tables

Files to update:

- `docs/ffi/go-interop.md` — the "Return type mapping" table
- `CLAUDE.md` — the "Type Mapping" table under "Go FFI / Interop Model"
- `templates/CLAUDE.md` — the user-facing template's FFI section

New mapping (replaces old):

| Go return | Sky type |
|---|---|
| `T` (single, no error, non-pointer) | `Result Error T` |
| `*T` (single pointer, no error) | `Result Error (Maybe T)` |
| `(T, error)` / `error` | `Result Error T` / `Result Error ()` |
| `(T, bool)` | `Result Error (Maybe T)` |
| `(T, *NamedError)` where NamedError implements error | `Result Error T` |
| `(T, U)` (neither error nor bool) | `Result Error (T, U)` |
| `*T` (method-returning) | getters/setters have nil-receiver checks |

Add a note: "All FFI calls return `Result Error T`. This is
intentional — the FFI boundary is a trust boundary. See
`docs/ffi/boundary-philosophy.md`."

### 2. Write `docs/ffi/boundary-philosophy.md`

New doc explaining the design:

- Sky's stdlib is the preferred path (no Result tax)
- Go FFI is the escape hatch (Result tax = intentional friction)
- The analogy: Elm ports = typed airlock to untrusted JS;
  Sky FFI = typed airlock to unchecked Go
- Result vs Task: Result for synchronous FFI (call already happened);
  Task for deferred Sky effects (hasn't run yet)
- Why this is different from Rust's `unsafe` (Go isn't unsafe,
  it's just untyped from Sky's perspective)

### 3. Update code samples in docs

Grep all `.md` files under `docs/`, `templates/`, and root for FFI
call examples. Every sample that calls a Go FFI function should show
`Result` handling (case match or `Result.withDefault`). Remove any
samples that show bare FFI returns without Result wrapping.

Specifically check:
- `docs/ffi/go-interop.md` examples
- `docs/getting-started.md` (`sky add` example)
- `templates/CLAUDE.md` FFI section
- `CLAUDE.md` "Opaque Struct Pattern" section

### 4. Verify all 18 examples build + key ones run

```bash
# Build sweep
bash scripts/example-sweep.sh --build-only
# Must: 18/18

# Runtime spot-checks
sky verify 12-skyvote    # exercises Error.toString (EnumTagIs path)
sky verify 13-skyshop    # exercises Firestore FFI (slice coercion + nil-pointer Maybe)
sky verify 15-http-server # exercises verify.json scenarios
sky verify 16-skychess   # exercises AI subscription suppression

# Go runtime tests
cd runtime-go && go test ./rt/

# Self-tests
pass=0; fail=0
for f in test-files/*.sky; do
    rm -rf .skycache
    ./sky-out/sky build "$f" >/dev/null 2>&1 && pass=$((pass+1)) || fail=$((fail+1))
done
echo "self-tests: $pass passed, $fail failed"
```

### 5. Run full cabal test suite

```bash
cabal test
```

All specs must pass. If VerifyScenarioSpec flakes from port
conflicts, retry in isolation (`--match "VerifyScenario"`).

### 6. Update `docs/FFI_BOUNDARY_FIXES.md`

Mark all four P0–P3 items as done (they landed in e1faa21).
Add verification dates/commit hashes.

### 7. Commit

Single commit with all doc updates:
```
docs: FFI boundary — update tables, samples, philosophy doc

Reflects P0–P3 fixes (e1faa21): named error types, nil pointer →
Maybe, (T, bool) → Maybe, nil-receiver checks. All examples build
+ run; all test matrices green.
```

---

## Verification gates (all must pass before the commit)

1. `bash scripts/example-sweep.sh --build-only` → 18/18
2. `cd runtime-go && go test ./rt/` → all pass
3. Self-tests → 67/67
4. `sky verify 12-skyvote` → runtime ok
5. `sky verify 13-skyshop` → runtime ok (or skip if Firestore needs credentials)
6. `cabal test` → all specs pass
7. No `.md` file under `docs/` or `templates/` references the old `(T, bool) → Maybe T` claim without the fix being landed
8. `docs/ffi/boundary-philosophy.md` exists and is linked from `docs/ffi/go-interop.md`
