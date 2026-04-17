# Known limitations resolution

**Goal.** Convert each entry in `docs/KNOWN_LIMITATIONS.md` into
either a shipped fix (with regression test) or a deliberate
deferral. By the end, `docs/KNOWN_LIMITATIONS.md` should be either
empty (everything fixed) or contain only items with explicit
"won't fix in v0.9" justification.

**Done condition.** `touch .claude/known-limitations-resolved`.
Stop-hook releases automatically.

If you genuinely need to pause, `touch .claude/allow-stop`.

---

## Operating principle

**Diagnostic-first.** For each limitation, the FIRST commit is a
test that fails (or surfaces the symptom in a CI-grep-able way).
The SECOND commit is the fix. This stops the loop accidentally
"fixing" something that wasn't actually broken, and gives every
fix a permanent regression fence.

**Don't change behaviour to make tests pass** — if a test is hard
to write, that means the contract is unclear; spec the contract
first then implement to it.

---

## Item 1 — Skychess AI sub-optimality (deep investigation)

The AI is algorithmically sound on paper. The bug is downstream.
Three diagnostic angles, run in order until one fails.

### 1a. Sky-level Move primitive tests

Add `tests/skychess-primitives-test.sky` exercising the chess
primitives in isolation:

```elm
module SkychessPrimitivesTest exposing (tests)

import Sky.Test as Test exposing (Test)
import Sky.Core.Dict as Dict
import Chess.Piece exposing (..)
import Chess.Board as Board
import Chess.Move as Move
import Chess.Eval as Eval

-- Place a White Queen on d1 (sq 59), verify Dict.get reads it back.
testQueenPlacement : Test
testQueenPlacement =
    Test.test "place queen, read back" (\_ ->
        let
            empty = Dict.empty
            withQueen = Board.setPiece 59 { kind = Queen, colour = White } empty
        in
            case Dict.get 59 withQueen of
                Just p -> Test.equal Queen p.kind
                Nothing -> Test.fail "queen disappeared from board"
    )

-- applyMove a White Queen from d1 (59) to d4 (35).
-- After: empty at 59, queen at 35, no captured.
testApplyMoveBasic : Test
testApplyMoveBasic =
    Test.test "applyMove d1->d4 moves the queen" (\_ ->
        let
            board = Board.setPiece 59 { kind = Queen, colour = White } Dict.empty
            (after, captured) = Move.applyMove 59 35 board defaultModel
        in
            case (Dict.get 59 after, Dict.get 35 after) of
                (Nothing, Just p) -> Test.equal Queen p.kind
                _ -> Test.fail "applyMove didn't relocate the queen"
    )

-- White queen on d4, Black knight on d6 (free capture).
-- Eval.evaluate should reflect material imbalance: +900 (Q) - 320 (N).
testEvalMaterial : Test
testEvalMaterial =
    Test.test "eval reflects material" (\_ ->
        let
            board = Board.setPiece 35 { kind = Queen, colour = White }
                  ( Board.setPiece 19 { kind = Knight, colour = Black } Dict.empty )
            score = Eval.evaluate board
        in
            Test.isTrue (score > 500 && score < 700)
    )

tests : List Test
tests = [ testQueenPlacement, testApplyMoveBasic, testEvalMaterial ]
```

You'll need to construct a stub `defaultModel` value with the
shape required by `Move.applyMove`. Inspect `examples/16-skychess/
src/State.sky:Model` for the field list. Many fields can be
defaulted (zero / Nothing / empty list); only `enPassantSquare`,
`whiteKingMoved`, `whiteRookAMoved` etc. matter for non-castling
moves.

Run: `(cd examples/16-skychess && sky test ../../tests/skychess-
primitives-test.sky)`. If `testApplyMoveBasic` fails — that's the
bug. Trace through `Board.movePiece` and `Dict.insert/remove` to
find why.

### 1b. Move-generation coverage

If 1a passes, suspect `Move.allLegalMoves` missing capture moves.
Add tests:

```elm
testQueenSeesCapture : Test
testQueenSeesCapture =
    -- White queen on d4 (35), Black knight on d6 (19). Queen
    -- should be able to move to 19 (capture knight).
    Test.test "allLegalMoves includes capture target" (\_ ->
        let
            board = ...
            moves = Move.allLegalMoves White board defaultModel
            hasCapture = List.any (\(from, to) -> from == 35 && to == 19) moves
        in
            Test.isTrue hasCapture
    )
```

### 1c. AI choice in obvious-capture position

If 1a + 1b pass, the bug is in scoring or ordering. Place a known
position where Black has one obvious capture (free queen), call
`Ai.bestMove`, assert it picks the capture:

```elm
testAiTakesHangingQueen : Test
testAiTakesHangingQueen =
    Test.test "AI captures hanging queen" (\_ ->
        let
            -- Set up a position where capturing the white queen is
            -- worth ~900 cp and any other move loses material.
            board = setupHangingQueenPosition ()
            modelBlack = { defaultModel | turn = Black, board = board }
            (from, to) = Ai.bestMove board modelBlack
        in
            Test.isTrue (from == BLACK_PIECE_SQ && to == HANGING_QUEEN_SQ)
    )
```

If THIS fails, the negamax math is the bug. Re-derive negative-max
on paper for a 1-ply tree and verify the sign convention matches
what the code does.

### 1d. Acceptance

After fixing the underlying bug:
- All three tests above pass
- `examples/16-skychess/e2e.json` extended to drive a 4-move game
  via Sky.Live event dispatch and assert the AI doesn't blunder
  material in a position designed to expose the bug

Update `KNOWN_LIMITATIONS.md` to remove the entry.

---

## Item 2 — Remaining CLI subcommand specs

Mechanical work. For each subcommand below, add
`test/Sky/Cli/<Cmd>Spec.hs` mirroring the structure of
`ExitCodesSpec.hs`. Use `withSystemTempDirectory` for isolation,
`readCreateProcessWithExitCode (proc sky [...])` for invocation.

### 2a. `sky init <name>`

```hs
it "scaffolds a buildable project" $ do
    sky <- findSky
    withSystemTempDirectory "sky-init" $ \tmp -> do
        (ec, _, _) <- runIn tmp sky ["init", "myapp"]
        ec `shouldBe` ExitSuccess
        doesFileExist (tmp </> "myapp" </> "sky.toml") `shouldReturn` True
        doesFileExist (tmp </> "myapp" </> "src" </> "Main.sky") `shouldReturn` True
        -- Scaffolded project must build clean
        (ec2, _, _) <- runIn (tmp </> "myapp") sky ["build", "src/Main.sky"]
        ec2 `shouldBe` ExitSuccess
```

### 2b. `sky run`

Verify `sky run src/Main.sky` propagates the app's exit code.
Build a fixture that calls `Process.exit 42` and assert the wrapper
exits 42.

### 2c. `sky fmt`

- **Idempotency**: format twice, assert byte-identical
- **Refusal on data loss**: feed a deliberately mangled input that
  parses to a tiny AST and assert `sky fmt` refuses to overwrite
  (don't lose >1/3 lines)

### 2d. `sky test`

Build a `tests/PassingTest.sky` and `tests/FailingTest.sky`,
verify the wrapper exits 0 / non-zero respectively.

### 2e. `sky add/remove/install/update`

These hit the network. Use a minimal Go module that's
already cached (e.g. `github.com/google/uuid` — pinned by every
example); verify add+remove are idempotent and update sky.toml.
For `update`, just verify exit 0 and a non-corrupting diff (no
required changes if everything's already at latest).

### 2f. `sky upgrade`

Tricky — hits GitHub. Either:
- Skip with a comment explaining (`expectationFailure` only if
  network) — pragmatic
- OR mock via `SKY_UPGRADE_URL` env var pointing at a local fixture
  (preferred — would also help users on air-gapped networks)

### 2g. `sky clean`

Create `sky-out/`, `.skycache/`, `dist/`, plus a non-managed file
like `README.md`; assert `sky clean` removes the first three but
leaves the user file alone.

### Acceptance

All seven specs added, wired into `Spec.hs` and `sky-compiler.cabal`,
`cabal test --test-options='--match "Sky.Cli"'` passes.

---

## Item 3 — LSP per-capability specs

Currently `Sky.Lsp.ProtocolSpec` covers `initialize` + `hover`.
Add (one spec each, can share a fixture file):

### 3a. definition (`textDocument/definition`)

Open a fixture with `import Lib.Util` and a call site of
`Util.foo`. Send `textDocument/definition` at the call site's
position; assert response includes `Lib/Util.sky` URI + the line
where `foo` is defined.

### 3b. references (`textDocument/references`)

Define `foo` in one file, use it in two others. Send `references`
at the definition; assert response includes both use-sites + the
definition itself.

### 3c. rename (`textDocument/rename` + `prepareProvider`)

Rename `foo` to `bar`. Assert workspace edit includes every
occurrence with the new name.

### 3d. document symbols

Send `textDocument/documentSymbol`; assert response is the file's
top-level declarations.

### 3e. formatting

Send `textDocument/formatting`; assert response is byte-identical
to `sky fmt --stdin` on the same input.

### 3f. completion (triggered on `.`)

Type `String.` in a fixture; send completion request at that
position; assert results include `String.length`, `String.toUpper`,
etc.

### 3g. diagnostics

Open a file with a type error via `textDocument/didOpen`; wait for
`textDocument/publishDiagnostics`; assert diagnostic lists the
type mismatch with correct range.

### 3h. semantic tokens

Send `textDocument/semanticTokens/full`; assert response is a
non-empty token array.

### Acceptance

Each capability has a spec in `test/Sky/Lsp/<Capability>Spec.hs`.
If a capability is declared in `docs/tooling/lsp.md` but doesn't
work in practice, **either implement it or remove the claim**.

---

## Item 4 — Port `sky verify --e2e` into Haskell

The bash harness at `scripts/example-e2e.sh` becomes the dev
convenience; the canonical runner moves into the compiler.

### 4a. Schema parser (Haskell)

Add `app/Main.hs` (or wherever `verify` is wired) JSON parser for
`examples/<n>/e2e.json`. Mirror the schema documented in the bash
harness — `kind`, `port`, `startupWaitMs`, `steps[]` with `args`,
`expectExit`, `expectStdoutContains`, `method`, `path`,
`expectStatus`, `expectBodyContains`, `expectStderrAbsent`,
`form`/`jsonBody`.

### 4b. Three runners

- `runCliContract` — proc-spawn `./sky-out/app` with args, assert
  exit code + stdout substrings
- `runServerContract` — boot subprocess, threadDelay startupWait,
  iterate steps via `http-client` (or shell out to curl with a
  cookie jar in a tmpdir), terminate
- `runLiveContract` — same as server, plus session-cookie capture
  for Sky.Live event dispatches

### 4c. CLI surface

`sky verify` (existing) keeps current behaviour — auto-includes
e2e if `examples/<n>/e2e.json` exists, OR add explicit `--e2e`
flag. Both options are fine; pick what matches the existing
verify code shape.

### 4d. CI swap

Once the Haskell runner produces parity output with the bash
script, replace `bash scripts/example-e2e.sh` with `sky verify
--e2e` in `.github/workflows/ci.yml`. Keep the bash script as a
dev-loop convenience; document the relationship in
`scripts/README.md`.

### Acceptance

- `sky verify --e2e` exits 0 against current HEAD with the same
  17/17 pass count
- A new `test/Sky/Build/E2eRunnerSpec.hs` covers the parser +
  runner with a tiny fixture project
- CI swap landed; bash script kept as dev convenience

---

## Item 5 — Domain-logic regression classes

These are categorical, not single bugs. Each gets a *capability*,
not a one-off fix:

### 5a. Sky-test runner wired into `cabal test`

`tests/**/*Test.sky` files should run automatically as part of
`cabal test`. Add `test/Sky/Build/SkyTestSpec.hs` that scans for
`tests/*.sky` files and invokes `sky test` on each, surfacing
failures as cabal-test failures. This unblocks targeted Sky-level
tests for chess, sky.Live concurrency, etc.

### 5b. Cross-session race fixture

For 12-skyvote: a spec that boots the server, fires N=10 parallel
HTTP requests doing `SubmitComment` on the same idea, asserts all
N comments persist (no constraint violation, no lost rows). Catches
DB key-derivation bugs at scale.

### 5c. External-service skip semantics

For Stripe/Firestore paths in 13-skyshop: the e2e contract has
`expectStderrAbsent` for `panic:`. Extend with explicit "Stripe
key not present → graceful Err message in body, not 500" so we
verify the credential-absent path renders correctly.

### Acceptance

5a is the foundation — once `sky test` runs in CI, 5b and 5c
become Sky-level test files. Ship 5a even if 5b/5c slip to a
follow-up.

---

## Item 6 — Doc reconciliation pass

Once items 1-5 land:

- Re-read `docs/tooling/lsp.md` — every claim true post-3?
- Re-read `docs/tooling/cli.md` — every command accurate post-2?
- Re-read `docs/KNOWN_LIMITATIONS.md` — purge entries that landed;
  rewrite remaining ones with current detail
- Re-read `CLAUDE.md` and `templates/CLAUDE.md` — code samples
  still valid against current compiler?

For any discrepancy: **either** update the doc to match reality
**or** fix the implementation to match the doc. Do NOT leave
docs aspirational.

---

## Item 7 — CI parity + push

After all items:
- Run `cabal test` (full)
- Run `bash scripts/example-sweep.sh --build-only`
- Run `bash scripts/example-e2e.sh` (or the new Haskell version)
- Run `(cd runtime-go && go test ./rt/)`
- Push the branch
- Watch CI green

If CI fails where local passes, root-cause the env diff (macOS vs
Linux, network availability, Go module cache state). Don't add
flake-tolerance retries — fix the source.

---

## Item 8 — Mark complete

Once items 1-7 land green:

```bash
touch .claude/known-limitations-resolved
```

Stop-hook gate releases.

---

## Per-session scope guidance

This brief is realistically 3-5 sessions of work. Each session
should land at minimum one item end-to-end (test fence + fix +
doc update + commit). Don't ship partial-state limbo where a
spec exists but the underlying fix is "TODO".

If a session can only land partial coverage on a multi-part item
(e.g. 3 of 7 CLI specs), **commit what's complete and update
KNOWN_LIMITATIONS.md** to reflect the new partial state honestly.
The marker only goes down when the actual limitations doc is
empty (or has only deliberate-deferral entries with stated
justification).

## Operating rules

- **No new v1.0 references** — Sky is v0.9
- **No FFI Result→Task changes** — settled design
- **Tests first, fix second**
- **No relaxation** — if a test is hard to write, the behaviour is
  the bug
- **Honest commits** — partial-state work labelled as such in the
  message
