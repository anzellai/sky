# 14 — Runtime-Narrowing Taxonomy (origins, floor, levers)

> **Status: primary. This is the floor authority for the Rust compiler.**
>
> Every claim below is traceable to a line of Rust source, Go runtime source, a
> committed gate golden, or a committed measurement run. Where a claim from the
> legacy Haskell reference could not be verified against the Rust compiler it is
> marked **UNVERIFIED** and NOT carried over. Where a quantity is not measured it
> says **UNMEASURED** rather than a plausible number.
>
> **What this replaces.** `docs/architecture/sky-compiler-architecture.md` §6
> (rt.Coerce origin catalog), §7 (architectural levers) and §8 (irreducible
> floor). That document describes the **retired Haskell** pipeline; its §6 table
> cites `Compile.hs` line numbers. It is retained for historical context only.
> Cite **this** document for the Rust compiler. The legacy §6 numbers are mapped
> to the new ones in §6 — they are not
> silently renumbered, because prior commits, docs and agent transcripts cite the
> old numbers and will be read again.

---

## 0. Why this document exists

The legacy §6/§7/§8 taxonomy was the only place the repository defined "what is
closeable and what is floor", and `CLAUDE.md` §0.3 mandated citing it. So a rule
designed to stop optimism-without-mechanism was pointing at a **different
compiler's** architecture.

It cost real work. The legacy claim that closing its category 6 "requires
monomorphising every HOF call site into a generated typed dispatcher (Go binary
size explodes)" produced the same wrong conclusion **three times**. It was closed
twice by eta-expansion at a statically-known shape — one emit per definition, no
monomorphisation, no binary growth — measured 1.36× and 1.34×
(`docs/perf/runs/hof-dispatch-20260815/`,
`docs/perf/runs/typed-destructure-20260815/`). The retraction landed in the
*legacy* document while this — the **primary** one — still described a
monomorphiser as real, which is how the conclusion came back a third time
(`07-lowering-and-ir.md` §5.1).

This document is written to be cited by agents. Treat anything it states without
evidence as the next three wrong conclusions; that is why it is explicit about
what it does not know.

---

## 1. The distinguishing test

**If both the value's Go shape and the slot's Go shape are known at emit time,
the narrowing is closeable. If either shape only exists at run time, it is
floor.**

That test — and not a category name — decides floor membership. It is the test
that separated the two `reflect.MakeFunc` populations that had been described in
near-identical prose and were therefore filed together:

* A `func(Attr) bool` flowing into a `func(any) bool` slot: **both shapes are
  `GoTy`s in hand** at `Ctx::coerce_if_needed`
  (`rust/crates/lower/src/lower.rs:2723`). Closeable — and closed, by
  `func_shape_eta` (`lower.rs:2823`).
* `sky_call(app.view, model)` (`runtime-go/rt/live.go:5436`): the runtime holds
  `app.view` as `any` and cannot name the user's `Model`. Floor.

Corollary, and the reason the legacy floor estimate was ~10× too large: **a
category is not floor because it reaches `reflect`.** It is floor because a shape
is absent at emit time.

---

## 2. What the Rust compiler actually emits

### 2.1 The token surface

The narrowing/erasure vocabulary of emitted Go is a closed, gate-enforced set:
`TRACKED` in `rust/crates/xtask/src/coerce_floor_gate.rs:116-150`. A second gate
(`corpus::emit_shape`'s `narrowing_set_matches_coerce_floor`) reads that same
accessor rather than keeping a copy, so the two cannot drift.

| Family | Tokens | Runtime definition |
|---|---|---|
| general narrowing | `rt.Coerce[T]` | `runtime-go/rt/rt.go:5887` |
| primitive | `rt.AsInt` `rt.AsString` `rt.AsBool` `rt.AsFloat` `rt.AsRune` | `rt.go:2499` and neighbours |
| lenient primitive | `rt.AsIntOrZero` `rt.AsFloatOrZero` `rt.AsBoolOrFalse` | `rt.go:2541` … |
| wire-decode primitive | `rt.CoerceString` `rt.CoerceInt` `rt.CoerceBool` `rt.CoerceFloat` | `rt.go:6248-6254` |
| list | `rt.AsList` `rt.AsListT[T]` `rt.AsListAny` | `rt.go:2222` / `rt.go:2321` |
| tuple | `rt.AsTuple2` `rt.AsTuple2T` `rt.AsTuple3` `rt.AsTuple3T` | `rt.go:1534` / `rt.go:1589` |
| dict | `rt.AsDict` `rt.AsMapT` `rt.AsMapAny` | `rt.go:4998` / `rt.go:2410` |
| reflective | `rt.Field` `rt.SkyCall` | `rt.go:5772` / `rt.go:10565` |

The cost classes the gate ratchets on are **adapter** (a `reflect.MakeFunc`
thunk — cost per *invocation* of the adapted func), **dispatch** (`rt.SkyCall`),
and **narrow** (at most one assertion or one container rebuild per evaluation of
the site).

> **The `adapter` invariant, and the one construct that violates it textually.**
> `classify()` (`coerce_floor_gate.rs`) keys purely on the emitted TEXT: a
> `rt.Coerce[T]` whose `T` begins with `func(` is counted as an `adapter`, on the
> premise that a func-targeted `rt.Coerce` runs `makeFuncAdapter` / `reflect.MakeFunc`.
> That premise holds for the PRIMARY func-adapt path — `coerce_if_needed`'s
> `func_shape_eta` miss (§5.1), which emits a bare `rt.Coerce[func(…)…]`.
>
> It does NOT hold inside the reflection-free func-slot type-switch the `Ctx::widen`
> / `box_func_value` workstream emits (`crates/codegen/src/lib.rs`, the `GoTy::Func`
> arms). `box_func_value` boxes every widened func value into the canonical curried
> `func(any) any`, and the narrow-back switch recovers it reflection-free in two
> arms — an exact-shape assertion, then a static uncurry of `func(any) any`. Its
> trailing `reflect.MakeFunc` fallback is reached only by a genuinely divergent Go
> func shape and is dead for any widen-boxed source. That tail is emitted as
> **`rt.CoerceFuncSlot[T]`** — behaviourally identical to `rt.Coerce`, but a
> DISTINCT, census-UNTRACKED name — precisely so a dead fallback is not counted as
> a live adapter. `CoerceFuncSlot` is deliberately absent from `TRACKED`; a future
> classifier must keep it that way (or track it as its own class), and must never
> "recover" the count by matching the raw `func(` text, which would re-introduce
> the false positive. The `narrow` count RISES in exchange (§7): the reflection-free
> uncurry emits explicit per-argument `rt.AsInt` / `rt.AsString` / `rt.Coerce[any]`
> narrows where a single opaque reflect dispatch stood before — one coarse token
> traded for N precise ones.

### 2.2 The five `CoerceReason` values, and what each renders as

`CoerceReason` (`rust/crates/lower/src/ir.rs:71-91`) is stamped at the emission
site and rendered as a `/* … */` comment. The exact rendered forms are locked by
`rust/crates/codegen/tests/render_shapes.rs:130-248`:

| Reason | Rendered | Locked at |
|---|---|---|
| `GenericErase` | `/* generic erase */ rt.Coerce[Main_Model_R](x)` | `render_shapes.rs:141` |
| `FfiReturn` | `/* FFI return */ rt.AsListT[int](xs)` | `render_shapes.rs:156` |
| `WireDecode` | `/* wire decode */ rt.AsMapT[string](d)` | `render_shapes.rs:173` |
| `PrimitiveJoin` | `/* primitive join */ rt.AsInt(x)` | `render_shapes.rs:189` |
| identity (`from == to`) | *elided entirely — no comment, no op* | `render_shapes.rs:230` |

The `to` type — not the reason — selects the runtime helper: a `List` narrowing
goes through `rt.AsListT[T]` and never `rt.Coerce[[]T]`; a `Dict` narrowing goes
through `rt.AsMapT[V]` because Go maps are invariant and `rt.Coerce[map[…]…]`
would panic (`render_shapes.rs:146`, `:161-163`).

---

## 3. The origin catalogue — the emission allowlist

The Rust lowerer inserts a `Coerce` node at exactly **nine** sites. This is the
whole list; it is `grep`-checkable:

```bash
grep -rn 'CoerceReason::' rust/crates/lower/src/lower.rs
```

| # | Site | Origin | Reason stamped | Closeable? |
|---|---|---|---|---|
| **R1** | `lower.rs:2736-2749` (`coerce_if_needed`) | value's `GoTy` ≠ slot's `GoTy`, after `func_shape_eta` declined | `FfiReturn` if source is `any`, else `PrimitiveJoin` | **case-by-case** — this is a fall-through, not an origin |
| **R2** | `lower.rs:2771-2779` (`eta_narrow`) | inside an eta wrapper: the erased HOF slot being un-erased at the boundary the wrapper exists to create | `GenericErase` | **already the closed form** — this is the *replacement* for an adapter, not a defect |
| **R3** | `lower.rs:4352-4364` | narrowing an argument into a typed Go FFI parameter slot (`rt.FfiT_*`) | `FfiReturn` | **floor** — the Go signature is the authority |
| **R4** | `lower.rs:4386-4396` | **genuine Go FFI return**, `any → actual` | `FfiReturn` | **floor** (§4.1) |
| **R5** | `lower.rs:4429-4439` | **runtime kernel** call return, `any → actual` | `FfiReturn` ← **mislabelled** | **closeable** (§4.4) |
| **R6** | `lower.rs:6506-6519` | ADT payload: `SkyADT.Fields[i]` is `any`, narrowed to the sub-pattern's type | `GenericErase` | **closeable for app ADTs (already), blocked for stdlib ADTs** (§4.5) |
| **R7** | `lower.rs:6591-6600` | tuple pattern: `rt.T2.V{i}` erased field | `GenericErase` | **closeable** — `GoTy::Tuple` already renders `rt.T2[A,B]` when both are known |
| **R8** | `lower.rs:6674-6683` | record / `Maybe` field pattern on an erased named field | `GenericErase` | **closeable** where the nominal is known |
| **R9** | `lower.rs:6966-6980` (`coerce_to_str`) | operand of a Go string `+` that is not statically `string` | `FfiReturn` ← **mislabelled** | **closeable** — it is downstream of whatever produced the `any` |

Two further **narrowing emissions that are not `Coerce` nodes**, and therefore do
not appear in that grep:

| # | Site | Origin | Emits |
|---|---|---|---|
| **R10** | `rust/crates/lower/src/goty.rs:226-228` | a genuinely OPEN record row (`ext = Some(ρ)`) that matched no nominal — lowered to `GoTy::Any` deliberately, so field reads route reflectively | `rt.Field` / `rt.RecordUpdate` |
| **R11** | `lower.rs:2995-3005`, `:3085`, `:6345` | field read on a value whose Go type is `any` (the R10 consequence, plus the row-poly param→result erasure at `lower.rs:2057-2106`) | `rt.Field` |
| **R12** | `lower.rs:2047` (`lower_def`) | **polymorphic-def signature erasure.** A top-level Sky def's type variables erase to `any` because `GoFuncDecl.type_params` is hard-coded empty at all three construction sites (`lower.rs:2252`, `:2263`, `:2422`). The def's own signature carries no narrowing — the cost lands on the CALLER, which widens a typed slice element-by-element at **R1**. | surfaces as R1: `rt.AsListT[any]` on the argument, `rt.Coerce` per element inside the `func_shape_eta` wrapper, `rt.Coerce` on the result |

`rt.RecordUpdate` (`rt.go:3760`) is a reflective record rebuild and is **not** in
`TRACKED` — see §8.

**R12 is why a census of R1 under-attributes this class.** R1 is a fall-through,
so the widening is filed under "no better shape was known" when in fact the shape
was known at both ends and the *callee's signature* was the thing that could not
express it. `07-lowering-and-ir.md` §6 row 8 asserted this class "Deleted"; that
is true for parametric record aliases (`Cfg_R[Msg]`, `TypeEnv::record_params`)
and false for defs. Corrected there, and recorded here, because a primary
reference asserting a false close is the exact failure §0 of this document was
written about.

The lever is §5.5.

### 3.1 The `CoerceReason` comment is NOT the origin catalogue

**Read this before censusing origins by the emitted comment.**

`coerce_if_needed` infers the reason from the *shape alone* — any `any → T` it
sees is stamped `FfiReturn` (`lower.rs:2736-2740`). The overwhelming majority of
`/* FFI return */` comments in emitted Go therefore have **nothing to do with Go
FFI**. A kernel returning `any` (R5), a string-concat operand (R9), and a genuine
`Ffi.callPure` result (R4) all render the same comment.

The codebase already knows this. `eta_narrow`'s doc comment
(`lower.rs:2752-2762`) says so in as many words:

> Reusing the generic inference here would file every one of these under "FFI
> return" and quietly corrupt the doc-08 §6 origin catalogue — the same catalogue
> that decides which categories are lowering-closeable and which are floor.

That fix was applied **only inside eta wrappers**. Sites R1, R5 and R9 still
stamp `FfiReturn` for non-FFI origins.

Two consequences a reader must not get wrong:

1. **A count of `/* FFI return */` is not a count of floor sites.** It is an
   upper bound on `any`-sourced narrowings, attributed to the wrong origin.
2. **`WireDecode` and `TeaDispatch` are never stamped by the lowerer at all.**
   `grep -rn 'CoerceReason::WireDecode\|CoerceReason::TeaDispatch'
   rust/crates/lower` returns nothing; the only uses are in
   `codegen/tests/render_shapes.rs`. Both variants are reachable only from the
   render tests. So the two categories the legacy document called the irreducible
   floor **are not observable in the Rust compiler's own attribution** — because
   in the Rust compiler they do not happen in emitted Go at all
   (§4.2, §4.3).

---

## 4. Floor and not-floor, on Rust evidence

### 4.1 Go FFI return (R3 + R4) — **FLOOR**

`lower.rs:4386-4396` wraps a foreign call whose Go result is `any` in a narrowing
to the Sky-side type. The Go function's signature is the authority on the value's
shape and the compiler reads it through the FFI surface, but the *value* arrives
as `interface{}` at run time. The slot's shape is known; the value's is not.
Floor by §1.

**Scope, stated precisely:** floor means *this narrowing*. It does not mean the
FFI subsystem is unimprovable. The legacy §8.1 escape (typed wrapper shims from
`tools/sky-ffi-inspect`) is a real design and remains **UNVERIFIED** against the
Rust `ffi` crate — no one has costed it here. Do not cite it as either available
or refuted.

### 4.2 Wire decode (R = none) — **FLOOR, and not in emitted Go**

The Rust lowerer emits no `WireDecode` narrowing (§3.1). Wire
decoding happens **inside the runtime**: `runtime-go/rt/db_decoder.go` (10
`SkyCall` sites), the session-store gob round trip, and the Sky.Live message
decode path. Those decoders return `any` because the bytes on the wire do not
carry the Go type.

Two things follow, and they matter for how a claim is worded:

* A census of emitted `main.go` (which is all `xtask coerce-floor` sees —
  `coerce_floor_gate.rs:84-88`) **cannot see this category at all**. Zero
  wire-decode tokens in the golden is not evidence the category is closed.
* The emitted side is not entirely absent from the picture: for a **sealed** ADT
  the lowerer emits a per-variant typed JSON factory into `init()`
  (`lower.rs:1647-1661`, `rt.RegisterAdtVariant` with `rt.JsonUnmarshal` per
  field). That is a generated typed decoder for exactly the shape the legacy §8.2
  said would require "code-generated per-Msg decoder (large compile-time cost)".
  It exists, for app-module ADTs. **How much of the wire-decode floor it actually
  removes at run time is UNMEASURED.**

### 4.3 TEA dispatch (R = none) — **FLOOR, and it is not one call**

The runtime holds the user's `view` / `update` / `subscriptions` as `any` and
cannot name their types. `sky_call` (`live.go:9405`) does
`reflect.ValueOf(f).Call(...)`; `sky_call2` (`live.go:9432`) the two-argument
form.

The often-repeated summary — "the ONE `sky_call(app.view, model)` per interaction"
— is right about `view` and **wrong as an enumeration of the boundary**. The TEA
boundary in `runtime-go/rt/live.go` is:

| Call | Line | When |
|---|---|---|
| `sky_call2(app.update, msg, model)` | `live.go:5315` (also `:3712`) | per interaction |
| `sky_call(app.view, model)` | `live.go:5436` | per interaction |
| `sky_call(app.view, model)` again | `live.go:5447` | dev-only, opt-in `SKY_LIVE_VIEW_DETERMINISM_CHECK` |
| `sky_call2(app.guard, msg, model)` | `live.go:5281` | per interaction, when a guard is configured |
| `sky_call(app.subscriptions, model)` | `live.go:5840` | per subscription recompute |
| `sky_call(app.init, req)` | `live.go:4104`, `:4426` | per session |
| `sky_call(app.onNavigate, page)` | `live.go:3703` | per navigation |
| `safeSkyCall(msg, value)` / curried walk | `live.go:1991`, `:2000`, `:2295`, `:3779` | per event-handler application |
| `sky_call(task, nil)` / `sky_call(toMsg, result)` | `live.go:5703`, `:5705` | per `Cmd.perform` |

All of these are floor **as calls**: the callee's shape exists only at run time.

`msg_dispatch.go` is the standing counter-move: per-Msg typed dispatch registries
populated from emitted `init()` blocks (`RegisterMsgUpdate` /
`RegisterMsgVariant` / `RegisterMsgDecoder`), consumed at `sky_call2`
(`msg_dispatch.go:243` onward, "Stage 6"). That narrows the *work inside* the
reflect call; it does not remove the reflect call. Its measured effect is
**UNMEASURED** in the committed perf corpus.

### 4.4 NOT floor: the kernel return (R5)

Every runtime kernel is `any`-based by ABI (`func String_append(a, b any) any`),
so `kernel_call` narrows the result at `lower.rs:4429-4439` and stamps
`FfiReturn`. It is not an FFI return and it is not floor: the kernel's real
result type is known to the compiler — `kernel_runtime_arity`
(`lower.rs:4449`) already reads a per-symbol table, and
`dict_typed_key_specialised` (defined `lower.rs:1986`, applied at
`lower.rs:4399`) already re-targets a kernel call
at a typed entry point (`rt.Dict_toListIntKey`) when the key type is known. The
same lever — a typed kernel entry point selected at emit time — applies to
returns.

**What is not established:** how many `narrow` tokens this accounts for. The
per-family breakdown exists (`xtask coerce-floor -v`) but is not committed, so
the number is **UNMEASURED**.

### 4.5 NOT floor, but blocked: stdlib ADT payloads (R6)

An ADT lowered as the erased bag (`type Name = rt.SkyADT`,
`codegen/src/lib.rs:184`; `SkyADT{Tag int; SkyName string; Fields []any}`,
`rt.go:4153`) forces a narrowing on every payload read. An ADT lowered as a
**sealed interface** (`GoTypeDef::SealedIface`, constructed at `lower.rs:1666`)
does not — the variant struct has typed `V{i}` fields.

Which one you get is decided by `sealed_unions` (`lower.rs:376-389`), and the
predicate has two clauses:

1. `should_seal_prefix` (`lower.rs:1774-1778`) — **excludes every `Sky_Core_*`,
   `Std_*` and `Sky_Http_*` ADT.**
2. every variant field type must resolve unambiguously.

So `Main_Msg` is sealed; **`Std_Ui_Element`, `Std_Ui_Attribute`,
`Sky_Core_Error`, `Std_Money` are not.** The stated reason is a runtime contract,
not a type-system limit (`lower.rs:1760-1768`): the runtime constructs those
values directly as `rt.SkyADT`, and `rt.SkyADT` does not implement the
`SkyVariant` interface, so sealing them would make runtime-produced values fail
the user-side variant assertion.

This is load-bearing, because the legacy category 11 was specifically about the
**`Element` / `Attribute` walker** — the exact set `should_seal_prefix` excludes.
`07-lowering-and-ir.md` §6 row 1 says that class is "**Deleted.** Sealed-iface ADT
emission … is the default here". **That is true for app-module ADTs and false for
stdlib ADTs**, and the stdlib ones are where the volume is: `26-ui-showcase`,
whose entire content is `Std.Ui` primitives, carries `narrow=446` in the golden.

**Classification: closeable in principle, blocked by a runtime contract.**
Closing it means making the runtime's directly-constructed `Std.Ui` values
satisfy the sealed representation. That is a floor-touching tactic under
`CLAUDE.md` §0.3 rule 5 and needs user authorisation before iterations are spent.

### 4.6 NOT floor by the test, but floor today: open rows (R10 / R11)

`goty.rs:226-228` erases a genuinely open record row to `GoTy::Any` **on
purpose**, and its comment calls the consequence "the documented irreducible
floor". The reasoning is sound and worth preserving: lowering an open row to a
closed anonymous struct physically DROPS the fields not named in the row, so a
`Dict k (List Record)` whose record is field-accessed silently loses `name`. The
erasure buys "no Sky program can ever silently lose record fields" at the price
of a reflective `rt.Field`.

By §1 this is floor — the value's shape is genuinely
unknown at emit time, because the row variable is unresolved. But note *why*: it
is a consequence of the erased-ABI + no-monomorphisation policy
(`07-lowering-and-ir.md` §5.1), not of a runtime contract. A different ABI would
classify it differently. Call it **policy floor**, and do not conflate it with
R4's **contract floor**.

---

## 5. The levers, on Rust evidence

### 5.1 Eta-expansion at the slot's shape — the primary lever

`func_shape_eta` (`lower.rs:2823-2929`) replaces a func-shaped `rt.Coerce` with a
closure **at the slot's shape** whose parameters narrow inward and whose result
widens back. Two forms:

* **Source is a func literal** — retype it *in place*: declare the params at the
  slot's types under fresh names, bind the body's original names to the narrowed
  values (`lower.rs:2857-2899`). Preferred because wrapping a literal in a second
  closure rebuilds the inner closure per call unless Go's inliner folds it.
  Measured on M1 / Go 1.26, six probes × six attributes: in-place ~1.8–4.2 µs/op
  vs ~4.3–9.0 µs/op for the wrapper form, **at identical allocation counts**
  (`lower.rs:2854-2856`).
* **Source is an identifier or selector** — wrap it (`lower.rs:2902-2929`).

It returns `None` — leaving the runtime coerce — in exactly four documented
cases (`lower.rs:2807-2822`, `:2832-2847`):

1. the source is not itself a Go func (an `any`-typed thunk in a func slot is a
   genuine runtime narrowing);
2. the arities differ (Sky curries, Go does not; the runtime's
   uncurried-to-curried branch is what finishes a partial application);
3. the source expression is a call — the eta wrapper calls its source once per
   invocation, so `mkPredicate cfg` would be re-evaluated per element;
4. a param or result whose narrowing would **rebuild** a slice or map —
   `rt.AsListT[T]` / `rt.AsMapT[V]` copy element-by-element, so a `List.foldl`
   with a `Dict` accumulator would go O(n·k) where the reflect adapter is O(1).
   *Never trade an O(1) reflect box for an O(n) copy.*

The same eta-expansion is what `kernel_value_eta` (`lower.rs:3349-3361`) and
`lower_ctor_value` (`lower.rs:3363-3419`) already do for their own shape
mismatches.

**The lever's discipline, learned the expensive way:** the decision is driven by
the SLOT (`expected`), never by the reference's own inferred type. The first
version of `kernel_value_eta` keyed on the inferred type and eta-expanded
`List.map String.toUpper xs`, where `any(rt.String_toUpper)` was already valid Go
— *widening* the floor across 13 examples, caught by `coerce-floor`
(`lower.rs:3332-3339`).

### 5.2 Slot-typed construction

Where a literal has no identity beyond its contents, take the slot's type as
authoritative rather than narrowing back into it:

* **Record literals** — `lower_expr` adopts the slot's nominal when the slot is a
  named record carrying exactly this literal's field names
  (`lower.rs:2699-2714`), so `{ key = k, value = v }` is built AS `Main_Kv_R{…}`
  instead of built anonymously and narrowed with `rt.Coerce`.
* **`if` / `case` / `let`** — the same principle, `lower.rs:2678-2684`.

### 5.3 Typed kernel entry points

`dict_typed_key_specialised` (`lower.rs:1986`, applied at `lower.rs:4399`)
re-targets a kernel call at a typed entry point when the key type is known.
Generalising this to kernel
*returns* is the R5 lever (§4.4).

**Two shipped instances**, both re-pointing at a typed twin that already existed
in `runtime-go/rt` with **no caller anywhere in the repo** — the same "compiled
into every binary and unreachable" condition Stage 2 and Stage 3 found:

* **The list arm of `++`.** `lower_binop`'s `"++"` arm emits
  `rt.List_appendT[T](a, b)` when both operands lower to the same
  `GoTy::Slice(T)` and `provable(T)` holds, instead of
  `rt.AsListT[T](rt.Concat(any(a), any(b)))`. The erased form misses
  `rt.Concat`'s `[]any` fast path on any typed slice, `rt.AsList`-reflect-widens
  **both** operands element-wise, and the enclosing slot narrows all of it back:
  five slices and ~2(n+m) element boxes per evaluation, against one `append`
  into one fresh slice.
* **Unary list kernels that only read a length.** `kernel_call` emits
  `rt.List_isEmptyT[T](xs)` / `rt.List_lengthT[T](xs)` over a proven slice,
  instead of `rt.AsBool(rt.List_isEmpty(any(xs)))`, whose body calls the
  unexported `asList` and therefore reflect-walks the list and boxes every
  element in order to compute `len(items) == 0`.

`list_unary_prim_twin` (`lower.rs`) stops at twins returning a Go **primitive**,
and that boundary is load-bearing rather than a convenient stopping point:
`bool` and `int` need no container rebuilt on the way out, so the twin is a
strict removal. `rt.List_headT` returns `rt.SkyMaybe[A]` where its consumers take
`rt.SkyMaybe[any]` — a distinct instantiation needing a reflective
`rt.MaybeCoerce` rebuild, so it RELOCATES cost, which is the same reason `head`
is absent from `SKY_LIST_HOF_TWINS`. `rt.List_reverseT` / `takeT` / `dropT` pass
that test but are O(n) work rather than an O(1) length read, so they are a
different measurement and are left for a later tranche per §5.5's staged-rollout
rule. **`::` is deliberately untouched** — it was this stage's negative control,
and re-pointing it would have destroyed the only evidence that the routing keys
on proven operand types rather than on operator shape.

Three properties of this lever are worth carrying:

* **The failure mode is a `go build` error, never a silent miscompile** — the
  same property §5.5 records. Committing to `[]T` at emit time is sound exactly
  when the operand really is a `[]T`, which is what Go's own type checker
  decides.
* **A typed twin is not automatically a correct twin.** `rt.List_appendT` was
  `return append(a, b...)`, which reuses the left operand's backing array
  whenever it has spare capacity, where `rt.Concat` has always returned a fresh
  slice. Sky lists are immutable values, so re-pointing at it as written would
  have made `ys ++ zs` mutate a list nobody appended to — and one of the sites
  the change converts in `19-skyforum` has a live Sky.Live **model field** as its
  left operand. Every corpus gate passes that bug; it returns the right value and
  corrupts a different one. Before re-pointing at any unreachable twin, check it
  against the semantics of what it replaces, not just its type.
* **A frame invisible to pprof is not a frame that costs nothing.**
  `rt.List_isEmpty` does not appear at all in a 385-node listing at
  `-nodefraction=0`, and `rt.List_length` sits at 0.011% — yet removing them was
  worth **−3.6%** of all objects per interaction. The cost was the `any(xs)` box
  at the CALL site, attributed to whichever function inlined it, and the erased
  body forced Go to keep three `Std_Ui.renderNodeAs` closures as real functions
  (`renderNodeAs.func1.{1,2,4}`, 888–899 objects/interaction each). Replacing the
  body with `len(xs) == 0` made them inlinable and the frames stopped existing.
  A profile attributes to frames; a cost paid at a call site and inlined away has
  no frame to be attributed to.

Measured effect on a real app: `docs/perf/runs/stage4-typed-list-plumbing-20260816/`
— **−21.3% / −21.9%** objects per interaction for the pair, at 94 and 974
elements, with the allocation effect flat across the 10× view-size change.

### 5.4 Sealing more ADTs

The R6 lever (§4.5). Requires
a runtime change; floor-touching under `CLAUDE.md` §0.3 rule 5.

### 5.5 Typed instantiation of a polymorphic Sky def

The R12 lever. §5.3 generalised from kernels to **Sky-source defs**, with a Go
generic supplying the typed entry point instead of a second hand-written runtime
symbol — so unlike §5.3 it needs **no runtime change at all**.

Emit a qualifying polymorphic def as `func F[T1 any, T2 any](…)` and instantiate
it at each call site from the arguments' own lowered Go types. Every piece of
machinery already exists: `GoFuncDecl.type_params` (`ir.rs:228`) is rendered by
`codegen/src/lib.rs:145-154`; `GoExprKind::GenericCall` (`ir.rs:132`) is already
emitted by `list_hof_typed` (`lower.rs:4644`); `sky_ty_to_go_params`
(`goty.rs:108`) already maps a `Ty::Var` to a supplied `GoTy`, which is how
parametric record aliases emit today.

Three properties decide whether it is sound at a given def:

* **The body must not re-narrow.** It does not, because `destructure_ops`
  (`lower.rs:7159-7176`) keys on `GoTy::Slice(_)` — including `Slice(TyVar)` —
  and selects `rt.SkyLenT` / `rt.SkyElemT` / `rt.SkyTailSliceT`
  (`rt.go:8669`, `:8672`, `:8681`), which compile to `len(xs)` / `xs[i]` /
  `xs[1:]`. This is what separates the Rust path from the legacy Haskell
  generic emission, whose bodies still carried `rt.AsList` and `rt.Coerce[T2]`
  (`Monomorphise.hs:542`).
* **A zero-param def cannot be generic.** The CAF path emits a package-level
  `GoItem::Var` (`lower.rs:2178-2258`) and Go has no generic package-level
  variable. The comment at `lower.rs:2175` already assumed this.
* **A generic Go function has no bare value form.** Every non-call reference —
  partial application, a value position, a cross-module reference — must emit
  `F[any, …]`, whose stencil is character-for-character the erased function it
  replaces. So an unproven site keeps today's behaviour, allocation, and token
  count. Every failure mode of this lever is a `go build` error, never a silent
  miscompile.

**On the no-monomorphisation policy (§5.6).** This is one emit per definition in
the lowerer's output — which is what `lower.rs:2804` is about. Go's own
stenciling then merges instantiations by GC shape, so the growth is bounded by
distinct GC shapes rather than by instance count. Do not claim "no binary
growth"; Stage 2 crossed the same line with `rt.List_mapT[A,B]` and left the
size **UNMEASURED**.

### 5.6 What is NOT a lever

**Monomorphisation.** There is no monomorphiser, there never was one in the Rust
compiler, and it is policy rather than a gap
(`07-lowering-and-ir.md` §5.1; `grep -rn 'mono_instances\|subst_tyvars'
rust/crates` returns nothing). Sky's Go ABI is erased; a monomorphiser would have
to un-erase it first and would buy binary growth proportional to instance count.
**Before concluding anything here needs monomorphisation, apply
§1** — if the shape is statically known, the answer
is an eta-expansion.

---

## 6. Mapping from the legacy §6 numbers

Prior commits, docs and transcripts cite the legacy numbers. This is the
translation. "No analogue" means the Haskell mechanism does not exist here —
not that the problem was solved.

| Legacy § | Legacy name | Rust analogue | Fate |
|---|---|---|---|
| 1 | `coerceToFieldType` final-else fallback | **R1** `coerce_if_needed` | **transfers.** Same role: the fall-through when no better shape is known. |
| 2 | Primitive helper (`rt.CoerceString/Int/…`) | **R1** with `PrimitiveJoin` → renders `rt.AsInt` / `rt.AsString` (`render_shapes.rs:189-192`) | **transfers, renamed.** The `rt.Coerce*` primitive tokens remain in `TRACKED` but the Rust lowerer's primitive path renders `rt.As*`. |
| 3 | Map→struct narrowing for Db rows | no emission site — the narrowing lives inside `rt.Coerce`'s reflect path (`rt.go:5887` onward) and `db_decoder.go` | **moves into the runtime.** Not visible to an emitted-Go census. |
| 4 | TEA dispatch return narrowing (*legacy: FLOOR*) | **no emitted analogue** — `runtime-go/rt/live.go` `sky_call`/`sky_call2` | **transfers as a runtime floor.** Never appears in `main.go`. See §4.3. |
| 5 | Ctor partial-application adapter | `lower_ctor_value` (`lower.rs:3363`) eta-expands by construction | **closed by construction.** |
| 6 | Polymorphic kernel-fn arg | `func_shape_eta` (`lower.rs:2823`) | **closed for the statically-shaped majority**; 35 residual `adapter` tokens across 9 of 61 projects (§7). |
| 7 | Record-update / RecordExt narrowing | **R10 / R11** — `goty.rs:226-228` open-row erasure → `rt.Field` / `rt.RecordUpdate` | **transfers, with a different justification** — a deliberate correctness trade, not a missing context. See §4.6. |
| 8 | Cross-module dep-ctx fallback | **no analogue.** There is no dep-emission context: lowering is one whole-program pass from a single `main` root with one worklist (`lower.rs:591-613`, `07-lowering-and-ir.md` §5.2). | **gone by construction.** |
| 9 | Go FFI return (*legacy: FLOOR*) | **R4** `lower.rs:4386-4396`, plus **R3** on the argument side | **transfers, still floor.** |
| 10 | gob/JSON wire decode (*legacy: FLOOR*) | **no emitted analogue** — `db_decoder.go`, session-store gob, Sky.Live decode | **transfers as a runtime floor**, partially met on the emitted side by generated per-variant JSON factories for **sealed** ADTs (`lower.rs:1647-1661`). |
| 11 | Element / Attribute / Msg sealed-iface walker | **R6** `lower.rs:6506-6519`, gated by `sealed_unions` (`lower.rs:376-389`) | **splits.** `Msg` (app ADT) → closed. `Element` / `Attribute` (`Std_*`) → **still open**, excluded by `should_seal_prefix`. |
| 12 | Anonymous-record narrowing | `GoTy::Struct` anon structs (`goty.rs:229-251`); narrowing falls to **R1** | **transfers, much reduced** — a CLOSED record keeps its precise anon struct. |
| — | *(new in Rust)* | **R5** kernel return, `lower.rs:4429-4439` | **new category.** No legacy number; the Haskell pipeline did not have an all-`any` kernel ABI narrowed at one enumerated site. |
| — | *(new in Rust)* | **R9** string-concat operand, `lower.rs:6966-6980` | **new category.** |
| — | *(new in Rust)* | **R2** `eta_narrow`, `lower.rs:2771-2779` | **new, and it is the CLOSED form** — an R2 token is what an adapter *became*. Counting it as a regression is the misreading the golden's header warns about. |

Legacy §7 levers 7.1 (LowerCtx propagation), 7.2 (σ-recovery into dep ctx), 7.5
(IORef → reader threading) describe Haskell plumbing with **no Rust analogue**:
lowering carries `expected` as a parameter (`lower.rs:2719`), there is no dep
context, and there are no compiler globals to thread. Legacy §7.3 (sealed-iface)
maps to §5.4; legacy §7.4 (per-instance kernel σ) is
**superseded** — it proposed monomorphisation, and §5.1 is the cheaper mechanism.

---

## 7. The census

`xtask coerce-floor` re-emits every project through the same
`emit_example_source` path `repro`/`build-run` use and counts the `TRACKED`
tokens in the emitted `main.go`. The blessed result is
`rust/crates/xtask/coerce_floor.golden`.

At `4d50b447`:

```bash
$ grep -vc '^#' rust/crates/xtask/coerce_floor.golden          # 61   projects
$ grep -v '^#' … | grep -o 'adapter=[0-9]*' | cut -d= -f2 | paste -sd+ | bc   # 35
$ grep -v '^#' … | grep -o 'narrow=[0-9]*'  | cut -d= -f2 | paste -sd+ | bc   # 9479
$ grep -v '^#' … | grep -vc 'adapter=0'                        # 9    non-zero
```

**61 projects · 35 `adapter` across 9 of them · 9,479 `narrow` · 0 `dispatch`.**

Three things a reader must not conclude from that:

* **`dispatch=0` is not progress.** `rt.SkyCall` is reached from inside the
  runtime, never emitted into `main.go`. The column is an armed tripwire for a
  lowering that starts emitting it (`coerce_floor_gate.rs:84-88`).
* **The golden's header quotes 24 residual adapters**, from a 56-project
  measurement taken at the eta-expansion transition. The current total is 35
  across a 61-project set. Both are true of their own moment; re-derive from the
  golden rather than quoting either.
* **`narrow` is one class, not one cost.** `rt.AsListT[T]` rebuilds a slice
  element-by-element; `rt.Coerce[int]` is one assertion. The classes separate
  cost *orders*. Nothing in the census weights by execution frequency — that is
  what `docs/perf/runs/` is for (`coerce_floor_gate.rs:69-91`).

### 7.1 `examples/*/sky-out-rust/main.go` is not evidence

Those files are **untracked local build artefacts** (`git log -- <path>` is
empty). At the time of writing they predate the eta-expansion commit `e613cbec`
(2026-08-15 22:40) and still contain `rt.Coerce[func(string) any](…)` adapters in
projects the golden records at `adapter=0`. The prebuilt `rust/target/release/sky`
and `.../xtask` are likewise older than `e613cbec`.

**Use them for SHAPE — what an emitted construct looks like — never for COUNTS.**
For counts, cite the golden. For a post-eta emitted line, the one committed
quotation is `docs/perf/runs/forum-rebaseline-20260816/forum-baseline.md:126-132`,
from a build at `50c8dcee`:

```go
rt.AsListT[Std_Ui_Element](rt.List_indexedMap(
    any(func(_p0 any, _p1 any) Std_Ui_Element {
        return View_Posts_postRow(v_0, rt.AsInt(_p0), rt.Coerce[State_Post_R](_p1))
    }),
    any(v_1)))                       // v_1 is []State_Post_R — a TYPED slice
```

That is the R2 shape: the callback is a func literal **retyped in place** at the
erased slot's shape, with the narrowings pushed inside. No `rt.Coerce[func(…)…]`
wraps it. Note what remains visible in it: a typed `[]State_Post_R` is widened to
`any`, re-boxed element-by-element by the runtime helper, and narrowed back — the
round trip §9.2 is about, and the one still open after the adapter half closed.

---

## 8. What this document does NOT establish

Stated plainly, because a reference whose blind spots are undocumented gets cited
for things it never checked.

* **Per-category token counts.** `xtask coerce-floor -v` prints a per-family
  breakdown; it is not committed. Which of R1/R5/R6/R7/R8 dominates the 9,479
  `narrow` tokens is **UNMEASURED**. Do not assert a ranking.
* **`rt.RecordUpdate` is not counted anywhere.** It is a reflective record
  rebuild (`rt.go:3760`) and is absent from `TRACKED`
  (`coerce_floor_gate.rs:116-150`). R10/R11's cost is invisible to the census.
* **The runtime's own narrowing is invisible to the census.** By design
  (`coerce_floor_gate.rs:84`). Wire decode and TEA dispatch are therefore
  unmeasured *by that gate*, not absent.
* **Whether a narrowing is CORRECT.** This is a cost taxonomy. Soundness is
  `xtask repro` / `build-run` / the panic-class gates.
* **The FFI typed-shim escape** (legacy §8.1) — **UNVERIFIED** against the Rust
  `ffi` crate.
* **`msg_dispatch.go`'s typed-dispatch registries** — populated and consumed, but
  their effect on interaction cost is **UNMEASURED** in the committed corpus.
* **Line numbers drift.** Every `file:line` here was read at `4d50b447`. Prefer
  the named function (`Ctx::coerce_if_needed`, `func_shape_eta`, `sealed_unions`)
  and use the line as a hint.

---

## 9. Empirical corrections already earned

Recorded here because each one was paid for, and because the claim each replaced
is still quoted in older material.

### 9.1 The monomorphisation dichotomy is FALSE

**Retracted claim** (legacy §8.3, and `07-lowering-and-ir.md` before it was
corrected): closing the HOF-adapter category "requires monomorphising every HOF
call site into a generated typed dispatcher (Go binary size explodes)".

**What happened instead:** eta-expansion at the statically-known shape, one emit
per definition, no monomorphisation, no binary growth.

| Change | Measurement | Run |
|---|---|---|
| eta-expand a func value into a func slot | **1.36×** throughput, ranges non-overlapping (132.2/137.1/137.2 → 184.9/184.9/184.9 interactions/s; p95 ~580 ms → ~362 ms) on `examples/26-ui-showcase`, 384 elements, `GOMAXPROCS=1`, closed loop, 25 sessions, 20 s, 3 runs/arm, compilers differing only by this branch | `docs/perf/runs/hof-dispatch-20260815/` |
| typed list destructuring | **1.34×** throughput, ranges non-overlapping | `docs/perf/runs/typed-destructure-20260815/` |
| adapter census across the 56 projects emitting under both compilers | `adapter` **269 → 24** (−91%); 24 projects driven to 0; **0 rose**. `narrow` 8055 → 8234 (+179) — the trade, since an N-ary callback swaps one coarse token for N precise ones. Total 8324 → 8258 | `coerce_floor.golden:37-46` |

The conclusion was reached **three times** before it was tested once. Both closes
came from applying §1, and nothing else.

### 9.2 The per-element `rt.SkyCall` path is CLOSEABLE, not floor

The legacy §8.3 heading named `rt.SkyCall` without distinguishing the TEA
boundary from the per-element path inside the erased list helpers
(`List_mapAny`, `List_filterMap`, `List_foldlAnyT`, `List_indexedMap`,
`List_find` — `rt.go:3152-3489`). The section was then mis-cited as floor for a
category that is not.

The two populations, separated by §1:

* **TEA boundary** — floor (§4.3).
* **Per-element helper path** — the callback's shape and the slot's shape are
  both known at emit time. Closeable, and the adapter half is closed
  (§5.1). What remains
  open is the *boxing round trip*: the emitted call widens a typed slice to `any`,
  `rt.asList` re-boxes every element, `SkyCall` reflect-calls per element, and
  `rt.AsListT` walks the `[]any` back, asserting per element
  (`forum-baseline.md:134-147`).

### 9.3 "~100 ns per element. Bounded." was wrong on both halves

The retracted text (legacy §5.3) carried no measurement. What has been measured:

**The cost was understated.** On the `Std.Ui` marker scan
(`hasMarker name attrs = List.any (\a -> isMarker name a) attrs`), six probes over
six attributes = 36 visits: **318 allocations with the adapter, 126 without** —
5.3 allocations per element visit. `Std.Ui` runs that scan six times per element
of every layout.

**"Bounded" was wrong about the population.** From
`docs/perf/runs/forum-rebaseline-20260816/`, `examples/19-skyforum` at
`50c8dcee`, M1 arm64, `GOMAXPROCS=1`, 50 sessions closed-loop, three runs per
size, every run asserting `patch_rate: 1`:

| Quantity | Measured |
|---|---|
| CPU samples with `reflect.Value.call` on the stack, 974 elements | **64.9 – 66.5%** |
| …with `rt.SkyCall` on the stack | 64.8 – 66.3% |
| Allocation *underneath* `reflect.Value.call` | **89.2%** (94 el) / **91.1%** (974 el) |
| Allocation underneath `rt.SkyCall` | 87.0% / 90.9% |
| The erasure round trip's OWN bookkeeping (self-allocation, sums without double-counting) | **20.8%** / **20.3%** |
| Objects per rendered element | **~250** (`objects = −34 + 248 × elements`, R² = 0.99999) |
| Interaction cost per element | **18.3 µs** (30–94 el) rising to **20.4 µs** (382–1614 el) |
| The diff — the only output the interaction needs | **1.1 – 1.2%** of CPU |

The share is flat across a 10× change in view size, so it is structural, not a
small-view artefact.

**Not stated here: a per-dispatch nanosecond cost, or a dispatch count per
interaction.** Both are **UNMEASURED** — no artefact in `docs/perf/runs/` counts
`SkyCall` invocations, and `skyCallDirect`'s `[]reflect.Value` is mostly
stack-allocated, so the allocation profile cannot be read as a call count either.
What *is* pinned is their product: at 974 elements, 0.65 × 17.8–18.7 ms ≈
**11.5–12.4 ms per interaction** on stacks bearing a reflective dispatch. A
figure for either factor alone has to be measured, not divided out.

### 9.4 Two structural facts worth carrying

From the same run, at 974 elements:

* **Half the machine is the garbage collector and its allocator (49.0–49.6%). A
  quarter is reflection (23.3–24.5%). Under 4% is the user's compiled Sky.**
* A Sky.Live interaction rebuilds the whole `Element` tree through reflective
  dispatch, converts it to `Html`, converts that to `VNode`, renders the entire
  page to a string — and then diffs. The reply on the wire is **411–413 bytes**.

Caveats the run states for itself: arm64, one host, one commit; one interaction
shape (an upvote toggle that re-renders the whole page); the CPU runs use the
`memory` session store, so the gob path is absent from those profiles by
construction; no network term. And its own §4 records that **CPU self-time
attribution is unreliable on that host at high interaction rates** — the analysis
rests on the 974-element runs, where three repeats agree, and on allocation
profiles, which agree to 0.2%.

---

## 10. How to cite this document

A claim that a tactic closes a runtime-narrowing goal must name:

1. **The origin** — an `R`-number from §3, with its `lower.rs` /
   `live.go` / `goty.rs` site.
2. **The lever** — a subsection of §5.
3. **The floor check** — apply §1 explicitly. If the
   tactic touches R3/R4 (§4.1), the wire decoders
   (§4.2), the TEA boundary
   (§4.3) or the stdlib-ADT representation
   (§4.5), it is
   floor-touching and needs user authorisation per `CLAUDE.md` §0.3 rule 5.
4. **The verification** — which gate would go red if the tactic regressed.
   `xtask coerce-floor` refuses to bless an `adapter` increase, so an adapter
   regression cannot be normalised away.

A verdict that cannot make all four citations is not a verdict.

---

## References

* `rust/crates/lower/src/lower.rs` — the emission allowlist, `func_shape_eta`,
  `sealed_unions`
* `rust/crates/lower/src/goty.rs` — Sky type → `GoTy`, the open-row erasure
* `rust/crates/lower/src/ir.rs` — `CoerceReason`
* `rust/crates/codegen/tests/render_shapes.rs` — the locked rendered forms
* `rust/crates/xtask/src/coerce_floor_gate.rs` + `coerce_floor.golden` — the census
* `runtime-go/rt/rt.go`, `runtime-go/rt/live.go`, `runtime-go/rt/msg_dispatch.go`
* `docs/perf/runs/forum-rebaseline-20260816/` — the only attribution data from a
  real application producing real patches
* `docs/perf/runs/hof-dispatch-20260815/`, `docs/perf/runs/typed-destructure-20260815/`
* [`07-lowering-and-ir.md`](07-lowering-and-ir.md) §5.1 — no monomorphiser, and why
* [`08-go-codegen.md`](08-go-codegen.md) §3 — the runtime ABI
* `docs/architecture/sky-compiler-architecture.md` — **legacy Haskell reference,
  historical context only**
