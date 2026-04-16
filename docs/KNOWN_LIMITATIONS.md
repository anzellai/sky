# Known limitations

Deliberate scope decisions for the v0.9 line. Each entry explains
the gap, the justification for deferring, and the workaround.
Limits below are **won't-fix in v0.9**; the roadmap treats them as
v0.10+ / v1.0 concerns.

---

## Skychess AI plays sub-optimal moves

**Gap.** The 16-skychess example's AI sometimes makes moves that
don't capture obvious hanging pieces or plays material-losing
moves.

**Known.** Algorithmically the code is sound (2-ply negamax,
correct `oppositeColour`, correct sign conventions, reasonable
material + piece-square tables). The bug is downstream. Likely
candidates:

1. `Move.applyMove` producing a partially-updated board via
   `Dict Int → map[string]any` key-conversion asymmetry.
2. `Move.allLegalMoves` missing captures for some piece kinds.
3. `Eval.evaluate` piece-square-table indexing off-by-one in the
   Black-mirror path.

**Won't fix in v0.9 because:** root-causing requires a chess-deep-
debug session with Sky-level primitive tests — writing board
fixtures, asserting `applyMove` move-by-move, and tracing the
negamax tree manually for a known-good position. This is a
quality issue in a single example, not a compiler or runtime
correctness issue. The example is still playable; the AI is just
weak.

**Workaround for users.** None; play against a weaker opponent.

**Workaround for investigators.** `sky test tests/**/*Test.sky`
now works (the module-discovery fix shipped alongside
`test/Sky/Cli/TestSpec.hs`). A future session should add
`examples/16-skychess/tests/ChessPrimitivesTest.sky` exercising
the primitives in isolation, then fix at the root.

---

## CLI per-subcommand specs — 2 gaps remain (dep commands + upgrade)

**Covered** (one spec module per command under `test/Sky/Cli/`):
- `sky --version`, `sky build` (ok / syntax error / Go-level error),
  `sky check` — `ExitCodesSpec.hs`
- `sky init <name>` — scaffolding + scaffold-builds-clean — `InitSpec.hs`
- `sky run` — exit propagation + stdout capture — `RunSpec.hs`
- `sky fmt` — second-pass idempotency — `FmtSpec.hs`
- `sky clean` — removes managed dirs only, preserves user files — `CleanSpec.hs`
- `sky test` — pass/fail propagation — `TestSpec.hs`

**Remaining gaps:**
- `sky add/remove/install/update` — hit the Go module proxy
  (proxy.golang.org) and so can't run reliably in offline CI.
- `sky upgrade` — hits GitHub releases; same issue.

**Won't fix in v0.9 because:** hermetic testing of these commands
requires either a local HTTP mock (substantial code) or a
`SKY_UPGRADE_URL` / `GOPROXY=off`-style env-override path (a
non-trivial refactor of the dep-fetch code). Both are tooling
improvements, not correctness fences. In practice: the example
sweep under `scripts/example-sweep.sh --build-only` exercises
`sky build` against every example's declared Go deps, catching
dep-resolution regressions holistically.

**Workaround for users.** Standard Go module semantics apply;
`sky add <pkg>` works like `go get`.

---

## LSP capabilities — all specced (resolved)

Every capability advertised by `sky lsp` now has an end-to-end
integration spec under `test/Sky/Lsp/`:

| Capability | Spec |
|---|---|
| `initialize` + capabilities payload | `ProtocolSpec.hs` |
| `textDocument/hover` | `ProtocolSpec.hs` |
| `textDocument/definition` | `CapabilitiesSpec.hs` |
| `textDocument/documentSymbol` | `CapabilitiesSpec.hs` |
| `textDocument/formatting` | `CapabilitiesSpec.hs` |
| `textDocument/references` | `CapabilitiesSpec.hs` |
| `textDocument/rename` | `CapabilitiesSpec.hs` |
| `textDocument/completion` | `CapabilitiesSpec.hs` |
| `textDocument/semanticTokens/full` | `CapabilitiesSpec.hs` |
| server stays alive on broken `didOpen` | `CapabilitiesSpec.hs` |

Known follow-up: the harness doesn't listen for server-pushed
notifications, so `publishDiagnostics` is verified indirectly
(server remains responsive to a follow-up request after opening a
syntactically-broken file). A future enhancement could add a
notification queue to the harness.

---

## E2E harness is bash, not native `sky verify --e2e`

**Gap.** `scripts/example-e2e.sh` (300 lines of bash) is the
authoritative end-to-end runner. CI invokes it after `sky verify`.

**Won't fix in v0.9 because:** porting to Haskell (so `sky verify
--e2e` becomes the canonical command) is a quality improvement,
not a correctness concern. The bash runner:
- Passes all 17 example contracts
- Runs cleanly in CI on both macOS and Linux
- Supports the same `e2e.json` schema the Haskell port would read

The port matters for single-binary purity (Sky already ships one
`sky` binary; the bash script is an external dependency). That's
a v0.10+ concern.

**Workaround.** Run `bash scripts/example-e2e.sh` locally; CI
does the same.

---

## "If it compiles, it works" — residual categorical gaps

The v0.9 soundness audit closed every documented counterexample
for source-to-Go-codegen correctness. Four classes of regression
remain outside the audit's reach:

1. **Algorithmic correctness** — chess AI example above. The
   compiler cannot verify domain logic is *correct*, only that
   it type-checks.
2. **DB constraint design** — fixed case-by-case (12-skyvote
   PRIMARY KEY collision on identical comments); the class
   persists wherever user code derives keys deterministically
   from non-unique inputs.
3. **Race conditions** — Sky.Live session locking handles
   per-session serialisation but cross-session writes (concurrent
   comment inserts on the same idea) aren't tested at scale.
4. **External service dependencies** — Stripe/Firestore examples
   build clean but require live credentials to genuinely run; e2e
   contracts skip the deep-API path.

**Won't fix in v0.9 because:** these are open-ended quality
concerns, not finite-scope bugs. Each is addressed by targeted
Sky-level tests when caught in the wild, not by a one-off fix.

**Foundation shipped.** `sky test tests/**/*Test.sky` now works
(module-discovery bug where `tests/` wasn't an implicit source
root was fixed alongside `test/Sky/Cli/TestSpec.hs`). Future
sessions wanting to fence any of the four classes above add a
Sky test file and wire it into CI via the existing
`test/Sky/Cli/TestSpec.hs` pattern or a dedicated
`test/Sky/Integration/*.hs` that invokes `sky test`.

The v0.9 line ships with: HM soundness, FFI trust boundary,
exhaustive pattern matching, 67 self-tests, 18 example sweep,
17 e2e contracts, 10 LSP capability specs, 7 CLI specs, and
the entire audit-remediation test matrix green. Everything
above is future work on top of that floor.
