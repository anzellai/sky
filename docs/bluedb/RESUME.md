# BlueDB clean-slate rebuild — RESUME (authoritative handoff)

**Read this FIRST in a fresh session.** Single entry point for continuing the
`feat/bluedb` mandate. Current tip: `feat/bluedb` @ `5c1beb69` (pushed).
Rebased onto `origin/main` @ `fdbc398d` on 2026-08-08. A pre-rebase safety ref is
at `feat/bluedb-backup-prerebase`.

## ⚠️ 2026-08-09: a whole-goal Judge returned NOT ACHIEVED — 9 gaps

**This document previously said "4.5 of 5 phases done, remaining = P5e UI".
That was wrong, and the way it was wrong is the most important thing on this
page.** A fresh-context adversarial Judge over the five original goals found the
branch was not close to done, and the follow-up work found more. Do not trust a
phase marked ✅ below without re-verifying it — several were certified by gates
that could not fail.

### What the phase table got wrong (all verified, all now fixed)

| Recorded as | Reality |
|---|---|
| P4 reactivity ✅ Judge-verified, gate = "2-browser live demo" | **Every reactive app deadlocked on initial page load.** `handleInitial` held `sess.mu`; `setupSubscriptions` → `reactiveEnsureStartedHook` → `ensureReactiveStarted` re-locked it. The demo gate cannot have been run on this code. Fixed `393b6ff9`. |
| P5d durability ✅ "ALL 6 mutate-and-ack paths" + B3 tripwire | **It was 4 of 6, then 6 of 9.** `reactiveRefreshOnce` and `dispatchOneWsSub` acked with zero `store.Set`; a verification Judge then found three MORE (`handleSSE` resync persisted AFTER the ack; drop-resync never persisted; `handleEvent`'s desync arm returned before its persist). The B3 tripwire could not see them — it grepped `sseCh <-` while they wrote to the `ResponseWriter`. Fixed `393b6ff9` + `5c1beb69`. |
| P5e backend ✅ "done, tested" (`31b05b35`) | **That commit broke every non-Persist Sky app.** It added a `sky-app/bluedb` import outside the materialisation gate, so `examples/01-hello-world` failed `go build`. `cargo test --workspace` and `go test ./rt/...` stayed green. Fixed `19cffb93`. The backend also had ZERO production callers — no route, no handler, no tab. |
| goal #2 SERIALIZABLE ✅ | The only discriminating cross-backend proof (`TestWriteSkewPostgres`) had **never run**: the test read `SKY_TEST_PG_URL`, CI set `SKY_TEST_POSTGRES_DSN`. The whole `runtime-go/bluedb` suite was in no gate at all. Fixed `c248417c`. |

### The lesson, stated plainly
Three separate gates on this branch recorded PASS while the thing they guarded
was broken or never executed. **A fix behind a gate that cannot fail is not a
fix.** Every gate added since is proven non-vacuous BY MUTATION — reintroduce the
defect, watch it go red, restore. Do the same for anything you add.

## Status after the 2026-08-09 iteration

**Closed and independently re-verified** (a fresh Judge re-ran them and confirmed
2 of 4, then the remaining holes were closed and re-proven):
- **G1** reactive-init deadlock — `59-persist-live` serves HTTP 200 in 56ms.
- **G3** CI gating — bluedb suite gated on both runners with `-tags pebblegozstd`
  (which is NOT cosmetic: without it CI links cgo DataDog zstd while shipped apps
  link pure-Go klauspost); Postgres write-skew proof runs and discriminates.
- **P0** non-Persist buildability — gate now derives from the file's real import,
  plus symbol-level coverage.
- **Persist-before-ack** — `persistBeforeAck` is now the sole persist and
  DOMINATES all 15 acks. The tripwire is an AST dominance analysis that emits the
  ack-site table itself (`go test -run EverySeqAdvancingAckPersistsFirst -v`), so
  the inventory cannot drift. A textual rule was tried and rejected: it passes
  with the bug reintroduced, because a persist in a mutually exclusive branch
  satisfies it.
- **Session hijack (NEW, pre-existing, all Sky.Live apps)** — `handleEvent` took
  the session id from the request BODY with no cookie binding, so anyone holding
  a victim's sid could drive their session. CSRF was never a backstop (it is a
  double-submit check never bound to the sid — proven e2e with an attacker
  carrying a fully valid session + CSRF token).
- **Goal #3 docs** — `docs/skypersist/overview.md`, gated (16/16 doc examples
  compile); `AGENTS.md` no longer tells users BlueDB is WIP.
- Flaky `TestNB1` — was a mid-flight observation point in the test harness, not a
  dropped eval; fixed with a real completion barrier.

**Still open — in priority order:**
1. **G2 / goal #5** — Console admin access. Design is grilled twice and ready
   (`phase5e-closure-design-v2.md` v2.1); NOT implemented. See the scope question
   below before starting.
2. **G4** — SQLite "serializable" is a process-wide `SetMaxOpenConns(1)` clamp
   emitting `BEGIN IMMEDIATE`, not an isolation level; the repo's own test proves
   READ COMMITTED behaves identically. The guard `driver != "pgx"` fails open for
   any future driver.
3. **G5** — every transactional scan is O(all rows) (`txn.go` full-iterate +
   RAM filter) against a Phase-1 gate demanding O(log n + k).
4. **G6** — goal #1's RAM half: the bound came from main's pre-mandate tiered
   cache, the default `memory` store never evicts, SSE-connected sessions are
   never evicted, and no test caps session count or bytes.
5. **G9** — non-durable insert serial (id reuse after restart); `liveInto`
   silently never updating on a SQL backend.
6. **`[data] driver` is a NO-OP** — the compiler writes `DB_DRIVER` (from `[data]`
   AND legacy `[database]`) and NOTHING reads it; driver selection is by DSN
   shape. `driver = "postgres"` beside `./app.db` silently opens SQLite. There is
   a passing test (`build.rs:1576`) pinning the dead key.
7. **`sendBeacon` unload flush has always been 403'd** on any CSRF-enabled app
   (`sendBeacon` cannot set headers) — debounced input lost on tab close.
   `docs/skylive/production-resilience.md:213` claims a session-bound CSRF token
   the code does not implement.
8. **Reactive capability gate `os.Exit(1)`s on the FIRST SESSION, not at boot**,
   under `sess.mu` — an app passes its health check, then dies when the first
   user loads a page.
9. **Tenant-scoped reactive apps never see background-job writes** — writes are
   tagged with the writing goroutine's session tenant; cron/CLI/plain-HTTP tag the
   empty tenant and the partition is strict with no `withTenant` escape hatch.
10. **`AGENTS.md:258` describes `kernel_api.rs` + its CI gate as current. Neither
    exists** — not on this branch, not on `origin/main`. Phantom instruction.
11. **`sky build` never passes `-tags pebblegozstd`**, so its `CGO_ENABLED=1`
    FFI-retry path links cgo zstd (`phase1-status.md:205`).

## ⛔ SCOPE QUESTION FOR THE USER — do not silently decide this
Goal #5 verbatim is **"Built-in Sky Console admin access to records."** The words
"read-only", "CRUD" and "LIST/detail" appear NOWHERE in the user's goal — they
originate in agent-authored docs, and the doc previously cited as mandating
read-only in fact RECOMMENDS shipping the edit form. The `goty.rs` collision long
cited as blocking edits **does not block it**: fixed in v0.19.1, `Std.Live` never
imports `Std.Analytics`, and `EventProp` appears 0 times in the generated console.
v2.1 designs read-only as a complete 5e-1 and specifies the write surface as
5e-2. **A Judge must return NOT ACHIEVED for goal #5 on read-only alone** until
the user rules. Ask; do not narrow.

## Status (original phase table): 4.5 of 5 phases done (all on origin/feat/bluedb)

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
| **P5d** durability | R1 acked-then-lost (A1) closed on ALL 6 mutate-and-ack paths + B3 tripwire | ✅ safety done; marker+A2 = deferred optimizations |
| **P5e** Data tab | auto-admin read-only + fail-closed tenant (goal #5) | 🟡 backend done (fail-closed gate + enum + row-read, tested); UI tab wants browser verify |
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
> **A1 FULLY CLOSED (sync mode, persist-by-default policy):** an audit of EVERY mutate-and-ack path
> in `live.go` found the surface was wider than the initial fix — persist-before-ack is now wired at
> ALL of them (`app.store.Set` before the ack, gated on a shipped frame, nil-store-guarded):
> `handleEvent` (:4575, pre-existing) · sendBeacon **batch** (:4435) · **runPerformBody** (:5391) ·
> **runSubscriberDispatch** pub/sub (:~5850) · **runStreamSubscriberDispatch** stream/websocket
> (:~6072) · the **Time.every tick** goroutine (view-changing ticks only; :~5620). Tests:
> `live_perform_persist_test.go` (perform + pub/sub). Full rt -race green. ⇒ the `Persist.durable`/
> ephemeral marker is now confirmed an **OPTIMIZATION** (skip the persist tax on known-ephemeral
> high-frequency Msgs), NOT a blocking safety fix — A1 is closed by persist-default.
> **B3 DONE:** `live_persist_invariant_test.go` — a tripwire pinning the 8 emit sites; a new one
> trips it, forcing persist-before-ack. STILL OPEN (refinements, NOT blocking A1):
> **A2 + the marker are COUPLED and deferred together.** persist-by-default now writes the session
> store on every mutate-and-ack, so making sqlite sessions `synchronous=FULL` (A2, power-loss
> durability) unconditionally would fsync EVERY write — a perf regression. Correct order: implement
> the ephemeral/durable **marker first** (classify which writes matter), THEN durable writes use
> FULL and ephemeral ones skip. Both are optimizations on top of the closed A1 (process-crash
> durability holds today on all durable stores). Marker design analysis is below (race-free path).
>
> **Marker design analysis (done, for the implementer):** recommend **persist-by-DEFAULT + an
> explicit `ephemeral` opt-out** (safe direction — a wrong default can't lose data; only the app's
> explicit opt-out skips persist). The classification must be **race-free**: do NOT use a mutable
> per-transition session field (`runPerformBody` reads the persist gate AFTER releasing `sess.mu`,
> so a concurrent dispatch flipping a shared flag races → mis-gated persist → data loss). Two safe
> options: (a) `dispatch` RETURNS the ephemeral bool (invasive — touches all `dispatch` callers:
> `live.go:4181,4980,5338` + `dispatchBatched`), captured under the lock like `haveFrame`/`snap`;
> or (b) **constructor-level static classification** — the app declares ephemeral Msg constructor
> NAMES once at config time (like `msgTags`), and the persist gate does a READ-ONLY lookup of the
> incoming msg's constructor (no per-transition mutation, no race). Option (b) is cleaner + race-free.
> Cmd rep is `cmdT{kind:"..."}` (`live.go:1558-1560`); a marker cmd would be `cmdT{kind:"ephemeral"}`.
> This is delicate concurrent surgery on the persist path → wants the grill/Judge, not sync self-review.
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
> **BACKEND DONE (sync mode):** `runtime-go/rt/console_data.go` — `consoleDataAccess` is the
> FAIL-CLOSED gate (grill B2 fix: verified-but-no-tenant → DENY, never all-tenants; dev unscoped;
> tenant claim → scoped; explicit super-admin → unscoped). `adminEmbeddedCollections` enumerates
> collections across open embedded backends (`embeddedByID` + `EmbeddedBackend.CollectionNames`);
> `adminReadRows` reads rows (unscoped all-scan — only reachable when the gate grants unscoped).
> Tests: `console_data_test.go` (fail-closed matrix + non-vacuous B2 + enum + row-read). REMAINING:
> (1) the **tenant-scoped row filter** (per-collection tenant column → a Where cond; until then a
> scoped decision must NOT call adminReadRows); (2) the **console Data-tab UI** (a tab in
> `sky-bundled/console/src/` consuming these, read-only) + wiring the tab to an admin endpoint; (3)
> SQL-backend collections (only embedded enumerated today). The UI wants BROWSER verification.
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
