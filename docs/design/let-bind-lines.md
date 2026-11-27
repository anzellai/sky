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
  gets "a case arm uses `->`". Two shapes of a `let` line name the line to
  write instead, one `[E0001]` each: `x = <- e` gives "write `x <- e`: a bind
  line uses `<-` in place of `=`", and a second `<-` on one line (`x <- y <-
  e`) gives "one bind line binds one pattern: put `y <- e` on its own line
  above". A nested `let` on the line keeps its own `<-`.
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
  `Ty::Error` suppresses the error. So does an error inside `e`: a type error, or a
  name the resolver could not bind (`[E1001]`, typed as a fresh variable). The
  first error is the one reported, never an `[E2015]` / `[E2016]` for the same
  line. So does a call in `e` of an unannotated def whose own inference
  reported an error (`World::app_failed_defs`): its `[E2015]` is the one. A function after `<-` whose result has a named head is `[E2016]`
  naming the missing arguments (`Task.succeed` needs 1 more).
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
- Mixed types in one block: ordinary `[E2001]`, with a note. Two type names
  that read the same without their modules (`Decoder` of `Sky.Core.Json.Decode`
  and of `Std.Db.Decode`) are shown as the module writes them, by import
  alias (`Decode.Decoder`, `DbDecode.Decoder`), else by the full module name.
- A block whose type has no `andThen` (`String`, `Int`, a `List`, a record),
  for example the result of `f : Int -> String`: the note says "`f` returns
  `String`, so the `Task` after `<-` cannot run here: a bind line on a `Task`
  needs a block that is a `Task` too. Make `f` return `Task Error String`
  (and its callers run it), or do this work in a function that returns a
  Task." It is advice: a changed signature is not a code fix this note
  checks. A `Maybe` or `Result` line adds the idiom that takes it apart.
  A `Task` line in a block of the TEA update shape `( model, Cmd msg )`
  says instead: "`update` returns `( Model, Cmd Msg )`, so a Task cannot run
  in it directly. Put the Task block in its own function and run it with
  `Cmd.perform`: `( model, Cmd.perform (save model) Saved )`; its result
  comes back as a `Msg`." It names the def's model parameter and the `Msg`
  constructor that takes a `Result`. A block of a type its use fixes (the TEA shape
  `( model, Cmd msg )`, a view type `Element msg` / `Html msg`, `Sub msg`)
  never gets "Make `update` return …": a `Maybe` or `Result` line there
  says "`update` returns `( Model, Cmd Msg )`, so a `Maybe` cannot be
  chained here. Handle it with `case String.toInt "4" of Just n -> … ;
  Nothing -> …`, or `Maybe.withDefault 0 (String.toInt "4")`." with the
  line's own names (`Ok` / `Err` and `Result.withDefault` for a `Result`).
  Every note that prints a type shows a
  record by the alias name the def's annotation wrote (`Model`).
- `[E2015]` names a type variable that is not a name written in the def
  (`a <- t` shows `b`, never `a`).
- The block's type is the expected type when it is known: a block in tail
  position of an annotated def's body (through `let`, `if`, `case` and the
  continuation of a bind line, that is its later lines and its `in`
  expression) has the annotated result. Its first line unifies its result with it, so each
  line whose head differs is the one blamed, at its own `<-`, with the
  conversion of that line (`n <- Task.fromResult (parse s)`), tried against
  the expected type. The `andThen` is still chosen from the type of `e`
  alone, so a well-typed program chooses the same functions with and without
  the annotation (`ty/tests/let_bind.rs`). With no expected type, the first
  line's head sets the block's type. Before the last line of a block checks
  the `in` body, the earlier lines' names take the types their `andThen`s
  give them (when their uses accept those types), so an `in` body of the
  wrong type is blamed at the body, with its note, for one line or many.
- When the head of `e` is known as the line is inferred (the common case),
  its `andThen` is chosen then, and the pattern takes the type it gives
  BEFORE the continuation is inferred. A misuse of the bound name (`u.age +
  u.name`, `String.length a`, in a later line or the body) is blamed at the
  use, as in a plain `let`. A line whose head is not known yet keeps the
  end-of-def resolution above.
- `e : List x` gives `[E2016]` with a note naming `M.sequence` or
  `M.combine` (`Task.sequence`, `Result.combine`, `Maybe.combine`), where `M`
  is the head of the block's type, else of the element's, only when the def
  with the line so converted type-checks (`fixed_def_checks`). The element's
  type may still hold variables (`Task a Int` from `Task.succeed`). With the
  note the message does not say "use `case`".
- A block in which a line was refused has a free type, as the hand-written
  chain with the same clash has, so a caller of an unannotated def reports no
  second clash on it.
- `e` of a rigid annotation variable `m` is `[E2015]` that names `m` and a
  type with an `andThen` of the block's head (`Result Error m`), not "Annotate
  it".
- A note names a fix ONLY when the whole fixed definition type-checks. One
  test decides every such note (`Infer::fixed_def_checks`, ty/src/bind.rs):
  the fix is applied to a copy of the def's resolved body (HIR), and the
  whole def is inferred again in a run of its own, against its annotation,
  as the checker does. The fix is named only when that run has no error.
  The run is side-effect free, runs only on the error path, once per
  candidate note, and a run started for a note tries no fix of its own. A
  value the fix leaves to the user (`err` in `Result.fromMaybe err`) is a
  typed hole in the re-check, and the note says what it is: "where `err` is
  the `Error` to report (e.g. `Error.invalidInput "…"`)", and adds ", with
  `import Sky.Core.Error as Error`" inside the parentheses when the module
  has no import of `Sky.Core.Error`. (Generally, an `[E1001]` for `Q.x`
  where no import is named `Q` and exactly one stdlib module is written `Q`,
  by its last segment or its last two run together, and exports `x`, adds
  "Add `import <Module> as Q`.") The fixes so
  decided: the conversion of a line or of the `in` body (`Task.fromResult
  (Result.fromMaybe err e)`, `Task.fromResult`, `Result.fromMaybe err`,
  `Result.toMaybe`), the wrap of the `in` body (`Task.succeed` / `Ok` /
  `Just` / the home `succeed`), and "bind it with `a <- e`" for a name bound
  with `=` to a value of type `M … x` whose use refuses `M … x` and takes
  `x`. When no fix checks, the note gives the types and, for a `Maybe`, a
  `Result` or a `Task`, the idiom in words ("`String.toInt` gives a `Maybe
  Int`: handle `Nothing` with `case` or `Maybe.withDefault`"); for a
  function, how many arguments make it fit. A type name in a note takes
  "a" or "an" by its first sound (`an Int`).
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
