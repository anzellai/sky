# Session checkpoint — 2026-08-16, `feat/embedded-postgres`

Branch tip `362f27df`, **207 commits ahead of `main`**, tree clean, **nothing pushed**.

## Performance programme — measured, merged

94-element `19-skyforum`, one M1 core, postgres store: **135 → ~1,170 interactions/sec (~8.7×)**.

| stage | change | measured |
|---|---|---|
| compiler | eta-expand func→func; typed list accessors | 1.80× |
| Stage 1 | runtime constant factors | −54% objects, −19.4% wall |
| Stage 2 | call site picks the typed list helper | 1.55× / 1.71× |
| Stage 3 | provable `foldl`/`any` take typed twins | 1.19× / 1.16× |
| Stage 4 | provable `++` and unary list kernels | 1.12× / 1.16× |

Also merged: **core scaling is real** (79–80% per *physical* core doubling; the flat
curve was SMT), **memory does not bind** (625–685 kB/session), a **security fix**
(client wire strings could select constructors from an unasked ADT), **21 false doc
claims** corrected, and the runtime-narrowing taxonomy **re-derived from the Rust
compiler** (the old one described the retired Haskell pipeline and had produced the
same wrong conclusion three times).

## Capacity, as measured and projected

- e2-small, postgres, n=300: **64.3/s measured** (pre-Stage 3) → **~90/s** projected.
- e2-medium: **261.5/s measured** → **~364/s** projected.
- **n2-standard-8 (4 PHYSICAL cores): ~1,150/s projected** — meets the 300–500
  sessions @ 1,000+/s target. **Unconfirmed**; one direct run would settle it.
- Sizing must count **physical cores** — a GCE vCPU is an SMT thread.
- `GOGC=400` + `GOMEMLIMIT≈750MiB`: **+19% at 759 MB**. Bare `GOGC=800` is a trap
  (1,827 MB, 68% run-to-run spread, would OOM; multiplies the per-session slope 4.4×).

## In flight when the session ended

| agent | state |
|---|---|
| Framework comparison (Django+HTMX, Next.js) | 3 commits; found **3 fairness defects, 2 penalising Sky** — Sky was the only arm not running as it ships (dev console mounted in-process); Next's CSRF check silently not running; Next's query a different shape. **Numbers not yet reported.** |
| GC default + startup banner | `gc_tuning.go`, `startup_report.go` in progress. Ships `GOMEMLIMIT` derived from `detectRAMBytes()`, and a dev banner naming the production checklist. |
| Corpus emission fix | **PR BLOCKER.** `coerce-floor` is RED at base — a *win* (adapter −7) failing the exact-match ratchet by design. Cannot bless: **only 31 projects emit against ~61 golden rows.** |

## The blockers to "ready to merge"

1. **`coerce-floor` red at base** — see above. CI cannot go green until resolved.
2. **Nothing has been pushed.** 207 commits, every green is local, on a machine where
   a stale 4-day-old log, a refused redirect, a missing binary and a port collision
   each produced a confident green result today.
3. **Embedded PostgreSQL E2E never run.** No `postgres-bundle-v*` release exists and
   the workflow is absent from `main`, so its only trigger is a tag push with no
   re-run lever. Steps 0–1 pass (18.6 exists upstream, checksum matches the pin
   byte-for-byte, tag derives to `postgres-bundle-v18.6`). Steps 2–4 need no release.
4. **Grill round 8 not run.** Seven rounds, seven breached.
5. **Capacity figures in `README.md` / `AGENTS.md` / `docs/skydb/embedded-postgres.md`
   and the unpushed sky-lang.org blog post still quote falsified numbers.**

## Agreed next structural lever

**Model-diff-driven selective render** — diff the *model*, use a static map of which
view subtrees read which fields, re-render only dirty regions. Sound because `view` is
pure. Detail in `.claude/AUTONOMOUS_GOAL.md`; fold in the derived-value graph,
hybrid static/runtime dependency capture and keyed-collection ops from the user's
reference doc. **Do not let me sketch the mechanism** — two of my proposals were
revised by consults today and both replacements were better.

## Gates that reported success while checking less than they claimed (four, this session)

A missing `timeout` binary (exit 0 having run nothing) · `build-run --golden` without
`--all` (8 of 24, PASS) · two sharding gates comparing a constant to itself (`ok` in
0.023 s) · `coerce-floor` measuring ~half its golden. **Disbelieve a pass that is too
fast, too quiet, or too convenient.**

## Measurement traps, earned

- `prof_wall_s` is integer-second arithmetic — ±4% swing; Stages 2–3 used it. Use
  `prof_cpu_delta_s`.
- CPU self-time on this host redistributes **±37%** between identical runs. Allocation
  profiles reproduce to 0.2%. Ground claims in allocation.
- `coerce-floor` counts **sites, not executions** — it said −5.3% where the measured
  allocation effect was −23%.
- A memory-store measurement does **not** transfer to postgres (twice: the +33% GC win
  became +24%, and the session-store cost assessment inverted).
