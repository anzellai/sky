# FFI boundary docs + samples + tests cleanup

**You are continuing work without a human in the loop.** The compiler
fixes landed in commit `e1faa21` (FFI boundary P0–P3 gaps). The
remaining work is updating docs, code samples, tests, and templates
to match the new return-type mapping. **Do not stop until every
verification gate passes and `docs/ffi/boundary-philosophy.md`
exists** — the stop-hook will keep blocking otherwise.

If you genuinely need to pause for human review, `touch .claude/allow-stop`.
Don't reintroduce automated brakes.

---

## Background context (read once, then act)

Sky's Go FFI returns `Result Error T` for every call — this is by
design. The FFI boundary is a trust boundary (analogous to Elm's
ports). The user explicitly confirmed: **Result for synchronous FFI,
Task only for deferred Sky effects**. Don't propose changing this.

Before this session's work, four return-type patterns slipped
through without proper Sky-side typing. Commit `e1faa21` fixed all
four:

- **P0 — Named error types**: `sky-ffi-inspect` now uses
  `types.Implements` (not string-match `"error"`) so `*os.PathError`
  etc. are correctly classified as fallible.
- **P1 — Nil pointer → Maybe**: `func F() *T` now generates
  `Result Error (Maybe T)` via `rt.NilToMaybe`.
- **P2 — `(T, bool)` → Maybe**: comma-ok now generates
  `Result Error (Maybe T)` via `rt.CommaOkToMaybe`.
- **P3 — Nil-receiver checks**: every method wrapper with a
  pointer receiver returns `Err(ErrFfi "nil receiver: …")` instead
  of panicking.

The compiler is built and signed; `sky-out/sky --version` reports
`v0.9.0`. The example sweep is 18/18, runtime Go tests pass.

---

## Items (work top-to-bottom)

### Item 1: Update return-type mapping tables

Three tables need the new mapping. **Authoritative truth:**

| Go return | Sky type |
|---|---|
| `T` (single, no error, non-pointer) | `Result Error T` |
| `*T` (single pointer, no error) | `Result Error (Maybe T)` |
| `(T, error)` | `Result Error T` |
| `error` | `Result Error ()` |
| `(T, bool)` (comma-ok) | `Result Error (Maybe T)` |
| `(T, *NamedErr)` where NamedErr implements error | `Result Error T` |
| `(T, U)` (neither error nor bool) | `Result Error (T, U)` |

Files:
1. `docs/ffi/go-interop.md` — replace the "Return type mapping" table
2. `CLAUDE.md` — under "Go FFI / Interop Model", the "Type Mapping" table
3. `templates/CLAUDE.md` — the user-facing template's FFI section

In each, add a one-line link: "All FFI calls return `Result Error T`. See [boundary-philosophy.md](./boundary-philosophy.md) for why."

### Item 2: Write `docs/ffi/boundary-philosophy.md`

This is the load-bearing deliverable. **The stop-hook gates on this
file's existence.** Structure:

```markdown
# FFI boundary philosophy

## The trust boundary

Sky's Go FFI is a **trust boundary**, not a transparent function call.
Even when Go's signature looks safe (`func F() string`), the call
crosses into code Sky's type checker can't see — the Go compiler is
the only gatekeeper, and Go's type system permits panics, nil
pointers, interface-nil, OOM, goroutine leaks, and runtime errors
that Sky's HM types can't model.

This is the same problem Elm solves with **ports**: typed airlocks
to JavaScript that decode incoming values and reject what doesn't
fit. Sky applies the same principle to Go: every FFI call returns
`Result Error T`, forcing the user to acknowledge the boundary at
each call site.

## Why Result, not Task

| Sky type | Meaning | Use for |
|---|---|---|
| `Result Error T` | "this crossed a boundary, here's the outcome" | Synchronous FFI calls |
| `Task Error T` | "this will cross a boundary when you say go" | Deferred Sky effects (File.readFile, Time.sleep) |

FFI calls execute immediately — wrapping them in Task would imply
"hasn't run yet" which is misleading. If a user wants a deferred
FFI call, they can write `Task.lazy (\_ -> Ffi.call ...)` explicitly.

## Why Result on every FFI call (even pure-looking ones)

Three reasons:

1. **Go can panic anywhere.** Even functions Go authors mark as
   pure can fail — third-party packages have bugs, nil pointers
   sneak in, init() in some imported package can leave global state
   broken. The Result wrapping turns every panic into a typed Err
   instead of a process crash.

2. **Honest types.** A function that *might* fail returning a bare
   `T` is dishonest. `Result Error T` matches what can actually
   happen at runtime.

3. **Intentional friction.** Discouraging Go FFI use is a feature.
   Sky's stdlib should grow to cover most use cases. The Result
   tax is a signal: "you're leaving Sky's safety guarantees,
   consider whether you really need to."

## Comparison: Rust's `?` and `unwrap`

Rust forces explicit handling of `Result` via the `?` operator
(propagate) or `.unwrap()` (panic-on-err for known-safe cases).
Sky's `Result.withDefault`, `Result.map`, `case ... of Ok ... | Err ...`
serve the same role: every call site explicitly acknowledges the
fallibility. There's no implicit unwrap — the compiler won't let
you accidentally use a `Result T` as if it were `T`.

## What this means in practice

- **Prefer Sky stdlib.** `Std.File`, `Std.Http`, `Std.Db`, `Std.Auth`,
  `Std.Time`, `Std.Random`, `Std.Process`, `Std.Crypto`, etc. cover
  most needs without the Result tax.
- **When you need Go FFI**, expect to handle `Result` at every call
  site. Use `Result.withDefault` for "bail to a fallback", `case`
  for branching on Ok/Err, `Result.map`/`andThen` to chain.
- **For nil-prone Go returns** (`*T`, `(T, bool)`), you get
  `Result Error (Maybe T)` — handle the boundary failure (Result)
  AND the absence (Maybe) explicitly.
```

Cross-link from `docs/ffi/go-interop.md`: add a "See also" line
near the top pointing to `boundary-philosophy.md`.

### Item 3: Audit code samples in docs

Grep all `.md` files under `docs/`, `templates/`, and root for FFI
call examples (anything calling a function from `Github.Com.*`,
`Net.Http.*`, `Fyne.Io.*`, `Stripe.*`, etc.):

```bash
grep -rn "Uuid\.\|Stripe\.\|Firestore\.\|Mux\.\|Fyne\." \
    docs/ templates/ CLAUDE.md README.md *.md 2>/dev/null
```

For each match, verify the sample shows `case ... of Ok / Err`
or `Result.withDefault` handling. If a sample shows bare FFI
return without Result wrapping, fix it.

The `docs/getting-started.md` `sky add github.com/google/uuid`
example should show:
```elm
import Github.Com.Google.Uuid as Uuid

main =
    case Uuid.newString of
        Ok id ->
            println id
        Err e ->
            println ("uuid failed: " ++ Error.toString e)
```

### Item 4: Mark P0–P3 done in `docs/FFI_BOUNDARY_FIXES.md`

Update each `## P0/P1/P2/P3` heading: append `— landed e1faa21`
to the title. Add a closing note at the end:

```markdown
---

## Status

All four items landed in commit `e1faa21` (2026-04-16). Docs +
samples updated in this commit (record actual hash). Verification:
18/18 example sweep, all rt go tests, all cabal specs.
```

### Item 5: Run full verification gates

In order. **All must pass before committing.** If any fails,
investigate and fix the underlying issue before continuing —
do not skip or relax.

```bash
# 1. Compiler builds + version sanity
cabal install --overwrite-policy=always --installdir=./sky-out \
              --install-method=copy exe:sky
codesign -s - sky-out/sky    # macOS only; harmless elsewhere
sky-out/sky --version        # must print `sky v0.9.0 (haskell)`

# 2. Self-tests (every fixture builds clean)
pass=0; fail=0
for f in test-files/*.sky; do
    rm -rf .skycache
    ./sky-out/sky build "$f" >/dev/null 2>&1 \
        && pass=$((pass+1)) || fail=$((fail+1))
done
echo "self-tests: $pass passed, $fail failed"
# Acceptance: 67 passed, 0 failed

# 3. Example sweep (build-only)
bash scripts/example-sweep.sh --build-only
# Acceptance: 18 passed, 0 failed

# 4. Runtime Go tests
(cd runtime-go && go test ./rt/)
# Acceptance: ok

# 5. Cabal test suite (~25 minutes — run in background, wait for it)
cabal test
# Acceptance: all specs pass

# 6. Sky verify on key examples
./sky-out/sky verify 12-skyvote      # rt-built error → kindLabel path
./sky-out/sky verify 16-skychess     # subscription suppression
./sky-out/sky verify 15-http-server  # verify.json scenarios
# Acceptance: each prints "runtime ok"
```

### Item 6: Commit

Single commit covering all the doc updates:

```bash
git add docs/ templates/ CLAUDE.md README.md
git commit -m "$(cat <<'EOF'
docs: FFI boundary — tables, samples, philosophy doc

P0–P3 fixes landed in e1faa21. This commit makes the docs honest
about the new return-type mapping:
  - Updated return-type tables in docs/ffi/go-interop.md,
    CLAUDE.md, templates/CLAUDE.md
  - Added docs/ffi/boundary-philosophy.md explaining the trust
    boundary design (Elm ports analogy, Result vs Task)
  - Audited code samples for Result handling
  - Marked all four P-items done in docs/FFI_BOUNDARY_FIXES.md

Verification: 18/18 example sweep, 67/67 self-tests, all rt go
tests pass, full cabal test suite green.
EOF
)"
```

### Item 7: Verify the stop-hook releases

After the commit, `docs/ffi/boundary-philosophy.md` exists. The
stop-hook will release on the next end-of-turn. You can stop
cleanly without `touch .claude/allow-stop`.

If for some reason the gate doesn't release (typo in filename,
hook script bug), `touch .claude/allow-stop` and surface the
issue to the user.

---

## Operating rules

- **Don't ask permission.** This is autonomous work.
- **Don't summarise progress every step.** Do the work, run the
  gates, commit. The git log is the audit trail.
- **Don't reintroduce automated brakes** in the stop-hook. The
  user explicitly removed those.
- **Don't add v1.0 references.** v0.9 is the current version.
- **Don't change FFI from Result to Task.** Settled design.
- **Failing tests are non-negotiable.** If a verification gate
  fails, fix the underlying issue. Don't relax acceptance criteria.

## Done condition

`docs/ffi/boundary-philosophy.md` exists, the doc commit landed,
all six verification gates pass green. Stop-hook releases automatically.
