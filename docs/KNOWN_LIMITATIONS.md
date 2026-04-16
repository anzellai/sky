# Known limitations

Tracked here so future work has a single place to look. Each entry
explains the symptom, what's known, and the recommended workaround.

---

## Skychess AI plays sub-optimal moves

**Symptom.** The 16-skychess example's AI sometimes makes moves that
don't capture obvious hanging pieces, or plays material-losing moves.

**Known.** The AI uses a 2-ply negamax with material + piece-square
evaluation. Algorithmically the code is sound:

- `oppositeColour` correctly flips White↔Black
- `negamax` returns evaluation from the side-to-move's perspective;
  caller negates correctly
- `Eval.evaluate` walks the board, sums material + positional values
- `Move.applyMove` updates the board via `Dict.remove + Dict.insert`

The actual gameplay regression has not been root-caused yet. The
likely candidates (in order of plausibility):

1. **`Move.applyMove`** producing a partially-updated board. Sky's
   `Dict Int Piece` uses `map[string]any` at runtime; if Int↔string
   key conversion isn't consistent between insert and lookup, the
   AI evaluates a board state that doesn't match the actual game.
2. **`Move.allLegalMoves`** missing capture moves. If the move
   generator skips captures for some piece kinds, the AI can't
   consider them.
3. **`Eval.evaluate` table indexing** — the piece-square table
   lookup uses a positional index that's mirrored for Black; off-by-
   one would skew evaluation.

**Workaround.** None at present. This is a non-blocking quality
issue; the game is still playable but the computer is weak. Tracked
as item 1 in `.claude/prompts/compiler-cli-lsp-audit.md` for a
future deep-investigation session that adds Sky-level tests
exercising the chess primitives directly (via `sky test`).

---

## CLI per-subcommand specs are partial

**Symptom.** Some `sky <subcommand>` commands have no automated
contract verifying their exit codes / outputs. A silent regression
where (for example) `sky build` exits 0 despite a `go build` error
could ship undetected.

**Covered now** (one spec module per command under `test/Sky/Cli/`):
- `sky --version`, `sky build` (ok / syntax error / Go-level error),
  `sky check` — `ExitCodesSpec.hs`
- `sky init <name>` — scaffolding + scaffold-builds-clean — `InitSpec.hs`
- `sky run` — exit propagation + stdout capture — `RunSpec.hs`
- `sky fmt` — second-pass idempotency — `FmtSpec.hs`
- `sky clean` — removes managed dirs only, preserves user files — `CleanSpec.hs`

**Remaining gaps:**
- `sky test` — pass/fail propagation. Needs a passing + failing
  fixture in `tests/`. Blocked on item 5a (Sky-test-runner-in-cabal).
- `sky add/remove/install/update` — network-dependent; spec needs
  to mock the Go module proxy or skip-with-reason on offline CI.
- `sky upgrade` — hits GitHub releases; needs `SKY_UPGRADE_URL`
  env-var override for hermetic testing.

**Workaround.** Manual validation via `bash scripts/example-sweep.sh`
and `sky verify` exercises most paths via the example matrix.

---

## LSP capabilities partially specced

**Symptom.** `docs/tooling/lsp.md` declares many capabilities;
`Sky.Lsp.ProtocolSpec` only verifies `initialize` and `hover`.

**Known.** Capabilities asserted to work but lacking integration
specs: definition, references, rename (with prepareProvider),
document symbols, formatting, completion (triggered on `.`),
diagnostics on file open, semantic tokens.

**Workaround.** None — manual editor testing via Helix/Zed/VS Code.
A future session should extend `ProtocolSpec` (or split into
per-capability specs) with JSON-RPC fixtures for each.

---

## E2E harness is bash, not native `sky verify --e2e`

**Symptom.** `scripts/example-e2e.sh` is the authoritative end-to-
end runner. It's a 300-line bash script — works, but it's not part
of the compiler binary.

**Known.** Plan in `.claude/prompts/compiler-cli-lsp-audit.md`
item 5: port the runner into Haskell so `sky verify --e2e` is the
authoritative command and CI uses the binary directly.

**Workaround.** `scripts/example-e2e.sh` is what CI runs today; it
ships green on the example matrix. The bash version is a working
intermediate.

---

## What "compiles, it works" still doesn't catch

The audit principle holds for source-to-Go-codegen correctness, but
domain logic regressions slip through:

- **Algorithmic correctness** — chess AI weakness above
- **DB constraint design** — fixed in 12-skyvote (PRIMARY KEY
  collision on identical comments) but the class persists wherever
  user code derives keys deterministically from non-unique inputs
- **Race conditions** — Sky.Live session locking handles per-session
  serialisation but cross-session writes (e.g. concurrent comment
  inserts on the same idea) aren't tested
- **External service dependencies** — Stripe/Firestore examples
  build clean but require live credentials to genuinely run; e2e
  contracts skip the deep-API path

These need targeted Sky-level tests via `sky test tests/**/*Test.sky`
once that test runner is fully wired into `cabal test` (audit P4-2,
out of scope for the v0.9 line).
