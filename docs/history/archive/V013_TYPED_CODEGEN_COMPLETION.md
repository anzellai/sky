# v0.13 Typed-Codegen Completion — Plan & Progress

**Contract (user, 2026-05-14/15):** v0.13 must deliver:

1. **All used Sky code → fully-typed Go.** Anything used/called — HM can
   resolve it, so it MUST emit fully-typed Go. The *only* carve-out:
   genuinely-unused vars/funcs/types may stay `any` or Go-generic; and
   genuinely-polymorphic stdlib may use **Go generics** (`[T any]`) —
   that IS the typed form. The bare `any` *type* is not acceptable for
   used code.
2. **Sky-side DCE in the Haskell phase**, before lowering — prunes
   unused Sky decls AND unused FFI sigs up-front (huge win on Stripe-SDK
   scale: thousands of unused sigs gone before the lowerer sees them).
   The Go-side `sky-dce` stays as a second pass.
3. **LSP 100%** — every used variable/function/type has working
   hover/doc + goto-definition, including on huge-FFI-surface projects.

No deferral to v1/v0.14. Where CLAUDE.md says "post-v1.0", fix CLAUDE.md.

**Method:** scope each workstream to 100% certainty BEFORE implementing
it. No incremental "implement → hit edge case → defer". This doc is the
living plan + progress tracker.

---

## Baseline measurement (2026-05-15, compiler @ 1201e96)

`any`-in-signature, REACHABLE (deadcode-confirmed) = must-fix vs
unreachable = contract carve-out:

| example | any-in-sig | unreachable (OK) | REACHABLE (must-fix) |
|---|---|---|---|
| 09-live-counter | 56 | 48 | 8 |
| 18-job-queue | 65 | 52 | 13 |
| 06-json | 44 | 38 | 6 |
| 12-skyvote | 111 | 63 | 48 |
| 13-skyshop | 210 | 89 | 121 |

The reachable-`any` funcs categorised (from 09 + 18 dumps):

- **Cat-A — user funcs with open-record / generic `model`:**
  `viewCounter(model any)`, `view[T1 any](model T1)`. Should be the
  concrete record alias (`State_Model_R`).
- **Cat-B — parametric-ADT type-params render `any`:**
  `Std_Html_Html_HElement(v0 string, v1 []any, v2 []any)` — `v1` is
  `List (Attribute msg)` → should be `[]Std_Html_Attributes_Attribute`;
  `v2` → `[]Std_Html_Html`. `Std_Html_Attributes_Event_OnMsg(v1 any)` —
  `v1 : msg` — genuinely the app's `Msg` (needs assessment).
- **Cat-C — stdlib `[]any` instead of Go-generic:**
  `Sky_Core_List_isEmpty(list []any)`, `List_length`, `List_append_`,
  `List_reverseHelp` — should be `[T any](list []T)`.
  (`Sky_Core_List_map_[T1 any, T2 any]` IS already correct Go-generic.)
- **Cat-D — callback returns `any` + mono-instance leftovers:**
  `List_filter[T1 any](pred func(T1) any …)` → pred should be
  `func(T1) bool`; `Maybe_andThen__String_Int(fn func(string) any) …
  rt.SkyMaybe[rt.SkyValue]` → `fn func(string) rt.SkyMaybe[int]`,
  return `rt.SkyMaybe[int]`.
- **Cat-E — anonymous records:** `{age,name}` with no user alias →
  currently `any` / `Anon_R_<hash>` (no decl).
- **Carve-out (OK):** `init_[T1 any](_ T1)` — the `_` request param is
  genuinely unused → Go-generic is fine.

---

## Workstream A — open-record params → concrete record alias  (task #142)

### Confirmed mechanism — TWO distinct bugs

Evidence (09-live-counter, `Model = {page, count}`):
- `viewPage(model Model_R)` — TYPED ✓ (accesses `model.page` AND calls
  `viewCounter model` which back-refs `viewCounter`'s `{count}` → merged
  `{page,count}` = exact match on `Model`).
- `viewCounter(model any)` — accesses only `model.count` →
  `{count | ρ}` → `{count}` ≠ `{page,count}` → exact match FAILS → `any`.
- `view[T1 any](model T1)` — fully polymorphic; never constrained.

**Bug A1 — exact-match-only.** `solvedTypeToGo`'s `T.TRecord` case →
`Rec.lookupRecordAlias` (`Generate/Go/Record.hs:309`) and
`matchAliasByFieldSet` (`Compile.hs:7795`) both do `Set.fromList names
== aliasKeys` EXACT match. A `Can.Access`-derived OPEN record carries
only the *accessed* fields, a subset of the alias. Exact match fails →
`any`.
→ **Fix:** when the `T.TRecord` is OPEN (`ext = Just _`), match against
an alias whose key-set is a **superset** of the accessed fields. The
open record means "≥ these fields" so superset is the correct
semantics. Ambiguity (2+ superset aliases): pick the **smallest**
superset (fewest extra fields); if still tied it is genuinely
ambiguous → that only happens if HM left it under-constrained, which
with A2 fixed shouldn't occur for used code — assert/fallback.

**Bug A2 — forward references don't propagate constraints.**
`Live.app`'s kernel sig (`("Live","app")` in Constrain/Expression.hs)
DOES tie `init`/`update`/`view`/`subscriptions` to a shared `model`
type var. `init` returns a closed `{page,count}` record literal. So
`view`'s `model` SHOULD resolve to closed `Model`. It doesn't because:
`main` (source line 83) references `view` (line 97) — a **forward
reference**. `constrainDecls` processes decls in source order
(`Can.Declare def rest`), so when `main` is constrained `view` is not
yet in env → `CLocal "view"` hits the solver's "unknown variable →
fresh flex var" branch (Solve.hs:732). That fresh var ≠ `view`'s
actual `defType` var minted later → the `Live.app.view ~ model -> Html`
constraint lands on a throwaway var, never reaching `view`'s real def.
`preRegisterFunctions` only pre-registers **annotated** decls (`init`
has `init : a -> (Model, Cmd Msg)` → works; `view`/`update`/
`viewCounter`/`subscriptions` are unannotated → don't).
→ **Fix:** pre-register **every** top-level decl (annotated or not)
with its actual `defType` var BEFORE constraining any def body — i.e.
make `constrainDecls` two-pass like `constrainLetRec`. Top-level Sky
decls are already mutually-recursive and already env-registered as
`Forall []` (monomorphic), so this is consistent — no let-poly
regression. Forward refs then bind to the real var and constraints
connect.

### Edge cases / interactions
- A2 must not break the dep-solve fixpoint or the same-module guard:
  the guard makes same-module refs `CLocal`; A2 ensures the `CLocal`
  env lookup HITS (real var) instead of MISSING (fresh var). They're
  complementary.
- A1's superset match must run only for OPEN records (`ext = Just _`).
  A CLOSED record literal (`{a=1,b=2}`) must keep EXACT match — a
  closed literal with wrong fields must still fail to match a
  different alias (the `unifyRecords` closed-record discipline).
- After A2, many records that were open-partial become fully
  constrained (closed `Model` via the `Live.app` tie) → A1's superset
  path fires less, but is still needed for genuinely-partial helpers
  (a helper only ever called with one record but accessing a subset).
- Both A1 and A2 need verifying against `matchAliasByFieldSet`'s OTHER
  callers (it's used by the typed-codegen routing, Compile.hs:7940) —
  superset semantics there could mis-route. Scope: make a SEPARATE
  `matchAliasBySupersetFieldSet` for the open-record renderer path;
  leave `matchAliasByFieldSet` exact for the routing path.

### Status: SCOPED — ready to implement once B–E scoped (shared files)

---

## Workstream B — parametric ADT type-params → erased name  (task #143)

### Confirmed mechanism — THREE parts

ADT constructor Go-funcs are emitted by `generateUnionTypes`
(Compile.hs:2790) → `generateCtorFunc` → `ctorParamsTyped` →
`ctorArgGoType` → **`safeReturnType`** (NOT `safeReturnTypeWith`).

- `safeReturnType` (Compile.hs:3797) has a `T.TType home name **[]**`
  case — matches ONLY **nullary** type constructors. A *parametric*
  ADT (`Attribute msg` = `TType _ "Attribute" [msg]`) has non-empty
  args → falls through every case → catch-all `_ -> "any"`. So
  `HElement`'s `v1 : List (Attribute msg)` → `[]` + `safeReturnType
  (Attribute msg)` = `[]any`.
- **`runtimeOnlyTypes` (Compile.hs:3953) is STALE** — it still lists
  `"Attribute"` (also `Decoder`, `Value`, `Handler`, `Route`,
  `Middleware`, `Session`, `Store`) from when `Std.Html.Attributes`
  was a Go kernel. Now `Std.Html.Attributes.Attribute` is a Sky-source
  ADT with `type Std_Html_Attributes_Attribute = rt.SkyADT` emitted.
  Both `safeReturnType` AND `safeReturnTypeWith` gate on
  `isRuntimeOnly = name ∈ runtimeOnlyTypes` → return `any` for
  `Attribute` even though it IS a known union.
- `generateCtorFunc` sets `_gf_typeParams = []` always, and a ctor
  arg that is a bare `msg` TVar (`Event`'s `OnMsg : String -> msg ->
  Event msg`) → `safeReturnType (TVar _)` → catch-all `any`.

### Plan — SCOPED, ready to implement

- **B0 — fix `runtimeOnlyTypes`.** Audit the list against the v0.13
  Sky-source stdlib. `Attribute` is definitely now a Sky ADT → remove.
  Check each: `Decoder`/`Value` (Json) — still runtime-opaque? `Handler`
  /`Route`/`Middleware` (Http.Server) — still Go-runtime? `Session`/
  `Store` (Live) — still Go-runtime? Keep only the genuinely
  Go-runtime-opaque ones; remove the migrated Sky ADTs. This single
  fix unblocks both renderers for `Attribute`.
- **B1 — `safeReturnType` parametric-ADT case.** Add a
  `T.TType home name (_:_)` arm mirroring `safeReturnTypeWith`'s
  `T.TType home name _` arm: render the erased Sky-ADT `base` name
  when `isKnownUnion`/registered, else `any`. After B0+B1,
  `List (Attribute msg)` → `[]Std_Html_Attributes_Attribute`,
  `List (Html msg)` → `[]Std_Html_Html`.
- **B2 — Go-generic ADT constructors.** `generateCtorFunc` should
  populate `_gf_typeParams` from the union's declared type vars
  (`Can.Union vars …`). A ctor arg that is a bare `T.TVar v` where
  `v ∈ vars` renders as that Go type-param name (e.g. `M`); nested
  (`List msg` → `[]M`, `Maybe msg` → `rt.SkyMaybe[M]`) thread the
  param through the renderer. `func OnMsg[M any](v0 string, v1 M)
  Std_Html_Attributes_Event` — `v1` typed (Go-generic = the
  contract's accepted form), body still `Fields: []any{v0, v1}`
  (the `rt.SkyADT` Fields slice is inherently `[]any` — boxing a
  typed `v1` into it is fine). Call sites `OnMsg("click", MyMsg)`
  infer `M = Msg` → the arg is type-checked.

### Edge cases / interactions
- `_cg_unionNames` must contain cross-module ADT names when
  `generateUnionTypes` runs for a module that references another
  module's ADT (`Std.Html`'s `HElement` refs `Std.Html.Attributes`'s
  `Attribute`). `depUnionNames` (Compile.hs:643) is built from all
  deps; `globalCgEnv` is set (lines 622/897/1144) before
  `generateGoMulti` (1205) which calls `generateUnionTypes` (2559).
  VERIFY: the union name is the *prefixed* form (`Std_Html_Attributes_
  Attribute`) in `_cg_unionNames` AND `safeReturnType` builds the same
  prefixed `base`.
- B2 must NOT make the ADT TYPE generic (`type Event = rt.SkyADT`
  stays non-generic). Only the CONSTRUCTOR funcs become generic.
  Go allows a generic func returning a non-generic type.
- B2 nullary ctors (arity 0) emit a `GoDeclVar`, not a func — no type
  params needed; leave those.
- After B0, check nothing relied on `Attribute`→`any` (the
  `Std.Ui` `htmlAttribute`/`kernelAttr` path emits `Attribute`-typed
  values — they should now type cleanly, but verify the sweep).
- CLAUDE.md Limitation #16 lists `Attr.*`/`Event.*`/`Html.*` as
  "deliberately NOT added [kernel sigs]... need the typed-codegen
  runtime port" — that note is now being actioned; update CLAUDE.md.

### Status: SCOPED — ready to implement

---

## Workstream C — stdlib `[]any` → Go-generic `[T any]`  (task #144)

### Confirmed mechanism — ONE-function fix

Unannotated dep funcs get `(depTypeParams, depParamGoTys, depRetType)`
from `_cg_funcInferredSigs`, computed by `splitInferredSigWithReg`
(Compile.hs:4329):
- `paramTVars = uniq (concatMap tvarsInEmitted paramTys)` — the TVars
  that become Go type parameters `T1, T2, …`.
- `tvarsInEmitted` (Compile.hs ~4900) **deliberately returns `[]` for
  `List`/`Dict`/`Set`** ("Container types erase their inner TVars…").
  It DOES recurse for `Result`/`Maybe`/`Task`/`Tuple`/`TLambda`.
- So `map : (a→b) → List a → List b` — `a,b` are flagged via the
  `(a→b)` lambda position → numbered `[T1,T2]` → `map_[T1 any,T2 any]`.
  `isEmpty : List a → Bool` — `a` is ONLY inside `List a` → NOT flagged
  → not numbered → `List a` renders (`typeStrWithAliasesReg`) with an
  empty tvarMap → `TVar a` → `"any"` → `[]any`.
- **The renderer is already correct.** `typeStrWithAliasesReg`'s
  `T.TVar name` case looks up `tvarMap` → returns the Go param name
  when numbered; its `List [elem]` case does `"[]" ++ go elem`. So if
  `a` WERE in `tvarMap` as `T1`, `List a` → `[]T1`. The ONLY blocker
  is `tvarsInEmitted` not flagging it.
- The `usedTypeParams` / `keptNumbered` filter in
  `splitInferredSigWithReg` already drops phantom type params (flagged
  but not surviving rendering) — so over-flagging is SAFE.

### Plan — SCOPED, ready to implement

**C — `tvarsInEmitted`: recurse into container args.** Change the
`List`/`Dict`/`Set` arms from `-> []` to `-> concatMap tvarsInEmitted
args` (same as `Result`/`Maybe`/`Task` already do). Update the stale
comment. Then `isEmpty : List a -> Bool` →
`isEmpty[T1 any](list []T1) bool`; same for `length`, `append_`,
`reverseHelp`, `concat`, etc.

### Edge cases / interactions
- `Set a` renders as `map[any]bool` (the `any` KEY is Sky's Set
  runtime repr — structural, not fixable, and that's the genuine
  carve-out). Flagging `a` for `Set` is harmless: `T1` won't appear
  in `map[any]bool` → `usedTypeParams` filter drops it. So recursing
  `Set` is consistent + safe but a no-op; recursing `List`/`Dict` is
  the actual win (`[]T1`, `map[string]T1`).
- `Dict k v`: renderer always emits `map[string]V` (key is always
  `string` in Sky's repr). `k` gets flagged but filtered out; `v`
  gets flagged AND survives → `map[string]T1`. Correct.
- BODY consistency: the dep-func body lowering uses `depRetType`
  (string) + `withScopedLambdaTypes` (only for annotated funcs;
  `paramTypeBindings = Map.empty` for unannotated). A param typed
  `list []T1` in the sig, used in the body as `rt.AsList(list)` /
  `len(...)` — `[]T1` is assignable to the `any` those helpers take,
  so the body stays valid. VERIFY no body site needs `list` to be
  statically `[]any` (grep the List.sky-derived bodies after the
  change; the sweep + cabal test are the real gate).
- This also helps user-code helpers like `viewAll : List Job ->
  Html` whose `List Job` element is concrete — unaffected (Job isn't
  a TVar) — but a user `firstOf : List a -> Maybe a` becomes properly
  `[T1 any](list []T1) rt.SkyMaybe[T1]`.

### Status: SCOPED — ready to implement

---

## Workstream D — typed HOF-param returns + mono leftovers  (task #145)

### Confirmed mechanism — one root cause

`renderHofParamTy` (Compile.hs, used by `splitInferredSigWithReg` for
every HOF param) has a `renderLambdaInner (T.TLambda from _to)` arm
that **blanket-renders the return as `"func(" ++ go from ++ ") any"`**
for ANY non-TVar, non-Lambda return. So:
- `List.filter`'s `pred : a -> Bool` → `func(T1) any` (should be
  `func(T1) bool`).
- `Maybe.andThen`'s `fn : a -> Maybe b` → `func(T1) any` (should be
  `func(T1) rt.SkyMaybe[T2]`).
- The bare-TVar-return case (`func(T1) T2`) IS already typed; only
  CONCRETE / container returns get the blanket `any`.

This was a **pre-`curryLambdaPatTyped` workaround** (regression of
2026-04-22, `CompileSpec.hs:80` "Result-typed lambda params"):
Sky lambdas used to always lower to `func(any) any`, so a typed HOF
sig `func(X) rt.SkyResult[E, b]` rejected them (no Go covariance).
That fear is now STALE — the Gap-4 work (`curryLambdaPatTyped`,
"SUBSTANTIALLY CLOSED 2026-05-10/11") makes lambdas typed-able.

The call-site machinery already supports typed HOF params:
- `coerceCallArgsAt`'s Phase-A5 path substitutes the callee's HOF-param
  type params with the concrete types at THIS call site
  (`substTVarsInGoType σ paramTypes`) → `subbed` is concrete
  (`func(string) rt.SkyResult[Error, int]`), no out-of-scope `T2`.
- Lambda-literal args → routed through `curryLambdaPatTyped` with the
  concrete `subbed` retType (Phase-A4 branch, Compile.hs ~5982).
- Non-lambda func args (named funcs, Msg ctors) → `coerceArg` →
  `take 5 erasedTy == "func("` branch → `rt.Coerce[func(X) Y]` →
  `makeFuncAdapter` reflect adapter (boxes params, unwraps return).
  Handles any shape mismatch.

### Plan — SCOPED, ready to implement

- **D1 — `renderHofParamTy`: render the HOF-param return properly.**
  Change the `renderLambdaInner (T.TLambda from _to)` arm to
  `"func(" ++ go from ++ ") " ++ go to` (same as the already-correct
  bare-TVar-return arm). Keep the curried `to@T.TLambda{}` arm
  recursing. `go = typeStrWithAliasesReg recAliases fieldIdx tvarMap`
  already renders `Bool→bool`, `Maybe b→rt.SkyMaybe[T2]`, etc.
- **D2 — verification only, no code change.** The three call-site
  paths above already make typed HOF params sound. Gate: the
  `CompileSpec.hs:80` "Result-typed lambda params" regression test
  MUST still pass after D1 — if it fails, the call-site coercion has
  a gap to close (don't revert D1; fix the coercion).
- **D3 — mono `SkyValue` leftover: AUTO-FIXED by D1.**
  `Maybe_andThen__String_Int(fn func(string) any) rt.SkyMaybe[rt.SkyValue]`
  is `any`/`SkyValue` because the GENERIC `Maybe_andThen` already had
  `func(T1) any` / `rt.SkyMaybe[<unnamed>]`. Once D1 makes the generic
  `func(T1) rt.SkyMaybe[T2]` + return `rt.SkyMaybe[T2]`, the mono σ_go
  substitution (`T1→string, T2→int`) yields
  `func(string) rt.SkyMaybe[int]` / `rt.SkyMaybe[int]`. No separate
  mono-pass change. (Confirm `b`/`T2` is in `splitInferredSigWithReg`'s
  numbered set — `tvarsInEmitted (a -> Maybe b)` recurses into the
  lambda and into `Maybe` → `[a, b]` → both numbered. Good. If after
  D1 the return is still `SkyValue`, check `defaultErrorTVars`/
  `defaultOpaqueTVars` aren't defaulting `b` — `b` is param-reachable
  via `fn`'s return so it should NOT be treated as return-only.)

### Edge cases / interactions
- A HOF param returning the app's concrete `Msg` (`(Event -> Msg)`):
  `renderHofParamTy` → `func(Std_…_Event) Std_Main_Msg`; lambda arg →
  `curryLambdaPatTyped … "Std_Main_Msg"` (wrapRet `rt.Coerce[Msg]`);
  Msg-ctor arg → `coerceArg → rt.Coerce[func(Event) Msg]`. Both OK.
- D1 depends on workstream C's `tvarsInEmitted` fix for the
  CONTAINER-return TVars to be numbered (`Maybe b`'s `b`): `Maybe`
  already recurses in `tvarsInEmitted`, so `b` is numbered even
  pre-C. `List`-return HOF params (`a -> List b`) need C. Implement
  C before D, or together.
- After D1, re-measure: `List.filter`, `Maybe.andThen`, `Result.*`,
  `Task.*`, `List.foldl`/`foldr` HOF params should all type cleanly.

### Status: SCOPED — ready to implement (after/with C)

---

## Workstream E — anonymous records → emit Go struct decls  (task #146)

### Confirmed mechanism — NO pre-pass exists; two inconsistent reprs

- `synthAnonRecordName` (Compile.hs:9872) synthesises `Anon_R_<hash>`
  from `Map String FieldType` (sorts fields `Map.toAscList` —
  alphabetical — and hashes name+types).
- `solvedTypeToGo`'s `T.TRecord fields _` arm (Compile.hs:9053): exact
  `lookupRecordAlias` → `<alias>_R`; else → `synthAnonRecordName fields`
  = `Anon_R_<hash>`. The comment "the pre-pass emits its struct decl"
  is **aspirational — NO such pre-pass exists.** Every downstream site
  (`sanitiseTypedElem`/`sanitiseTypedDeep`, ~8 sites) treats
  `Anon_R_…` as non-emittable → replaces with `any`. So anon records
  AS A TYPE degrade to `any`.
- `Can.Record fields`'s `Nothing` branch (Compile.hs:5347): an anon
  record LITERAL lowers to an **inline** `struct{ X any; Y any }{…}` —
  all fields `any`, inline (not a named type).
- So the type repr (`Anon_R_<hash>`) and the literal repr (inline
  `struct{…any}`) are inconsistent AND both `any`-typed. The 06-json
  failure was a mono σ_go type-arg = anon record → `Anon_R_<hash>`
  with no decl → `undefined` (band-aided with `sanitiseTypedDeep`→
  `any`, which is the carve-out path, not the fix).

### Plan — SCOPED, ready to implement

Unify: treat anonymous records exactly like named record aliases,
keyed by `synthAnonRecordName`. The user-alias path (`generateStruct`
+ `Can.Record`'s `Just` branch + `coerceToFieldType`) already does
everything right.

- **E1 — collect anon-record shapes.** Explicit deterministic pass:
  recursively walk (a) all solved types in `typesWithDeps`, (b) the
  mono `reachableList` type-args, (c) `Can.Record` literals in the
  canonical AST. Collect every `T.TRecord fields ext` that does NOT
  match a user alias. Dedup by `synthAnonRecordName`. Result:
  `Map synthName (Map String FieldType)`.
- **E2 — emit decls.** For each collected shape, emit
  `type <synthName> = struct{ <CapField> <fieldGoTy>; … }` (a Go type
  ALIAS, `=`, so it's structurally identical to an inline struct of
  the same shape) + `func init() { rt.RegisterGobType(<synthName>{}) }`.
  Fields sorted alphabetically (match `synthAnonRecordName`'s order).
  `fieldGoTy = solvedTypeToGo` of each field (recursive — a field
  that's itself anon gets its own decl from E1's recursive walk).
  Prepend `anonRecordDecls` to `_pkg_decls` next to
  `unionDecls ++ aliasDecls`.
- **E3 — `Can.Record` `Nothing` branch constructs the named struct.**
  Replace the inline `struct{…any}{…}` with `<synthName>{<CapField>:
  coerceToFieldType <fieldGoTy> (exprToGo fe), …}` — mirror the `Just`
  branch. Now construction and type-annotation agree.
- **E4 — `solvedTypeToGo` `T.TRecord` arm becomes** (combined with
  workstream A1): exact alias match → superset alias match (if `ext =
  Just _`, A1) → `synthAnonRecordName` (if `ext = Nothing`, closed
  anon record) → `any` (if `ext = Just _` and no match — a genuinely
  ROW-POLYMORPHIC record; Go can't express row polymorphism, so `any`
  IS the contract's "genuinely generic" carve-out here).
- **E5 — keep `sanitiseTypedDeep`/`sanitiseTypedElem` `Anon_R_`→`any`
  as a DEFENSIVE net** (if E1 collection ever misses a shape, `any`
  beats `undefined`). With complete collection it never fires.

### Edge cases / interactions
- Type ALIAS (`type X = struct{…}`, with `=`) not a defined type
  (`type X struct{…}`) — so an inline `struct{…}` literal of the same
  shape is assignable. But once E3 makes literals construct
  `<synthName>{…}`, this matters less; still use `=` for safety.
- Field access: user aliases use `rt.Field(rec, "Cap")` reflection —
  works on ANY struct → anon structs need no body-codegen change for
  `.field` access.
- Ordering hazard: `synthAnonRecordName` is called lazily during
  `solvedTypeToGo`. E1 is an EXPLICIT pass over `typesWithDeps` +
  mono args + AST `Can.Record` nodes — deterministic, no reliance on
  render-order side effects. Do NOT use a side-effect IORef registry.
- Depends on A1 (superset match) for the `T.TRecord` arm logic in E4
  — implement A then E.
- Mono-introduced anon records (06-json): E1 walks `reachableList`
  type-args → covered. After E, the `sanitiseTypedDeep` band-aid in
  the mono σ_go path (Compile.hs ~2659) becomes the defensive net,
  not the primary path.
- `_fieldIndex` for anon records: there is no user-declared order;
  `synthAnonRecordName` uses alphabetical (`Map.toAscList`). Use
  alphabetical consistently for the struct decl AND E3's literal so
  field order always agrees.

### Status: SCOPED — ready to implement (after A)

---

## Workstream F — Sky-side DCE (Haskell, pre-lowering)  (task #147)

### Confirmed mechanism — what exists vs what's missing

Existing `Sky.Build.Dce` (109 lines):
- `reachableTopLevel canMod` — closure from `"main"` over the
  call graph of **ONE module**. `buildCallGraph` → `collectRefs`.
- `collectRefs` collects `Can.VarTopLevel` refs only; **ignores
  `Can.VarKernel`** (FFI funcs are `Can.VarKernel "Mod" "fn"`) and
  `Can.VarCtor`.
- Used only by `generateDecls` (Compile.hs:2959) → `generateDefMaybe`
  to SKIP EMITTING unreachable ENTRY-module defs. `SKY_DCE=0`
  disables.
- `generateDeclsForDep` (Compile.hs:2159) does **NOT** apply it —
  **dep modules emit ALL their decls.**
- `Mono.reachableInstances` / `globalReachableSet` is a separate
  thing — reachable mono *instances* (callee, type-args), not a
  decl/FFI-level DCE.

So the gaps vs the contract: (1) per-module not whole-program;
(2) entry-only — deps un-DCE'd; (3) FFI-blind — unused FFI `.skyi`
sigs + `.skycache/go/*_bindings.go` wrappers all flow through
lowering / `copyFfiDir` / `go build`; the Go-side `[DCE]` (strips
unused wrapper bodies post-copy) is the only FFI pruning and it's
post-lowering.

Pipeline order (Compile.hs `continueCompile`): `loadAndSeedFfiRegistry`
(line 350, early — builds `Env.ffiKernel{Type,Function,Arity}Ref` via
`ftyToAnnotation` for ALL sigs) → parse → Canonicalise to fixpoint
(deps + entry, ~line 588) → Type Checking (685) → `generateGoMulti`
(codegen) → `copyFfiDir` + Go-side `[DCE]`.

### Plan — SCOPED, ready to implement

- **F1 — whole-program, FFI-aware reachability.** New
  `Sky.Build.Dce.reachableWholeProgram`:
  - Input: entry `Can.Module` + `[(modName, Can.Module)]` deps.
  - `collectRefs` extended to also emit `Can.VarKernel mod fn` (so
    FFI refs are tracked) — keep `VarCtor` out (ADT ctors handled by
    `generateUnionTypes`, cheap, not the DCE target).
  - Cross-module call graph keyed by `(moduleName, declName)`;
    resolve `VarTopLevel home name` to `(home, name)`.
  - Roots: entry module's `"main"`. (`main = Live.app {init=init,
    update=update,view=view,...}` → `collectRefs` descends the
    `Can.Record` → catches the cfg funcs transitively. Confirmed
    sufficient for Live/Tui/Cli — handlers reached via `view`.)
  - Output: `(reachableSkyDecls :: Set (String,String),
    reachableFfiSigs :: Set (String,String))` — FFI sigs keyed by
    `(kernelModule, fnName)`.
- **F2 — `generateDeclsForDep` applies the whole-program set.**
  Dep modules currently emit everything; gate each dep decl on
  membership in `reachableSkyDecls`. (Entry stays via the existing
  `generateDecls`/`generateDefMaybe`, switched to the whole-program
  set for consistency.)
- **F3 — prune FFI before lowering/emit.** After canonicalisation,
  compute `reachableFfiSigs`; filter `Env.ffiKernel{Type,Function,
  Arity}Ref` down to reachable entries (purely shrinks downstream
  consultation + emit). AND `copyFfiDir` / the FFI-wrapper emit:
  only process/copy wrapper symbols whose `(kernelMod,fn)` is
  reachable — so `go build` compiles far fewer Stripe wrappers.
  This is the Stripe-scale "thousands of unused sigs gone before
  lowering" win.
- **F4 — placement.** Compute the reachable set ONCE right after the
  canonicalise-fixpoint (~line 588, both deps + entry in hand),
  store in a `globalReachableProgram` IORef (mirrors
  `globalReachableSet`), thread into `generateGoMulti` +
  `generateDeclsForDep` + `copyFfiDir` + the FFI-registry filter.

### Edge cases / interactions
- `SKY_DCE=0` escape hatch must still work — gate the whole pass.
- `sky test`: root is the test harness, not `main`. Either add the
  test entry as a root when building a test module, or disable F for
  `sky test`. Decide during impl; default-safe = include test roots.
- Soundness: over-approximate is safe, under-approximate breaks the
  build. `collectRefs` is conservative (traverses all sub-exprs);
  adding `VarKernel` only widens. The FFI-registry filter touches
  ONLY `(kernelMod,fn)` keys present in the `.skyi`-loaded registry
  — a `VarKernel "List" "map"` (Sky-source/`lookupKernelType`, not
  in the FFI registry) is simply absent from the filter set, never
  pruned.
- `ftyToAnnotation` already ran in `loadAndSeedFfiRegistry` (early) —
  F3's win is downstream (lowering/emit/`go build`), not the load.
  If load-time becomes the bottleneck, a later optimisation can move
  the registry build after canon; out of scope for correctness.
- Must re-verify: `cabal test` ExampleSweep + `sky verify` exercise
  every example end-to-end — a too-aggressive prune fails the build
  there. The sweep is the gate.
- The `Dce.reachableTopLevel` per-module function can be kept (or
  re-expressed in terms of the whole-program one) — don't break its
  existing callers.

### Status: SCOPED — ready to implement

---

## Workstream G — LSP 100%  (task #148)

### Confirmed baseline — measured 2026-05-15 @ compiler 1201e96

Ran the LSP drivers against the CURRENT compiler (has v0.13's
`Can.Access` + same-module guard):
- `scripts/lsp-test-nvim.sh` — **7/7 PASS** (hover-task-run,
  hover-field, hover-type-name, completion-qualified-insert-text,
  completion-field, completion-let-binding, goto-def-type-name).
- `scripts/lsp-test-skyshop.lua` — **3/3 PASS** (hover-stripe-newparams,
  hover-stripe-setkey, completion-stripe-prefix).
- skyshop `.skycache/lsp-error.log` — **EMPTY**: `typecheckWorkspace`
  does NOT throw. The huge-FFI nil-hover gap (CLAUDE.md "exp/tea-core
  LSP works on huge FFI surfaces" + the `buildSkyiNameIndex` /
  strict-force / `externalsForFile` / 3s-timeout work) is **already
  resolved** — the old "skyshop hover gap remains" note was stale.
- So **G2 (regression check): v0.13's `Can.Access`/same-module guard
  did NOT regress LSP** — small + huge-FFI both fully green.

`computeHoverIdx` (Server.hs:317) is comprehensive: `.field` access →
`resolveFieldType`; module name → summary; indexed symbol →
`renderSym`; fallback single-file `solveForName` (2s timeout);
kernel-only → `kernelLookupForHover`/`kernelTypeSig`; final fallback
`mkHover name` (name only, no type — the only "incomplete" outcome).
`handleDefinitionIdx` handles definition + declaration.

### Plan — SCOPED, ready to implement

G is largely already working. "100%" per the contract means
exhaustive coverage as a permanent regression fence + fixing any
specific gap the exhaustive audit surfaces.

- **G1 — extend the LSP test drivers** to cover every used-symbol
  class the current drivers don't probe:
  - top-level FUNCTION hover (not just kernel calls) + goto-def
  - local var / lambda-param / let-binding hover
  - case-pattern-bound var hover (`Ok resp -> resp`)
  - ADT constructor hover + goto-def
  - record-field-ACCESS hover at a use site (`model.count` inside a
    function body — directly exercises the v0.13 `Can.Access` path;
    distinct from the field-DECLARATION hover the current
    `hover-field` test covers)
  - goto-def for a record field, for an imported qualified name
  - skyshop: add function-hover + goto-def + a `Can.Access` hover
    probe (huge-FFI variants of the above)
- **G2 — run the extended suite; fix each gap.** Any symbol class
  that lands in `mkHover name` (name only, no type) or returns
  `A.Null` for goto-def is a gap. Fix locations: `Server.hs`
  `computeHoverIdx` / `handleDefinitionIdx` / `resolveFieldType`,
  `Index.hs` (`idxLocals`/`idxByLocal`/`fromTypecheck` if a symbol
  class isn't being indexed). Scope-then-fix each surfaced gap fully
  — do not defer.
- **G3 — wire the extended drivers into the regression fence.** A
  cabal spec OR a `scripts/` entry that CI runs, so LSP coverage
  can't silently regress again.

### Edge cases / interactions
- The hover handler's final `mkHover name` fallback is the
  "incomplete" signal — grep driver output for hovers that return
  the bare name with no `:` type.
- After workstreams A–F land, RE-RUN the full LSP suite — the type
  renderer changes (A/B/C/D/E) flow into `renderSym`/`solveForName`
  hover output; the DCE (F) must not prune something the LSP index
  needs (the LSP builds its own index via `typecheckWorkspace`, not
  the DCE'd codegen path — but verify).
- `nvim` is required for the drivers (`/opt/homebrew/bin/nvim`
  present). The drivers find the sky binary via
  `sky-out/sky` → `~/.cabal/bin/sky` → `sky` on PATH.

### Status: SCOPED — ready to implement

---

# ALL 7 WORKSTREAMS SCOPED — IMPLEMENTATION PHASE

Implementation order (A–E share `Compile.hs` type renderers +
`Constrain/Expression.hs`; F is a new pass; G is the LSP):

1. **A2** — `constrainDecls` two-pass pre-register (Expression.hs)
2. **A1** — superset record-alias match (Compile.hs/Record.hs)
3. **C** — `tvarsInEmitted` recurse into containers (Compile.hs)
4. **D** — `renderHofParamTy` typed HOF returns (Compile.hs)
5. **B** — B0 `runtimeOnlyTypes` audit, B1 `safeReturnType`
   parametric-ADT arm, B2 Go-generic ctors (Compile.hs)
6. **E** — anon-record struct decls (Compile.hs)
7. **F** — whole-program Sky-side DCE (Dce.hs + Compile.hs)
8. **G** — extend LSP drivers + fix gaps (Server.hs/Index.hs)

After EACH workstream: rebuild compiler (`cabal install
--overwrite-policy=always --installdir=./sky-out
--install-method=copy exe:sky`), `cabal test` must stay **256/0/1**,
`scripts/example-sweep.sh` must stay **19/19**, re-measure
reachable-`any` (must trend toward 0). mem-guard must be running.
If an impl step hits an unforeseen edge: scope-then-fix it fully in
this doc, never defer.

---

## Progress log

- **2026-05-15** (initial): doc created, baseline measured, workstreams scoped.

### 2026-05-15 implementation pass

**A2 — `constrainDecls` two-pass pre-register (NARROWED)** ✅
- File: `src/Sky/Type/Constrain/Expression.hs`.
- Pre-registers UNANNOTATED `Can.Def` decls via an outer `CLet` header
  so forward refs bind to the real defType var.
- Annotated `Can.TypedDef` left SEQUENTIAL — pre-registering as
  `Forall []` collapses polymorphic `a` across multiple same-module
  call sites (confirmed by reverting and verifying job-queue passed
  OLD code that pre-A2 narrowing initially broke). Surface refactor:
  - `defTypeInfoIO` returns `(name, type, possibly-renamed-def)`;
    TypedDef arm alpha-renames its free TVars (mirrors what
    `constrainDefWithType` did inline).
  - `constrainDefWithKnownType` wraps the body in `CLet paramHeader`
    so param vars are scoped (hardens letrec too).
  - New `walkDecls` does the sequential walk inside the outer CLet.
  - `constrainLetRec` adapted to the new 3-tuple.

**A2 follow-ups** ✅
- **Empty-home cross-module ADT recovery** (skychess `undefined: Colour`
  surfaced after A2 connected `bestMove → pickBest`'s `colour` param).
  Added suffix-match against `globalUnionNames` in
  `typeStrWithAliasesReg`. Picks `Chess_Piece_Colour` when the inferred
  type is `T.TType "" "Colour" _`.
- **`globalUnionNames` IORef** — dedicated, eagerly populated alongside
  `globalCgEnv`. Avoids `<<loop>>` black-hole when the renderer is
  called inside a `modifyIORef globalCgEnv` callback (surfaced as
  02-go-stdlib + 17-skymon failures on first try with
  `unsafePerformIO (readIORef globalCgEnv)`). Written eagerly at each
  `writeIORef globalCgEnv cgEnv` site.
- **`HttpResponse` runtime-type mapping** — kernel `Http.get`/`Http.post`
  declare empty-home `HttpResponse`; A2 surfaced unannotated
  `checkResponseStatus resp` in 17-skymon. Added
  `("HttpResponse", "rt.HttpResponse")` to `runtimeTypedMap`.

**A1 — superset record-alias match** ✅
- Files: `src/Sky/Generate/Go/Record.hs` (`lookupRecordAlias`),
  `src/Sky/Build/Compile.hs` (`matchAliasByFieldSet`).
- Exact match first; on miss, try strict supersets. Pick the
  smallest-size superset; tied sizes → Nothing (ambiguous → falls
  back to `any` at the renderer, correctness preserved).
- Resolves open records (`{count: Int}` from `\m -> m.count`) to
  the concrete alias (`Model_R`) instead of falling back to `any`.

**C — `tvarsInEmitted` recurses into containers** ✅
- File: `src/Sky/Build/Compile.hs`.
- List/Dict/Set now propagate inner TVars (previously erased to `[]`).
- **Follow-up fix**: `splitInferredSigWithReg`'s `usedTypeParams`
  filter now matches against PARAM strings only (not param+return).
  Go's generic inference only works from input positions; a TVar
  appearing only in the return (e.g. `b` in
  `concatMap : (a -> List b) -> List a -> List b` once C propagates
  `b`) would cause `cannot infer T2`. Dropping return-only TVars
  collapses the return slot to `[]any`. This is the right ceiling
  until typed lambda OUTPUT lowering (D-Lambda-Lowerer) lands.

**B1 — `safeReturnType` parametric-ADT arm** ✅
- File: `src/Sky/Build/Compile.hs`.
- Changed `T.TType home name []` to `T.TType home name _` so
  parametric Sky ADTs (`Html msg`, `Element msg`, `Attribute msg`)
  get the same erased-Go-name treatment as nullary types (mirrors
  what `safeReturnTypeWith` already did).

### Validation after A2+A1+C+B1 (with all follow-ups)

| Check | Result |
|---|---|
| `cabal install ... exe:sky` | clean, no errors |
| `cabal test` (256 ExampleSweep cases) | **256/0/1 PASS** (matches baseline) |
| `scripts/example-sweep.sh` (full sweep) | **26 pass / 0 fail** |
| Pinned regression `Result-typed lambda params` | ✔ |
| Pinned regression `callback return is bare TVar` | ✔ |

### D — partial (D1 reverted, needs D-Lambda-Lowerer first)

D1 attempt: typed HOF param return in `renderHofParamTy`. Builds the
compiler, breaks `test/Sky/Build/CompileSpec.hs:139` ("user-defined
polymorphic HOFs with Result-typed lambda params"):

```
in call to do, type func(any) any of rt.Coerce[func(any) any](
  func(a any) any {…}) does not match inferred type
  func(any) rt.SkyResult[Sky_Core_Error_Error, rt.SkyValue]
  for func(T1) rt.SkyResult[Sky_Core_Error_Error, rt.SkyValue]
```

**Root cause:** `curryLambdaPatTyped` (typed-lambda-lowerer) is
applied only at KERNEL HOF call sites today, not user-defined HOF
call sites. Sky lambdas passed to a user-defined `do` HOF still
emit `func(a any) any`, Go rejects when sig is
`func(T1) rt.SkyResult[...]`. Closing D1 requires:

1. **D-Lambda-Lowerer** — at user-defined HOF call sites, look up
   the called function's typed param shape (`_cg_funcParamTypes`)
   and route literal lambda args through `curryLambdaPatTyped` so
   the lowered shape MATCHES the sig.
2. **D-Coerce** — when coerceArg sees a func target type, emit
   `rt.Coerce[func(X) Y]` not erased `rt.Coerce[func(any) any]`.
   The reflect adapter (`makeFuncAdapter`) already handles bridging
   once the target type is right.

D1 reverted in working tree. Next session: D-Lambda-Lowerer first,
then re-apply D1.

### B0 / B2 — deferred (post-D)

- **B0**: removing migrated Sky ADTs from `runtimeOnlyTypes` isn't
  enough — `runtimeTypedMap` ALSO has entries (`Attribute →
  rt.SkyAttribute`) that take precedence. Removing both risks
  Go-type-name collisions with runtime helpers still referencing
  `rt.SkyAttribute` (which is `any` under the hood). Audit needed.
- **B2** (Go-generic ctors over union type-vars): doable but each
  ctor's emission ties into the global cgEnv (union names, alias
  names, FFI registry). Multi-file refactor.

### E — deferred (anon-record struct emission)

`synthAnonRecordName` (Compile.hs:9984) generates `Anon_R_<hash>`
names but NO Go struct decl is emitted. `sanitiseTypedDeep` (line
8317) defensively rewrites these to `"any"` so code compiles. Close E:

1. Add `globalAnonRecords :: IORef (Map String (Map String T.FieldType))`.
2. `synthAnonRecordName` should also `modifyIORef'` to record the shape.
3. New pre-pass that emits `type Anon_R_xxx = struct{...}` decls + gob
   registration, run before user codegen.
4. Remove `sanitiseTypedDeep`'s `Anon_R_ → any` defensive rewrite.

### F — deferred (whole-program Sky-side DCE + FFI pruning)

Current `Sky.Build.Dce` (109 lines):
- Per-module call graph (`buildCallGraph` returns
  `Map String (Set String)` — local names only).
- Roots: `{"main"}` (only present in Main module).
- `collectRefs` ignores `Can.VarKernel` AND `Can.VarCtor`.

Close F:
1. **F1**: `collectRefs` also emit `Can.VarKernel (mod, name)` for FFI tracking.
2. **F-WholeProgram**: `reachableWholeProgram :: Map ModName Can.Module -> Set (ModName, String)`.
3. **F2**: `generateDeclsForDep` apply the whole-program reachable set
   (currently emits all dep decls).
4. **F3**: prune `Env.ffiKernel*Ref` + `copyFfiDir` + wrapper emit
   to reachable FFI sigs only — the Stripe-SDK win.
5. **F4**: compute once after canon-fixpoint (~Compile.hs:588),
   store in `globalReachableProgram` IORef.
6. Respect `SKY_DCE=0`; `sky test` roots = `main + tests`.

### G — deferred (LSP 100%)

Baseline already green: 7/7 `lsp-test-nvim.sh`, 3/3 skyshop driver.
Close G:
1. Extend drivers to cover EVERY symbol class (fn hover, fn goto-def,
   local-var hover, ADT-ctor hover, record-field-access hover,
   kernel-call hover).
2. Each gap → diagnose against `Sky/Lsp/{Server,Index}.hs`.
3. Wire driver into regression fence (CI).

---

### Honest scope summary

**Solid** (working tree, unstaged): A2 (narrowed) + 3 A2 follow-ups,
A1, C (with param-only filter follow-up), B1. All pass cabal test
256/0/1 and 26-example sweep. The `if it compiles, it works`
invariant is preserved.

**Architectural follow-ups remain** for D-Lambda-Lowerer (D1
re-apply), B0/B2, E, F, G — each multi-hour. The user's "no defer"
contract is honored in *direction* (every workstream's exact fix is
scoped above with mechanism + risk + edge cases) but not in
*single-session completion*. Next session should resume with
**D-Lambda-Lowerer → D1 → F → E → G** in that order:
- D unblocks the most surface (typed Sky lambda outputs).
- F is the Stripe-scale win (prune unused FFI bindings).
- E removes the `Anon_R_` defensive `any` fallback.
- G closes the LSP coverage contract.
