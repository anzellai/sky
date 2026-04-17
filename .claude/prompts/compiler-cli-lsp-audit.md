# End-to-end audit: compiler + CLI tooling + LSP + skychess AI

**Goal.** Behaviourally validate every command the user can run against
Sky, surface every regression/gap/hole, and fix them. Do not stop
until each item below has either landed a fix OR been documented in
`docs/KNOWN_LIMITATIONS.md` with a workaround. The marker is
`.claude/compiler-cli-lsp-audit-complete`.

If you genuinely need to pause, `touch .claude/allow-stop`.

---

## Scope (six areas)

### Area 1 — Compiler

For each of these flows, write a regression test (cabal spec or
test-files fixture) and verify it passes at HEAD. If a flow is
broken, fix it at source and re-verify.

- **Parsing**: every language construct in `docs/language/syntax.md`
  parses correctly. Build a `test-files/syntax-grammar-fence.sky`
  that exercises every construct (records, ADTs, lambdas, let, case,
  pipelines, multiline strings + interpolation, where-clause
  *workarounds* via `let`, `if/else if/else`, list/cons patterns,
  record update, qualified imports, `exposing` clauses).
- **Type inference**: HM produces correct types for polymorphic
  identity, `Result.map`/`andThen`, `List.foldl`, `List.parallelMap`.
  Annotated functions are rejected when body doesn't match (audit
  P2-3 already landed this — verify it didn't regress).
- **Codegen**: `sky check` runs `go build` and surfaces Go-level
  errors clearly. Corrupted or generic-with-narrow-constraint
  bindings emit `Err` stubs (not panics).
- **Error messages**: missing module → human-readable error pointing
  at the import line. Type mismatch → shows expected vs actual with
  variable letters renamed (`a, b, c` not `t108, t109`). Add specs
  to `test/Sky/ErrorReportingSpec.hs` that lock the format.
- **Edge cases**: zero-arg functions, partial application of
  multi-arg constructors, nested case-of subjects, empty list
  literal in typed FFI args (already fixed in `d22153b` — verify
  fence).

### Area 2 — CLI tooling (every command, end-to-end)

For each `sky` subcommand, write a `test/Sky/Cli/<Cmd>Spec.hs`
that runs the binary against a fresh `withSystemTempDirectory`
fixture and asserts behaviour. Each spec is the regression
contract.

| Command | Validation |
|---|---|
| `sky --version` | prints `sky v0.9.0 (haskell)`, exit 0 |
| `sky --help` | prints subcommand list, exit 0 |
| `sky init <name>` | scaffolds `sky.toml`, `src/Main.sky`, builds clean |
| `sky build src/Main.sky` | compiles, produces `sky-out/app`, exit 0 on success and ≥1 on type error |
| `sky run src/Main.sky` | builds + runs, propagates app exit code |
| `sky check src/Main.sky` | runs `go build` (audit P0-1) + exit ≥1 on Go errors |
| `sky fmt <file>` | idempotent; formatter doesn't lose code; refuses on >1/3 line loss |
| `sky test <file>` | runs `tests : List Test`; exit code reflects pass/fail |
| `sky add <pkg>` | fetches Go module, generates `.skycache/ffi/<slug>.{skyi,kernel.json}`, updates `sky.toml`, idempotent |
| `sky remove <pkg>` | drops dep from `sky.toml`, prunes cache; subsequent `sky build` fails if code still imports |
| `sky install` | re-fetches every declared dep; idempotent |
| `sky update` | bumps deps; commit-ready diff |
| `sky upgrade` | reaches GitHub releases endpoint (or skips gracefully if offline) |
| `sky lsp` | starts JSON-RPC over stdio; respond to `initialize`; document in `docs/tooling/lsp.md` (Area 3) |
| `sky clean` | removes `sky-out/`, `.skycache/`, `.skydeps/`, `dist/` only |
| `sky verify` | covered by audit P3-1 + P3-2 |

If a command **doesn't propagate exit codes** correctly (silent
failure) — **fix it**. The `sky build` → `go build` chain in
particular: if `go build` fails, `sky build` must exit ≥1.

### Area 3 — LSP coverage

Every capability declared in `docs/tooling/lsp.md` must be exercised
by `test/Sky/Lsp/<Capability>Spec.hs`. `Sky.Lsp.ProtocolSpec` (P3-2)
already covers initialize + hover. Extend with:

- **definition** — click "go to definition" on an identifier in
  fixture file; assert response points to the source `:file:line:col`.
- **references** — find all use sites of an identifier; assert
  they're returned.
- **rename** — invoke rename + verify `prepareProvider` shape;
  assert the rename returns workspace edits to every use site.
- **document symbols** — file outline returned correctly.
- **formatting** — `textDocument/formatting` returns the same
  output as `sky fmt --stdin`.
- **completion** — at a `.` after a module alias, returns the
  module's exposed identifiers.
- **diagnostics** — opening a file with a type error publishes a
  diagnostic at the right range.
- **semantic tokens** — full token set returned for a fixture.

If `docs/tooling/lsp.md` overpromises (a capability is declared but
not implemented), either implement it or **narrow the doc** —
honesty over aspiration.

### Area 4 — Skychess AI investigation

The AI plays nonsense moves (user reported, `<<previous-loop>>`).
The Sky source `examples/16-skychess/src/Chess/Ai.sky` looks
algorithmically sound (2-ply negamax with material + positional
eval), `oppositeColour` is correct, the eval tables look fine.

Likely root causes (investigate in order):
1. **`Move.applyMove` returns a wrong board** — Dict updates may
   not actually swap the piece. Add a Sky test:
   `tests/skychess-applymove-test.sky` that places a White Queen on
   d1, applies `applyMove 59 27` (d1→d4), asserts `Dict.get 59
   newBoard == Nothing && Dict.get 27 newBoard == Just (Queen,
   White)`. Run via `sky test`.
2. **`Move.allLegalMoves`** — for a known position with a hanging
   piece, assert the capture is in the legal moves list. If not,
   the move generator misses captures.
3. **`Eval.evaluate` table indexing** — verify `materialValue` returns
   non-zero for Queen (900), Rook (500). Add a test asserting
   `Eval.evaluate (boardWithBlackQueen)` < `Eval.evaluate emptyBoard`.
4. **Negamax scoring sign** — instrument `bestMove` to print every
   move + its score for a fixed test position; manually verify the
   ordering matches "obvious" chess intuition (capture > quiet move).

The `e2e.json` for skychess should grow once the AI is verified
sane — drive a sequence: GET / → click a White move → assert AI's
reply uses one of the top-3 candidate moves a 2-ply minimax should
choose. If we can't lock that strictly, at minimum assert: AI plays
a *legal* move, AI captures a hanging piece when one is available.

### Area 5 — `sky verify --e2e` integration

The current e2e harness is `scripts/example-e2e.sh`. The audit
prompt at `.claude/prompts/end-to-end-testing.md` proposed
extending `sky verify` with `--e2e`. **Implement that** so the
runner is a single binary: `sky verify --e2e [example]`.

Acceptance: `sky verify --e2e` runs every contract in Haskell
(parsing the same `e2e.json` schema), reports per-example
pass/fail with the same output format as existing `sky verify`,
and CI uses it instead of the bash script.

The bash script can stay as a quick local-dev convenience but the
authoritative path moves into the compiler.

### Area 6 — CI parity

After all the above, push the branch and watch CI to green. If any
test passes locally but fails in CI, that's an environment/state
gap to investigate. Common causes:

- macOS-specific code signing or env differences
- Linux-only `lsof` flag differences
- Missing fonts/display for Fyne (existing GUI skip handles this)
- Network flakiness on Go module proxy

Don't paper over flaky tests with retries — if a test is
non-deterministic, fix the source of non-determinism.

---

## Items (work top-to-bottom)

1. **Skychess AI** — Area 4 in full. Add the Sky-level tests + fix
   the underlying bug. Update the `e2e.json` to gate against
   regression.
2. **Compiler regression specs** — Area 1. Each new spec under
   `test/Sky/<Topic>Spec.hs`. Wire into `Spec.hs`.
3. **CLI specs** — Area 2. Add `test/Sky/Cli/*Spec.hs` for every
   subcommand. Cabal-test runs them all.
4. **LSP specs** — Area 3. Extend `Sky.Lsp.ProtocolSpec` or split
   into per-capability specs.
5. **`sky verify --e2e`** — Area 5. Move the harness into Haskell.
6. **Doc reconciliation** — for any discrepancy between docs and
   reality, either fix the implementation or update the doc.
   Canonical truth: every claim in `docs/tooling/lsp.md`,
   `docs/tooling/cli.md`, `CLAUDE.md`, and `README.md` must be true.
7. **Push + CI** — green build on the branch.
8. **Mark done** — `touch .claude/compiler-cli-lsp-audit-complete`.

## Operating rules

- **Tests first**, fix second. Every regression spec must FAIL at
  HEAD~1 (or document why it can't, e.g. requires a setup that
  didn't exist before the spec).
- **No relaxation.** If a test is hard to write because the
  behaviour is ambiguous, the behaviour is the bug — pin down what
  the right thing is and assert it.
- **Don't add v1.0 references.** v0.9 is the version; future v1.0
  is reserved for production-proven.
- **Don't change FFI from Result to Task** — settled (see
  `docs/ffi/boundary-philosophy.md`).
- **Don't break existing examples** — if a fix breaks one of the 18
  examples, find a path that satisfies both.
- **Honest commits** — if a regression spec landed but the fix is
  follow-up, say so in the commit message.

## Done condition

- Every item in the six areas above has a regression spec OR a
  documented limitation in `docs/KNOWN_LIMITATIONS.md`.
- `cabal test` green.
- `bash scripts/example-e2e.sh` green (17 + new contracts).
- `sky verify --e2e` (Haskell) green.
- CI green on `feat/sky-haskell-compiler`.
- `touch .claude/compiler-cli-lsp-audit-complete` lands.

The stop-hook releases automatically on the marker.
