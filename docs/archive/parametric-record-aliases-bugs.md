# Parametric record aliases — three known bug surfaces

A `type alias Cfg msg = { ..., onX : ... -> msg, ... }` exposes a
family of Sky compiler issues that all relate to monomorphisation
+ lowering of parametric record types. Hit during skydeploy's
sky-editor package extraction; will hit every future Sky widget
package that uses a Cfg-style API.

## Surface 1 — HM can't access polymorphic fields on parametric aliases

```
type alias Cfg msg = { onSubmit : Form -> msg }
trigger : Cfg msg -> Form -> msg
trigger cfg form = cfg.onSubmit form
```

→ `Variable 'cfg' type mismatch: Cfg msg vs { onSubmit : a | ...}`

The constraint solver doesn't unify `Cfg msg` with the structural
row `{ onSubmit : a | ... }`. Drop the signature and structural
inference works — but then the function gets `Coerce[any]`
wrapping that breaks at runtime (Surface 2).

**Repro:** `test-files/record-field-partial-app.sky` —
build fails at type-check.

## Surface 2 — partial-applied ctors in record fields type-erase incorrectly **[CLOSED 2026-05-24]**

```
type alias Cfg msg = { onSubmit : Form -> msg, ... }

-- Call site:
view { ..., onSubmit = SaveFile appId fileName }
```

Sky lowers this to:

```go
rt.Coerce[func(any) State_Msg](
    func(__p0 Editor_Form_R) State_Msg {  ← TYPED param
        return State_Msg_SaveFile(appId, ..., rt.Coerce[Editor_Form_R](__p0))
    })
```

The inner lambda has type `func(Editor_Form_R) State_Msg`
(typed param). The outer `Coerce[func(any) State_Msg]` then does
`v.(func(any) State_Msg)` which fails — Go function types are
nominal in their parameter types.

The `makeFuncAdapter` fallback in `rt.Coerce` (line 4553 of
`rt/rt.go`) handles this for FFI callbacks via
`reflect.MakeFunc` — but the fast-path direct assertion (line
4483) gets there first when target = `func(any) X`. For function
types the fast path is wrong — should always go via
`makeFuncAdapter` when target/source signatures differ.

**Workaround in skydeploy:** widget exposes Msg callbacks as
positional args (not record fields). See
`packages/sky-editor/src/Editor.sky`.

**Repro after fixing Surface 1:** the same .sky program at the
`cfg.onSubmit form` call panics at runtime.

**Resolution (2026-05-24).**

1. `rt.Coerce[T]` for function targets now always goes through
   `makeFuncAdapter` (rt/rt.go:4486) — Fix A above.
2. Parametric record alias struct emission (`generateAlias` /
   `generateAliasForDep` in `src/Sky/Build/Compile.hs`) was changed
   to non-generic with Sky TVar fields erased to `any` in field
   types. So `Editor_Cfg_R` stays a plain struct and Go's "cannot
   use generic type without instantiation" error class is avoided.
3. Combined, the slot↔value func-signature mismatch is bridged at
   the call boundary by `makeFuncAdapter` without needing the
   struct to be generic.

## Surface 3 — structurally-identical record aliases generate distinct Go structs

```
-- packages/sky-editor/src/Editor.sky
type alias Form = { content : String, action : String }

-- control-plane/src/State.sky
type alias FileForm = { content : String, action : String }
```

Lowers to `Editor_Form_R` and `State_FileForm_R` — two distinct
Go structs with identical field sets. Sky's source-level types
are structural; the Go nominal structs make crossover impossible
even via `rt.Coerce` (different struct types are unconvertible).

Bites when one module declares the form shape (the widget's
declaration becomes the wire decoder's target) but the consumer
expects its own aliased shape on the Msg constructor.

**Workaround in skydeploy:** consumer re-aliases the widget's
type. `State.FileForm = Editor.Form`.

## Proposed fixes (in priority order)

### Fix A — `rt.Coerce` always goes through `makeFuncAdapter` for function targets

```go
func Coerce[T any](v any) T {
    if t, ok := v.(T); ok {
        return t
    }
    var zero T
    targetTy := reflect.TypeOf(zero)

    // Function types: always adapt via MakeFunc. The fast-path
    // direct assertion would only succeed for exact signature
    // match, which is the same case the (T) assertion above
    // would have caught. Once we're past that, any function
    // value coerced to a function target needs MakeFunc.
    if v != nil {
        rv := reflect.ValueOf(v)
        if rv.IsValid() && rv.Kind() == reflect.Func &&
           targetTy != nil && targetTy.Kind() == reflect.Func {
            return makeFuncAdapter[T](rv, targetTy).(T)
        }
    }
    // ... rest unchanged
}
```

This is a one-block change. Closes Surface 2. Doesn't fix
Surface 1 or 3 but unblocks the most painful runtime crash.

Risk: function-targeted Coerce calls that already work (exact
signature match) now go through MakeFunc (slower, ~100ns per
call). Bounded — only the dispatch-boundary Coerces hit this.

### Fix B — monomorphisation canonicalises record types by sorted field signature

Two `type alias X = { f1: T1, f2: T2 }` declarations should
emit ONE Go struct keyed by `canonicalKey(sortByName(fields))`.
Subsequent aliases reference the same struct via Go's
`type X = Foo` (true alias, same type).

Where: `src/Sky/Build/Monomorphise.hs` — the record-type
emission path. Detect duplicates by canonical key, dedupe.

Closes Surface 3. Doesn't help Surface 1 or 2.

Risk: existing callers that rely on the distinct nominal struct
break (none expected; structural types in Sky never carried
nominal identity).

### Fix C — HM solver accepts row-polymorphic field access on
parametric aliases

The constraint `Cfg msg ⊓ { onSubmit : a | r }` should unify by
unfolding the alias: `{ onSubmit : Form -> msg } ⊓ { onSubmit :
a | r }` → `a ↦ Form -> msg, r ↦ {}`.

Where: `src/Sky/Type/Constrain/` and the unify pass. Aliases
should expand transparently when on either side of a row
constraint.

Closes Surface 1. Bigger change — affects every alias-touching
constraint.

## Effort estimates

| Fix | Risk | Effort | Unblocks |
|---|---|---|---|
| A | low | half day | sky-editor record-field callbacks; future Cfg APIs |
| B | medium | 1-2 days | cross-package record aliases; future package extractions |
| C | high | 2-3 days | natural Cfg-record APIs with type signatures |

**Combined: ~1 working week** for a clean solution to all three.

Pre-fix, skydeploy continues with the workarounds documented in
each module. They're well-isolated and reverting them once the
compiler fix lands is mechanical.
