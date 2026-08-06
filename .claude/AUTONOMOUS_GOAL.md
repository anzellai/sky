# AUTONOMOUS GOAL — BlueDB clean-slate rebuild (feat/bluedb)

## Verbatim user mandate (2026-08-06)
> back to feat/bluedb, and continue in fully autonomous mode -- don't need to ask
> me questions, permissions, continuity. set look/schedule automatically without
> unnecessary long pauses.
> the task is simple, e2e implemented, tested + verified; each phase is grilled
> under our original goals.
> you may proceed now.

Earlier (the rebuild mandate, 2026-08-06):
> this is just a exp branch, start over again on a feat/bluedb branch -- reuse
> anything useful, and start off from main branch AGAIN. implement this time
> properly and grill all the way.

## What "done" means (the ONLY authority — do NOT scope down)
Build the BlueDB clean-slate data layer per the GRILLED design docs on this branch
— `docs/bluedb/clean-slate-architecture.md` (Phase 0) + `docs/bluedb/phase1-engine-design.md`
(Phase 1) — **e2e implemented, tested + verified, each phase grilled**, until the
ORIGINAL GOALS are achieved. Only an independent adversarial Judge (fresh context,
given this goal) may declare a phase or the whole done — I cannot self-declare.

### The original goals (the rebuild must deliver these; from the strategy discussion)
1. Session-bounded Model state sync.
2. Unified store: high-throughput lock-safe parallel + scalable + reliable + ACID
   (**real SERIALIZABLE via SSI/index-range validation** — user chose SSI) + secure,
   with UNIFIED APIs shareable across dbs (sqlite/postgres/bluedb).
3. Easy + simple; low-level APIs only for the 0.001%.
4. Notify clients of changesets (query/row-scoped, in the commit path).
5. Built-in Sky Console admin access to records.
Kills the 5 dev pains: juggling multiple stores · per-db quirks/non-portable code ·
data migration · session RAM overflow · too many configs / no unified query.

### The locked architecture (grilled; do NOT re-litigate the decisions)
Embed **Pebble** (empirically CGO=0 cross-compile-verified) + **MVCC-timestamp-in-key**
(comparer MIRRORs cockroachkvs, `Name="skydb.mvcc.v1"` IRREVERSIBLE, gated by
`base.CheckComparer`) + **single-writer committer** (HLC floor `max(persisted+1,wall)`,
metadata-in-batch) + **SSI transaction** (index-range read-set validation) +
**commit-path query-scoped reactivity** (changelog carries NewIndex/OldIndex) +
**opaque L1 changelog** + **watermark GC** (persisted monotone threshold T, physical
side-deletes) + **DX collapse** (one `[data]` config, one Persist API, one migration,
auto-admin) + **runtime-loud reactive-capability check** (compile-time backend gating
impossible by theorem). Reactivity embedded-first; SQL = storage + post-v1 NOTIFY bridge.

### Phased roadmap (architecture §7) — bottom-up, each design→grill→implement→verify→Judge
- **Phase 1 — engine substrate** (Pebble+MVCC+committer+changelog+GC+errorfs harness).
  Gate: `base.CheckComparer` green; versioned KV + snapshot reads; single-writer
  group-commit w/ HLC floor + metadata-in-batch; changelog (commitTs-indexed, opaque);
  watermark GC (threshold T, physical deletes); flock; the crash corpus green via
  Pebble `errorfs`; point r/w p99 ≤ old, group-commit ≥ ~51k/s, ordered range O(log n+k),
  no RAM ceiling.
- **Phase 2 — SSI transaction + validated commit** (index-range read-set, retry-bound +
  typed Conflict + hot-key pessimistic fallback). Gate: serializability conformance
  suite (write-skew/phantom rejected, lost-update prevented, read-your-writes) green.
- **Phase 3 — logical API + backend adapters** (Persist front + Cond/Query + embedded/
  sqlite/postgres adapters). Gate: SQL≡KV parity green.
- **Phase 4 — query-scoped reactivity in the commit path** (promote the delta engine).
  Gate: 2-browser live demo + query-scoped delete-re-run + realistic-N fan-out honesty.
- **Phase 5 — DX collapse** (one `[data]` config, `sky data migrate` incl. session blob
  version, auto-admin, session-store adapter, R1 async-persist funnel + durability tier).

## Loop protocol (INVIOLABLE — CLAUDE.md §0)
Each iteration: (1) re-read THIS file, quote the goal (drift gate); (2) cross-check the
planned step is IN the goal (not a narrowed scope); (3) design→grill(≥2 adversaries)→
implement (worktree, isolated)→three-leg verify (unit -race + integration + real use)→
fresh-context Judge. A phase closes ONLY on a Judge PASS with NO "but/except/however/
caveat/mostly/essentially/for the scope of/modulo". Commit at each verified sub-milestone;
push at phase boundaries. Stop ONLY on a genuine implementation blocker (surface it, keep
the loop alive). No long pauses — drive via agent-completion re-invocation + a long
fallback wakeup. Never declare done myself.

## Non-negotiables carried in
mem-guard running · timeout-bound every long run · CGO=0 cross-compile stays green ·
`-tags pebblegozstd` · CheckComparer is the irreversible gate · no runtime panic from
well-typed code · the crash corpus is day-one, not bolted on.
