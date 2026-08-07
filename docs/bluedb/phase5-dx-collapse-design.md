# BlueDB Phase 5 — DX collapse: design

> **Status:** design only (no implementation). Branch `feat/bluedb` @ `f3b1c2a6`.
> Companion to `docs/bluedb/clean-slate-architecture.md` §"DX collapse" (`:367-390`),
> §5.5-5.7 (`:883-940`), §6.5 (`:1044-1078`), Phase-5 roadmap (`:1182-1205`), and
> risks R1/R9 (`:1221-1229`, `:1300-1309`). Extends the Phase-1..4 substrate already
> shipped on this branch (engine `runtime-go/bluedb/`, reactive gate
> `runtime-go/rt/bluedb_reactive_gate.go`, `Std.Persist`, `Std.Codec`, `Std.Db.Store`).
>
> Phase 5 is the FINAL phase: it does not add a storage capability — it **collapses
> the developer surface** onto the substrate the lower phases built. Every deliverable
> below is a DX win (one config, one migration, one admin, one session model, one
> persist funnel), each mapping to an already-resolved architecture risk.

---

## 0. What exists today (the ground Phase 5 stands on)

Every claim in this doc is anchored to code on `feat/bluedb`. The grounding map:

| Surface | Where it lives today | Phase-5 verb |
|---|---|---|
| sky.toml → runtime defaults | `read_sky_toml_config` (`rust/crates/project/src/build.rs:804-876`); emits `rt.SetSkyDefault(<suffix>, val)` per section arm | **SUBSUME** `[database]`/`[live]`/`[analytics]` into `[data]` |
| `[database]` arms | `build.rs:829` (`DB_DRIVER`), `:835` (`DB_PATH`) | fold into `[data]` |
| `[live] store`/`storePath`/`ttl` arms | `build.rs:852-857` (`LIVE_STORE`/`LIVE_STORE_PATH`/`LIVE_TTL`) | fold into `[data]` |
| `[analytics] dbPath`/`retention` arms | `build.rs:839-846` (`ANALYTICS_DB_PATH`/`ANALYTICS_RETENTION`) | fold into `[data]` |
| `[data] reactiveScope` | **runtime already reads `SKY_DATA_REACTIVE_SCOPE`** (`bluedb_reactive_gate.go:35`) but **the CLI does NOT parse any `[data]` section yet** (`build.rs` has no `("data", …)` arm) — the gate comment `bluedb_reactive_gate.go:17-18` names a CLI mapping that is unbuilt | **WIRE** the first real `[data]` key |
| `sky db` verbs | manual string dispatch in `cmd_db` (`rust/crates/sky/src/main.rs:2592`); handlers `cmd_db_gen:1696` / `cmd_db_apply:1878` / `cmd_db_status:2069` / `cmd_db_seed:2176` / `cmd_db_push:2227` / `cmd_db_reset_drop:2347` | **ALIAS+EXTEND** as `sky data …` |
| Migration diff engine | `rust/crates/sky/src/db_migrate.rs:88` (`diff`), migration JSON `{id, ops}` (`:330`), snapshot `db/schema.json`, committed `db/migrations/*.json` | reuse; add session-model collection |
| Dialect-safe render + ledger | `renderMigOp` (`runtime-go/rt/db_migrate_ops.go:93`), `Db_migrateApply` + `_sky_migrations` (`runtime-go/rt/db_auth.go:1705`, UNSCOPED bypass `:1687`) | reuse |
| `Std.Db.Migrate` | `sky-stdlib/Std/Db/Migrate.sky:20` (`migrateOps`), `:27` (`renderMigrations`) | reuse (PORT) |
| `Store.Project` bridge | `sky-stdlib/Std/Db/Store.sky:537` (`type Project`), `:531` (`toTable`), `:558` (`dumpSchema` → `Db_dumpProject`) | reuse |
| Session store | `SessionStore` interface (`runtime-go/rt/live_store.go:333`); backends memory/sqlite/postgres/redis; `chooseStore` (`:1501`) | **ADD** `data`/`embedded` backend |
| Session Model blob | `storableSession` struct (`live_store.go:1247-1276`) — **NO version field**; `encodeSession:1278` / `decodeSession:1389` via gob-by-field-name | **ADD** blob version tag |
| Session-blob reset behaviour | gob decode by field name; type change → decode fail → **silent reset to `init`** (`exp/bluedb:docs/bluedb/migration.md:9-20`); `Live.withMigrate` is a hand-guarded `model→model` on resume | **VERSION+MIGRATE** |
| R1 persist funnel | exactly **3** `store.Set` sites (`live.go:4213` handleInitial, `:4567` handleEvent, `:6235` handleSSE) + idle-evict flush (`live_store.go:728`); async producers `runPerformBody:5317`, `dispatchBatched:4672`, `Time.every` (`~:5504-5604`) mutate the shared session + push SSE **but never Set** | **COLLAPSE** to one funnel + durability tier |
| Console admin | 6 read-only tabs (`sky-bundled/console/src/State.sky:18-24`); `AnalyticsTab.sky:80` renders aggregate/list; **no `DataTab.sky` on this branch** (only in a worktree); embedded via `register_v3.go` → `MountLiveSubAppInProcess` | **ADD** read-only Data tab |
| Data endpoint hardening | `SKY_CONSOLE_DATA=readonly|readwrite` + `SKY_ADMIN_TOKEN`, no loopback bypass, session stores excluded from writes (`exp/bluedb:docs/bluedb/README.md:141-149`) | reuse |
| Tenant gate | `HubStoreReaderWithTenant` (`hub_bridge.go:122`), `tenantPrefixForSession:539`, `rejectCrossTenantSvc:561`; SQL `AND … LIKE prefix||'%'` | reuse pattern |
| Edit-form codegen block | `record_fieldsets` keyed by field-NAME set (`rust/crates/lower/src/goty.rs:69`), `select_record_candidate:256-289` mis-picks when field values erase to `any` → CoerceFailure (`record_fieldset_collision_erased` memory) | **read-only floor** |

**One honest reframe, carried from the architecture (grill fix #11, `:369-373`):** the
CONFIG collapse is the headline win. Joins / GROUP BY / aggregates, transaction
*guarantees*, and raw-KV remain distinct escape hatches. Phase 5 does **not** claim a
single uniform model over all of that.

---

## 1. One `[data]` config (Decision P1 — `:124`, §5.5 `:883-902`)

### 1.1 Failure mode this closes

"Which database?" — an app juggles THREE configs with three mental models
(`[database]` app data, `[live] store` sessions, `[analytics] dbPath` events), the
backend is *named in code*, and sqlite→postgres *touches code* (`:124`). The reactive
gate already reads `SKY_DATA_REACTIVE_SCOPE` (`bluedb_reactive_gate.go:35`) but **no
`[data]` section is parsed by the compiler yet** — so today an operator who writes
`[data] reactiveScope = "single-instance"` per the gate's own hint
(`bluedb_reactive_gate.go:173`) is silently ignored. Phase 5a is the first code that
makes `[data]` real.

### 1.2 The schema

```toml
[data]
backend     = "embedded"        # embedded (default) | sqlite | postgres | cluster
path        = "data/app.blue"   # backend=embedded/sqlite
# url       = "DATABASE_URL"    # backend=postgres/cluster (env-name or literal DSN)
scope       = "user"            # session | user | tenant | global  (reactive sync unit)
consistency = "strong"          # strong (default) | snapshot | bounded <ms> | eventual
reactiveScope = "single-instance"  # operator topology assertion (Phase 4 gate)
ttl         = "30m"             # session TTL (absorbs [live] ttl)
retention   = "90d"             # analytics prune window (absorbs [analytics] retention)
```

Sessions + app data + analytics all live in the one backend; graduate
embedded→postgres→cluster by changing `backend`, app code unchanged (`:897-899`).

### 1.3 How it maps to the runtime (the SetSkyDefault fold-in)

The runtime already speaks in `SKY_*` suffixes. `[data]` is a **front-end alias** that
expands to the SAME suffixes the three old sections emit, so **no runtime code
changes for the config collapse** — only `build.rs` gains a `("data", …)` arm block.
Precise mapping (new arms in `read_sky_toml_config`, `build.rs:827` match):

| `[data]` key + backend | Emits (`extra_defaults`) | Consumed by |
|---|---|---|
| `backend=embedded`, `path=P` | `DB_DRIVER=bluedb`, `DB_PATH=P`, `LIVE_STORE=data`, `LIVE_STORE_PATH=P`, `ANALYTICS_DB_PATH=P`, `DATA_BACKEND=embedded` | `Db.connect`/`detectDriver`, `chooseStore` (`live_store.go:1501`), `analyticsStorePath` (`analytics_store.go:107`), reactive gate |
| `backend=sqlite`, `path=P` | `DB_DRIVER=sqlite`, `DB_PATH=P`, `LIVE_STORE=sqlite`, `LIVE_STORE_PATH=P`, `ANALYTICS_DB_PATH=P`, `DATA_BACKEND=sqlite` | as above |
| `backend=postgres`, `url=U` | `DB_DRIVER=postgres`, `DB_PATH=$U`, `LIVE_STORE=postgres`, `LIVE_STORE_PATH=$U`, `ANALYTICS_DB_PATH=$U`, `DATA_BACKEND=postgres` | as above |
| `scope` | `DATA_SCOPE` | reactive fan-out unit (Phase 4) |
| `consistency` | `DATA_CONSISTENCY` | read path (`consistency` §6.2) |
| `reactiveScope` | `DATA_REACTIVE_SCOPE` | `bluedb_reactive_gate.go:35` (already read) |
| `ttl` | `LIVE_TTL` | session TTL |
| `retention` | `ANALYTICS_RETENTION` | analytics prune |

Two subtleties this table must get right, or the collapse silently mis-wires:

1. **`DB_PATH` vs a Postgres URL.** `[database] url` today maps to `DB_PATH`
   (`build.rs:835`) as an alias, and `detectDriver` routes by DSN shape. `[data]`
   preserves that — `path`/`url` both seed `DB_PATH`. **Grill hazard:** the same value
   must simultaneously be a valid *session* store path AND an *analytics* store path
   AND an app-`Db` DSN. For embedded/sqlite that is one file (fine); for postgres it is
   one DSN pointed at three logical namespaces — see §1.6.
2. **`LIVE_STORE=data`** is a NEW store kind (the session-store adapter, §4). It must
   be added to `chooseStore`'s switch (`live_store.go:1501`) or it hits the
   fail-loud unknown-kind default (`:1578`) — that is intentional wiring order:
   Phase 5c (the adapter) must land before Phase 5a can emit `LIVE_STORE=data`.
   Until 5c, `[data]` emits `LIVE_STORE=sqlite`/`postgres` (the existing kinds) so 5a
   ships independently (see §6 dependency chain).

### 1.4 Precedence (unchanged, inherited)

Process env > `.env` > `sky.toml` (`docs/sky-toml.md:282-296`). `[data]` seeds are
`SetSkyDefault` (only applied when the matching env is unset), so a deploy still
overrides any `[data]` value via the concrete `SKY_*` env var. **Grill answer:** there
is no new precedence surface — `[data]` expands to existing suffixes at compile time,
and the runtime's precedence machinery is untouched.

### 1.5 Backward compatibility (the 30-example / real-app question)

This is the sharpest backward-compat attack. Resolution — **additive + deprecating,
never breaking, for two minor versions:**

- **Old sections stay honored.** The `("database"|"live"|"analytics", …)` arms in
  `build.rs:829-857` are NOT deleted in Phase 5. An app on `[database]`+`[live]`
  builds byte-identically. The example sweep stays green because nothing is removed.
- **`[data]` wins on conflict, with a WARN.** If BOTH `[data]` and a legacy section
  set the same suffix, `[data]` is pushed LAST into `extra_defaults` (last-writer in
  the emitted `SetSkyDefault` sequence wins at init) AND the compiler prints
  `warning: [data] and [database] both set the app DB path; [data] wins — remove
  [database]`. This is a compiler diagnostic, not a runtime one, so it is seen at
  build.
- **Deprecation window.** `[database]`/`[live].store`/`[analytics]` emit a
  `deprecated: fold into [data] — see docs/bluedb/migration-to-data.md` WARN when
  `[data]` is ABSENT too (i.e. any app still on the old sections is nudged), starting
  the release Phase 5a ships in. They are removed no earlier than **two minor
  versions** later, and only after the example sweep is migrated.
- **`sky data init --from-legacy`** (a migration helper, Phase 5b) rewrites an
  app's `sky.toml`: reads the three old sections, writes one `[data]` block,
  comments out the originals with a `# migrated to [data]` marker. Idempotent; a
  no-op if `[data]` already present.

**What breaks for existing example apps — explicit list.** *Nothing at build time.*
The sweep (`scripts/example-sweep.sh`) stays green with zero example edits because the
legacy arms are retained. The examples are migrated to `[data]` incrementally as a
**documentation** exercise (each PR migrates a handful, re-runs the sweep), NOT as a
Phase-5a gate. skydeploy / sky-lang.org / darraghstudio are on `exp/bluedb`'s
`[bluedb]` section (`exp/bluedb:docs/bluedb/README.md:151-170`) — that section name is
NOT on `feat/bluedb`, so those apps' rename to `[data]` is a one-line edit tracked in
their own repos, decoupled from the compiler release.

### 1.6 The Postgres one-DSN-three-namespaces hazard (grill pre-empt)

For `backend=postgres`, one `url` backs sessions + app + analytics. Today those are
three separate concerns that could point at three databases. Folding them means:
- **Table-name collision.** Sessions use their own table (sqlite/postgres session
  store DDL, `live_store.go:493`/`:828`), analytics uses `analytics_events`
  (`analytics_store.go:87`), app tables are user-declared. As long as the session +
  analytics table names are namespaced (`_sky_sessions`, `analytics_events`) there is
  no collision with user tables — **assert this in a boot check** (§1.7).
- **Connection-pool pressure.** Three subsystems on one DSN share one Postgres. The
  session store already caps `MaxOpenConns` for the analytics writer
  (`analytics_store.go:14-22`); the design must document the aggregate pool budget so
  a single small Postgres isn't exhausted. This is an ops note, not a blocker.

### 1.7 Boot assertion (fail-loud, not silent)

At boot, when `DATA_BACKEND` is set, emit a one-shot check (mirrors the reactive
gate's fail-loud pattern, `bluedb_reactive_gate.go:151`): the resolved session,
analytics, and app paths are consistent with the declared backend; a
`backend=postgres` with an embedded `.blue` path is a HARD-FATAL config error, not a
silent fallback. This closes the class where a mis-typed `[data]` silently degrades
(the exact failure the session-store fail-loud default `live_store.go:1578` and the
reactive gate were built to prevent).

---

## 2. `sky data migrate` — one migration, incl. the session blob (Decision P?, §5.6 `:904-916`, R9 `:1300-1309`)

### 2.1 Failure mode this closes

Two, actually:
- **Migration juggling** — app schema is `sky db migrate`, but sessions and analytics
  have no migration story at all.
- **R9 — the session-blob silent reset (`:1191-1202`, `:1300-1309`).** The session
  Model blob has **NO schema-version tag** (`storableSession`, `live_store.go:1247-1276`
  — no version field). A breaking Model-shape change across a deploy makes gob decode
  fail, and the runtime **silently resets the session to `init`**
  (`exp/bluedb:docs/bluedb/migration.md:9-20`). `sky db migrate` scopes to *declared
  collections* only (`db/schema.json`), and **the gob Model blob is not a declared
  collection**, so the reset recurs no matter how good the app-table migration is.

### 2.2 `sky data migrate` = `sky db migrate` unified + one new collection

`sky data …` is an ALIAS layer over the existing manual dispatch (`cmd_db`,
`main.rs:2592`). The verbs map 1:1: `sky data migrate --gen` → `cmd_db_gen:1696`,
`sky data migrate` → `cmd_db_apply:1878`, `sky data status` → `cmd_db_status:2069`.
**No diff-engine rewrite** — `db_migrate.rs:88` `diff`, the JSON `{id, ops}` format
(`db_migrate.rs:330`), the dialect renderer (`db_migrate_ops.go:93`), and the
`_sky_migrations` ledger (`db_auth.go:1705`) all PORT unchanged.

The unification is: `sky data status` reports pending/applied across **all three
representations** (app collections, analytics, session blob) in one table, where
today `cmd_db_status:2069` reports only the app-table ledger. Analytics tables are
already schema-managed (`analyticsSchemaStmts`, `analytics_store.go:87`) — they fold
in as a declared collection with a fixed schema. The genuinely new work is the
**session Model as a versioned collection** (§2.3).

### 2.3 The session-blob version tag (R9 requirement (a))

Add a version field to the persisted blob. Concretely:

```go
type storableSession struct {
    BlobVersion int   // NEW — the session Model schema version; 0 == pre-Phase-5 (untagged)
    Model       any
    // … existing fields (live_store.go:1248-1276) unchanged …
}
```

- `encodeSession` (`live_store.go:1278`) stamps `BlobVersion` from a compiler-emitted
  constant `rt.SkySessionModelVersion` (see §2.4).
- `decodeSession` (`live_store.go:1389`) reads `BlobVersion`. A blob whose version is
  **older** than the running binary's version is routed through the migration ladder
  (§2.5) BEFORE it becomes a live `liveSession`. Pre-Phase-5 blobs decode with
  `BlobVersion=0` (gob zero-value) — the exact "adopt-as-legacy" pattern already used
  for `HasAnalytics`/`IdentityValid` (`live_store.go:1258-1272`), so **existing
  persisted sessions decode cleanly with no reset** on the Phase-5 upgrade itself.

### 2.4 Versioning the Model shape — how the number is derived

The version must change **iff the Model's gob-relevant shape changes** (field
add/remove is gob-tolerant; a type change is not — that is the reset trigger,
`migration.md:12`). Design choice, ordered by preference:

- **Structural hash (preferred).** The compiler already walks the Model's Go struct
  shape to register gob types (`RegisterSkyGobTypes`, `live_store.go:82`;
  `gobRegisterAll`, `:1293`). Emit `rt.SkySessionModelVersion` as a stable hash of the
  Model's **field-name→gob-type** signature (the same signature gob keys on). A pure
  field add/remove keeps gob-compatible AND can keep the same version (gob handles it);
  a **type change flips the hash**, which is exactly the case that needs a migration.
  This is derivable at compile time from the HIR Model type — no user bookkeeping.
- **Explicit `[data] modelVersion = N` (fallback).** If the structural hash proves too
  coarse/fine in practice, fall back to an operator-declared integer bumped by hand.
  Rejected as the *default* because hand-bumping is the "hand-guarded, not structural"
  anti-pattern R9 (c) explicitly forbids (`:1199`).

### 2.5 Structural `withMigrate` (R9 requirement (c) — `:1199`)

Today `Live.withMigrate` is a `model → model` function run on resume — **hand-guarded**
(the app author must remember to null-check the new field), and it runs on EVERY
resume regardless of whether a migration is needed. R9 demands a **structural**
version: idempotent by construction.

Design: `withMigrateFrom : Int -> (OldModel -> Model) -> …` — a *versioned* migration
step. The runtime applies the chain of steps whose `from` version is `< running
version AND >= blob version`, in order, exactly once, at decode time (§2.3), never on a
same-version resume. Because the step is keyed on a version delta, it is idempotent by
construction: a session already at the running version skips the chain entirely. This
is the session-blob analogue of the app-table ledger (`_sky_migrations`) — a blob
carries its applied-through version the way a DB carries its ledger rows.

**Crash-safety of the blob migration itself** carries forward the app-migration
discipline (`:915`): the migrated blob is written with the new `BlobVersion` **last**
(write-version-last), so a crash mid-migration leaves the OLD versioned blob intact and
the migration re-runs on next decode — never a half-migrated session.

### 2.6 Atomicity / rollback across the three representations (R9 requirement (d) — `:1200-1202`)

Today the story is **forward-only, no rollback, no cross-store atomicity**
(`:1308-1309`). Honest scope for Phase 5:

- **Within the BlueDB backend (embedded/cluster):** app-collection migrations + the
  session-blob-version bump + analytics DDL can be driven through the **single
  committer** (`runtime-go/bluedb/committer.go`) in ONE atomic batch — this is a
  genuine cross-representation atomic migration *when all three live in one BlueDB
  file*. That is the payoff of the config collapse: they are literally one store.
- **On the SQL backend (sqlite/postgres):** app tables + analytics tables are SQL DDL
  (transactional per statement in the `_sky_migrations` apply, `db_auth.go:1705`); the
  session blob is a row in the session table. Cross-store atomicity across a genuinely
  separate session DB and app DB is **NOT** offered (SQL DDL is not two-phase across
  databases). The honest contract: **per-store forward-only + a documented recovery
  order** (migrate app → migrate analytics → bump session-blob version LAST, so a
  crash mid-sequence leaves sessions on the old version and they re-migrate cleanly).
- **Rollback** stays out of scope (matches app-migration today — forward-only,
  `:1308`). The design states this loudly rather than implying reversibility.

**Grill answer to "a Model change mid-deploy vs live sessions — corruption, lost
state, or clean version-and-migrate?":** with §2.3-2.5, a breaking Model change is a
**clean version-and-migrate** — the old blob is detected by version, the migration
chain runs once at decode, the new blob is written version-last. The pre-Phase-5
silent-reset (`migration.md:12`) is eliminated for any app that declares a
`withMigrateFrom` step; an app that declares NO step for a type-changing field still
gets the *old* behaviour (reset to `init`) but now with a **WARN log naming the
version gap** instead of a silent reset — so it is never silent again.

---

## 3. Auto-derived admin (goal #5, §5.7 `:918-939`)

### 3.1 Failure mode this closes

"I have data but no way to see/edit it without writing an admin app." Every declared
collection (a `Store`/codec) should get a CRUD admin for free.

### 3.2 The read-only floor (what ships)

A new **Data tab** in the console. Note the correction from the architecture: there is
**no `DataTab.sky` on `feat/bluedb`** — `:385`/`:922` cite a path that exists only in a
worktree. So the Data tab is **net-new** (not a PORT), added to the six-tab enum
(`sky-bundled/console/src/State.sky:18-24`) and the tab bar (`View.sky:196`), following
the `AnalyticsTab.sky:80` read-only aggregate/list pattern already proven.

The Data tab renders, per declared collection:
- **List** — an ordered range scan + cursor pagination over the collection (the engine
  gives ordered iteration natively, `runtime-go/bluedb/keys.go`).
- **Row detail** — a read of one record by primary key, fields rendered from the
  codec's declared shape.
- **`Cond` filter** — the query builder's `Cond` leaves (`Std.Db.Store` /
  `Std.Persist`) drive an injection-safe filtered scan.

This is achievable now and matches the architecture's honest floor (`:920-926`).

### 3.3 The edit form — GATED, read-only until the codegen bug is fixed

A scalar-field EDIT form is **net-new AND blocked** (`:928-937`). The generic
`{ field : String, value : String }` form-row record is *exactly* the shape that
triggers the `record_fieldset_collision` codegen bug: `record_fieldsets` is keyed by
field-NAME set (`rust/crates/lower/src/goty.rs:69`), and when a form-row's field values
flow from a tuple / `fst`/`snd` (erasing to `any`), `select_record_candidate`
(`goty.rs:256-289`) cannot distinguish the user's `{key,value:String}` from
`Std.Analytics.EventProp {key,value:PropValue}` (in scope transitively via `Std.Live`)
and picks the wrong alias → **CoerceFailure at runtime**
(`record_fieldset_collision_erased` memory).

**Decision: read-only is the Phase-5 floor.** The edit form ships **only after** one of:
1. `goty.rs` `select_record_candidate` is fixed to prefer the user-module / most-
   specific record when a literal field value is `any`-erased (or to thread the
   annotated return type to pin the record) — the proper fix named in the memory; OR
2. the form uses the documented **tuple workaround** (`(String, String)`, no named
   `{field,value}` record) — shippable sooner, uglier internally, invisible to the
   user.

The design **recommends option 2 for a Phase-5e edit form** (tuple-backed, so the edit
form is not hostage to a compiler fix) while filing option 1 as the real fix. Even
option 2's form is **scalar-only**: relations / enum-choices / validation / nested
records map to a JSON blob the generic form cannot structure — those stay out of scope,
edited via app code (`:930-932`).

### 3.4 Tenant isolation + auth (the attack-surface answer)

The Data tab is a privileged admin surface. Its safety reuses proven gates:

- **Endpoint hardening (PORT):** the data endpoint requires `SKY_ADMIN_TOKEN` bearer
  with **no loopback bypass**, `SKY_CONSOLE_DATA=readonly|readwrite` gates writes in
  every env, a custom header defeats cross-site POST, values are bounded, every
  mutation is audit-logged, and **session stores are excluded from writes** (a raw
  write corrupts the gob frame) — all from `exp/bluedb:docs/bluedb/README.md:141-149`.
- **Tenant scoping (PATTERN reuse):** reads are scoped exactly like the hub reader —
  derive the prefix from the session identity (`tenantPrefixForSession`,
  `hub_bridge.go:539`), reject an explicit out-of-prefix arg
  (`rejectCrossTenantSvc:561`), and let the SQL / engine layer enforce the row filter
  (`AND … LIKE prefix||'%'`, `hub_bridge.go:112-114`) — the v0.16.6 gate. On the BlueDB
  engine the analogue is the write-time-verified tenant tag from Phase 4
  (`bluedb_reactive.go:194` `WatchTenant`) applied as a read filter. **Absent a
  verified tenant identity, the Data tab shows NOTHING** (fail-closed, the reactive
  gate's `""`-bucket discipline, `phase4-reactivity-design.md:485`).
- **Who can access:** admin access requires the console auth gate
  (`SKY_CONSOLE_AUTH=token|app`, production-fatal if unset) AND `SKY_ADMIN_TOKEN` for
  the data endpoint. The Data tab is not reachable in production without both.

**Grill answer to "is edit safe to ship?":** No — read-only is the floor. Edit is
gated on a known codegen bug; when it ships (tuple-backed, Phase 5e) it is scalar-only,
`readwrite`-gated, tenant-scoped, audit-logged, and never touches session stores.

---

## 4. Session-store-as-collection (goal #1, §Phase-5 `:1184-1185`)

### 4.1 Failure mode this closes

Session RAM overflow + sessions lost on restart + a fourth store to configure. The
session Model should be a row in the data layer — persisted, restart-surviving, scaling
via the shared backend, with native expiry replacing the "8-byte-TTL dance"
(`:1184-1185`).

### 4.2 A new backend, NOT a replacement (the compose question)

`chooseStore` (`live_store.go:1501`) switches on `LIVE_STORE`: memory / sqlite /
postgres / redis, with a fail-loud default (`:1578`). Phase 5c **adds a case**:

```
case "data", "embedded", "bluedb":
    return newBlueDBStore(path, ttl, idleEvict)   // implements SessionStore
```

The existing four backends are **untouched** — `[data]` is opt-in via `LIVE_STORE=data`
(emitted by §1.3 once 5c lands). `newBlueDBStore` implements the SAME `SessionStore`
interface (`live_store.go:333`): `Get`/`Set`/`Delete`/`NewID`/`Close`/`Broker`/`Ping`.
The adapter is the missing `runtime-go/rt/live_store_bluedb.go` (confirmed absent on
this branch). It stores one collection row per session, keyed by `sid`, valued by the
`storableSession` blob (§2.3, now version-tagged).

### 4.3 The invariants that MUST hold (single-owner + mutex + fan-out)

These are load-bearing (CLAUDE.md §"Multi-tab"). The adapter preserves them by
**reusing the existing session machinery, not reinventing it:**

- **Single-owner shared pointer.** The durable backends keep an in-memory
  `memCache map[string]*liveSession` so `Get` returns the SAME pointer to every caller
  (`live_store.go:627` sqlite). `newBlueDBStore` keeps the identical `memCache` — the
  BlueDB row is the *durable* copy; the live pointer is still one-per-sid in RAM. So
  the per-session mutex (`liveSession.mu`, `live.go:2146`) still serializes all
  dispatch/render/persist on one owned object. **The store change does not touch the
  ownership model.**
- **Per-session mutex + serialization.** Unchanged — the mutex lives on the
  `liveSession`, not the store.
- **Multi-tab fan-out.** Unchanged — one `sseCh` per session + `fanOutFrame`
  (`live.go:6707`) via `ensureSSERelay` (`:6614`). The store is not in the fan-out path.
- **Native expiry.** BlueDB's version/TTL replaces the sqlite session TTL column dance;
  `Delete`/idle-evict (`live_store.go:728`) route to a collection delete. The idle-evict
  flush becomes a committer write (§5).

**Grill answer to "does it hold the single-owner + mutex + fan-out invariants?":** Yes
— because the adapter is a `SessionStore` swap *below* the `liveSession` ownership
layer. Every invariant lives on the `liveSession` object and the SSE relay, neither of
which the store touches. This is the same reason sqlite/postgres/redis already hold
them.

### 4.4 Scaling + reactive-scope interaction

- **Single-instance embedded** — sessions in the one `.blue` file, restart-surviving,
  zero-ops (the sqlite-equivalent default). The reactive gate
  (`bluedb_reactive_gate.go`) already treats the session store as **independent** of
  the data backend's replica scope (`:21-24`): a single-instance embedded data backend
  is correctly gated on `reactiveScope`, and its session store being embedded is not an
  additional hazard.
- **Multi-replica** — the session store must be a SHARED backend (CLAUDE.md production
  gate). `backend=postgres`/`cluster` puts sessions in the shared store; sticky
  sessions + the cross-instance broker rules (CLAUDE.md §"Horizontal scale") are
  unchanged. **`backend=embedded` with >1 replica is a config error for sessions** —
  the same single-instance constraint the reactive gate enforces for data, surfaced at
  the §1.7 boot check.

---

## 5. R1 async-persist funnel + durability tier (R1 `:1044-1078`, `:1221-1229`)

### 5.1 The confirmed bug (not inherited — closed)

Today there are exactly **three** `store.Set` sites (`live.go:4213` handleInitial,
`:4567` handleEvent, `:6235` handleSSE) + the idle-evict flush (`live_store.go:728`).
The async producers — `runPerformBody` (Cmd.perform completion, `live.go:5317`),
`dispatchBatched` (`:4672`), the `Time.every` tick loop (`~:5504-5604`), pub-sub,
WebSocket — **mutate the shared `*liveSession` under `sess.mu` and push an SSE frame
but NEVER call `store.Set`** (confirmed by the sqlite Set comment naming them as
"late-async results whose eventual Set must be corpse-guarded",
`live_store.go:603-610`). So on a durable backend an async mutation is **acked to the
browser (SSE frame shipped) but only flushed to disk on the NEXT handleEvent** — a
crash in that window loses an acked mutation. This is R1 exactly
(`:1046-1049`).

**Baseline for the sync path:** `handleEvent` today already persists BEFORE the ack —
`store.Set` at `:4567` runs before the POST `/_sky/event` response is written below
(`:4546-4548`, the acting-tab ack). So the sync path is persist-before-ack, but
**per-event, including keystrokes** — that is the fsync-per-keystroke tax the tier must
relieve.

### 5.2 One committer-gated funnel (grill fix #3 — `:1051-1058`)

All model-mutation-then-emit paths collapse into ONE chokepoint:

```
applyModelDelta(sess, delta)  →  committer.commit(Sync)  →  emitFrame(sess)
```

- **Every** async producer (`runPerformBody`, `Time.every`, pub-sub, WebSocket
  delivery, reactive refresh) and the sync `handleEvent`/`handleInitial`/`handleSSE`
  path routes through `applyModelDelta`. **No path emits a frame on its own.** The
  audit target the architecture names is WebSocket `sendToClient`, which today can
  deliver directly (`:1056-1057`) — it must be re-routed.
- The committer is the existing single-writer (`runtime-go/bluedb/committer.go`); on
  SQL backends the analogue is the session `store.Set` gated behind the same funnel.
- **If any path keeps its own emit, R1 recurs** (`:1058`) — so the funnel is enforced
  structurally: `emitFrame` is only callable from inside `applyModelDelta`'s tail, and
  the three current `store.Set` sites + the async SSE-push sites collapse to this one
  path. This is a real refactor of `live.go`'s emit surface, not a wrapper.

### 5.3 The durability TIER — the exact boundary (grill fix #3 — `:1060-1074`)

Persist-before-ack on *every* mutation would pay one fsync per keystroke for a single
typing user (no concurrency to amortize the group commit). The tier:

| Tier | What | Persist? | Ack contract |
|---|---|---|---|
| **Ephemeral input** | mid-type text (`onInput`), transient UI toggles, cursor/hover state | render WITHOUT fsync | frame ships immediately; loss on crash is acceptable (it is not a semantic commit) |
| **Semantic transition** | submit, status change, any mutation a user expects to survive restart | **persist-BEFORE-ack** | frame ships ONLY after `committer.commit(Sync)` returns |
| **Coalescing** | a single user's burst of semantic writes | one fsync per ~1–5 ms window (Nagle-style) | each write in the window acks after the shared fsync returns |

**How a mutation is classified.** The tier boundary is decided at `applyModelDelta`
from the **Msg / event kind**, not per-call-site (so it can never drift, `:1077-1078`):
- An `onInput`-sourced delta on a field the app has NOT marked durable → ephemeral.
- A form `onSubmit`, an explicit `Persist.*` transaction, a Cmd.perform completion
  carrying a semantic result, a `Time.every`-driven state advance the app treats as
  durable → semantic.
- The app declares the boundary once (e.g. which Msgs are semantic) rather than at each
  emit site; default-semantic for Cmd/tick/pubsub/WebSocket completions (the R1 paths),
  default-ephemeral for raw `onInput`. **This default is the safe one:** the paths that
  had the R1 bug (async completions) default to persist-before-ack; only literal
  keystrokes are demoted.

**Explicitly forbidden (`:1071-1074`):** persist-THEN-ack with an in-flight window
(ack the frame, persist afterward). That is "the R1 bug renamed." The ordering is
strictly persist-**before**-ack within the tier + coalescing window.

### 5.4 Proof sketch: no acked semantic transition is lost on crash

1. A semantic delta enters `applyModelDelta` (the only mutation path, §5.2).
2. `emitFrame` for a semantic delta is unreachable until `committer.commit(Sync)`
   returns (§5.3 row 2) — enforced by the funnel structure, not convention.
3. `commit(Sync)` returns only after the Pebble batch is `Apply(Sync)`'d to disk — the
   durability contract "acked only after recoverable" (`exp/bluedb:docs/bluedb/durability.md:7`,
   architecture `:955-957`).
4. Therefore: frame-acked semantic ⇒ committer-durable. The crash window between
   "async mutation" and "next event" that loses data today (§5.1) does not exist,
   because the async path now persists before it emits.
5. Ephemeral deltas that are lost on crash were never acked as durable (they are
   explicitly the lossy-safe tier) — so no *semantic* transition is lost.

**Interaction with retry storms (R4×R1, `:959-969`):** if the committing transaction
exhausts its retry bound, it returns a typed `Conflict` into `update()` AND **the frame
still acks (with the error result)** — it must never hang (`:967-969`). So the "ack on
every path" invariant holds even on the error path; the funnel acks a `Conflict`, never
nothing.

---

## 6. Sub-phasing (5a → 5e)

Ordered by dependency + risk. Each sub-phase is independently shippable (builds green,
sweep green, its own gate) and independently verifiable. The ordering is NOT arbitrary:
5a is pure config front-end (lowest risk, unblocks the rest); the session-store adapter
(5c) must precede `[data]` emitting `LIVE_STORE=data` (a wiring dependency called out
in §1.3); the async funnel (5d) is the highest-risk `live.go` surgery and depends on
the committer being the session persist path (5c); auto-admin (5e) depends on nothing
but the read surface and ships last because its edit form is gated.

### 5a — `[data]` config front-end (SUBSUME, no runtime change)
- **Build:** the `("data", …)` arm block in `read_sky_toml_config` (`build.rs:827`);
  `[data]` expands to existing `SKY_*` suffixes (§1.3); legacy sections retained +
  deprecation WARN (§1.5); the §1.7 boot consistency check; wire `reactiveScope` →
  `SKY_DATA_REACTIVE_SCOPE` (the gate at `bluedb_reactive_gate.go:35` already reads it).
- **Emits `LIVE_STORE=sqlite/postgres` (existing kinds) until 5c lands.**
- **Gate:** a `sky.toml` with only `[data]` builds an app whose sessions + app +
  analytics resolve to the same store; a unit test on `read_sky_toml_config` asserting
  the emitted `extra_defaults` for each backend (extend `build.rs:1472-1591` test
  style); the full example sweep stays green (legacy sections untouched).
- **Risk:** LOW (compile-time string mapping; no runtime code).

### 5b — `sky data` verb alias + migration helper + unified status
- **Build:** `sky data migrate/--gen/status` aliasing `cmd_db_*` (`main.rs:2592`);
  `sky data init --from-legacy` rewriting `sky.toml` (§1.5); `sky data status` reports
  app + analytics (both already schema-managed) in one table.
- **Gate:** `sky data migrate --gen` produces the same migration JSON as `sky db
  migrate --gen` for an app-table-only project (byte-identical, proving the alias is a
  no-op refactor); `--from-legacy` on a `[database]`+`[live]` app yields a valid
  `[data]` block that builds identically (round-trip test).
- **Risk:** LOW-MED (CLI plumbing; the diff engine is untouched).

### 5c — session-store-as-collection adapter (`live_store_bluedb.go`)
- **Build:** `newBlueDBStore` implementing `SessionStore` (`live_store.go:333`); the
  `chooseStore` case `"data"|"embedded"|"bluedb"` (`:1501`); `memCache` single-owner
  pointer reuse (§4.3); native expiry; **the blob version tag `BlobVersion`**
  (`storableSession`, `live_store.go:1247`) and `encode`/`decodeSession` stamping/reading
  it (§2.3). After this lands, 5a's `[data]` emits `LIVE_STORE=data`.
- **Gate:** the existing session-store conformance suite runs green against the new
  backend (mirror `live_store_restart_test.go` / `live_store_roundtrip_test.go` /
  `live_store_sqlite_hardening_test.go`); a restart test proves sessions survive; a
  pre-Phase-5 blob (BlobVersion=0) decodes without reset; the multi-tab fan-out +
  single-owner invariants pass (mirror `live_session_lifecycle_test.go`).
- **Risk:** MED (new backend, but a well-fenced interface; the invariants live above it).

### 5d — R1 async-persist funnel + durability tier
- **Build:** collapse the 3 `store.Set` sites + all async SSE-push sites into one
  `applyModelDelta → committer.commit(Sync) → emitFrame` chokepoint (§5.2); re-route
  WebSocket `sendToClient`; the durability tier classification (§5.3) with the safe
  defaults (async completions = semantic, raw `onInput` = ephemeral); the ~1–5 ms
  coalescing window; ack-on-error-path (R4×R1).
- **Gate:** a **crash test** — an async (Cmd.perform / Time.every) semantic mutation is
  acked, the process is killed before any subsequent event, restart proves the mutation
  survived (this is the R1 regression test that FAILS on today's code, §5.1); a
  keystroke-firehose bench proves ephemeral input does NOT fsync-per-keystroke; a
  hot-key retry test proves the frame acks a `Conflict` and never hangs.
- **Risk:** HIGH (`live.go` emit-surface surgery; the funnel must be structurally
  enforced or R1 recurs). This is the riskiest sub-phase — it is deliberately LAST
  among the runtime changes and depends on 5c (committer as session persist path).

### 5e — auto-derived Data tab (read-only floor; edit gated)
- **Build:** the net-new Data tab in the console (`State.sky:18-24` enum +
  `View.sky:196` bar), read list + row detail + `Cond` filter over declared
  collections (§3.2), following the `AnalyticsTab.sky:80` read-only pattern; the
  hardened data endpoint (PORT, `README.md:141-149`) + tenant scoping (PATTERN reuse,
  `hub_bridge.go:539-573`); regenerate the embedded console
  (`scripts/regenerate-console.sh`). **Edit form deferred to a 5e' add-on** —
  tuple-backed scalar-only (§3.3), shipped only after the `goty.rs:256-289` fix or via
  the tuple workaround, `readwrite`-gated.
- **Gate:** the Data tab lists + filters a declared collection in the console; a
  cross-tenant read returns nothing (fail-closed, mirror `hub_tenant_test.go:81-206`);
  the data endpoint refuses without `SKY_ADMIN_TOKEN`; session stores are not writable.
- **Risk:** MED (net-new UI; the edit form's codegen gate is explicitly out of the
  read-only gate).

**Is this honest sub-phasing or a hidden monolith?** Each sub-phase ships behind its own
gate and leaves the tree green: 5a is invisible until an app writes `[data]`; 5b is a
CLI alias; 5c is an opt-in store backend; 5d is a funnel refactor gated by a crash
test; 5e is a read-only console tab. The one genuine coupling — `[data]` wanting to
emit `LIVE_STORE=data` before 5c exists — is resolved by 5a emitting the existing store
kinds first and flipping to `data` after 5c (§1.3). No sub-phase requires another to be
*half-done*; each is a complete, verifiable slice.

---

## 7. Grill pre-emption (the attacks, answered)

- **Backward-compat / 30 examples + real apps.** §1.5: legacy sections retained +
  deprecation WARN for 2 minor versions; `[data]` wins on conflict with a build WARN;
  `sky data init --from-legacy` rewrites the manifest; the sweep stays green with ZERO
  example edits because nothing is removed. Real apps (skydeploy/sky-lang.org/
  darraghstudio) are on `exp/bluedb`'s `[bluedb]` (not on this branch), so their rename
  is a one-line, repo-local edit decoupled from the compiler release.
- **Session-blob migration correctness.** §2.3-2.6: version tag on the blob
  (BlobVersion, pre-Phase-5 blobs decode as 0 → no reset on upgrade); structural
  version hash from the Model shape; versioned `withMigrateFrom` chain applied once at
  decode, write-version-last for crash safety; an undeclared type change now WARNs with
  the version gap instead of silently resetting. A mid-deploy Model change is a clean
  version-and-migrate.
- **Durability-tier soundness.** §5.3-5.4: ephemeral input (keystrokes) = ack-without-
  fsync (lossy-safe); semantic transitions (submit/commit/async completion) = persist-
  before-ack; classification at the funnel from Msg kind (never per-call-site);
  persist-THEN-ack explicitly forbidden; proof sketch that frame-acked-semantic ⇒
  committer-durable; ack-on-error-path so the frame never hangs (R4×R1).
- **Auto-admin attack surface.** §3.3-3.4: read-only floor; edit gated on the
  `record_fieldset_collision` codegen bug (`goty.rs:256-289`) — ships tuple-backed +
  scalar-only + `readwrite`-gated later; tenant scoping via the v0.16.6 pattern
  (`hub_bridge.go:539-573`) fail-closed on missing identity; bearer + no-loopback +
  audit-log + session-stores-excluded from the hardened endpoint.
- **Scope realism.** §6: five independently gated sub-phases, each leaving the tree
  green; the one coupling (LIVE_STORE=data vs the adapter) is sequenced explicitly.

---

## 8. Open questions / weakest points (most likely to fail a grill)

1. **The durability-tier CLASSIFICATION is the softest claim (§5.3).** "Semantic vs
   ephemeral, decided at the funnel from Msg kind" needs a concrete, non-leaky
   mechanism. The design says "the app declares which Msgs are semantic" — but a
   default-semantic-for-async / default-ephemeral-for-onInput heuristic can misclassify:
   a `Time.every` tick that is genuinely ephemeral (a clock display) would fsync every
   second; an `onInput` that IS semantic (a draft the user expects to survive) would be
   lost. **Weakest point:** if classification is wrong, we either reintroduce the
   fsync-per-tick tax OR silently drop a mutation the user thought was durable. This
   needs a first-class app-facing declaration (e.g. a `Persist.durable`/`Persist.ephemeral`
   marker on the Msg or the delta) designed in detail before 5d — the heuristic alone
   is not defensible.

2. **The structural Model-version HASH (§2.4) may be too coarse or too fine.** gob
   tolerates field add/remove but not type change; a hash over field-name→gob-type must
   flip on exactly the type-change case and NOT on the tolerant cases — otherwise it
   either forces needless migrations (too fine) or misses a breaking change (too
   coarse, → the silent reset returns). Whether the compiler can derive precisely
   gob's compatibility relation from the HIR Model type is unproven and needs a
   focused spike (the fallback `[data] modelVersion = N` is the hand-guarded
   anti-pattern R9 forbids).

3. **§5.2's funnel is a real `live.go` refactor, not a wrapper — and `live.go` is
   9067 lines.** Structurally enforcing "no path emits a frame except through
   `applyModelDelta`" across handleInitial/handleEvent/handleSSE/runPerformBody/
   dispatchBatched/Time.every/pub-sub/WebSocket is broad surgery with cascade risk
   (the CLAUDE.md "cascade-risk" class). If the funnel is enforced by convention rather
   than by making `emitFrame` unreachable outside the funnel, R1 silently recurs on the
   next new emit site. This is the highest-execution-risk claim in the doc.

4. **Cross-store atomicity is only real on the embedded/cluster backend (§2.6).** On
   SQL backends the migration across app-tables + analytics + session-blob is
   per-store forward-only with a documented recovery order — NOT atomic. A grill that
   demands "one migration, atomic across all three" for the postgres backend will find
   the honest answer is "no, and here's why (SQL DDL is not two-phase across
   databases)." The doc states this loudly, but it is a genuine limit of the collapse,
   not a solved problem.

5. **The Postgres one-DSN-three-namespaces model (§1.6) is asserted, not benchmarked.**
   Sessions + analytics + app on one Postgres sharing one connection budget is an ops
   claim; a real multi-tenant SaaS at scale may need them split, which partially
   *un-collapses* the config. The design keeps the escape hatch (per-suffix env
   override still works) but the "one store for everything" headline is strongest for
   embedded/single-instance and softens at postgres scale — which the architecture's
   own grill fix #11 (`:369-373`) already concedes.

6. **`DataTab.sky` is net-new, contradicting the architecture's "ADAPT/PORT" framing
   (`:385`, `:922`, `:1204`).** The architecture repeatedly cites
   `sky-bundled/console/src/DataTab.sky` as an existing file to adapt; it does not
   exist on `feat/bluedb` (only in a worktree). So the auto-admin is more work than the
   roadmap implies. Minor, but a grill checking the reuse list will catch the
   discrepancy — flagged here so it is not a surprise.
