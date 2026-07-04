# v0.17.8+ — sky fmt comment preservation via AST

**Issue:** [#144](https://github.com/anzellai/sky/issues/144) — comments dropped from `(\_ -> body)` lambda positions during `sky fmt`.

**Cause:** `preserveTopLevelComments` in `app/Main.hs` is a string-level post-processor. It uses "prev-line" and "next-line" text anchors. The formatter reshapes multi-line `(\_ -> body)` into single-line, so neither anchor matches, and comments silently drop.

**Design goal:** replace the string workaround with **AST-driven** comment emission — the AST already carries `Src._comments :: [A.Located String]` per module, populated by the parser's post-scan. The formatter should read that list and interleave comments during emission based on source line + column info.

## Grill findings (workflow `wf_6b112652-d91`)

Two adversarial critics both returned **REVISE** — the naive "thread queue through walker" plan has 4 blockers:

1. **Block vs line unrecoverable.** `_comments` stores body WITHOUT delimiters. `{- foo -}` becomes indistinguishable from `-- foo`. Emitting a block as a line comment breaks the source. **Fix requires parser tag.**
2. **Trailing vs own-line lost.** Parser `trimStart`s the body. `x = 1 -- foo` (col-10 trailing) and `-- foo` (col-1 own-line) are stored identically. **Fix requires col + kind tag.**
3. **Format.hs is 30+ pure `Int -> Src.X -> String` helpers.** Threading a queue requires State monad OR explicit `(queue, String)` on every arm. Any arm that forgets to thread silently drops. Massive rewrite scope.
4. **Idempotence drift.** Pass 1 re-places comments; pass 2 sees them at NEW positions and emits identically byte-wise but the semantic anchoring is drifted from user's original layout.

## Phased rollout (this doc == the plan)

### Phase 1 — Parser + AST schema (this session)

**Status:** in progress on `feat/v0.17.8-fmt-ast-comments`.

Additive schema change. Backward-compat via a wrapper type.

* Add `ParsedComment` in `Sky.AST.Source`:
  ```haskell
  data CommentKind = CommentLine | CommentBlock deriving (Show, Eq)
  data CommentPos  = CommentOwnLine | CommentTrailing deriving (Show, Eq)
  data ParsedComment = ParsedComment
      { _commentKind :: !CommentKind
      , _commentPos  :: !CommentPos
      , _commentText :: !String   -- body (delimiter-stripped, whitespace as-is)
      , _commentCol  :: !Int      -- source start column (1-based)
      } deriving (Show)
  ```
* Change `_comments :: [A.Located String]` → `[A.Located ParsedComment]` on the `Module` record.
* Update `Sky.Parse.Module.collectComments` to tag on collect:
  * `CommentLine` when opened with `--`
  * `CommentBlock` when opened with `{-`
  * `CommentOwnLine` when the comment's start col is the first non-whitespace char on its row (only whitespace before)
  * `CommentTrailing` when non-whitespace code exists BEFORE the start col on the same row
  * `_commentCol` = `A._col (A._start region)` at collect time
  * `_commentText` = raw body, NO `trimStart` (preserve leading whitespace for round-trip)
* Update the two current consumers to the new type:
  * `Format.hs:60` `_ = Src._comments m` — no change to behavior, just to typing
  * `Main.hs preserveTopLevelComments` — the string post-processor still runs, but needs to know how to project `ParsedComment` back to a raw `String` (`toRawComment :: ParsedComment -> String`)

Phase 1 verification:
* `cabal test` green (no behavior change; workaround still runs).
* `sky fmt` on issue #144's fixture produces the SAME dropped-comment output as today (Phase 1 is groundwork, not the fix).

### Phase 2 — Format.hs emission infrastructure

* Introduce a Reader/State cascade or an explicit `EmitCtx { pending :: [Located ParsedComment], out :: String }` that every helper threads.
* Add `drainBefore :: Region -> M ()` that flushes any pending comment whose `_start._line` is BEFORE the given region's start line.
* Wire drain into ~15 hole points identified by the grill:
  * Between top-level decls (`Format.hs:56`)
  * Between imports (`Format.hs:52`)
  * Module header / exposing (`fmtModuleHeader`)
  * Union constructors (`Format.hs:156-160`)
  * Record-type fields (`Format.hs:227-238`)
  * List/tuple/record-literal elements (`Format.hs:313 / 317 / 322`)
  * Record-update fields (`Format.hs:326-332`)
  * Binop segments (`Format.hs:346-363`)
  * If/else-if/else branches (`Format.hs:378-381`)
  * Let bindings (`Format.hs:384-390`)
  * Case branches (`Format.hs:393-398`)
  * Value's type-sig / def separator (`Format.hs:170-176`)
  * Lambda body (`Format.hs:366-371`) — **this is #144's shape**
  * Case-branch arrow → body (`Format.hs:440-442`)
  * Def LHS → multi-line RHS (`Format.hs:446-463`)
* Trailing comments: attach to the emitting node's SAME line (append after emitted expression, before final `\n`).
* Block comments: re-emit with `{- ... -}` wrap.

Phase 2 verification:
* Full existing `CommentsSpec` (11 shapes) green with **preserveTopLevelComments DISABLED** but `fmtSafetyCheck` still running as belt-and-braces.
* Issue #144 fixture: 3 previously-dropped lambda-body comments now survive.
* `sky fmt` two-pass byte-identical on 40-example sweep.

### Phase 3 — Delete `preserveTopLevelComments`

* Once Phase 2 verifies all 11 CommentsSpec shapes + #144 + a two-pass idempotency check on every example, delete `preserveTopLevelComments` from `app/Main.hs` (~500 LOC removal).
* Rewrite `FmtSpec.hs:69-114` (currently asserts `ExitFailure 1` on the #144 shape) to assert `ExitSuccess` + 9/9 comments present.
* Keep `fmtSafetyCheck` + `fmtForceMessage` as the safety net.

### Phase 4 — Ship v0.17.8

* Cabal test full suite (green).
* Example sweep (all 32 clean-build).
* `sky verify` runtime for CLI + web.
* Playwright.
* Tag v0.17.8 + downstream deploys.

## Non-negotiables

* No behavior regressions on the 11 shapes CommentsSpec locks today.
* `sky fmt --stdin` remains idempotent for two passes on any input.
* Block comments preserve their `{- -}` shape (never emit as line).
* Trailing comments stay trailing (never lifted to own-line).
* Multi-line block comment bodies preserve internal whitespace + newlines.

## Out of scope

* Adding trailing-comment slots to per-node Located AST wrappers. The flat `_comments` list carries region info, and drain-by-line at emit time is sufficient without touching every AST constructor. If a later requirement (e.g., LSP hover-over-comment) needs per-node attachment, that's a v0.18 concern.
