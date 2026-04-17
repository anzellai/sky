# Soundness + LSP diagnostic parity

**Goal.** Sky's own compile pipeline catches every source-level error
at the Sky layer — not as a fallback through `go build`. Every error
the compiler catches ALSO flows through `textDocument/publishDiagnostics`
so the user sees it in their editor immediately, not at `sky build` time.

Dev experience is top priority. "If it compiles, it works" depends on
the compiler actually catching what it claims to catch.

**Done condition.** `touch .claude/soundness-lsp-complete`.
Stop-hook releases automatically.

If you genuinely need to pause, `touch .claude/allow-stop`.

---

## Verified gaps (caught by empirical testing, commit d0becf4 onwards)

### Gap 1 — Undefined variable not caught by canonicaliser

**Symptom.** Typo like `messgae` passes through Sky's name resolver,
emits `rt.Log_printlnT(any(messgae))` to Go, `go build` fails. `sky
check` exits non-zero but prints "compiler-side bug — Sky type system
accepted the program but Go did not." The error is neither
user-friendly nor positioned.

**Root cause.** `src/Sky/Canonicalise/Expression.hs` (or
`Environment.hs`) resolves identifiers against imports + local env
but silently passes through unresolved names. There's no
`Left UnboundVariable` path — the canonicaliser emits a placeholder
reference and trusts downstream type inference to catch it. HM
doesn't, because an unbound name defaults to a fresh type variable
without constraint.

**Fix.** Canonicaliser must return `Left "line:col: Undefined name: <name>"`
for any identifier that doesn't resolve to:
- a local let / lambda binding
- a top-level declaration in the current module
- an imported name (qualified or via `exposing`)
- a constructor from the same sources
- a stdlib kernel

**Test first.** `test/Sky/Canonicalise/UnboundSpec.hs`:
```hs
it "rejects a typo at canonicalise time with line:col" $ do
    src <- writeFixture "main = println messgae"
    result <- Compile.compile config src outDir
    case result of
        Left err -> do
            err `shouldContain` "Undefined name: messgae"
            err `shouldContain` "line"  -- has position
        Right _ -> expectationFailure "typo accepted"
```

Write this first; it should FAIL against HEAD. Then fix the
canonicaliser. Test passes. Commit as:
`[soundness/unbound] canonicaliser rejects undefined names`

### Gap 2 — LSP doesn't run exhaustiveness / unbound-name checks

**Symptom.** `sky check` reports `EXHAUSTIVENESS ERROR` on a
non-exhaustive case. Editor (via LSP) shows no diagnostic. User
only sees the error at build time.

**Root cause.** `src/Sky/Lsp/Server.hs:runPipeline` stops at
Parse → Canonicalise → Constrain → Solve. The separate
exhaustiveness pass (invoked in `Sky.Build.Compile.compile`)
never runs in the LSP path.

**Fix.** Extend `runPipeline` (or `computeDiagnostics`) to also run:
- the exhaustiveness pass from `Sky.Type.Exhaustiveness`
- (after Gap 1 lands) the canonicaliser's unbound-name check

Each error translates to an LSP `Diagnostic` with `severity: 1`
(error) and the correct range. Use the existing `stripMsgPos`
helper for position extraction.

**Test first.** Extend `test/Sky/Lsp/CapabilitiesSpec.hs` with a
didOpen-of-broken-file + publishDiagnostics-waiter:

```hs
it "publishes diagnostics on non-exhaustive case" $ do
    -- Needs harness upgrade: listen for server-pushed
    -- `textDocument/publishDiagnostics` notifications after didOpen
    ...
    didOpen hin fixture srcWithNonExhaustive
    diags <- awaitNotification "textDocument/publishDiagnostics"
    (anyDiagMatches "case does not cover" diags) `shouldBe` True
```

The harness enhancement (notification queue) is a sub-task of this
item. Add a tiny queue helper in `LspTestHarness.hs` (extract from
the duplicated boilerplate in ProtocolSpec.hs +
CapabilitiesSpec.hs — this is the excuse to DRY them).

Commit as:
`[soundness/lsp-diag] LSP runs exhaustiveness + unbound-name passes`

---

## Broader principle — canonicaliser is the source of truth for semantic errors

Audit every pass downstream of Parse and verify that every error
it can produce:
1. Has a clear, user-facing message
2. Has line:col position (or a clear reason why not)
3. Causes `sky check` / `sky build` to exit non-zero with the error
   at the top (not buried under "Compilation successful" noise)
4. Flows through `publishDiagnostics` in the LSP path

Candidates to audit (each in turn — diagnostic first, fix second):
- **Unused imports** — Sky currently silently accepts them. If
  the language philosophy is to warn, wire the warning through.
  If it's fine, document the decision in CLAUDE.md.
- **Shadowing** — `let x = 1 in let x = 2 in x` should warn or be
  rejected based on the language decision. Check P2-2 audit notes
  for the current stance.
- **Non-exhaustive if-else-if chain** — `if x == 1 then a` with no
  else is a type error (not a Bool → a completion) but the
  message should be specific.
- **Record field typos** — `record.naem` when the field is `name`
  should be a type error with a clear message, not "Cannot unify
  { naem : a | r } with { name : String, ... }".

Not every case needs a separate commit — batch related catches
(e.g. record-field-typo message improvement) into one.

---

## LSP harness enhancement (sub-task of Gap 2)

Current `ProtocolSpec.hs` and `CapabilitiesSpec.hs` duplicate the
JSON-RPC framing, send/recv helpers, init + didOpen helpers. Extract
to `test/Sky/Lsp/Harness.hs` with:

```hs
module Sky.Lsp.Harness
    ( withLsp
    , initializeLsp
    , sendRequest
    , recvResponseFor
    , recvNotification       -- new: wait for a server-pushed notif
    , awaitNotification      -- with timeout
    , ...
    ) where
```

Both existing specs should import from there instead of inlining.
This unblocks the diagnostic-awaiter test for Gap 2.

---

## Verification per item

For each gap fix:
1. Failing test committed (separate commit OR same commit with
   `[broken]` annotation explaining the expected fail window)
2. Fix committed immediately after; test now green
3. `cabal test --test-options='--match "<new spec name>"'` passes
4. `bash scripts/example-sweep.sh --build-only` green
5. `bash scripts/example-e2e.sh` green
6. `pass=0; fail=0; for f in test-files/*.sky; do rm -rf .skycache;
   ./sky-out/sky build "$f" >/dev/null 2>&1 && pass=$((pass+1)) ||
   fail=$((fail+1)); done; echo "self-tests: $pass/$fail"` → 67/0

---

## Items (work top-to-bottom)

1. **Gap 1: canonicaliser catches unbound names.** Spec first,
   fix second. Update `docs/KNOWN_LIMITATIONS.md` to note "caught
   at compile time" instead of "falls through to Go".
2. **LSP harness DRY** — extract shared helpers into
   `test/Sky/Lsp/Harness.hs`.
3. **Gap 2a: LSP runs exhaustiveness.** Extend `computeDiagnostics`.
   Add diagnostic-awaiter spec using the new harness.
4. **Gap 2b: LSP runs unbound-name check** (after Gap 1).
5. **Broader audit** — walk through the candidates under "Broader
   principle" above. Each batch lands with a regression fence.
6. **CI parity** — push; watch CI green.
7. **Mark done** — `touch .claude/soundness-lsp-complete`.

---

## Operating rules

- **Tests first.** No spec-less fix.
- **Honest positions.** Every diagnostic carries `line:col` when
  the pass knows the location. If it genuinely can't (e.g. a
  cross-module whole-program check), document why at the call
  site.
- **Don't degrade existing diagnostics.** Exhaustiveness already
  has good positions — don't regress them while adding LSP flow.
- **No v1.0 references.** Sky is v0.9.
- **No Result→Task FFI changes.** Settled design.
- **If a fix changes a user-visible error message format**, update
  any existing specs that depend on the old wording.

## Scope

Realistically 2-4 sessions. Each session lands at minimum one
item end-to-end. Don't ship spec-without-fix limbo — if a spec
exists, the fix lands in the same session.
