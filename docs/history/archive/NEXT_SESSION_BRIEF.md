# Next-session brief — P7 and P8 execution

Read this **after** `docs/PRODUCTION_READINESS.md`. This file is the
technical detail the plan's P7/P8 amendments point at. If anything here
contradicts the plan doc, the plan doc wins — file an update here.

The Stop hook at `.claude/hooks/sky-v1-loop.sh` will block you from
ending the session until the final `v1.0` commit lands. Do not remove
the hook. If you need to pause, `touch .claude/allow-stop`.

---

## §1 — P7: typed FFI wrappers (adaptor pattern)

### Current shape

`src/Sky/Build/FfiGen.hs` emits every wrapper as `(any) any`:

```go
func Go_Uuid_parse(p0 any) (out any) {
    defer SkyFfiRecover(&out)()
    out = Ok[any, any](pkg.Parse(p0.(string)))
    return
}
```

Callers pass `any`-typed values. The `p0.(string)` assertion at runtime
is the only thing catching type mismatch.

### Target shape

Emit **two** wrappers per FFI function:

```go
// Typed — the real Go signature from the inspector.
func Go_Uuid_parseT(s string) (out rt.SkyResult[string, uuid.UUID]) {
    defer rt.SkyFfiRecover(&out)()
    r, err := pkg.Parse(s)
    if err != nil {
        out = rt.Err[string, uuid.UUID](err.Error())
    } else {
        out = rt.Ok[string, uuid.UUID](r)
    }
    return
}

// Adaptor — keeps the any/any calling convention alive for call sites
// that haven't yet been migrated. Thin; just coerces and delegates.
func Go_Uuid_parse(p0 any) any {
    return Go_Uuid_parseT(p0.(string))
}
```

Call-site codegen (in `Sky/Build/Compile.hs`) gains an opt-in to prefer
the `T` suffix when the HM-inferred argument types at the call site
are concrete. Until the opt-in is flipped everywhere, `Go_Uuid_parse`
remains callable and the sweep stays green.

### Implementation steps (in this order)

1. **Extend `FnInfo`** (already in `FfiGen.hs`) with a boolean
   `_fnHasTypedWrapper`. Don't touch the inspector JSON — compute this
   from whether all parameter + return Go types are expressible
   (no `<-chan`, no `map` of complex keys, no generic type params with
   narrower constraints).
2. **Emit both wrappers** from the `DirectCall` branch. Add a helper
   `emitTypedWrapper :: FnInfo -> [(String, String)] -> [(String, String)] -> String`
   alongside the existing `emitTypedCall`.
3. **Register the typed name** in the FFI registry (`FfiRegistry.hs`).
   The any/any wrapper stays the public resolve target; the typed
   version is an internal optimisation.
4. **Migrate one caller**. Pick `Go_Uuid_newString` (zero-arg, return
   String — trivially typed). Change `solvedTypeToGo` / call-site
   codegen for this one name to emit the `T` form. Rebuild, sweep,
   confirm 18/18. Commit `[P7/partial] Go_Uuid_newString typed call`.
5. **Batch the rest** by category: zero-arg functions first, then
   unary-string, then unary-int, etc. One commit per category, sweep
   after each. Use `grep -c "(p0 any) any" examples/*/ffi/*.go` as
   the progress metric — commit note should record the delta.
6. **Retire the any/any adaptors** only after the count hits zero.
   That's the P7 completion commit: `[P7] FFI wrappers fully typed`.

### Acceptance gates (from the plan)

- `grep 'func [A-Z][a-zA-Z0-9_]*(p0 any' examples/*/ffi/*.go` returns
  nothing.
- `scripts/example-sweep.sh --build-only` 18/18.
- Cabal test 7/7.
- `rt.ResultCoerce` / `rt.MaybeCoerce` call-site count (the number
  recorded in the P6 commit — 276) drops by ≥ 50% on FFI-heavy
  examples (03, 05, 08, 11, 13).

### Do NOT

- Do not delete the any/any wrappers before call-site migration is
  complete. That gives a red sweep between commits.
- Do not touch `runtime-go/rt/rt.go` reflect helpers. Those are P9's
  dynamic path and stay.

---

## §2 — P8: kernel stdlib retype (alphabetical, per-module)

### Entry preconditions

P7 landed. Every FFI wrapper has a typed variant. The call-site
migration machinery exists. **Do not start P8 before P7 is green.**

### Module order (alphabetical; commit one per entry)

Char → Dict → Encoding → File → Http → Io → Json → List → Math →
Maybe → Path → Process → Random → Regex → Result → Set → String →
Task → Time.

### Per-module recipe

For each module (example: `List`):

1. Open `runtime-go/rt/rt.go`, locate every `Sky_Core_List_*(...)any`.
2. Change signatures to generics. `Sky_Core_List_map` goes from
   `(f any, xs any) any` to
   `[A, B any](f func(A) B, xs []A) []B`.
3. Add `Sky_Core_List_mapAny(f any, xs any) any` as a thin adaptor
   (same pattern as P7). This keeps legacy any/any call sites alive.
4. Update `src/Sky/Generate/Go/Kernel.hs` — the `KernelInfo` entry
   stays; add a sibling `KernelInfoT` with the typed name and Sky
   type signature.
5. Update `src/Sky/Canonicalise/Module.hs:kernelFunctions` if the
   set of exposed names changes (it shouldn't — new name is internal).
6. Rebuild: `cabal install exe:sky --overwrite-policy=always
   --install-method=copy --installdir=sky-out`
   && `install -m 755 sky-out/sky ~/.local/bin/sky`.
7. Run `bash scripts/example-sweep.sh --build-only`. Must be 18/18.
8. Commit `[P8/List] retype Sky_Core_List_* with generics`.

### Do NOT

- Do not batch two modules into one commit. Per-module blast-radius
  control matters.
- Do not change kernel function names. External ABI.
- Do not touch the non-kernel helpers (SkyEqual, AsInt, etc.). They
  dispatch on runtime values, not Sky types — keep them as-is.

---

## §3 — Completion commit

When P7 and P8 are done, the tracker in
`docs/PRODUCTION_READINESS.md` has every row ☑. Run the full verify:

```bash
cabal test --test-show-details=direct
bash scripts/example-sweep.sh --build-only
grep -rn "any) any" src/ runtime-go/rt/ | grep -v P9_RESERVED | head
grep -rn "TODO\|FIXME" src/ runtime-go/rt/ | head
```

The last two should return nothing (or only comments in the P9
reflect path labelled `P9_RESERVED:`).

Then:

1. Amend `docs/PRODUCTION_READINESS.md` to add a new top-level
   heading `## Current state snapshot — v1.0 complete` (this is the
   Stop-hook's exit signal). Update the old snapshot section to say
   "superseded 2026-MM-DD".
2. Commit: `v1.0: production readiness plan complete`.
3. The Stop hook will now allow the session to end.

---

## §4 — When the hook blocks you

The hook output lands in your tool-result stream. Read the `reason`
field; it tells you exactly what to do next. The hook is not your
enemy — it exists because previous sessions ended at phase 4-6 with
a "session summary" and the plan needs the last two phases.

Acceptable reasons to pause (`touch .claude/allow-stop`):
- You identified a genuinely novel obstacle not covered here — add a
  new `[plan]` amendment that names it, then pause for human review.
- A test failure you can't diagnose after 15+ minutes.
- The machine OOM'd or Go toolchain is broken.

Unacceptable reasons (hook will rightly block):
- "This phase is large."
- "Context is getting full."
- "I've made good progress for today."
- "The user probably wants to review."
