# v0.13 Phase A4 + A7 — Per-Instance Monomorphisation + Sky-level DCE

Status: design (perf/v0.13)

## The mandate

> NO go any type should happen ever, as we're tracing usage based sky
> code, which means we're guaranteed to have concrete types, if it's not,
> the code is 100% not used/called in sky code — as HM can infer all
> used types.

Sky source emits Go with:
1. Zero `any` in function signatures
2. Zero Go generics (`[T any]`)
3. Zero `func(any) any` lambdas
4. Only functions/instances reachable from `main` are emitted

The runtime kernel (`rt.*` in `runtime-go/`) stays Go-side FFI per the
"apart from Go FFI/unsafe" carve-out — it provides typed wrappers
(`AsList`, `Concat`, `CoerceString`, etc.) that the emitted code calls
across the typed/untyped boundary.  Inside the kernel `any` is fine;
across the boundary (function sigs of emitted code) it is not.

## Why monomorphisation

HM has already done the type analysis.  Every reachable polymorphic call
site has concrete type arguments by the time HM completes — captured in
`Solve.CallInstance` records during the solve pass (Phase A1 / A2 / A3).

For each `(callee, [concrete_type_args])` instance we emit a SPECIALISED
Go function with:
- Concrete Go types for every param.
- Concrete Go return type.
- Body with every TVar substituted by the instance's concrete types.
- Nested calls re-resolved to OTHER specialised instances by mangled
  name.

Recursive calls inside a function's body use the SAME instance (Sky's
HM is monomorphic recursive, no polymorphic recursion) — so the
recursion is well-defined: `Sky_Core_List_map__Int_String` recursively
calls `Sky_Core_List_map__Int_String`.

## Reachability + DCE

Starting set: `main` (the program's entry).  Its body's call graph is
walked transitively.  At each `Can.Call`:

1. Resolve the callee (Can.VarTopLevel / VarKernel / VarLocal).
2. Find the call's `CallInstance` (already captured by the solver at
   pass-2 time, keyed by `(line, col)`).
3. Apply the current instance's σ to the call's type-args (if the
   callee is generic in the OUTER instance's tvars).
4. Add (callee, type-args) to the reachable set.
5. Recursively walk the callee's body with the NEW σ.

When the recursion terminates (no new instances added), the set is
the transitive closure.  Anything not in the set is dead — don't emit.

**Inside a per-instance body**, σ is applied at every type lookup so:
- Pattern types substitute.
- Recursive call's instance args substitute (via the outer σ).
- Lambda param + return types substitute.
- VarLocal references with HM-typed bindings substitute.

## Naming convention (no clash)

Functions specialise to:

    <mangledQualName>__<argTypes>

Where:
- `mangledQualName` = `Sky_Core_List_map` (the Phase A2 mangle).
- `argTypes` = concrete types joined by `_` via `mangleType` (per
  `Sky.Build.Monomorphise.mangleInstance`).

Example: `Sky.Core.List.map` instantiated at `(Int, String)` →
`Sky_Core_List_map__Int_String`.

**Lambdas need unique names because they're function-scoped.** The
challenge: a lambda inside `Sky_Core_List_map__Int_String` must NOT
clash with a lambda in `Sky_Core_List_map__String_Int` OR with another
lambda in the same function.

**Naming scheme**:

    <enclosing-mangled-name>_lambda_<seq-id>

Where `seq-id` is a per-function counter incremented at each Lambda
node encountered during AST walk.  Stable: source-text order
determines order of emission.  Hierarchical for nested lambdas:

    Sky_Core_List_map__Int_String_lambda_0          -- outer
    Sky_Core_List_map__Int_String_lambda_0_inner_1  -- nested

Per-instance: each specialised function emits its own lambda set.
`Sky_Core_List_map__Int_String_lambda_0` and
`Sky_Core_List_map__String_Bool_lambda_0` are distinct.

**Alternative considered: source-position-based** (`lambda_L<line>_C<col>`).
Rejected because the same source position can produce multiple lambdas
across instances — confusing in stack traces, and breaks DCE
(the position is shared but the specialised lambda isn't).

**Closure capture**: if a lambda references variables from its
enclosing scope, those variables retain their concrete types from the
outer instance.  The closure is implicit in Go (lambdas are closures).
The captured variables' types are the outer instance's σ-substituted
types.

## Algorithm — Sky-level DCE

```
INPUT:
  - moduleGraph: every Can.Module reachable from entry
  - solvedTypes: per-function type from HM
  - callInstances: captured CForeign sites (qualName, [Can.Type], region)

PROCEDURE:
  workset := {(main, [])}            -- entry point, no type args
  reached := {}                       -- emitted instances
  lambdaCounts := empty               -- per-instance lambda ID counter
  
  WHILE workset is non-empty:
    pop (callee_q, sigma_outer) from workset
    if (callee_q, sigma_outer) in reached: continue
    add (callee_q, sigma_outer) to reached
    
    if callee_q is a kernel name (lookupKernelType returns Just):
      mark kernel routing instance.  Kernel sigs are emitted by the
      kernel-emitter pass, possibly with their own DCE.
      continue
    
    if callee_q is FFI (rt.* package): same as kernel
    
    body := lookup canDef body for callee_q in moduleGraph
    
    walk body recursively:
      for each Can.Call (func, args):
        resolve func to qualified name f_q
        lookup CallInstance at this region: (f_q, [t1, t2, ...])
        apply sigma_outer to [t1, t2, ...] -> [s1, s2, ...]
                                              (concretes substituted)
        push (f_q, [s1, s2, ...]) to workset
      
      for each Can.Lambda:
        increment lambdaCounts[current_instance]
        register (callee_q, sigma_outer, lambda_id) -> typed_lambda
      
      for each Can.VarTopLevel f or VarKernel f:
        treat as Can.Call with no args (point-free reference)
        push (f, []) — but how do we get its type args?
        ... actually point-free references in HM-typed code don't make
        sense without specialisation.  Sky may need to hoist them
        OR forbid them in the typed surface.

OUTPUT:
  - reached: set of (qualName, [Can.Type]) instances to emit
  - lambdaCounts: per-instance lambda numbering map
```

## Specialisation — per-instance emission

```
emitInstance :: (qualName, [Can.Type]) -> [GoDecl]
emitInstance (q, ts) =
  let canDef = lookupDef q
      annot  = generaliseToAnnotation (solvedType q)
      sigma  = buildSubstitution annot ts   -- (Phase A2)
      mangled = mangleInstance (q, ts)      -- (Phase A2)
      
      -- Substitute types in the body's AST
      body' = substituteTypesInBody sigma canDef.body
      
      -- Walk body, emitting typed Go
      goBody = exprToGoMono sigma body'
      
      -- Param + ret types from sigma applied to canDef's annotation
      paramGoTypes = map (typeToGo . substituteType sigma) canDef.paramTypes
      retGoType    = typeToGo (substituteType sigma canDef.retType)
  in
      GoFuncDecl mangled
        (zipWith GoParam canDef.paramNames paramGoTypes)
        retGoType
        goBody
```

Body walks:

- `Can.Call (Can.VarTopLevel ... f) args` →
  look up call's CallInstance, apply σ, emit `mangledInstance__σ(args)`.
- `Can.Call (Can.VarLocal name) args` →
  emit `name_local_call`.  Locals already have typed Go params from
  the enclosing function's params or let-binding types.
- `Can.Lambda params body` →
  emit a typed Go func literal with typed params/return derived from
  the lambda's HM-inferred type.  The lambda gets a unique name
  `<mangledOuter>_lambda_<seq>` if it needs to be hoisted; otherwise
  inline `func(p T1) T2 { ... }`.
- `Can.Let def body` →
  `def`'s value type comes from HM.  Emit `var name T = value` or
  `name := value` with concrete T.
- `Can.VarLocal name` →
  emit `name`.  The Go type comes from the enclosing scope's binding.

## Lambda emission details

Lambdas are usually INLINED at their use site (Go func literals).
They're hoisted to a named top-level function ONLY when:
- They're recursive (rare in Sky).
- They're shared across multiple use sites (currently impossible —
  every lambda AST node is a unique reference).

**Inline emission**: `func(p1 T1, p2 T2) RetT { ... }`.  Each param's
type comes from σ applied to the HM-inferred lambda type.

**Curried lambdas**: `\a b -> body` → `func(a T1) func(b T2) T3 { ... }`.

## Migration strategy — incremental

Phase 1: Build the infrastructure (reachable-set walker, instance
emitter, mangle helpers — most of these already exist from A1-A3).

Phase 2: Implement `emitInstance` for ONE specific Sky-source function
(say `Sky.Core.Maybe.withDefault`).  Verify end-to-end:
- Per-instance emission with concrete types
- Call-site rewrite to mangled name
- Original generic emission disabled for this function

Phase 3: Generalise to ALL Sky-source functions (Maybe, Result, List,
Basics, Std.Ui, etc.).

Phase 4: Drop the generic emission entirely.  All Sky-source
emissions are per-instance.

Phase 5: Lambda typed emission everywhere (no more `func(any) any`
at lambda creation sites).

Phase 6: DCE — only emit instances reachable from main + ports
(Live.app callbacks, etc.).  Currently the kernel + runtime helpers
also need a typed DCE pass.

## Open questions

1. **Point-free references**: `let f = List.map` — what's the
   instance?  HM may resolve it but we'd need to know the use site.
   Probably emit nothing until USED (lazy mono).

2. **First-class function values**: `[map, filter, foldl]` — list of
   functions.  Each function's type must agree.  HM might force a
   single type for all.  Then mono is straightforward.

3. **Kernel-runtime boundary**: `rt.AsList(x)` returns `[]any` — Sky
   code calling it expects a typed slice.  Solution: add typed
   wrappers `rt.AsListT[T](x)` (most already exist).  Sky emission
   substitutes T from σ.

4. **FFI calls**: user calls `Sky.Ffi.call "go-pkg.fn" args`.  These
   are the user's escape hatch.  Stay any-typed, documented.

5. **Cmd.perform / Sub.every**: kernel-typed.  The callback's input
   type comes from the Task's return.  Already typed-aware in
   v0.12.x typed-codegen.

## Estimated scope

This is the proper monomorphisation pass.  Multi-day implementation
even for one function.  Multi-week for the full surface.  But the
foundation (Phase A1-A3) is in place — the work is "wire up the
emission pass" not "design the monomorphisation algorithm".

Realistic shipping order:
1. Maybe + Basics (small, non-HOF) — proves the architecture.
2. Result + List (non-HOF) — covers the medium surface.
3. List (HOF) — proves typed lambda emission.
4. Std.Ui (huge surface) — proves it scales.
5. Drop the generic-emission code path entirely.
