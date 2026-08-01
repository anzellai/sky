# Autonomous mandate — compiler + stdlib e2e test coverage (v0.19.x)

Set: 2026-08-01. Branch: feat/std-analytics.
(Supersedes the completed codec-derivation + kernel-metadata mandates.)

## User's goal (verbatim — the authority on "done")

> "we need to have test suites, or extensive single example testing all of
> these, not just regressed items, but most particular compiler issues that
> could go wrong. do you have any good idea how we can test everything
> compiler + stdlib e2e?"
>
> "ok keep going, grill + deep analysis for the fix/implementation, and fully
> tested + verified"
>
> "in autonomous mode"
>
> "must fix L1, C2 -- lenient vs strict, i prefer strict"
>
> "ok fully autonomous mode on, after fully tested + verified, usual e2e flow
> tag release, redeployments"

## End state (the mandate now runs all the way to a shipped release)

1. Finish + FULLY test/verify: conformance suite + all fixes (incl. L1 O(n) list
   ops, C2 strict decoder — both user-mandated).
2. Fresh-context adversarial JUDGE must return 100% before "done".
3. Usual e2e release flow (CLAUDE.md release checklist, incl. new step 3b
   conformance): rebuild, smoke, cargo test --workspace + xtask gates,
   conformance, clean-build examples, verify-all-web, verify-cli, from-scratch.
4. Tag the release (v0.19.3 — CHANGELOG already staged) + gh release from the
   CHANGELOG section (release-notes.sh). User has explicitly authorised the tag.
5. Redeployments: SkyDeploy bump SKY_VERSION + deploy (per §5 + memory), and the
   downstream apps (darraghstudio already on the fixed compiler; re-verify).

## What this means (the standard to hit)

Close the "compiles-clean-behaves-wrong" gap that let 8 real bugs ship this
session (all passed `sky check` + `go build` + corpus gates + oracle, yet were
runtime-behavioral bugs). Build **comprehensive compiler + stdlib e2e testing**
so this class is caught going forward:

- **Layer 1 — stdlib conformance suite** (Sky source, `sky test`, asserted with
  ADVERSARIAL inputs, not happy-path). One suite per module; start with the
  modules that bit us (`Std.Db.Store`, `Std.Codec`, `Std.Log`, `Std.Ui`) and
  grow to broad coverage.
- **Layer 2 — property / round-trip tests** (codec/JSON/base64 round-trip
  identity, Store write-read identity, orderBy stable total order, etc.).
- **Layer 3 — kitchen-sink behavioral e2e** (assert behavior, not just 200).
- Wire into CI / the release gate.

Plus: finish + verify the **compiler lint** (memoized-effect CAF warning) and
confirm all 9 findings are sound + correct + verified.

## The hard discipline (INVIOLABLE — CLAUDE.md §0)

- Each conformance test must be MEANINGFUL: provably FAIL on the buggy behavior
  (demonstrate the red state), not just pass on the fixed stdlib.
- I cannot declare "done" — only an independent adversarial Judge agent with
  fresh context, given this verbatim goal, may return "100% achieved". Any
  "but/except/mostly/for the scope of" in a PASS → NOT done.
- Full sweep + gates at milestone boundaries; narrow gates per change.
- Only stop on a genuine implementation blocker needing user decision.

## Findings tracked (this session)

1. CAF memoized DB read (listActive) — app fixed; compiler lint in progress
2. withOnNavigate sig — fixed + ty regression
3. Ui.button type=button — fixed
4. sky.toml inline comments — fixed + regression
5. Store multi-col ORDER BY reversed — fixed + manual verify
6. errRef frozen clock — fixed
7. withConn* swallow + db boot-race — fixed
8. Log structured attrs dropped — fixed + Go regression
9. compiler memoized-effect lint — fork in progress
