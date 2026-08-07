# BlueDB clean-slate rebuild — RESUME (authoritative handoff)

**Read this FIRST in a fresh session.** Single entry point for continuing the
`feat/bluedb` mandate. Current tip: `feat/bluedb` @ `27470bff` (all pushed).

## Status: 4.5 of 5 phases done (all on origin/feat/bluedb)

| Phase | What | State |
|---|---|---|
| P1 engine | Pebble+MVCC+committer+changelog+GC | ✅ closed + Judge-verified |
| P2 SSI | serializable txns (index-range read-set) | ✅ closed + Judge-verified |
| P2-fix | `blindPut` OldIndex under-reject (found by P4 grill) | ✅ fixed + verified (`aba0611a`) |
| P3 unified API | Std.Persist across embedded/sqlite/postgres, real SERIALIZABLE | ✅ closed + Judge-verified |
| P4 reactivity | query-scoped commit-path, fail-closed tenant, capability gate | ✅ closed + Judge-verified |
| **P5a** `[data]` config | subsumes [database]/[live].store/[analytics]; **wins** over legacy | ✅ shipped (`edcfc5b8`), tested |
| **P5b** `sky data` verb | alias over `sky db` | ✅ shipped (`c5153cc4`) |
| **P5c** session-blob envelope | version-gate → reset-not-corrupt (priv-esc class) | ✅ shipped (`27470bff`), tested |
| **P5c** store-as-collection | goal #1 — back sessions with bluedb Persist | ⏳ deferred (additive; existing stores serve #1) |
| **P5d** durability funnel | R1 acked-then-lost | ❌ TODO — data-loss-critical |
| **P5e** Data tab | auto-admin read-only + fail-closed tenant | ❌ TODO |
| WHOLE-GOAL Judge | all 5 original goals, fresh-context adversarial | ❌ TODO (after 5d/5e) |

## ⚠️ AUTHORITATIVE SOURCES — in priority order
1. **`.claude/AUTONOMOUS_GOAL.md`** — the verbatim mandate + the 5 original goals + the loop protocol.
2. **`docs/bluedb/phase5-grill-findings.md`** — the 9 blocking findings + the **v2 fix directions**.
   THIS is the spec for 5d/5e. It OVERRIDES the v1 design doc.
3. **`docs/bluedb/phase5-dx-collapse-design.md`** — ⚠️ **v1, SUPERSEDED in part.** Its structure/
   sub-phasing is fine, but its *mechanisms* for session-versioning (auto-hash), durability
   (heuristic), the funnel (convention), and auto-admin (fail-open) are WRONG per the grill —
   use the grill-findings v2 directions instead.
4. Memory `bluedb_clean_slate.md` — full phase history + decisions.
5. `docs/bluedb/clean-slate-architecture.md` + `phase1..4-*-design.md` + `phase4-grill-findings.md`.

## Methodology (INVIOLABLE — CLAUDE.md §0)
Per sub-phase: **design → grill (≥2 fresh-context adversaries) → implement (worktree executor) →
three-leg verify (unit -race + integration + real build) → fresh-context Judge to CLOSE.** Only an
independent Judge may declare a phase done. Push at phase boundaries. This is why 5d/5e want agents.

## 5d — R1 durability funnel (data-loss-critical). Grill A1/A2 + B3.
> **PARTIAL DONE (sync mode):** persist-before-ack wired for **Cmd.perform completions**
> (`runPerformBody`, `live.go` — `app.store.Set` before the SSE frame, gated on a shipped frame;
> nil-store-guarded). Closes the PRIMARY A1 case (a perform result acked-then-lost) for the durable
> session stores (sqlite/postgres/redis) against a PROCESS crash. Tests: `live_perform_persist_test.go`.
> STILL OPEN: the first-class `Persist.durable`/ephemeral marker (fine-grained control; today
> Time.every ticks stay ephemeral by construction, Cmd.perform persists always); **A2** (SQL
> `synchronous=FULL` vs WAL NORMAL — host power-loss); **B3** (structural `fanOutFrame` enforcement).
- **A1 (acked-then-lost):** the design's semantic-vs-ephemeral HEURISTIC is undecidable at the emit
  site (an `onInput` autosave draft or a bare `onClick "Place Order"` would be classed ephemeral →
  acked without persist → lost on crash). FIX: a **first-class `Persist.durable` marker** the app
  sets on semantic transitions; keystroke `onInput` ephemeral by default; everything marked durable
  persists-before-ack. Design the exact Sky API + how the runtime reads the marker at the emit site
  (it MUST be decidable there).
- **A2:** a marked-durable write on a SQL session backend must fsync — `sqliteStore.Set` runs under
  `PRAGMA synchronous=NORMAL` (`live_store.go` ~:518), which does NOT fsync per commit under WAL.
  Use `synchronous=FULL` for durable writes, or scope+document the guarantee per backend.
- **B3 (structural, not convention):** the REAL emit method is **`fanOutFrame`** on `*liveSession`,
  called from **≥3 sites: `live.go:4216, 4587, 6622`** (NOT `emitFrame`/`applyModelDelta` — those
  don't exist; the v1 design named phantoms). Go has NO intra-package access control, so a lowercase
  funnel stays callable everywhere. Make it STRUCTURAL: move the sse-emit+persist chokepoint into a
  SEPARATE Go package whose only exported entry is the funnel (a bypass won't compile), OR a CI/test
  grep-gate asserting the `fanOutFrame`/`sseCh`-write callers are exactly the funnel.
- Confirmed live: `runPerformBody` (`live.go:5317`) + `Time.every` (`live.go:5540-5610`) mutate Model
  and push to `sess.sseCh` and NEVER `store.Set` — the acked-not-durable bug. `handleEvent`
  (`live.go:4567`) already persists-before-ack.
- Gate: an acked-then-lost regression test (draft/onClick), the backend fsync, and the structural
  bypass-prevention gate.

## 5e — read-only Data tab (auto-admin, goal #5). Grill B2.
- Auto-render a read-only CRUD LIST/detail for every declared collection in the Sky Console.
- **B2 (fail-OPEN today):** the reused v0.16.6 pattern returns "" for a no-tenant session
  (`hub_bridge.go:539-548`) and `rejectCrossTenantSvc(_, "")` returns IN-SCOPE (`:559-564`) → a
  no-claim admin reads ALL tenants. INVERT it for the admin surface: no verified tenant → show
  NOTHING (fail-closed). And the hub's `service_name LIKE` filter has NO analogue for user tables —
  specify a concrete per-collection tenant-column row filter (or the Phase-4 `WatchTenant` scope).
  Gate access behind `SKY_CONSOLE_AUTH`. Edit form is gated on the `goty.rs` record-fieldset
  collision (ships read-only; edit later, tuple-backed).
- `SKY_CONSOLE_DATA` / `live_store_bluedb.go` / `DataTab.sky` are NET-NEW (not ports).

## Environment gotchas (these cost HOURS — obey)
- **`CARGO_TARGET_DIR=/Users/anzel/.cargo/bin`** → a `cargo build --release -p sky` binary lands at
  **`/Users/anzel/.cargo/bin/release/sky`**, NOT `rust/target/release/sky`. `command cp -f` it to
  `sky-out/sky`.
- **`cp` is aliased interactive** → ALWAYS `command cp -f`; a bare `cp` hangs on a prompt.
- **zsh `noclobber`** → `>` refuses to overwrite an existing file (silently aborts the command); use
  `>|` to force.
- After editing a `crates/*` Rust file, confirm **`Compiling project`/`Compiling sky`** in the build
  output before trusting a gate — a stale mtime can skip recompiling `project` (the stale-binary
  trap that faked a materialization "leak" once). `touch` the file if needed.
- Ephemeral Postgres for write-skew/serializable tests: server tools at
  `/opt/homebrew/opt/postgresql@14/bin`; the Unix socket dir MUST be short (`-k /tmp/skpg`, <103
  bytes) or the server won't bind; start with stdin closed (`</dev/null`).
- `bluedb` package imports ONLY pebble + stdlib — never `rt` (the layering rule; `rt` imports `bluedb`).
- Run `nohup ./scripts/mem-guard.sh >/tmp/mem-guard.out 2>&1 & disown` if `pgrep -f mem-guard.sh` is empty.

## Design nuance worth keeping (discovered, not obvious from the docs)
- **5c session envelope = explicit version only.** A structural type-fingerprint was considered but
  `decodeSession` has no current-Model instance to compare against, so it can't validate a
  fingerprint at decode. The explicit `[data] sessionVersion` is the load-bearing signal (developer
  bumps on a gob-silent semantic change); gob itself errors+resets on shape changes. Envelope format
  is `[4-byte magic "SKS1"][big-endian uint32 schemaVersion][gob]`; legacy blobs (no magic) decode
  on the v0 path. Tests: `live_store_envelope_test.go`.
- **5a precedence:** `SetSkyDefault` is FIRST-wins (`lower.rs:785`); `[data]` wins by being pushed
  into `extra_defaults` BEFORE the legacy sections (`read_sky_toml_config` prepends `data_defaults`).
- **5a→P4 coupling:** `[data] reactiveScope` → `SKY_DATA_REACTIVE_SCOPE` makes the already-shipped P4
  boot-gate's own sky.toml hint truthful (it reads that exact env via os.Getenv).
