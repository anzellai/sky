# Forum re-baseline — the interaction cost on an application, at HEAD

Every millisecond in the Sky.Live performance programme so far came from
`examples/26-ui-showcase`. That app's home view is 99.3% model-independent
construction, its model is `{count : Int}`, and it contains no `List.map`, no
`case` and no `if`. It is a poster, not an application.

This run re-baselines on `examples/19-skyforum`, where **90.6% of the home
page's nodes sit behind one first-class function value** —
`List.indexedMap (postRow maybeSession) posts`, `src/View/Posts.sky:17` — and
measures the same quantities the same way so the two are comparable.

**This is a fresh baseline of HEAD, not a before/after.** `50c8dcee` already
carries eta-expansion (`hof-dispatch-20260815`, 1.36×) and typed list
accessors (`typed-destructure-20260815`, 1.34×), 1.80× cumulative on showcase.
Nothing here is a comparison against a pre-change build.

## The defect this run exists to correct

`../attribution-20260815/viewsize/forum-r{1,2,3}` are the only forum data in
the corpus, and all three carry:

```json
  "outcomes": { "ok_no_patches": 5394 },
  "valid": false,
  "invalid_reason": "no interaction produced a single patch: the server never
                     ran the diff path, so this measures an empty exchange"
```

They are the "94-element" arm of the published claim that **"4.1× the elements
costs 2.4× the interaction — +139%"**
(`../../skylive-interaction-cost.md:217-222`), and the only other point in it
is showcase's 384. That claim compares a run which produced patches against
three which produced none.

**Root cause, found and reproduced.** `skyliveload` chose its handler as *the
first `data-sky-hid` on the page ending in `.click`*. On skyforum that is the
site title in the top nav, `r.1#div.0#div.0#div.0#div.click`, wired to
`Navigate HomePage`. On the home page that Msg yields a model identical to the
one it started from — so an identical view, so **zero patches, every time**.
5,394 interactions of a user clicking a logo that does nothing.

Both halves are now closed:

* **The generator** picks its handler deliberately (`-hid-context`), can
  script the session state an interaction needs (`-setup`), counts patches,
  checks each patch names a `sky-id` the client is actually holding, and
  fails the run below `-min-patch-rate` (default 0.9) rather than only on zero.
  Its `-self-check` now does four interactions, not two, and requires a patch
  on all four.
* **The harness** propagates the generator's exit status. `perfrun.sh:150`
  read `wait "$GEN_PID" || true`; skyliveload had exited 2 and said why, and
  `|| true` discarded it. `forumrun.sh` stamps `REJECTED` in the output
  directory instead, and runs the self-check as a **precondition**, before the
  measurement window opens.

The falsifier is recorded in `harness/`: pointing the fixed generator at the
old handler reproduces `patches 0 total, 0.00 per interaction, 0.0% of
interactions bore one` and exits non-zero.

## Conditions

| | |
|---|---|
| Host | Apple M1, 8 cores, 16 GB, macOS 26.5.2 — **arm64** |
| Commit | `50c8dcee` on `feat/embedded-postgres` (worktree branch `perf/forum-rebaseline`) |
| Go | 1.26.1 |
| App | `forumbench` — `examples/19-skyforum` plus one `init`-only view-size lever (`harness/forumbench-Main.sky.patch`, 85 lines) |
| Interaction | signed-in upvote toggle: `UpvotePost` → `List.map (togglePostUpvote …)` over every post → full `view` re-render → **2 patches, every press** |
| Session store | `memory` (CPU runs) · **`postgres` 14** (memory runs) |
| Load average | 1.87–3.31 on 8 cores — the machine was shared |
| Generator | `tools/skyliveload`, same host, loopback, 0.06–5.5% of the machine |
| Repeats | **3 everywhere**, ranges reported |

### The view-size lever

`FORUM_POSTS` is read once in `init` by `System.getenvOr`. Nothing on the
interaction path reads it: `view`, `update` and every `View/*` module are
byte-identical to the stock example. **One binary serves every view size**, so
a fitted intercept is not confounded with a recompilation.

`FORUM_POSTS=5` is the stock seed and reproduces `examples/19-skyforum`
exactly — 94 `sky-id` elements, 135 open tags, verified against the stock
build. Element counts are 16/post and are counted **from the HTML the app
served during the run** (`viewsize.txt`), never from an expectation:

| `FORUM_POSTS` | 1 | 3 | 5 | 12 | 23 | 60 | 100 |
|---|---|---|---|---|---|---|---|
| `sky-id` elements | 30 | 62 | **94** | 206 | 382 | 974 | 1614 |

Sizes below the stock seed exist so the fixed-term regression **interpolates**
its intercept rather than extrapolating it. The published showcase fixed term
was extrapolated from two points at 94 and 384 elements, 94 elements outside
the data, where the mild superlinearity documented in `ANALYSIS.md` shows up
as a large change in the constant.

## Layout

```
harness/          the scripts; all of them, and the one-file app delta
  forumrun.sh       run driver — perfrun.sh plus the exit-status check and
                    the patch-production precondition
  sweep-cpu.sh      view-size matrix, repeats INTERLEAVED across sizes
  sweep-mem.sh      RSS under sustained load on the postgres store
  bucket.sh         disjoint self-time decomposition (9 buckets, sum = 100%)
  attrib.sh         cumulative CPU + allocation attribution of one run
  summarise.sh      one TSV row per run
  fit.sh            OLS of cost on element count, with the intercept's se
  pg-up.sh          the throwaway PostgreSQL cluster
  forum-setup.json  the two-step sign-in the vote handler requires
cpu-g1/  cpu-g8/   view-size matrix, 7 sizes x 3 repeats, GOMAXPROCS 1 and 8
noprof-g1/         the unprofiled control, for the profiler-overhead figure
showcase-g1/ -g8/  26-ui-showcase at the SAME commit, same harness
mem-pg/            n = 100/300/500 under sustained load, postgres store
mem-pgevict/       the same with SKY_LIVE_IDLE_EVICT=15s so eviction fires
g1.tsv g8.tsv      the summarised matrices
buckets-g1.tsv     per-run self-time buckets
```

## Method notes that matter

* **CPU per interaction** is a `ps(1)` process CPU-time delta over the
  steady-state profile window, divided by the interactions inside it —
  independent of pprof, so it doubles as the profiler-overhead check. This is
  `ms_win`. `ms_run` divides whole-run CPU (ramp and startup included) by
  window interactions and reads ~10% high; it is quoted only because the
  archived `scaling.tsv` is computed that way.
* **Every run asserts patch production**, before and during: a four-press
  self-check as a precondition, then `patch_rate` over the measurement window.
  Every run archived here reads `"patch_rate": 1` and
  `"patches_naming_absent_ids": 0`.
* **The CPU self-time attribution is unreliable on this host at high
  interaction rates**, and the three repeats are what show it. See
  `ANALYSIS.md` §4. The totals are repeatable to 2%; the allocation
  attribution to 0.2%; the *self-time split between the syscall and GC buckets
  at small view sizes* is not repeatable at all. Nothing in the analysis rests
  on a single-run split.
* **Allocation profiles bracket the CPU window** (`allocs-pre`/`allocs-post`,
  diffed with pprof `-base`), so a call site's time share and its allocation
  share cover the same interactions. `MemProfileRate` is Go's default; at
  ~2×10⁸ sampled objects per window there is no shortage of samples.
  Committed for the first repeat of each size only — the repeats agree to 0.2%.
* **`page.html` is not committed** (9.8 MB across the matrix). `viewsize.txt`
  records the counts taken from it and `forumrun.sh` regenerates it.

## Known limits

1. **arm64, one host, one commit.** Ratios should travel; absolute
   milliseconds should not (`../../skylive-remote-validation.md` found x86
   differs by ~30% on the memory figure).
2. **One interaction shape.** An upvote toggle re-runs `update` over the whole
   post list and re-renders the whole page. An app whose cost is in `update`
   rather than `view` would profile differently; the method is the
   transferable part.
3. **The CPU runs use the `memory` session store**, so the gob/encode path is
   absent from those profiles by construction. The memory runs use postgres
   and carry it.
4. **`ms_run` is an upper bound by ~10%** — whole-run CPU over
   window-only interactions.
5. **No network term.** Loopback only.
