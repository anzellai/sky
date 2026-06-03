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
