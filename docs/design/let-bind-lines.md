# Bind lines in `let` (`p <- e`) — design (v0.32.0, #342)

Status: approved for implementation. Two adversarial reviews are folded in.

## 1. The feature

```elm
let
    state <- Sync.get app.ref
    now <- nowOf state
    ctx = { now = now }
    _ <- Sync.set (f ctx state) app.ref
in
Task.succeed ctx
```

- A bind line `p <- e` is a line of a `let` block. The rest of the block (later
  lines and the `in` body) is the continuation: `andThen (\p -> rest) e`.
- `=` lines and `<-` lines mix in any order.
- `andThen` is chosen from the inferred type of `e`: the head type constructor
  (after alias expansion) names a home module, and that module's EXPOSED
  `andThen : (a -> T b) -> T a -> T b` is used. Kernel-implicit heads use a fixed
  table: `Task` → `Sky.Core.Task`, `Maybe` → `Sky.Core.Maybe`, `Result` →
  `Sky.Core.Result`. `Json.Decode.Decoder` works once §3.1 lands. User types
  (`type Parser a = …` whose module exposes `andThen`) work.
- Pattern `p`: any irrefutable pattern (name, `_`, unit, tuple, record, alias,
  a constructor of a one-variant union, nested). A refutable pattern is
  `[E3001]`.
- `<-` anywhere else is a parse error `[E0001]` with the hint "`<-` is a bind
  line; write it as a line of a `let` block". (Before: `[E1014]` unknown
  operator. Never valid, so not breaking.) A case-arm pattern (`v <- x -> 1`)
  gets "a case arm uses `->`".
- A `let` line with a `<-` (before its own `=`) whose left side is not a
  pattern is ONE rule, whatever the left side is: one `[E0001]` on the `<-`,
  "a bind line is `pattern <- expression`. The pattern is a name, `_`, `()`,
  or a pattern in parentheses, e.g. `( a, b ) <- e`, `(x as y) <- e`. A
  function goes on an `=` line." The left side is a pattern when it parses as
  one that ends at the `<-` and, when it starts with a lowercase name, is
  that name alone (as on an `=` line: `x as y`, `x :: xs` and `g x` are
  refused). An empty left side says "a bind line needs a pattern: write
  `_ <- e` to run `e` and drop its result". A refutable pattern still parses
  and is `[E3001]` later.
- No custom operators. Nothing else in the syntax changes.

## 2. Scoping

`resolve_let` (hir/src/resolve.rs ~2870) splits the lines at each bind line into
segments:

`Let{seg1, Call(BindAndThen, [Lambda([p], Let{seg2, …body}), e])}`

- Each segment is one recursive group, exactly as a whole `let` is today (any
  order, mutual recursion, let-generalisation).
- `e` sees seg1 (and outer scope), not `p` and not later segments.
- A line before a bind line cannot see a name defined after it. A reference
  across the split is `[E1001]` with the hint "a bind line splits the block:
  move `a` below `p <- …`", where `a` is the line that reads the name and
  `p <- …` is the bind line directly above the name's definition (always a
  valid fix). In the expression of a bind line the hint is "move the
  definition of `c` above `p <- …`".
- An annotation line directly before a bind line (`x : T` then `x <- e`) is
  refused with a hint to annotate `e` instead.
- A block whose LAST line is a bind line is fine: the body is the `in`
  expression.

## 3. Soundness fixes found during design (ship in the same release, §0.2.2)

### 3.1 `Std.Db.Decode` has its own `Decoder`

Today `Std.Db.Decode` signatures use the kernel-implicit bare `Decoder`
(sky-stdlib/Std/Db/Decode.sky:139, hir/src/kernel.rs:972), the same nominal
type as `Json.Decode.Decoder`. `Decode.decodeString (DbDecode.int "age") …`
passes `sky check` and panics (`TypeMismatch … rt.AsInt`). Give
`Std.Db.Decode` its own opaque `type Decoder a` (as `Std.Xml.Decode` and
`Std.Config` do). Audit every other `KERNEL_IMPLICIT_TYPES` name for the same
shared-name collision and fix each one found. Migration entry: code that wrote
a Db decoder's type as the Json `Decoder` must write `DbDecode.Decoder`.

### 3.2 Irrefutable binder positions

Today `\(Just x) -> x` applied to `Nothing` silently returns `0`, and
`let (Just x) = m in x` panics. Add ONE irrefutability check in the
exhaustiveness pass (ty/src/exhaustive.rs) for every binder position: lambda
parameters, function parameters, let destructures, bind patterns. A refutable
pattern is `[E3001]` with "use `case`". Find the union through
`CtorRef.type_`, never by name (D1).

## 4. Typing

- New `Res::BindAndThen` (hir/src/ids.rs). The resolver gives the placeholder
  `Var` the span of the `<-` token. The synthesised `Call` and `Lambda` get NO
  span.
- ty special-cases `Call(Var(BindAndThen), [lam, e])` (infer.rs ~1712 and the
  other Call arms ~3492). It infers `e` and the lambda, and records a PENDING
  BIND `(expr_id, ty_of_e, ty_of_lambda, result_ty)`.
- At the end of each def's inference, BEFORE generalisation, solve pending binds
  repeatedly until no more resolve (one choice can fix another): expand aliases,
  read the head; if concrete, resolve the home module's exposed `andThen`
  through HIR's own qualified-name lookup (the same `Res` a hand-written
  `M.andThen` gives), instantiate its scheme with `infer_res`, unify with
  `lam -> e -> result`. Also check the scheme unifies with the template
  `(a -> T b) -> T a -> T b`, else `[E2016]`.
- Still unresolved after the loop: `[E2015]` (needs an annotation). A head of
  `Ty::Error` suppresses the error.
- Results go in a `BindElaboration: HashMap<ExprId, Res>` built on the CHECK
  path (`numeric_elaboration_of` pattern, ty/src/db.rs:67). The lowering path
  never chooses again: it reads the table and instantiates the chosen DefId with
  its own lowering-path scheme.
- Dependency order: the SCC graph (ty/src/sig.rs ~1067) adds a conservative
  edge from each def with a bind line to every candidate `andThen` (the kernel
  three, and every exposed `andThen` in imported modules and the def's own
  module). `Maybe.andThen` and `Result.andThen` carry their annotations. An
  annotation alone typed each continuation at its instance while the def still
  erases to `any` (doc 14 R12), so a chain gained an eta wrapper with an
  `rt.AsInt` per parameter. The lowering therefore sends a call whose argument
  and continuation lower to proven Go types to the typed twins
  `rt.Maybe_andThenT` / `rt.Result_andThenT` (doc 14 §5.3 on a Sky def,
  `lower.rs` `and_then_plan`); an unproven call keeps the erased def
  (`project/tests/and_then_no_new_narrowing.rs`, `coerce-floor`).
- A bind line inside a module's own `andThen` that would select that same def is
  refused (`[E2016]`, "a bind line in `andThen` itself").
- Shadowing: `BindAndThen` is not a name lookup, so a local `andThen` is never
  picked. Pin it with a test.

### Error codes

- `[E2015]` BIND NEEDS A TYPE: "`x <- t` chooses `andThen` from the type of
  `t`, which is still `a`. Annotate `t`, e.g. `fetch : String -> Task Error User`."
- `[E2016]` NO ANDTHEN: "`Decoder` (module P) has no
  `andThen : (a -> Decoder b) -> Decoder a -> Decoder b`; use `case`, or add
  one to P". A stdlib module says "use `case`" only. A wrong-shaped
  `andThen` adds "(its `andThen : …` has another shape)" and never suggests
  calling it by name. Also covers an unexposed andThen ("expose it") and the
  self-reference case.
- Mixed types in one block: ordinary `[E2001]`, with a note suggesting
  `Task.fromResult` / `Maybe.toResult err`.
- Docs: `docs/errors/E2015-*.md`, `E2016-*.md`, the diagnostics registry
  (diagnostics/src/lib.rs ~304) and doc 03 §14.

## 5. One elaborated body for every pass after ty

- A salsa query in ty, `elaborated_body(module, def)`: the shared `Arc` when
  the def has no bind line (keeps lower.rs:12272 true), else an arena clone with
  each placeholder replaced by the chosen `Res`. ExprIds are unchanged, so
  `BodyTypes` keys stay valid.
- EVERY consumer that runs after inference reads it: ty's own post-passes
  (check.rs, obligations.rs `callees`, numeric.rs, pubsub_topic.rs,
  retained_body.rs, form_submit.rs, codec_elem.rs, db.rs:118), lower (incl.
  store_lint.rs, numeric.rs, retained_types.rs), skydb, project (spa_partition,
  spa_model, spa_capability_domain, view_memo, diagram, OpenAPI), sky-lsp, the
  sky crate (fuzz, test, doc), xtask gates.
- A source-scan test fails on a raw `.bodies` / body read after ty outside the
  accessor (with an allow-list for the pre-ty passes: resolve, value_cycle, sig
  SCC construction).
- Every `_ =>` arm over `Res` in a post-ty consumer that would silently drop an
  unknown callee becomes fail-closed (a `BindAndThen` reaching it is an internal
  error, never a silent skip).

## 6. Other passes

- Sky.Spa `splice_of` (spa_partition.rs ~7614): a synthesised bind `Call`
  splices as `(M.andThen (\<p text> -> <splice rest>) (<splice e>))` from its
  children, never by span. `update_leaf_cmd_spans` (~7760) recurses through
  the bind Call/Lambda. Test: an update arm written with bind lines gives the
  SAME partition plan as the hand-written `e |> Task.andThen (\p -> …)` and the
  `Task.andThen (\p -> …) e` shapes.
- Text scans replaced by CST reads: spa_split.rs ~2629 (`split_once('=')` let
  locals), sky/src/main.rs ~3086 `capture_main_boot_setup`, app_entry.rs ~764
  (inside names) and ~1563-1621 (`main = let _ <- setup in App.run app` carries
  the bind lines as setup).
- task_discard.rs and coverage.rs: no change; tests pin it.

## 7. Formatter and LSP

- Lexer: `classify_op` maps `<-` to a new kind `LArrow`; grammar `let_binding`
  parses `pat <- expr` into `BindBinding`; kind.rs / kind_class.rs / ast.rs.
- fmt (pretty.rs ~1219, ~1626): render `pat <- expr` like a destructure with
  `<-`; include `BindBinding` in `let_bindings`; `LArrow` in the token-multiset
  check. Cases: comments between lines, multi-line `e`, `_ <- e`, tuples;
  idempotence tests.
- LSP: hover on `<-` shows the chosen `andThen` with its type and the bound
  type; go-to-definition on `<-` goes to that `andThen`; semantic token KEYWORD
  for `LArrow`; inlay hints for bind binders (lib.rs ~1954); rename of a bound
  name works through the lambda parameter PatId.

## 8. Task helpers (pure Sky, `Sky.Core.Task`)

```elm
bestEffort : Task e a -> Task x ()
unless : Bool -> Task e () -> Task e ()
toResult : Task e a -> Task x (Result e a)
```

No kernel-table entry (pure Sky resolves to `Res::Def`). `toResult` collides
with `Maybe.toResult` under two `exposing (..)` imports: add it to
`EXPECTED_COLLISIONS` (hir/tests/stdlib_collisions.rs) with a migration note,
and bring the stdlib history snapshots up to date (stdlib_history.rs).

## 9. Tests (doc 13 §2)

- syntax parse tests and snapshots; fmt idempotence.
- ty/tests/let_bind.rs: Task, Result, Maybe, Json Decoder, Xml and Config
  Decoder, a user type in another module imported under an alias that is not
  its last segment, a user `type Decoder` (D1); annotated, unannotated and
  `any`-holed `e` (D3); every reject (E0001, E1001 split, E2015, E2016, E3001,
  mixed types).
- Regression tests for §3.1 and §3.2 (the repros).
- Corpus: a `let_bind` stratum (monad × pattern × line position × annotation)
  and reject rows.
- Conformance: tests/conformance/tests/LetBindConformanceTest.sky (same result as
  `andThen`, effect order, stops on Err / Nothing, 2,000,000 iterations of
  recursion through a bind) and the three helpers.
- LSP: hover, go-to-definition, semantic token, inlay.
- Sky.Spa: an update arm with bind lines in an example; the partition-plan
  equality test; `sky fuzz --target web:app`.
