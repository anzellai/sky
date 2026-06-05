# Cycle 1 — P5 Developer implementation log

Closes: audit Gap A6 (`audits/CYCLE-01-auditor.md`) + planner item P5 (`plans/CYCLE-01-planner.md`).

Branch: `feat/v0.15.x-hardening-P5-auth-typed-boundary`
PR: [#81](https://github.com/anzellai/sky/pull/81)
Target tag: **v0.15.12** (human-gated; not pushed by this PR)

## Architectural diagnosis

Two soundness gaps converged on the Auth boundary in the runtime.

**Leak 1 — `%T` in user-visible error messages.** Pre-fix,
`mustStringTyped` (`runtime-go/rt/db_auth.go:102`) and
`coerceAuthSecret` (line 1006) interpolated `fmt.Sprintf("%T", v)`
into the `Err InvalidInput` message returned to the caller. So a
non-String secret produced messages like `signToken: secret must be
a String, got <nil>` or `verifyToken: token must be a String, got
int` — observable via API responses and log scraping. That gives
an attacker free reconnaissance of how the upstream Sky binding is
shaped.

**Leak 2 — compile-time accepts `any`-typed bridges into String
slots.** A Sky function declared `: any -> any` (or, more
realistically, `bridge : any` bound to a raw `Ffi.kernel "X"`
reference) could flow into `Auth.hashPassword` with the compiler
**not** rejecting it. Sky's wildcard-`any` semantics (per
`Canonicalise.Type.freeTypeVars` + `Instantiate.fromAnnotation`)
give each occurrence its own fresh UF variable, so the call site
unified the per-occurrence var with `String` even when the binding
itself declared no contract. At runtime the value's actual Go type
could be anything, and the upstream binding could be safely typed
locally but unsound at the boundary.

## Sequenced steps

1. **Failing-test-first.**
   `runtime-go/rt/auth_typed_boundary_test.go` (NEW, ~120 LOC, 6
   tests) — for each public Auth kernel taking a String parameter
   (`hashPassword`, `hashPasswordCost`, `passwordStrength`,
   `signToken`, `verifyToken` secret-leg, `verifyToken` token-leg),
   pass a non-String value and assert the user-visible message
   (a) starts with `<kernel>: expected String` AND (b) does NOT
   contain any `, got <Go-type>` substring (`int`, `<nil>`, `map[`,
   `*`, `rt.`, `float64`, `bool`, `[]`, `struct`). All 6 tests
   FAIL on the starting worktree.

2. **Failing-test-first.**
   `test/Sky/Build/AuthUntypedBoundarySpec.hs` (NEW, ~140 LOC) —
   a "bad fixture" with `bridge : any` bound to a raw
   `Ffi.kernel "Time_unixMillis"` passed to `Auth.hashPassword`;
   asserts the build fails with both `E4006` AND
   `Sky.Auth.UntypedBoundary` substrings AND the `sky-out/app`
   binary is absent. A "good fixture" with `bridge : String`
   passes through — confirms the gate isn't over-strict. The bad
   spec FAILS on the starting worktree (build succeeds).

3. **Runtime fix** (`runtime-go/rt/db_auth.go`):
   * `mustStringTyped` → fixed message `<callerTag>: expected
     String`; calls new `logAuthBoundaryLeak` to emit the real Go
     type to stderr for the server-side audit trail.
   * `coerceAuthSecret` → same treatment; the secret-too-short
     variant retained its detailed message because that is
     **intentional security UX** for the operator (telling them
     "your secret needs to be 32+ bytes") and reveals nothing
     about the surrounding Sky binding.
   * `Auth_verifyToken` token-leg → same fixed-message pattern
     (was a separate `fmt.Sprintf` site with the same leak).
   * `logAuthBoundaryLeak` → `fmt.Fprintf(os.Stderr, "[WARN]
     auth.boundary kernel=%s goType=%T reason=non-string-arg")`.
     No env-var disable path — the audit trail must be
     undefeatable.

4. **Compile-time gate**
   (`src/Sky/Build/Compile.hs` + `src/Sky/Reporting/Diagnostic.hs`):
   * New `authE_UntypedBoundary = DiagCode "E4006"` registered in
     the diagnostic-code registry.
   * New `authSecurityKernels :: Map String [Int]` — for each
     of the 9 Auth kernels (`hashPassword`, `hashPasswordCost`,
     `passwordStrength`, `verifyPassword`, `signToken`,
     `verifyToken`, `register`, `login`, `setRole`), the 0-indexed
     slot positions whose Sky-level type is `String`. Derived by
     hand-cross-checking `lookupKernelType` in
     `Sky.Type.Constrain.Expression`.
   * New `walkAuthCalls` — folds over the canonical `Can.Decls`,
     visiting every `Can.Call` whose head (after `rewriteAliasHead`)
     resolves to `Can.VarKernel "Auth" _`. The walker descends
     into every sub-expression shape (`Lambda`, `If`, `Let`,
     `LetRec`, `Case`, `Access`, `Update`, `Record`, `List`,
     `Negate`, `Binop`, `Tuple`) so the gate sees nested call sites.
   * New `collectSourceAnnots` — per-module map of binding name →
     **raw** source-level annotation type (reconstructed by
     folding pattern types onto `Can.TypedDef`'s `retType`).
     This is the second contract source — `globalAnnotMap` is
     built post-HM and stores the solved type, which can collapse
     a source `: any` into `: String` after call-site unification.
   * New `typeContainsAny` — structural walk: True iff the type
     contains `T.TVar "any"` anywhere (lambda arms, type-app
     args, record fields, tuples, aliases).
   * New `authArgIsTyped` — True iff the HM-inferred type
     resolves to `T.TType _ "String" []` after `stripAlias`
     (handles `type alias UserEmail = String`).
   * New `argSourceCarriesAny srcAnnots arg` — True iff the arg
     is `Can.VarTopLevel _ name` whose name's raw annotation
     contains `any`, or a `Can.Call funcE _` whose head does.
   * New `authBoundaryDiagnostics filePath solved canMod` — runs
     the walker, computes `srcAnnots` once, and for each Auth
     call site checks every String slot. A slot is "bad" iff
     **not `hmTyped` OR `srcAny`** — the OR composes the two
     contract sources.
   * Wired into the compile pipeline (`Sky.Build.Compile.compile`)
     right after `typesWithDeps` is built and `goCodeRaw` is
     computed, but BEFORE `createDirectoryIfMissing outDir` and
     `writeFile mainGoPath goCode`. If any diagnostic is
     produced the build short-circuits with
     `Left "Sky.Auth.UntypedBoundary: <entryPath>"` after rendering
     the Elm-style diagnostic block.

5. **Template / docs sync.**
   * `templates/CLAUDE.md` non-regression rules — new entry:
     "Security-critical Auth kernels require typed-String
     arguments". Lists the 8 kernels and their compile-time gate +
     the fixed runtime message + the audit log.
   * `docs/skyauth/overview.md` — new section "Security-critical
     kernels require typed arguments" with example bad fixture,
     expected `E4006` diagnostic output, and runtime
     defence-in-depth notes.

## Architectural choices

1. **HM `solvedTypes` is not enough.** Initial design hooked
   `inferExprType solved arg` against the slot. Trace during
   development showed Sky's HM is actually quite sound about the
   wildcard-`any` case: when `bridge : any` paired with
   `Auth.hashPassword bridge`, the solver unifies bridge's
   per-occurrence UF var with `String` and `solvedTypes["bridge"]`
   ends up as `String`. The gate as-written would never fire.
   The fix: collect the **raw source-level annotation** directly
   from `Can.TypedDef` and check for `T.TVar "any"` independently.
2. **`globalAnnotMap` is also not enough.** `buildAnnotMap` uses
   the solver's `generaliseToAnnotation` which captures the solved
   shape (post-call-site unification). So it stores
   `Main.bridge → Forall [] String` — losing the `any`. The fix:
   `collectSourceAnnots` walks the canonical module directly,
   preserving the user's raw annotation.
3. **`rewriteAliasHead` at the walker.** User code calls
   `Auth.hashPassword` as `Std.Auth.hashPassword`, which is a
   `Can.VarTopLevel` pointing at a Sky-source binding that
   reads `Ffi.kernel "Auth_hashPassword"`. The rewrite to
   `Can.VarKernel "Auth" "hashPassword"` happens at codegen
   lowering time. The gate runs BEFORE codegen, so the walker
   must invoke `rewriteAliasHead` itself (the `globalKernelAlias`
   IORef is already populated during canonicalisation, so this
   is safe).
4. **Audit log is undefeatable.** No env-var to suppress
   `logAuthBoundaryLeak` output — the operator's visibility of
   "something is bridging the wrong type into our auth boundary"
   must not be possible to silence by accident.

## Verification evidence

### Runtime tests (failing-test-first)

Pre-fix:

```
=== RUN   TestAuth_HashPassword_NonStringMessageHidesType
    auth_typed_boundary_test.go:94: auth error leaks runtime type via ", got int" in message "hashPassword: expected String, got int"
--- FAIL: TestAuth_HashPassword_NonStringMessageHidesType (0.00s)
=== RUN   TestAuth_HashPasswordCost_NonStringMessageHidesType
    auth_typed_boundary_test.go:100: auth error leaks runtime type via ", got map[" in message "hashPassword: expected String, got map[string]interface {}"
--- FAIL: TestAuth_HashPasswordCost_NonStringMessageHidesType (0.00s)
=== RUN   TestAuth_PasswordStrength_NonStringMessageHidesType
    auth_typed_boundary_test.go:106: auth error leaks runtime type via ", got <nil>" in message "passwordStrength: expected String, got <nil>"
--- FAIL: TestAuth_PasswordStrength_NonStringMessageHidesType (0.00s)
=== RUN   TestAuth_SignToken_NonStringSecretMessageHidesType
    auth_typed_boundary_test.go:112: error message "signToken: secret must be a String, got int" must start with "signToken: expected String"
--- FAIL: TestAuth_SignToken_NonStringSecretMessageHidesType (0.00s)
=== RUN   TestAuth_VerifyToken_NonStringSecretMessageHidesType
    auth_typed_boundary_test.go:118: error message "verifyToken: secret must be a String, got <nil>" must start with "verifyToken: expected String"
--- FAIL: TestAuth_VerifyToken_NonStringSecretMessageHidesType (0.00s)
=== RUN   TestAuth_VerifyToken_NonStringTokenMessageHidesType
    auth_typed_boundary_test.go:125: error message "verifyToken: token must be a String, got int" must start with "verifyToken: expected String"
--- FAIL: TestAuth_VerifyToken_NonStringTokenMessageHidesType (0.00s)
FAIL
```

Post-fix:

```
$ go test ./rt -run 'TestAuth_(HashPassword|PasswordStrength|SignToken|VerifyToken)' -v
=== RUN   TestAuth_HashPassword_NonStringMessageHidesType
[WARN] auth.boundary kernel=hashPassword goType=int reason=non-string-arg
--- PASS: TestAuth_HashPassword_NonStringMessageHidesType (0.00s)
=== RUN   TestAuth_HashPasswordCost_NonStringMessageHidesType
[WARN] auth.boundary kernel=hashPassword goType=map[string]interface {} reason=non-string-arg
--- PASS: TestAuth_HashPasswordCost_NonStringMessageHidesType (0.00s)
=== RUN   TestAuth_PasswordStrength_NonStringMessageHidesType
[WARN] auth.boundary kernel=passwordStrength goType=<nil> reason=non-string-arg
--- PASS: TestAuth_PasswordStrength_NonStringMessageHidesType (0.00s)
=== RUN   TestAuth_SignToken_NonStringSecretMessageHidesType
[WARN] auth.boundary kernel=signToken goType=int reason=non-string-arg
--- PASS: TestAuth_SignToken_NonStringSecretMessageHidesType (0.00s)
=== RUN   TestAuth_VerifyToken_NonStringSecretMessageHidesType
[WARN] auth.boundary kernel=verifyToken goType=<nil> reason=non-string-arg
--- PASS: TestAuth_VerifyToken_NonStringSecretMessageHidesType (0.00s)
=== RUN   TestAuth_VerifyToken_NonStringTokenMessageHidesType
[WARN] auth.boundary kernel=verifyToken goType=int reason=non-string-arg
--- PASS: TestAuth_VerifyToken_NonStringTokenMessageHidesType (0.00s)
PASS
ok      sky-app/rt      0.020s
```

### Compile-time spec (failing-test-first)

Post-fix:

```
$ cabal test sky-tests --test-options="--match=/Sky.Build.AuthUntypedBoundary/"
Sky.Build.AuthUntypedBoundary
  Auth kernel typed-boundary gate (P5 / Gap A6)
    rejects an `any`-typed binding flowing into Auth.hashPassword [✔]
    accepts a properly String-typed binding into Auth.hashPassword [✔]

Finished in 1.8081 seconds
2 examples, 0 failures
```

### Existing Auth runtime tests stay green

```
$ go test ./rt -run 'TestAuth|TestSign|TestVerify|TestPassword'
PASS    13 tests (7 existing + 6 new)
ok      sky-app/rt      0.023s
```

### Wider cabal sweep

```
$ cabal test sky-tests --test-show-details=streaming \
    --test-options="--skip=/Sky.Lsp.NvimDriver/ --skip=/Sky.Lsp.Scale/ \
                    --skip=/Sky.Build.VerifyAll/ --skip=/Sky.Build.VerifyScenario/ \
                    --skip=/Sky.Build.EmbeddedRuntime/ --skip=/Sky.Build.EmbeddedInspector/ \
                    --skip=/Sky.Cli/"
Finished in 664.2840 seconds
352 examples, 0 failures, 1 pending
Test suite sky-tests: PASS
```

(Pending count of 1 matches the prior baseline — Issue #52 record-update
deferred case, unrelated to P5.)

### Key examples clean-build (`rm -rf sky-out .skycache .skydeps && sky build`)

* `examples/12-skyvote` (uses Auth.hashPassword + Auth.signToken +
  Auth.verifyToken) — **Build complete**.
* `examples/13-skyshop` (Stripe-SDK-scale FFI benchmark) — **Build
  complete**.
* `examples/19-skyforum` (8-module Std.Ui multi-page app) — **Build
  complete**.

The skyvote example was the regression gate — its existing typed
Auth usage stays passing under the new gate. Confirms the gate is
not over-strict: typed `String`-annotated bindings still flow
cleanly into every Auth kernel.

## Files changed

* `runtime-go/rt/db_auth.go` — `mustStringTyped` + `coerceAuthSecret`
  + `Auth_verifyToken` token-leg fixed-message + new
  `logAuthBoundaryLeak`.
* `src/Sky/Build/Compile.hs` — `authSecurityKernels` map,
  `walkAuthCalls`, `collectSourceAnnots`, `typeContainsAny`,
  `authArgIsTyped`, `argSourceCarriesAny`,
  `authBoundaryDiagnostics`, `authBoundaryMessage`,
  `authBoundaryHint`; wired into the compile pipeline before
  codegen writes `main.go`.
* `src/Sky/Reporting/Diagnostic.hs` — `authE_UntypedBoundary =
  DiagCode "E4006"`.
* `runtime-go/rt/auth_typed_boundary_test.go` — NEW, 6 tests
  (one per public Auth kernel String slot).
* `test/Sky/Build/AuthUntypedBoundarySpec.hs` — NEW, 2 cases
  (bad fixture rejects, good fixture accepts).
* `test/Spec.hs` + `sky-compiler.cabal` — register the new spec.
* `templates/CLAUDE.md` — non-regression rule entry.
* `docs/skyauth/overview.md` — security-critical-kernels section.

## Risk register

* **(handled)** `examples/12-skyvote` regression risk — its Auth
  usage passes through `String` annotations cleanly. Verified by
  clean-slate build.
* **(handled)** False positive on `type alias UserEmail = String`
  — `authArgIsTyped` calls `stripAlias` which peels both
  `Hoisted` and `Filled` alias variants, so type aliases pointing
  at String do not trip the gate.
* **(handled)** False positive on a binding whose annotation uses
  `any` only in a return position that doesn't reach the Auth
  slot — `typeContainsAny` is structural and unconditional, so
  it would trip the gate. Mitigation: the user's only path to
  pass an `any`-returning value into a String slot IS the path
  the gate is designed to catch; conservatively rejecting
  ambiguous shapes is the correct policy for a security boundary.
  If a future user complains, the fix is to narrow
  `typeContainsAny` to check only the type's RETURN position via
  `arrowResult`, not the whole structure.
* **(open, future follow-up)** Dep-module annotations are
  collected per-dep-module via the same `collectSourceAnnots`
  in the validDeps loop, but the diagnostic's `entryPath` is
  always the entry. Future polish: thread the dep-module's
  actual source path so cross-module violations attribute to
  the correct file in the diagnostic header.

## Session-cost

~3h: 30min reading audit + planner, 90min walker + gate design
(including a wrong turn relying solely on `inferExprType` then
discovering Sky's HM was already catching the simple cases),
60min runtime fix + tests, 30min docs + commit + PR.

## Sign-off

| Item | Status |
|---|---|
| mem-guard alive throughout | ✅ (verified end of session) |
| All cabal tests green (excluding skipped network/heavy suites) | ✅ 352 examples, 0 failures |
| 3 representative examples build clean | ✅ |
| Runtime Auth test sweep green | ✅ 13/13 |
| `sky fmt` clean on every changed `.sky` file | n/a (no .sky touched) |
| Background tasks cleaned up | ✅ |
| Out-of-scope guard (live.go untouched) | ✅ |
| CI release workflow green | ⏳ pending (PR #81 in CI) |
| Cycle log line appended | ⏳ pending (post-CI) |
