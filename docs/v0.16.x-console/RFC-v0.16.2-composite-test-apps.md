# RFC — v0.16.2 composite test apps (cabal sweep at scale)

> Status: design (2026-06-04). Surfaced by 3 hours of v0.16.1 tag-gate
> cabal investigation: cold-cache cabal test on Sky.Live generics
> instantiations × 200 fixtures balloons GOCACHE to 100+ GB and times
> out at 60-min budgets on developer laptops. CI works only because it
> skips Sky.Build.VerifyAll + has warm cabal-store cache from prior runs.

## The diagnosis

Each Sky→Go test fixture instantiates Sky.Live's generics
(`Cfg_R[T1 any]`, `SkyTask[Error, T]`, `Sub[T]`, `Cmd[T]`, …) with
**fixture-unique concrete types**. Go caches these per-instantiation,
content-addressed. With ~200 fixtures × ~10 generic types per fixture,
the cache has 2000+ unique compilation units to track. Each unit is
1–10 MB. Total: 5–20 GB of useless cache (each fixture's instantiation
is only used once).

Compounded by upstream https://github.com/golang/go/issues/76337 — go's
generic monomorphisation produces oversized cache entries.

## The proposed inversion

Instead of N small fixtures, ship a handful of composite apps that
exercise broad surfaces. Each composite produces ONE generic
instantiation set, cached once.

### Composite app inventory

| App | Surface | Replaces |
|---|---|---|
| `composite/01-sky-generics-app` | Lists / Dict / Maybe / Result / Json / Encoding / Decimal / Math / String / Regex / Crypto | 30+ Sky.Build/Type specs |
| `composite/02-sky-server-app` | Sky.Http.Server routes / middleware / static / streaming + Auth + Db + Pub/Sub + RateLimit + Cache | 20+ specs |
| `composite/03-sky-live-shop-app` | Sky.Live + Std.Ui + sessions (memory + sqlite) + chart primitives + console hooks (Stripe integration, no firebase) | 25+ specs |
| `composite/04-sky-ui-multibackend-app` | Std.Ui + Sky.Tui + Sky.Webview sharing the same `view` | 15+ specs |

Each composite is a real Sky app under `examples/` that the cabal test
suite builds + runs assertions against.

### Alternative: master app via MountLiveSubAppInProcess

A SINGLE master Sky.Live app at `examples/35-cabal-master/` mounting
every non-CLI example as a sub-app via the v0.16.1 PR10
`MountLiveSubAppInProcess` primitive. One `sky build` exercises every
example's compilation path in a single go-build invocation = single
GOCACHE pressure cycle. Bonus: dogfoods PR10's architecture as the
test infrastructure.

## Cost estimate

- 4 composite apps × ~500 LOC each = ~2000 LOC of Sky
- Hspec specs that build + assert against composites: ~30 specs
- Delete ~150 small fixture specs that the composites cover
- Net: ~1500 LOC reduction + cabal sweep that completes in <10 min

## Non-goals

- Replacing user-facing examples — `examples/01-*` through `examples/34-*`
  stay as user-targeted demos.
- Replacing bug-repro specs — `examples/*/sky.toml`-based regressions
  for specific issues stay as separate fixtures, since their precision
  matters.
- Solving Go #76337 itself — that's upstream.

## Open questions

1. Should the composites live under `examples/` (numbered) or
   `tests/composites/` (separate)? Probably `examples/` so they get
   the example sweep + Playwright coverage for free.
2. Stripe vs Square vs Stub for the "external API" in
   `sky-live-shop-app`? Stripe has the most realistic API; stub avoids
   the Stripe SDK introspection cost (~15 min cold).
3. Do master-app + composite-apps coexist, or pick one? Suggest
   composites first; master-app as v0.17 once stable.

## Verification methodology — measured, not asserted

The v0.16.1 cabal investigation proved that "looks fine in isolation"
doesn't catch resource regressions at scale. v0.16.2 work MUST land
with reproducible measurements, not "trust me it's better." Required
local experiments before any PR merges:

### Budget invariants (hard thresholds)

| Resource | Target | Failure mode if breached |
|---|---|---|
| Wall time, cold cache, full sweep | < 10 min on a 2024-era laptop | Composite design is too granular or wrong split |
| Peak GOCACHE size during sweep | < 5 GB | Generic instantiation surface still too wide |
| Peak RAM (resident) during sweep | < 4 GB | Go toolchain memory misuse |
| Disk-free floor during sweep | > 50 GB | Sweep is racing user's other work |
| Cabal-test exit code on green run | 0, with `examples,` + `Finished` lines present | Sweep died without proper Hspec teardown |

The numbers come from where v0.16.1 broke:
- 75-min runs that never finished → 10-min ceiling is the inversion goal
- 100+ GB cache balloon → 5-GB ceiling is what an honest design should hit
- 65 GB free dropping to single-digit GB → 50-GB floor catches it before disk fills

### Required experiments (committed to repo, not run-once)

Land each as a script + a recorded run-log under
`docs/v0.16.x-console/composite-test-experiments/`:

1. **`cold-cache-baseline.sh`** — wipe `~/Library/Caches/go-build` +
   `~/.cabal/store`, run the new sweep, record wall time + peak
   GOCACHE + peak RSS. This is the budget-invariant gate above.
2. **`warm-cache-baseline.sh`** — second run immediately after; expect
   < 2 min wall time (Go's content-addressed cache should mostly hit).
   Catches regressions in cache-hit ratio when sweep is re-run.
3. **`disk-pressure-experiment.sh`** — fill the disk to 70% before
   sweep starts (via a `dd` placeholder file), run the sweep, assert
   it either completes under budget OR fails fast with a clear error
   (no "terminated" mystery state).
4. **`memory-pressure-experiment.sh`** — same shape but with a
   controlled memory load running alongside. mem-guard's existing
   thresholds (CLAUDE.md §1) should kick in before the sweep OOMs.
5. **`scale-projection.sh`** — synthesize N×100 fixture-shaped fake
   examples (just `module M\nmain = println "x"`), measure the
   sweep's wall time + cache growth as N rises. Establishes the slope
   so we can predict when the design breaks again.

Output of each experiment is a tagged Markdown table with date +
laptop spec + measurements. Stored alongside the script so the next
contributor can re-run and compare.

### Scalability invariants (what makes this maintainable)

These are the rules a future PR has to keep true:

1. **Composite app count cap: 8.** If you'd need to add a 9th, the
   surface is too narrow — fold into an existing composite or split
   along a different axis.
2. **Each composite is buildable + runnable in isolation.** Hspec
   spec contributes nothing if `sky build composites/01-…` doesn't
   work standalone. Composites are NORMAL Sky apps that happen to
   exercise broad surfaces.
3. **Generic-instantiation budget per composite: < 30 unique types.**
   Use `sky-ffi-inspect` (existing tool) or extend it to emit a
   per-composite instantiation count. If a composite exceeds 30,
   either it's growing too big or Sky.Live's API surface should be
   simplified.
4. **Composite-app churn rate < 2 changes/month.** Composites are
   stable scaffolds; if they change weekly, something is wrong with
   the abstraction.
5. **Bug-repro fixtures stay separate.** When a user reports a
   specific bug (#387, #422, …), the regression test stays as a
   small `examples/<N>-repro-<id>/` directory, NOT folded into a
   composite. Composite tests are about resource-shaped coverage;
   repro tests are about precision.

### Out of scope for v0.16.2 (filed as v0.16.3+ items)

- `sky verify --parallel` work-stealing — would speed up CI but
  doesn't address the developer-laptop scenario.
- Upstream PR to Go #76337 — out of our control.
- `cabal test --jobs=N` for Hspec parallelism — Hspec's parallelism
  causes the shared-fixture races CLAUDE.md §381 + #408 already
  documented; not safe to enable yet.

### Exit criteria for the v0.16.2 cycle

All of the following must be true on `main` before tagging v0.16.2:

- [ ] Cold-cache cabal sweep completes in < 10 min on a 2024 MacBook
      Pro (M2 or newer).
- [ ] Peak GOCACHE during sweep < 5 GB.
- [ ] Peak RSS during sweep < 4 GB.
- [ ] All 5 recorded experiments above land with run-logs.
- [ ] The current ~200 fixture-shaped specs that the 4 composites
      cover are deleted (not just unused — removed from the suite).
- [ ] CI workflow updated to drop `--skip=Sky.Build.VerifyAll` (since
      VerifyAll is now redundant against the composites).
- [ ] Composite app sources are reviewed by another contributor for
      coverage gaps (i.e. someone reads `01-sky-generics-app` and
      checks every stdlib effect tier is exercised).
- [ ] `docs/v0.16.x-console/composite-test-experiments/SCALE.md`
      documents the scale-projection data so v0.17 contributors can
      see when the design will break next.
