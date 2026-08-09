# AUTONOMOUS GOAL — BlueDB v2: architecture-first redefinition (feat/bluedb-v2)

Set 2026-08-09. Branch `feat/bluedb-v2`, off `origin/main` @ `fdbc398d`.
Supersedes the `feat/bluedb` mandate (that branch is now REFERENCE ONLY).

## Verbatim user mandate (2026-08-09)

> could we start a NEW feature bluedb branch to redefine the bluedb architecture
> correctly, with ALL learnings throughout to set out 100% correct pathways and
> reimplement everything in a way that is complete and sound?

And the diagnosis that shapes HOW (same session):

> your issue I realise is context, whenever you need to compact session or ask me
> to start fresh session, everything goes downhill

Carried from the original rebuild mandate (still the product goals, unchanged):

> the task is simple, e2e implemented, tested + verified; each phase is grilled
> under our original goals.

## The five original goals (unchanged — the definition of the product)

1. Session-bounded Model state sync.
2. Unified store: high-throughput lock-safe parallel + scalable + reliable + ACID
   (**real SERIALIZABLE**) + secure, with UNIFIED APIs shareable across dbs
   (sqlite/postgres/bluedb).
3. Easy + simple; low-level APIs only for the 0.001%.
4. Notify clients of changesets (query/row-scoped, in the commit path).
5. Built-in Sky Console admin access to records.

Kills the 5 dev pains: juggling multiple stores · per-db quirks/non-portable code ·
data migration · session RAM overflow · too many configs / no unified query.

## ⚠️ RULE ZERO — state is EXECUTABLE, never asserted

**This is the highest-priority architectural requirement, above any feature.**
It exists because the previous attempt's handoff doc said "4.5 of 5 phases done"
while the flagship path deadlocked on every page load, a P0 broke every
non-Persist app, and three gates recorded PASS on things that were broken or had
never executed. A fresh or compacted session inherits CLAIMS; claims survive
compaction while the evidence behind them evaporates.

Therefore, on this branch:

1. **Every one of the 5 goals has a numbered, runnable gate.** Goal status is
   COMPUTED by running it — never read from prose.
2. **`docs/bluedb/STATUS.md` is GENERATED OUTPUT** of that gate suite, never
   hand-written. One command tells any fresh session the true state in ~60s.
3. **No gate counts until proven falsifiable BY MUTATION** — reintroduce the
   defect, watch it go red, restore, and record both outputs. A gate that cannot
   fail is worse than no gate: it survives compaction as a green lie.
4. **No phase is "closed" by me.** Only a fresh-context adversarial Judge, given
   the verbatim goal, may close one. Any "but/except/however/caveat/mostly/
   essentially/for the scope of/modulo" in a PASS verdict → NOT closed.
5. **Prefer a real test on the real path over a source-grep tripwire.** Grep is
   defensible only for a genuinely lexical property. For structural properties
   (lock discipline, persist-before-ack) compute the property — see
   `feat/bluedb`'s AST dominance analysis, which emits its own site table so the
   inventory cannot drift. Note a TEXTUAL order rule was tried and rejected: it
   passes with the bug reintroduced, because a persist in a mutually exclusive
   branch satisfies it.

## What this rebuild must FIX that the last architecture did not

These are the genuinely architectural gaps (verified on `feat/bluedb`). The
previous attempt's failures were ~1/3 architectural, ~1/3 process, ~1/3
pre-existing Sky.Live runtime bugs. Only this third is in scope here:

- **A1 — cross-backend isolation is not unified.** SQLite "serializable" is a
  process-wide `SetMaxOpenConns(1)` clamp emitting `BEGIN IMMEDIATE`, not an
  isolation level; the repo's own test proves READ COMMITTED behaves identically.
  SSI is real but embedded-only. Goal #2 demands one contract across all three.
- **A2 — the engine has no index seek.** Every transactional scan is O(all rows)
  (full iterate + RAM filter); `P.index` does not seek (`TODO(phase3b/4)`).
  Contradicts the "fast, frequent small reads" north star AND Phase-1's own
  "ordered range O(log n+k)" gate.
- **A3 — goal #1 was never designed.** The RAM bound came from main's pre-mandate
  tiered cache; the default `memory` store never evicts, SSE-connected sessions
  are never evicted, no test caps session count or bytes, sessions-as-collection
  was deferred indefinitely.
- **A4 — tenant identity is not durable, so scoping is unsound.** The engine's
  write-time tenant tag is explicitly never persisted, so every tenant-scoped
  read must compare against an app-written, FORGEABLE row column. Goal #5's
  entire security model rested on this. Design durable, engine-attested tenancy.
- **A5 — reactivity does not compose with scale.** Embedded-only and
  single-instance (the capability gate exits the process otherwise), so goal #4
  cannot hold with goal #2's "scalable" on multi-replica deployments.
- **A6 — the config layer was never wired end-to-end.** `[data] driver` is a
  no-op: the compiler writes `DB_DRIVER` (from `[data]` AND legacy `[database]`)
  and NOTHING reads it; the driver is chosen by DSN shape. A passing test pins
  the dead key.

## Locked decisions (do NOT re-litigate)

- **KEEP the verified substrate; port TESTS, not code.** Pebble + MVCC-in-key
  (`base.CheckComparer` gate, `Name="skydb.mvcc.v1"` irreversible), the
  single-writer committer w/ HLC floor, the changefeed, SSI read-set validation,
  and the errorfs crash corpus are the most-verified parts of the prior work.
  Everything ABOVE that substrate is redesigned and rebuilt against the new
  architecture.
- **Pre-existing Sky.Live runtime bugs are OUT OF SCOPE here** — they affect every
  Sky app and ship separately on `fix/skylive-runtime-soundness` (off main):
  the `handleEvent` session hijack, the `sendBeacon` CSRF 403, the reactive
  gate's first-session `os.Exit`, and `live.go`'s implicit lock contract.
- **Goal #5 = READ *AND* WRITE. RULED BY THE USER 2026-08-09.** Asked directly
  whether "admin access to records" means read-only or read+write, the user chose
  **"Read + write, both in scope"**. So:
  - The Console can view AND edit/delete records.
  - **Goal #5 CLOSES ONLY WHEN WRITES WORK.** A Judge MUST return NOT ACHIEVED
    for goal #5 on a read-only surface. This is now a user decision, not an
    agent's reading — do not re-narrow it, and do not cite any prior doc's
    "read-only" wording as authority (that wording came from agent-authored docs;
    the doc previously cited as mandating it in fact recommends shipping writes).
  - 5e-2 must carry: the write path gated on the ENGINE-ATTESTED tenant (§5's
    durable tenancy, not a forgeable app-written column), a per-mutation audit
    trail, optimistic concurrency so the console cannot cause a lost update, and
    a confirm/undo story.
  - The `goty.rs` record-fieldset collision does NOT block the edit form (fixed
    v0.19.1; `Std.Live` never imports `Std.Analytics`; `EventProp` appears 0
    times in the generated console). Do not reinstate that excuse.

- **The reactive capability gate's first-session `os.Exit` lands HERE, not on
  `fix/skylive-runtime-soundness`** — verified 2026-08-09 that the gate is
  BlueDB-only and does not exist on `main`. §6.5's startup-check replacement is
  in scope for this branch.

- **U1 — order-preserving `Decimal`/`Money` index encoding: BUILD IT. RULED BY
  THE USER 2026-08-09.** v2.1 had parked this, leaving `Money`/`Decimal`
  un-indexable on the DEFAULT backend while `Std.Money` on `Std.Decimal` is
  `AGENTS.md`'s pinned currency default. The user chose to design it now, in P2
  alongside the index keyspace. It needs its own ordering gate covering
  negatives, zero, and differing scale (2 vs 8), plus round-trip. `P.index` on a
  `Money`/`Decimal` column must WORK on embedded — a build error there is no
  longer an acceptable outcome.

- **U2 — throughput floors: DERIVE FROM `feat/bluedb`, THEN RATCHET. RULED BY
  THE USER 2026-08-09.** Goal #2's "high-throughput" had no number, so no gate
  could fail on it. Measure the `feat/bluedb` substrate once (its group-commit
  and point-read numbers were gated and green), commit those as the floor in
  `docs/bluedb/baselines.json`, and require v2 to meet-or-beat; raise the floor
  when it improves. Do NOT seed baselines from whatever v2 happens to ship —
  that is the self-seeding vacuity the grill flagged in G4.3, and it is
  forbidden here.

## Reference material (read, don't trust)

- `feat/bluedb` — the prior attempt. `docs/bluedb/RESUME.md` there is now
  ACCURATE (rewritten 2026-08-09) and lists 11 open items + what each gate
  actually proved. Its design docs are grilled research; its phase ✅ marks are
  not evidence.
- `salvage/p5e-foundation` — the console admin-access foundation (authorization
  funnel with zero trust inputs, engine registry fix), with mutation proofs.
  Port-worthy against the new design.
- `exp/bluedb` — the first attempt; hardened SQL browse layer + prior art.
- Memory: `gate_vacuity_class.md`, `bluedb_clean_slate.md`.

## Method (per CLAUDE.md §0 / §0.4)

Per phase: **decide scope → design → grill (≥2 fresh-context adversaries) →
implement (worktree) → three-leg verify (unit -race + integration + REAL app) →
fresh-context Judge.** Architecture-Consult is Phase 0 of every compiler-level
workflow. Commit at verified sub-milestones; push at phase boundaries. Stop only
on a genuine blocker — surface it and keep the loop alive.

**Worktree hazard:** 8 of 8 agent worktrees in the prior session were created off
`main` rather than the branch tip. EVERY agent brief must open with "confirm
`git log --oneline -1` equals <base>; reset if not."

## Non-negotiables

mem-guard running · timeout-bound every long run · CGO=0 cross-compile green ·
`-tags pebblegozstd` in every Go test invocation AND in CI (without it CI links
cgo DataDog zstd while shipped apps link pure-Go klauspost) · `base.CheckComparer`
is the irreversible gate · no runtime panic or hang from well-typed Sky code ·
the crash corpus is day-one · every new gate mutation-proven.
