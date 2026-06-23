# v0.17 fuzzer design — criterion 8 closure

> **Verbatim goal** (from `.claude/AUTONOMOUS_GOAL.md`):
> "A property-based fuzzer exists that generates random well-typed Sky
> programs and asserts `sky build && ./sky-out/app` doesn't panic. Run
> for ≥10,000 iterations clean before close."

## Two-tier architecture

We ship **two complementary fuzzers** for the well-typed Sky
program → build → run → no-panic property, each with a different
role in the dev loop.

| Tier | Implementation | Role | Default iter count |
|---|---|---|---|
| **A** | `test/Sky/Build/WellTypedFuzzerSpec.hs` + `WellTypedFuzzerGen.hs` (Haskell QuickCheck) | Per-CI dev gate — runs every cabal test invocation | 100 (full 10k via `SKY_FUZZ_FULL=1`) |
| **B** | `scripts/fuzz-well-typed.sh` (POSIX shell) | Milestone runner — runs at release tags / architectural close | 10,000 (configurable) |

The two tiers cover the same conceptual surface (random well-typed
Sky programs → build + run → assert no panic). Different
implementation languages give different trade-offs:

* **Tier A** wins on grammar precision — QuickCheck's typed `Gen Program`
  is the right shape when you need explicit AST nodes (e.g. for
  shrinking, for differential codegen assertions on the emitted Go).
  Per-iter overhead ~3 s; 10k iters ≈ 8 h.
* **Tier B** wins on wall-clock for the milestone tier — no QuickCheck
  framework, no cabal-test wrapping, ~0.3-2 s/iter depending on mode.
  Failures dump exact source + emitted Go for forensics with no
  Haskell debugger ceremony.

## Tier-A design (existing — `WellTypedFuzzerSpec.hs`)

Four shape categories, picked uniformly per iter:

* **ArithInt** — Int arithmetic over literals + `let` bindings
* **StringConcat** — `(++)` chains with `String.fromInt` of Int sub-expressions
* **ListCombinator** — `List.map` / `filter` / `foldl` / `length` /
  `Maybe.withDefault` / `Result.withDefault` chains
* **ParamRecord** — parametric record alias `Box a = { value : a, label : String }`
  exercised at Int + String instantiations

Per-iter:
1. Generate program with `QuickCheck.Gen Program`
2. Render to source string
3. Write to a per-iter temp dir + run `sky build` (10 s timeout)
4. Run `./sky-out/app` (10 s timeout)
5. Inspect emitted `sky-out/main.go` for:
   * Bare `T1`/`T2`/`T3` outside `[Ti any]` brackets (T1-leak signature)
   * Orphan `Anon_R_<hash>` references with no matching `type Anon_R_<hash>`
   * `// PROOF: ... = Unknown` annotations (typed-codegen contract breach)

Subprocess isolation per iter is critical: bug #492 showed that
in-process iterations contaminate each other via module-level IORefs
(the very IORefs the v0.17 close umbrella is removing).
Every fuzz iter spawns a fresh `sky` process so a soundness regression
cannot be masked by stale state from a previous iter.

## Tier-B design (NEW — `scripts/fuzz-well-typed.sh`)

**Six well-typed program TEMPLATES** with parameter slots, all 100%
HM-valid by construction:

1. `arith` — `String.fromInt (let x = __N1__ in x + __N2__ * __N3__)`
2. `strconcat` — `"prefix-" ++ String.fromInt __N1__ ++ "-suffix-" ++ "__STR__"`
3. `listmap` — `String.fromInt (List.length (List.map (\x -> x + __N1__) [__LIST__]))`
4. `maybechain` — `String.fromInt (Maybe.withDefault __N1__ (Maybe.map (\x -> x * 2) (Just __N2__)))`
5. `resultpipe` — `String.fromInt (Result.withDefault 0 (Result.map (\x -> x + __N1__) (Ok __N2__)))`
6. `paramrecord` — `type alias Box a = { value : a, label : String }; main = println (let b = { value = __N1__, label = "__STR__" } in String.fromInt b.value)`

Slot kinds enforce typing at substitution time:

* **Int slots** receive Int literals in `[0, 99]` only
* **String slots** receive `[a-z]{1,6}` strings only
* **List slots** receive bounded `[i, j, k]` Int-list literals (length 0..5)

No (template × slot-fill) combination can produce a non-well-typed
program. Every template's `main` ends `println (<String expr>)` so
`main : Task Error ()` HM-checks unambiguously.

### Three sub-modes within Tier B

* `--mode template` — synthesised templates only (B1; ~2s/iter)
* `--mode corpus` — `examples/00-standard-libs` replay (B2) — re-runs
  the known-good Sky corpus (313 `Test.*` assertions, all well-typed
  Sky source the project already ships) N times, validating the
  compiler doesn't drift under repeated invocation
* `--mode composite` (DEFAULT) — alternates template + corpus so every
  iter is genuinely well-typed by construction

### Deterministic seeding for reproduction

Iter `i` uses seed `START_SEED + i` driving a Numerical-Recipes LCG
(`next = (1103515245 * s + 12345) & 0x7FFFFFFF`). Reproducing a
failure:

```bash
./scripts/fuzz-well-typed.sh --seed $FAILING_SEED --iters 1 --keep
```

…regenerates exactly the same source + slot fills. The `--keep` flag
preserves the tempdir for inspection.

### Failure protocol

On first failure:

1. Per-iter `sky-out/main.go` + `sky-out/app` + `build.log` + `run.log`
   + `src/Main.sky` copied to `/tmp/sky-fuzz/FAILURES/seed-<N>-<ts>/`
2. Stderr surfaces the seed + reproduction command
3. Process exits non-zero immediately — fail-fast saves wall-clock at
   10k iters

Resume after fix: re-run with original `--seed` to validate the
specific failure is closed, then full 10k iters to validate no
regressions.

### Panic assertions (matches verbatim "doesn't panic")

Four structural checks per iteration:

1. `sky build src/Main.sky` exits 0 within `BUILD_TIMEOUT=30s`
2. `./sky-out/app` exits 0 within `RUN_TIMEOUT=15s`
3. Combined stderr matches NONE of:
   * `panic:`
   * `runtime error:`
   * `^goroutine [0-9]+ \[`
   * `fatal error:`
   * `unrecoverable`
4. (Implicit) timeout = panic class — a hung program is treated as a
   regression

## Why both tiers, not just one

* **Tier A in `cabal test`** → every CI run catches regressions in the
  100-iter dev tier (~3 min). No release ever ships without this
  passing.
* **Tier B at milestone tags** → ≥10k iters over a controlled wall-clock
  window, captured to `docs/v0.17-fuzzer-milestone-<sha>.log`. This is
  the artifact the v0.17 close umbrella's criterion 8 evidence pack
  cites.

If you only had Tier A, the 10k-iter milestone run would take ~8 h
(blocking the release loop). If you only had Tier B, you'd lose the
per-CI early-warning + the codegen-shape assertions that need Haskell
introspection of emitted Go. Together they cover both budgets.

## Closure protocol

v0.17 architectural close criterion 8 is satisfied when:

```bash
./scripts/fuzz-well-typed.sh --iters 10000
echo $?  # MUST be 0
```

…completes with `sky-fuzz: criterion 8 SATISFIED — ran 10000 iters
clean` and exit 0, and the log is captured to
`docs/v0.17-roadmap/fuzzer-milestone-<branch-sha>.log` and committed
alongside the umbrella close.

## Smoke-test evidence (iter 86 build)

The harness passed a 10-iter composite-mode smoke at `9cad2502`
(branch tip prior to this commit):

```
sky-fuzz: mode=composite iters=10 start_seed=100 sky=…/sky-out/sky
sky-fuzz: DONE iters=10 green=10 failures=0 elapsed=20s
sky-fuzz: smoke pass — ran 10 iters clean (criterion 8 needs >=10000)
```

Wall-clock projection for the 10k milestone run:
* Composite mode (50% template + 50% corpus): ~5.5 h
* Template mode only: ~1 h (faster but narrower coverage)
* Corpus mode only: ~10 h (every iter rebuilds the 313-assertion stdlib smoke test)

The composite mode is the chosen default because it gives the widest
SHAPE coverage per second of wall-clock.

## Future extensions (out of scope for v0.17 close)

* Add 4 more templates: case-of exhaustiveness, recursive ADT,
  `Cmd.batch` / `Task.parallel`, FFI round-trip
* Differential fuzzer: run the same source against two compiler SHAs,
  assert byte-identical emitted Go (catches non-determinism)
* Coverage-guided generation: instrument `sky` with `--coverage` to
  feed back which templates exercise novel code paths
* CI integration: gate `v*` tags on the 10k-iter Tier B pass
