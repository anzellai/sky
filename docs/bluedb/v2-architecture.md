# BlueDB v2 — architecture

> **Status:** architecture design for branch `feat/bluedb-v2` (off `origin/main` @ `fdbc398d`).
> This document is what the grillers attack and what the phases implement. It contains no
> production code — contracts, encodings, signatures, gates, and a phase plan.
>
> **Authority:** `.claude/AUTONOMOUS_GOAL.md` on this branch. The five goals and RULE ZERO
> there are the definition of done; this document is subordinate to it and may not narrow it.
>
> **Relationship to `feat/bluedb`:** that branch is *research, not truth*. Its verified
> substrate (Pebble + MVCC-in-key + the `base.CheckComparer` gate, the single-writer committer
> with HLC floor, the changefeed, SSI read-set validation, the errorfs crash corpus) is kept.
> Everything above it is redesigned here. §0 lists every premise of the prior work that this
> design found to be false.

---

## 0. Premise audit — what the prior work asserted that is not true

The prior attempt repeatedly built on claims that did not hold. Every claim this design
relies on was re-verified against source. The following were found FALSE or materially
misstated. Each is cited so a griller can check it in one command.

### 0.1 About the current tree (`feat/bluedb-v2` = `origin/main` + the mandate doc)

| # | Claim | Reality |
|---|---|---|
| P1 | "`[data] driver` is a no-op" implies `[data]` exists | **`[data]` does not exist on `main` at all.** `read_sky_toml_config` (`rust/crates/project/src/build.rs:776-848`) handles `[database]` only. `[data]` is `feat/bluedb`-only. v2 introduces it net-new. |
| P2 | `Std.Persist` shipped (memory `std_persist_unified_data.md`, 2026-08-04) | **Not on `main`.** `sky-stdlib/Std/` has no `Persist.sky`. It exists only on `feat/bluedb` / `exp/bluedb`. Net-new here. |
| P3 | `DB_DRIVER` is written and read by nobody | **CONFIRMED, and worse: it is documented as functional.** Written at `build.rs:802`; zero readers in `runtime-go/`, `sky-stdlib/`, `sky-bundled/`. Driver is chosen by DSN shape (`detectDriver`, `runtime-go/rt/db_auth.go:337-351`). Documented as a working knob at `docs/sky-toml.md:202` and `docs/skydb/overview.md:558`. Pinned green by `build.rs:1442`. |
| P4 | Goal #2's "real SERIALIZABLE" merely lacks cross-backend unity | **On `main` it does not exist at any backend.** `Std.Db` exposes exactly one transaction verb — `withTransaction : Db -> (Db -> Task Error a) -> Task Error a` (`sky-stdlib/Std/Db.sky:216`) — implemented with a bare `d.conn.Begin()` (`db_auth.go:1364-1407`) = driver default = **READ COMMITTED on Postgres**. No isolation type, no isolation argument, no `40001` retry anywhere (`grep -rn "40001" runtime-go/` → empty). `Sky.Core.Error.isRetryable` returns `False` for `Conflict` (`Error.sky:192-209`). |
| P5 | `kernel_api.rs` + the `kernel_api_covers_registered_kernel_functions` CI gate exist | **Both deleted** (commit `054f6d26`). The gate exists in no workflow. **`AGENTS.md:258` still documents it as current and "fails CI on drift".** A durable instruction file asserting a phantom enforcement mechanism — fix in the same commit as this document's first phase. |
| P6 | A CI gate guards console drift | **NOT FOUND.** `grep -rni "drift\|console" .github/workflows/` → zero hits across all five workflow files. |
| P7 | The `goty.rs` record-fieldset collision lives in `codegen` and blocks the console edit form | **Path wrong, and it does not block.** The function is `select_record_candidate` at **`rust/crates/lower/src/goty.rs:274-302`** (`crates/codegen/src/` contains only `lib.rs`). It selects by field *type* (landed `1a7142f6`, v0.19.1). The erased-`any` recurrence is real but is a *documented workaround* (use a tuple), not a blocker. |
| P8 | `-tags pebblegozstd` is a non-negotiable already in force | **The string appears nowhere in the repo** except the mandate doc. CI runs `CGO_ENABLED=0 go test ./rt/...` (`rust-ci.yml:146`, `:219`, macOS job), which independently forecloses the cgo-zstd link path *for tests*. The real exposure is different and still open: **`sky build`'s CGO_ENABLED=1 FFI-retry path** (`build.rs:569`, `:576-595`) would link cgo DataDog zstd into a *shipped app* while the CGO=0 path links pure-Go klauspost. The requirement is therefore on `run_go_build_once`, not on `go test`. |
| P9 | `SchemaOf` exists | **Did not exist** when the prior phases relied on it; it exists **only** on `salvage/p5e-foundation` (`runtime-go/bluedb/embedded.go`, added by that branch). It is a *deliverable of the salvage branch*, not a pre-existing facility. |
| P10 | Sessions are serialised as JSON | **gob.** `docs/skylive/architecture.md:376` is wrong; `encodeSession` at `runtime-go/rt/live_store.go:1278`. |
| P11 | `docs/skylive/tiered-session-cache.md` describes a proposal | It says `Status: PROPOSED` but the cache **shipped** (`a6b4c443`), and its `decodeSession` line citation no longer resolves. Stale doc. |

### 0.2 About `feat/bluedb`'s own claims (research read as research)

| # | Claim in the prior docs | Reality in the prior code |
|---|---|---|
| P12 | `P.index` declares a secondary index | **There is no secondary-index keyspace.** `keys.go:19-22` defines the entire keyspace: `tagData 0x00`, `tagChangelog 0x01`, `tagMeta 0x02`. `index_key.go` is a *validation-coordinate* encoder whose output only ever lands in `IndexCoord.Key` or a read-set bound. |
| P13 | Scans are O(all rows in the collection) | **Worse.** The *precise* (declared-index) transactional path calls `tx.reader.Iterate(nil)` (`txn.go:566`) — the whole data keyspace across every collection — and recomputes `tx.indexCoords(k,v)` per row. The *unindexed* fallback `ScanCollection` prefixes by collection and is strictly cheaper. **Declaring an index makes a transactional query slower.** |
| P14 | `Backend.Capabilities()` gates multi-replica reactivity at startup | `Capabilities()` returns `CrossInstanceReactive: true` (`embedded.go:466-474`) — the opposite of the corrected matrix — **and has no production reader**. The real gate is a string classification of bindings behind a `sync.Once` on the *first session*. |
| P15 | The cross-instance reactive bridge exists and RG#1's fix ("empty tenant skips the broker publish") closes a leak | **The cross-instance path does not exist.** `grep "reactiveTenantTopic\|__bluedb:" runtime-go/` → nothing; `rt/bluedb_reactive.go` has no Broker reference. There is no publish to skip; RG#1's fix is vacuous, and the promised `Persist.withTenant` escape hatch was never built. |
| P16 | A dropped reactive delivery "self-corrects via the resync path, never a permanent silent loss" | **No production consumer reads the resync latch.** `NeedsResync()` / `ResyncPending()` are called only from tests; `markResyncAll` latches a flag nothing reads; `drainReactiveBurst` discards every `Change`. A drop while the rt loop is inside `reactiveRefreshOnce` leaves the session permanently stale. This is the same "gate that cannot fail" class the branch's own RESUME warns about — still open there. |
| P17 | Reactivity is query-scoped | Detection is query-scoped; **delivery is not**. The computed `Transition`/`Record`/`OrderChanged` are discarded and the consumer re-runs the full query (`Persist.toList` → full collection scan + full codec decode) **per session per notification**. |
| P18 | The index read-set range test is a biconditional (`⟺`) | Docs specify half-open `[lo, hi)`; shipped code uses **closed `[lo, hi]`** (`index_key.go:107-108`, `validate.go` `inRangeClosed`). Direction is safe (over-reject) but the stated `⟺` is false at the upper boundary. |
| P19 | ADR-001: backing sessions with a collection is "not a blocker" because TEA Models are typed records | **Unshown.** `Persist.collection` requires a `Codec a` *value supplied from Sky*, while the funnel's persist point holds `sess.model` as untyped `any` on the Go side. There is no mechanism by which `rt` obtains a codec for the app's Model. §4 dissolves this rather than assuming it. |
| P20 | `[data]` collapses sessions + app data + analytics into one backend | `[data] path` seeds **only** `DB_PATH`; `sessionPath` and `analyticsPath` remain separate keys. And `backend = "embedded"` emits `DB_DRIVER=embedded`, which nothing reads, so `Db.connect ()` opens **SQLite**. There is no config-driven way to select the embedded engine at all. |

**What this list is for.** Nothing in §1–§11 depends on an unverified claim. Where this design
relies on a fact, the fact is cited. Where a mechanism must be built that the prior work
*described* but did not build, it is listed as NET-NEW in §10, not as a port.

---

## 1. Goal → mechanism → gate

The five goals are quoted verbatim from `.claude/AUTONOMOUS_GOAL.md`. For each: the mechanism
that delivers it, the numbered gate that proves it, and — where the goal cannot be delivered
in full — the bound, stated plainly.

| # | Goal (verbatim) | Mechanism | Gate | Fully delivered? |
|---|---|---|---|---|
| 1 | *Session-bounded Model state sync.* | Sessions become a `_sky_sessions` Persist collection (§5); a two-part **count + bytes** ceiling over a resident cache; SSE-connected sessions become **deflatable** (spill Model/tree/bodies to the store, keep the connection) rather than immortal; provisional admission so a crawler GET does not mint a resident session; coalescing per-connection outbox | **G1.1** ceiling under N *active* SSE sessions · **G1.2** correctness across spill/rehydrate · **G1.3** no acked-then-lost across spill · **G1.4** provisional admission | **Yes for Model state.** Bound: per-SSE-connection overhead (goroutine + one coalesced frame) is linear in *connected clients* and is not eliminable — measured and published as a floor, not hidden. |
| 2 | *Unified store: high-throughput lock-safe parallel + scalable + reliable + ACID (**real SERIALIZABLE**) + secure, with UNIFIED APIs shareable across dbs (sqlite/postgres/bluedb).* | One promise, no isolation knob (§2): embedded SSI, sqlite WAL + `BEGIN IMMEDIATE` write-serialisation with a **split reader/writer pool**, postgres `SERIALIZABLE` + internal bounded retry; a closed driver registry that fails closed on an unknown driver; real index seeks over the MVCC keyspace (§3); durable engine-attested tenancy in the key (§5) | **G2.1** isolation conformance, **all three backends**, discriminating · **G2.2** index-seek complexity · **G2.3** index↔data consistency under crash · **G2.4** transact-body replayability (compile-time) · **G2.5** cross-tenant structural impossibility · **G2.6** substrate crash corpus | **Serializable: yes, all three.** Bounds: sqlite and embedded are **single-machine** (one writer); postgres SERIALIZABLE is not *strict* (no real-time order guarantee); "high-throughput parallel" means concurrent readers + one writer on sqlite/embedded, and true multi-writer only on postgres. Published as numbers, not adjectives. |
| 3 | *Easy + simple; low-level APIs only for the 0.001%.* | One `[data]` section wired end-to-end through a generated glue file (§7); one `Persist` API with no backend named in app code; zero-config default (embedded, `data/app.blue`); one migration story; `sky doc Std.Persist` generated from source | **G3.1** the 10-line app builds + runs + persists across restart, with **no** `[data]` section · **G3.2** doc-examples gate over `docs/skypersist/` · **G3.3** graduation: same source, `[data] driver` flipped embedded→sqlite→postgres, all three pass the same behavioural contract | Yes. |
| 4 | *Notify clients of changesets (query/row-scoped, in the commit path).* | Reactivity moves from the embedded commit path to the **Persist commit boundary**, so all three backends deliver; a `ChangeBus` (local / postgres `LISTEN`+changelog / redis) for multi-replica; the delta is **applied**, not used as a "go re-query" nudge | **G4.1** changeset delivery on **all three** backends · **G4.2** cross-tenant non-delivery · **G4.3** fan-out cost floor at realistic N · **G4.4** multi-replica misconfiguration is a **startup** fatal, never a first-session `os.Exit` · **G4.5** no permanently-stale session after a forced drop | **Yes, with a stated degradation ladder.** Bound: cross-replica delivery is at-least-once with gap recovery; postgres `NOTIFY` payload carries a summary, not the row body. |
| 5 | *Built-in Sky Console admin access to records.* | The `salvage/p5e-foundation` authorization funnel (zero-trust-input `Decide()`, fail-closed ordering, allow-list disclosure) ported onto §5's durable tenancy, which converts the "forgeable tenant column" from a documented weakness into a structural impossibility | **G5.1** funnel decision matrix incl. fail-closed ordering · **G5.2** a scoped admin read cannot cross tenants *even against adversarial row contents* · **G5.3** console Data tab e2e in a real browser | **5e-1 (read) yes.** **5e-2 (write) is specified but NOT decided** — read-only vs read-write is the user's call per the mandate. §8 designs read complete and specifies write precisely; it does not choose. |

Cross-cutting, serving all five:

| Gate | Proves |
|---|---|
| **G0.1** | `docs/bluedb/STATUS.md` is generated and matches a fresh run (hand edits detected) |
| **G0.2** | `rt` never imports `bluedb`; `bluedb` imports only pebble + stdlib |
| **G0.3** | A non-Persist app links no pebble, builds cold-cache offline, and ships no `bluedb/` |
| **G0.4** | No dead config key: every env suffix the compiler writes has a runtime reader, and vice versa |
| **G0.5** | `sky build` passes `-tags pebblegozstd` on **both** the CGO=0 and the CGO=1 retry path |
| **G0.6** | Every gate's recorded mutation still applies and still turns it red |

---

## 2. A1 — one isolation contract across embedded / sqlite / postgres

### 2.1 What is wrong today

Three separate defects, on two different branches:

1. **On `main` there is no serializable path at all.** `Db.withTransaction` → `d.conn.Begin()`
   (`db_auth.go:1364-1407`). Postgres gets READ COMMITTED. Write skew is available to every
   Sky app today.
2. **On `feat/bluedb`, sqlite's "serializable" is a pool clamp.** `dbSerializableTxAttempt`
   (`db_auth.go:1529-1587`) emits `BEGIN IMMEDIATE` over a pinned `*sql.Conn`, but the actual
   serialisation comes from `conn.SetMaxOpenConns(1)` applied unconditionally at connect
   (`db_auth.go:305-321`) for *every* SQLite pool. The branch's own test says so in its name:
   `TestWriteSkewSQLiteReadCommittedAlsoHolds_MaxConns1`
   (`persist_writeskew_test.go:129-141`). The requested isolation level is decorative.
3. **The dispatch fails open.** `if serializable && d.driver != "pgx"` (`db_auth.go:1535`) sends
   *any* future driver down the SQLite arm, while the `SetMaxOpenConns(1)` clamp that arm
   relies on is guarded by `if driver == "sqlite"`. A `mysql`/`duckdb`/`libsql` driver would
   take the sqlite path *without* the clamp and be silently non-serializable.

### 2.2 The decision: delete the knob

**Sky offers no isolation level.** There is one contract, and every backend either meets it or
refuses to start.

> **The Persist transaction contract.**
> 1. **Serializable.** Every committed `Persist.transact` is equivalent to some serial execution
>    of all committed transactions (conflict-serializable / ANSI SERIALIZABLE). Reads performed
>    outside a transaction observe a consistent committed snapshot.
> 2. **Automatic conflict resolution.** A transaction that cannot be so ordered is re-executed
>    internally with bounded jittered backoff. Only after the bound does the caller see
>    `Error Conflict`. Application code never handles `40001`, `SQLITE_BUSY`, or an SSI
>    validation failure.
> 3. **Durable on ack.** A returned commit survives process crash on every backend, and host
>    power loss when `[data] durability = "full"` (the default).
> 4. **Scoped by construction.** Every read and write is confined to the transaction's tenant
>    key-range (§5). There is no API that takes a tenant from data.

Why no knob: a per-backend capability cannot be expressed in Sky's type system — the backend is
a *runtime* value (the DSN arrives from the environment at boot; the image is built once), and
HM types cannot depend on a runtime value. The prior work reached this conclusion correctly
(`clean-slate-architecture.md`, Grill outcome 2) and then, inconsistently, shipped
backend-named connect functions (`connectKeyValue` / `connectRelational`) that put the backend
back into app source. v2 resolves it the other way: the *contract* is uniform, so there is
nothing to gate; the only genuinely non-portable surface (raw SQL) is handled in §7.4.

### 2.3 Per-backend mechanism

**Embedded (bluedb).** Unchanged from the verified substrate: begin-snapshot at
`readTs = durableHi`, point + index-range read-set (`readset.go`), commit-time validation over
the `(readTs, commitTs]` window (`validate.go`), single-writer committer, `Apply(pebble.Sync)`
before ack. Index-range recording is what makes it *serializable* rather than snapshot-isolated
— it witnesses predicate phantoms. §3 changes how a range is *read*, not what is *recorded*.

**SQLite.** True serializable is achievable, and the mechanism is not the pool clamp:

- **Split the pool.** One dedicated `*sql.Conn` for writes; a reader pool of
  `min(4, GOMAXPROCS)` connections. This replaces `SetMaxOpenConns(1)`, which today also
  serialises *reads* — a throughput bug and a self-deadlock hazard (a held transaction starves
  every other query of the single connection).
- **Every read-write transaction is `BEGIN IMMEDIATE`** on the writer connection. SQLite's
  write lock is taken at BEGIN, so read-write transactions execute in a strict serial order,
  machine-wide (it is a file lock, so it holds across processes too).
- **Read-only transactions and bare queries are `BEGIN DEFERRED`** on a reader connection under
  WAL, i.e. a consistent snapshot.

  *Why this is serializable, precisely.* The general claim "a read-only transaction under
  snapshot isolation is serializable" is **false** — that is exactly the Fekete et al. read-only
  anomaly, where a read-only transaction observes a state produced by two *concurrent* write
  transactions forming a dangerous structure. The argument here does not rely on that claim. It
  relies on the previous bullet: because every read-write transaction takes the write lock at
  `BEGIN`, **no two write transactions are ever concurrent**. The committed write history is a
  total order `T₁ < T₂ < …`, a reader's snapshot is exactly the state after some `T_k`, and a
  transaction that writes nothing can always be placed immediately after `T_k` without creating
  a cycle. The dangerous structure the anomaly requires cannot be constructed. Remove
  `BEGIN IMMEDIATE` and this argument collapses — which is precisely what the
  `sqlite-deferred` mutation in §2.5 demonstrates.
- **No deferred-then-write upgrade.** `Persist.transact` always takes the writer path;
  `Persist.read` / plain queries always take the reader path. This removes
  `SQLITE_BUSY_SNAPSHOT` upgrade aborts as a class rather than retrying them.
- **`PRAGMA synchronous = FULL`** on the writer connection when `[data] durability = "full"`.
  Today's `NORMAL` (`db_auth.go:305-321`) does not fsync per commit under WAL, so an acked
  transaction is durable only against process crash — the exact `A2` grill finding, deferred on
  `feat/bluedb`. It is not deferred here; it is a config key with a safe default.

  *Honest bound:* one global writer. Write throughput is one transaction at a time per
  database file, machine-wide. This is a **throughput** bound, not a correctness one, and it is
  published as a measured number by G2.1's throughput arm.

**Postgres.** `BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})` — per-transaction,
never a session default. Retry on `40001` (serialization_failure) and `40P01` (deadlock_detected),
classified from a typed `*pgconn.PgError`, not a substring match. (The prior implementation's
string fallback — `"could not serialize"`, `"database is locked"`, … at `db_auth.go:1479-1487` —
is kept only as a diagnostic breadcrumb, never as the classifier.)

*Honest bound:* postgres SSI provides serializability, not **strict** serializability — two
transactions may be serialized in an order that contradicts their real-time order. Sky does not
promise strict serializability on any backend and says so in `docs/skypersist/`.

### 2.4 Failing closed on an unknown driver

The `driver != "pgx"` predicate is deleted. Driver selection becomes a **closed registry**:

```go
// package rt — no bluedb import.
type Driver string
const (
    DriverEmbedded Driver = "embedded"
    DriverSQLite   Driver = "sqlite"
    DriverPostgres Driver = "postgres"
)

// IsolationStrategy is what a driver MUST supply to be usable. There is no default
// implementation: a driver with no strategy cannot be constructed.
type IsolationStrategy interface {
    BeginWrite(ctx context.Context) (Tx, error) // serializable read-write
    BeginRead(ctx context.Context) (Tx, error)  // consistent snapshot, read-only
    IsConflict(err error) bool                  // typed classification, no substrings
    SelfTest(ctx context.Context) error         // §2.5 — run at boot
}

var strategies = map[Driver]func(*sql.DB, DataConfig) (IsolationStrategy, error){}
```

`OpenData` looks the driver up in `strategies`. A miss is a **startup fatal** naming the driver
and the file to add it to. There is no arm that "falls through" to a weaker guarantee. Adding a
driver without a strategy fails to open, not fails silently.

DSN-shape sniffing (`detectDriver`) is demoted to a *hint used only when `[data] driver` is
absent* and, when it disagrees with an explicit `[data] driver`, is a startup fatal. This closes
the prior branch's silent `driver = "postgres"` beside `./app.db` → opens SQLite (P20).

### 2.5 Verification — the discriminating conformance suite (G2.1)

Asserting isolation is worthless; the prior branch asserted it three times. The suite is a
**parameterised anomaly corpus** driven through the *Sky-level* `Persist` API — not through
backend-specific SQL — and run against all three backends in one gate.

Anomalies (Adya / Hermitage naming), each a two-or-three-transaction interleaving:

| ID | Anomaly | Prevented by RC? | by SI? | by SERIALIZABLE? |
|---|---|---|---|---|
| A-G0 | dirty write | yes | yes | yes |
| A-G1a | dirty read | yes | yes | yes |
| A-G1b | intermediate read | yes | yes | yes |
| A-G1c | circular information flow | no | yes | yes |
| A-OTV | observed transaction vanishes | no | yes | yes |
| A-PMP | predicate-many-preceders | no | yes | yes |
| A-P4 | lost update | no | yes | yes |
| A-GS | read skew (G-single) | no | yes | yes |
| **A-G2i** | **write skew (G2-item)** | no | **no** | **yes** |
| **A-G2** | **anti-dependency cycle (predicate write skew)** | no | **no** | **yes** |
| **A-PMPW** | **predicate-many-preceders, write variant** | no | **no** | **yes** |
| **A-PH** | **phantom under an index seek** (§3) | no | **no** | **yes** |

The three bold rows plus A-PH are the **discriminators**: a backend that quietly gives snapshot
isolation passes everything above them and fails those. A backend that gives read committed
fails from A-G1c down. The suite therefore *distinguishes* rather than *certifies*.

**A-PH is new and specific to this design.** It exists because §3 introduces index seeks: a
seek reads *fewer* physical keys than a full scan, so a read-set that recorded only returned
keys would miss a phantom inserted into the seeked range. A-PH runs a seek-backed query,
concurrently inserts a row *into the seeked range*, and requires rejection. Without it, §3 could
silently weaken §2.

**Gate mechanics.**

```
cargo run -p xtask -- bluedb-gates --only=G2.1
  → embedded : in-process, temp dir
  → sqlite   : temp file, WAL
  → postgres : SKY_TEST_POSTGRES_DSN, or an ephemeral local cluster if absent
```

Postgres is not optional. The prior branch's single discriminating proof never executed for
weeks because the test read `SKY_TEST_PG_URL` while CI set `SKY_TEST_POSTGRES_DSN`
(`RESUME.md`). Here: a backend that cannot be reached is a **FAIL**, never a skip, and the gate
prints which of the three ran.

**Mutation proofs** (recorded in `docs/bluedb/mutations/G2.1.*.patch`, applied and verified by
`--verify-mutations`):

| Mutation | Expected |
|---|---|
| `embedded`: stub `validate()` to always accept (build tag `bluedb_mutation`) | A-G2i, A-G2, A-PMPW, A-PH go RED |
| `sqlite`: `BEGIN IMMEDIATE` → `BEGIN DEFERRED` on the writer | A-G2i, A-G2, A-PMPW go RED |
| `sqlite`: collapse the split pool back to `SetMaxOpenConns(1)` | throughput arm shows read concurrency = 1 → RED |
| `postgres`: `LevelSerializable` → `LevelRepeatableRead` | A-G2i, A-G2, A-PMPW go RED |
| registry: reinstate a `default:` arm for unknown drivers | G2.1's unknown-driver arm goes RED (it must fatal) |

Because each mutation turns a *different* subset red, the gate cannot be satisfied by a single
accidental invariant.

### 2.6 Making the bound visible before 3am

Compile-time backend typing is impossible (§2.2). Three mechanisms substitute, in order of
earliness:

1. **Build time.** `sky build` reads `[data]`. If the app statically references the SQL escape
   hatch (`Std.Persist.Sql.*`) the compiler records that in the generated glue; a build whose
   `[data] driver = "embedded"` and which references `Persist.Sql` is a **compile error** with
   the file:line of the offending call. This is the closest thing to compile-time capability
   checking that is sound, and it is real: the reference is static even though the DSN is not.
2. **Startup.** `IsolationStrategy.SelfTest` runs one A-G2i write-skew probe against the real
   configured database during boot, before the listener opens, behind
   `[data] startupSelfTest = true` (default true; `false` for read-replica boots). A backend
   that does not prevent it refuses to start with the anomaly named. This is the "3am" defence:
   a misconfigured Postgres (e.g. a pooler in transaction mode that breaks SSI) fails at deploy,
   not under load.
3. **Runtime.** `Error Conflict` after the retry bound is a typed, documented outcome with a
   `Retry-After`-style hint, surfaced in the console.

---

## 3. A2 — the index / seek layer

### 3.1 What exists and what does not

`index_key.go` on `feat/bluedb` is a **validation-coordinate encoder**, not an index. There is
no index tag in the keyspace (`keys.go:19-22`). Its output feeds `IndexCoord.Key` (changelog
witnesses) and `Txn` read-set bounds. The *storage* half was left as
`TODO(phase3b/4)` (`embedded.go:349-352`) and never landed. Consequences measured in §0.2/P13:
the declared-index transactional path iterates the **whole database** and is slower than the
undeclared one.

The good news is that the hard part — a single canonical order-preserving encoder shared by
scan bounds and change coordinates — already exists and is proven. v2 **promotes** that encoder
from validation-only to physical, extends it, and adds a keyspace.

### 3.2 Key encoding

BlueDB's MVCC layer treats the *user key* as opaque bytes: `encodeDataKey(userKey, commitTs)`
= `0x00 ‖ userKey ‖ 0x00 ‖ ~(wallMs BE8 ‖ logical BE4) ‖ 0x0D` (`keys.go`), and `Split` reads
the trailing length byte arithmetically without inspecting the user key. **Therefore the entire
index and tenancy design lives inside `userKey` and requires no change to `skydb.mvcc.v1`.**
The irreversible `base.CheckComparer` gate is untouched. This is load-bearing: it means §3 and
§5 are additive to the frozen substrate, not a store rewrite.

Two user-key namespaces:

```
ROW      userKey = 0x01 ‖ ten ‖ collID(BE4) ‖ pk
INDEX    userKey = 0x02 ‖ ten ‖ idxID(BE4)  ‖ cols ‖ pk
```

where

- `ten` — the tenant component, order-preserving and self-terminating (§3.3 escaping). For
  `[data] tenancy = "single"` it is the one-byte constant `0x01 0x00 0x01` (escaped empty
  string), so single-tenant apps pay 3 bytes and no branch.
- `collID` / `idxID` — stable `uint32` identifiers assigned by the migration ledger, **not**
  hashes of the name. A rename does not rewrite the keyspace; a drop retires the id forever.
- `cols` — the concatenation of the indexed columns' encodings (§3.3), each preceded by a
  null-tag byte.
- `pk` — the row's primary key, appended so index entries are unique even for a non-unique
  index, and so a seek yields pks directly.

Index entries are stored **as ordinary MVCC rows** (they go through `encodeDataKey`). This is
the single most important structural choice in §3: index entries inherit atomic commit, MVCC
visibility, snapshot reads, tombstones, and GC from the substrate for free. There is no second
storage path to keep consistent, no second GC, no second crash-recovery story.

Index entry **value** is empty. v2 has **no covering indexes**: a seek yields pks, then the
planner issues point gets. Cost is `O(log n + k·log n)`; `k` is the matched-row count, and the
point gets hit the same block cache. Covering indexes are explicitly deferred (§11).

**Versioning.** The index encoding carries its own version in the metadata keyspace
(`meta: "index_encoding" = "skydb.idx.v1"`). A change requires an **index rebuild**, not a store
rewrite — a `sky data reindex` operation — because index entries are derivable from data. This
is strictly weaker than the comparer's irreversibility and should not be conflated with it.

### 3.3 Order-preserving column encodings

Each component is `nullTag ‖ body`, `nullTag ∈ {0x00 null, 0x01 present}`, so NULLs sort first
and a NULL can never collide with a value.

| Sky type | Body | Order-preserving? |
|---|---|---|
| `Int` | BE8, sign bit flipped (`b[0] ^= 0x80`) | yes (existing `ColInt`) |
| `Bool` | one byte `0x00` / `0x01` | yes (existing `ColBool`) |
| `String` | escaped UTF-8 (below) | yes, **byte order** — not locale collation (§11) |
| `Time` | int64 micros → BE8, sign bit flipped | yes |
| `Float` | IEEE-754 total order: `u := Float64bits(f); if u>>63 == 1 { u = ^u } else { u |= 1<<63 }`, BE8. `-0.0` normalised to `+0.0`. `NaN` **rejected** at write with a typed error | yes (**net-new**) |
| `Decimal`, `Money`, `Bytes` | — | **no** — see below |

**Escaping for variable-width components.** A composite key with a variable-width component in
a non-final position is ambiguous unless escaped. (The prior code sidestepped this with
`checkCompositeLayout`, which *panics at encode time* — `index_key.go:144-164` — a latent trap
the design doc itself said must move to construction time and never did.) v2 uses the standard
escape:

```
0x00  →  0x00 0xFF        (escape)
end   →  0x00 0x01        (terminator)
```

This preserves byte order (`0x01 > 0xFF` is false, so the terminator sorts below any escaped
byte, which is what makes prefix comparison correct) and makes the component self-delimiting.
Fixed-width components (Int/Bool/Float/Time) are written raw with no terminator. With this,
**any** column order in a composite index is legal, and `checkCompositeLayout` is deleted rather
than relocated.

**Decimal / Money / Bytes.** No order-preserving encoding ships in v2. Rather than silently
degrading to a full scan (today's behaviour, which is how "declaring an index made it slower"
went unnoticed), `Persist.index "price"` on a `Decimal`/`Money`/`Bytes` column is a **build-time
error**. This is checkable because the collection declaration is static and
`Std.Codec.Shape = SRecord (List (String, ColType)) | SScalar ColType | SBlob`
(`sky-stdlib/Std/Codec.sky:78-80`) gives each column's type at compile time. `sky check` rejects
it naming the column, the type, and the two supported alternatives (index a derived integer
minor-unit column; or accept it as a residual predicate). Loud beats silent. Deferred to a later
cycle with a named design (§11).

### 3.4 Range extraction from a `Cond` tree

```go
type Span struct {
    Index    IndexID
    Lo, Hi   []byte
    LoIncl   bool
    HiIncl   bool
    Reverse  bool
}

type Access interface{ isAccess() }
type FullScan  struct{ Coll CollID }        // collection-prefixed — never the whole keyspace
type IndexSeek struct{ Span Span }
type PointGet  struct{ Coll CollID; PK []byte }

type Plan struct {
    Access                Access
    Residual              *CondNode // predicates the span does not imply; evaluated per row
    Order                 []OrderCol
    OrderSatisfiedByIndex bool      // when true, no in-RAM sort
    Limit, Offset         int
    Estimate              PlanEstimate // keys the planner expects to visit
}

func BuildPlan(q QueryPlan, schema CollSchema) Plan
```

**Extraction rule (leading-prefix, deterministic).**

1. Flatten the top-level `CondAnd` into a conjunct list. `CondOr` / `CondNot` are **not**
   decomposed into spans in v2 — they become residual. (A single-column `CondIn` *is*
   decomposed, into a multi-span union, because it is the common "status ∈ {a,b}" case.)
2. For each candidate index with column list `c₁..c_m`: find the longest prefix `c₁..c_j` such
   that each `c_i` has an equality conjunct. If `c_{j+1}` has a range conjunct (`gt`/`gte`/
   `lt`/`lte`, or two forming a bounded interval), extend the span with it.
3. Score `(j, hasRange, coversOrder)`. Pick the maximum; break ties by ascending `idxID` so the
   plan is **deterministic** — a non-deterministic planner makes G2.2 flaky and makes a
   `Plan` golden impossible.
4. `Lo`/`Hi` are built by the *same* encoder as the index entries (§3.3), through one function,
   so a bound and a coordinate can never drift. Exclusive bounds are expressed by
   `Lo = enc(v) ‖ 0x00` / `Hi = enc(v)` on the escaped form rather than by a separate
   comparison mode, so the seek is a plain byte range.
5. Conjuncts not implied by the span become `Residual`. A conjunct on an unindexed column is
   always residual.
6. If `j == 0` and there is no range: `FullScan{Coll}` over `Iterate(rowPrefix(tenant, collID))`
   — **collection- and tenant-prefixed**, which alone fixes the `Iterate(nil)` whole-database
   scan of P13.

**Fallback rule, and making it visible.** A `FullScan` is legal (it is the correct plan for
"give me everything") but it is never silent:

- `Persist.explain : Query a -> Task Error Plan` is public, and its rendering is stable enough
  to golden.
- In dev, a `FullScan` whose `Estimate.keys` exceeds `[data] fullScanWarnRows` (default 10 000)
  logs `persist.plan.fullscan` once per call site with the collection, the residual predicate,
  and the index that *would* have helped.
- In production it increments `sky_persist_fullscan_total{coll}`.
- `Persist.Test.assertNoFullScan` lets an app's own test suite fail on an accidental full scan.

### 3.5 Index maintenance inside the single-writer commit path

The txn already buffers writes, computes `indexCoords(userKey, record)` for the new image, and
reads the pre-image via `ensurePreimage` (`txn.go:243`). v2 changes what is done with them:

At `buildReq`, for every buffered write, emit **additional `VersionedWrite` entries** into the
same `CommitReq.Writes`:

- for each old coordinate not present in the new set → `Op = OpDelete` on the old index userKey
- for each new coordinate not present in the old set → `Op = OpPut`, empty value, new index
  userKey

Because they ride the same `CommitReq`, they are assigned the same `commitTs` and land in the
same Pebble atomic batch (`committer.go`), behind the same `Apply(pebble.Sync)`. **Index
maintenance is therefore inside the single-writer commit path by construction, with no new
machinery, no second writer, and no possibility of a torn index.** This is why index entries are
modelled as rows.

The pre-image read is a read at the transaction's snapshot, so it is recorded as a point read in
the read-set — which is exactly what makes a concurrent modification of that row a validation
conflict. Index maintenance therefore also stays **inside SSI's read-set** rather than beside it.

**Read-set for a seek.** `ScanRange` continues to record `indexRange{index, lo, hi}` — the
existing `readset.go` type, the existing `validate.go` `coordHit(New) || coordHit(Old)` check.
The *only* change is that the rows are now obtained by seeking instead of iterating. The
recorded predicate is identical, so §3 **preserves** §2's SSI proof rather than re-deriving it.
A-PH (§2.5) exists to prove that claim empirically rather than by assertion.

One correction carried from P18: the shipped interval is **closed** `[lo, hi]` while the design
said half-open. v2 fixes the design to match the code (closed, safe over-reject) and deletes the
false `⟺` from the doc, rather than changing the code and risking an under-reject.

### 3.6 The complexity gate (G2.2) — and why it is not a timer

A timing assertion at small N cannot distinguish `O(log n + k)` from `O(n)`; constants and cache
effects dominate. The gate measures **work**, deterministically.

The reader is instrumented with a counter owned by the engine (not by pebble's optional stats,
which are not depended upon here):

```go
type ScanStats struct {
    Seeks        int // SeekGE calls issued
    KeysVisited  int // iterator positions advanced over
    RowsReturned int
    IndexEntries int
    PointGets    int
}
func (r *pebbleReader) Stats() ScanStats
```

**Procedure.** For `N ∈ {1_000, 10_000, 100_000}` rows in one collection, an index on `status`,
and a value matching exactly `k = 10` rows:

| Assertion | Rationale |
|---|---|
| `Plan.Access` is `IndexSeek` on the expected index | plan shape, not timing |
| `Stats().KeysVisited ≤ k + 4·⌈log₂ N⌉ + 64` for each N | the actual complexity claim |
| `KeysVisited(100_000) < 2 × KeysVisited(1_000)` | sub-linear growth; a full scan gives 100× |
| `Stats().PointGets == k` | no over-fetch |
| the same query on the *unindexed* column visits ≥ N keys | the gate can observe the contrast |

Deterministic, no wall clock, no flake. **Mutation:** a build-tagged `planner.forceFullScan`
turns the seek off; `KeysVisited(10_000) ≈ 10_000` → RED at the second N already. A second
mutation removes the `pk` suffix from index keys (collapsing duplicates) → `RowsReturned < k` →
RED.

A **throughput** arm exists too but is a *floor with a recorded baseline*, not a correctness
assertion: `BenchmarkIndexSeek` must not regress more than 20% against the committed baseline in
`docs/bluedb/baselines.json`. Baseline regressions REPORT on a developer machine and FAIL in CI,
where the runner is fixed.

### 3.7 Index consistency under crash (G2.3)

A randomized workload (put/update/delete/transact across several indexes) runs under the ported
errorfs crash corpus. After each injected crash and recovery:

1. Re-derive every index entry from the data keyspace at the recovered `commitTs`.
2. Diff against the stored index entries: **byte equality**, both directions.
3. Assert zero orphan index entries and zero missing entries.
4. Assert the recovered `hlc_hi` ≥ every observed `commitTs`.

**Mutation:** drop the old-coordinate `OpDelete` emission in `buildReq` → orphan entries → RED.
**Mutation:** emit index writes in a *second* `CommitReq` → a crash between the two leaves a
torn index → RED.

---

## 4. A3 — session-bounded Model state (goal #1)

### 4.1 What exists today, measured

- The Model lives in `liveSession` (`runtime-go/rt/live.go:2068`): `model any`, `handlers`,
  `prevTree *VNode` (the full rendered tree, with a `map` per node), **two** full HTML body
  strings (`lastComputedBody`, `lastShippedBody`), an ingress channel of 16 `sseFrame`, and a
  per-connection channel of 16 more — where a patch frame's `data` is a **whole body**.
- Measured size: **~37 KB per session** (`docs/skylive/tiered-session-cache.md:3-9`, from the
  real OOM incident). Per SSE connection the worst case is ~17 × body size; at a 50 KB body that
  is ~850 KB **per connection**.
- Eviction is **100% time-based** (`idleEvictPass`, `live_store.go:696-746`). There is **no
  count cap and no byte cap** anywhere in `runtime-go/`.
- The **default** store is `memory`, which has no idle-evict tier at all — locked by
  `TestTiered_MemoryStoreNoOp` (`live_tiered_cache_test.go:304`).
- SSE-connected sessions are **immortal twice over**: the explicit
  `!sess.hasSSEConnOtherThan("")` guard (`live_store.go:715`, re-checked under lock at `:735`,
  locked by `TestTiered_SSEConnectedNeverEvicted`) *and* the 15-second heartbeat that calls
  `touchLastSeen()` (`live.go:6296`), which defeats the TTL reap as well.
- Admission control is a static path list (`isBrowserNoisePath`, `live.go:3977`). **Any routed
  GET without a cookie mints a full session** — the crawler OOM vector.
- Nothing measures or exports session count or bytes.

So goal #1 has no design, no bound, no metric, and two locked tests that forbid the obvious
mechanism. §4 replaces all of it.

### 4.2 Where session state lives

**Sessions become a Persist collection.** ADR-001 called this the correct architecture and then
deferred it as "non-urgent roadmap" on the grounds that the funnel had already delivered the
durability win. That reasoning is right about durability and wrong about goal #1: a bound
requires a **spill target**, and a spill target requires a durable store that the session layer
already speaks. Sessions-as-collection is therefore promoted from roadmap to the mechanism.

```
collection _sky_sessions
  sid        String   (primary key)
  tenant     String   (engine-attested, §5)
  blob       Bytes    -- the 5c envelope: "SKS1" ‖ BE32 schemaVersion ‖ gob
  updatedAt  Time
  bytes      Int      -- accounted resident size at last persist
  index (tenant, updatedAt)
```

**The blob stays opaque.** This dissolves P19/D14 — the objection that `rt` has no way to obtain
a `Codec` for the app's Model — instead of assuming it away. Only the *envelope* is typed; the
Model remains gob inside a `Bytes` column, exactly as today, with the 5c version envelope
(`"SKS1" ‖ BE32 ‖ gob`) unchanged. No compiler-side Model-codec injection is required, and
`Codec.auto` is never asked to derive a `Model`. What we gain is what we actually need: one
`[data]` backend, engine-native durability, a migration story, and a spill target.

`chooseStore` (`live_store.go:1501`, two callers only — `live.go:3610`,
`subapp_inprocess.go:402`) gains `case "data"` and that becomes the **default**. `memory`,
`sqlite`, `redis`, `postgres` remain as explicit opt-outs so no existing app breaks (ADR-001's
non-breaking constraint is kept).

### 4.3 The resident cache and its two-part bound

RAM holds a *cache*, not the truth.

```go
type sessionCache struct {
    maxEntries int   // [data] sessionCacheMaxEntries, default 10_000
    maxBytes   int64 // [data] sessionCacheMaxBytes,   default 64 MiB
    entries    int64
    bytes      atomic.Int64
    lru        // intrusive list, most-recent first
}
```

**Accounting.** Each session carries `residentBytes atomic.Int64`, recomputed at exactly one
place — the **persist-before-ack funnel** (`persistAndShipFrame`, commit `e1f6eaf2` on
`feat/bluedb`). The funnel is already the single persist point; making it the single accounting
point costs nothing and means the accounting cannot drift from reality by construction:

```
residentBytes = len(blob) + len(lastComputedBody) + len(lastShippedBody)
              + treeBytes(prevTree) + handlerBytes + fixedOverhead
```

`treeBytes` is computed during render (the walk already exists), not by reflection.

**A per-session hard cap.** `[data] sessionMaxBytes` (default 1 MiB). A transition whose
resulting session exceeds it fails the transition with a typed `Error` naming the session and
the size, *before* the ack. A session cannot grow without bound even if the cache has room; an
app that puts a 50 MB list in its Model learns at the first request, not at 3am.

### 4.4 Deflation — how an SSE-connected session becomes evictable

The current answer to "what happens when N connected sessions exceed the budget" is that the
question is unanswerable: connected sessions cannot be evicted. v2 replaces *eviction* with
**deflation**, which is a different operation:

| | Evict (today) | Deflate (v2) |
|---|---|---|
| Session identity | destroyed on `memory` | preserved |
| SSE connection | must be closed | **stays open** |
| RAM released | all | Model + `prevTree` + both bodies + handlers (~95% of the 37 KB) |
| Recovery | new session, user logged out | rehydrate from the store on the next event; re-render |
| Safe when store is non-durable? | no | **no — see §4.5** |

Deflation runs under pressure in LRU order over *all* sessions, connected or not, after
persisting through the funnel. A deflated session keeps its shell (`sid`, `sseConns`, mutexes,
`lastSeen`) — measured target ≤ 2 KB. The `!hasSSEConnOtherThan("")` guard is removed and
`TestTiered_SSEConnectedNeverEvicted` is **inverted** into
`TestSSEConnectedDeflatesUnderPressure` (an existing locked test that contradicts the goal is
changed deliberately, in the open, not worked around).

The 15-second heartbeat `touchLastSeen()` stays — it is correct for *liveness* — but the cache
orders by a separate `lastActivity` stamp updated only by real transitions, so a heartbeat no
longer makes a session immortal.

### 4.5 Admission control, and the non-durable case

**Provisional admission.** A first GET mints a `provisional` session: it is served, it sets the
cookie, but it is (a) not written to the store, (b) first in the eviction order, and (c) reaped
at `[data] provisionalTTL` (default 60 s) rather than the 30-minute TTL. A session is
**promoted** to established on its first SSE connect or first event — i.e. by evidence that a
real client is there. A crawler that never runs JS never creates an established session. This
alone removes the OOM vector documented in `tiered-session-cache.md`.

**When the store cannot absorb a spill.** Deflation requires a durable target. With
`store = memory` there is none, so under pressure the honest options are lose data or refuse
work. v2 refuses:

> With a non-durable session store, reaching the cache ceiling causes **new session admission to
> fail** with HTTP 503 + `Retry-After`, a rate-limited `session.capacity.refused` log, and a
> `sky_live_sessions_refused_total` counter. Existing sessions are never destroyed to make room.

This is loud, correct, and confined to a configuration (`memory`) that is dev-only in practice —
and `[data]`'s default is the embedded collection, which *is* durable, so the common path never
reaches it.

**Under pressure, in order:** deflate LRU-cold established sessions → drop provisional sessions →
(durable store) keep deflating; (non-durable store) refuse new admissions. Never destroy an
established session to reclaim memory.

### 4.6 The per-connection floor, and the coalescing outbox

The 16-deep per-connection channel of full-body frames is the largest remaining term and it is
*not* Model state, so §4.4 does not touch it. It is in scope for goal #1 because it is session
RAM.

The outbox becomes **coalescing**: a pending *patch* frame is replaced rather than queued (the
newer frame supersedes the older by construction — the client applies the latest body). Capacity
becomes 1 patch + a small queue for non-superseding events. The runtime already has a
drop-and-resync path (`sseConn.outOfSync`, `live.go:2711`) for the case where a frame is lost;
a *coalesced replacement* is strictly better than a drop and needs no resync.

Result: per-connection RAM ≈ 1 body + goroutine stack ≈ tens of KB, not ~850 KB.

**The irreducible part, stated:** each connected client costs one goroutine, one HTTP connection,
and one coalesced frame. That is linear in *connected clients* and cannot be removed by any data
layer. G1.1 measures it and publishes it as a floor rather than folding it into the "bound".

### 4.7 Gates

**G1.1 — ceiling under N active sessions.** A real Sky.Live app (the §7 example), `N = 50 000`
sessions each with an **open SSE connection** and a periodic transition, driven by a load
harness. Assertions:

- `sessionCache.bytes ≤ maxBytes` at every sample, and `entries ≤ maxEntries`
- process RSS ≤ `maxBytes + N × perConnFloor + slack`, where `perConnFloor` is measured in the
  same run at `N = 1 000` and printed
- heap growth from `N = 1 000` → `N = 50 000` is **sub-linear in the session-state term**
- zero `session.capacity.refused` (the store is durable)
- `sky_live_sessions_resident` and `sky_live_session_bytes` gauges exist and are non-zero
  (a bound that cannot be observed cannot be operated)

*Mutations:* restore the `!hasSSEConnOtherThan("")` immunity → RED (bytes exceed). Remove the
`maxBytes` check, keeping only `maxEntries` → RED (a few large sessions blow the byte budget).

**G1.2 — correctness across spill.** 1 000 sessions with distinct Models are forced through
deflate → rehydrate → transition. Every session's post-rehydration Model must equal the
pre-deflation Model with the transition applied; every SSE connection must still be attached.
*Mutation:* deflate without persisting first → RED.

**G1.3 — no acked-then-lost across spill.** The funnel's persist-before-ack property is
re-proven with deflation in the loop, using the **AST dominance analysis** ported from
`feat/bluedb` (which emits its own ack-site table so the inventory cannot drift; a textual order
rule was tried there and rejected as vacuous — a persist in a mutually exclusive branch
satisfies it). *Mutation:* add a new ack site that does not go through the funnel → RED.

**G1.4 — provisional admission.** 100 000 cookie-less GETs from a crawler-like client create
zero established sessions and bounded provisional RAM; a real client (SSE connect) is promoted
and survives. *Mutation:* promote on first GET → RED.

---

## 5. A4 — durable, engine-attested tenancy

### 5.1 What is wrong

`CommitReq.Tenant` is a **transient routing tag**, and the prior design says so explicitly
(`engine.go`): *"It is NEVER written durably: it is not part of ChangelogPayload, never reaches
`EncodeChangelogPayload`, and the L1 store never sees it."* A dedicated test,
`TestReactive_TenantNeverDurable`, locks that property. Consequently:

- Every tenant-scoped **read** must compare against an application-written row column
  (`CollSchema.TenantCol` on `salvage/p5e-foundation`), which the salvage branch's own comment
  concedes is *"a VIEW filter over application-declared data, not an authorization boundary"*.
- Off-session writes tag `""` (`currentSessionTenant()` returns empty for cron / CLI / webhook
  goroutines), so those rows are invisible to their owner — `RESUME.md` item 9 — with no escape
  hatch, because the `Persist.withTenant` that RG#1 promised was never built (P15).
- Goal #5's entire security model rests on the forgeable column.

### 5.2 The decision: tenancy is part of the key

Tenancy moves into the user key (§3.2). This **reverses** the prior "never durable" decision,
and `TestReactive_TenantNeverDurable` is deleted and replaced by its inverse,
`TestTenantIsDurableAndAttested`. The reversal is safe with respect to the frozen substrate: the
user key is opaque to the comparer (§3.2), so `skydb.mvcc.v1` is untouched.

```
ROW    userKey = 0x01 ‖ ten ‖ collID ‖ pk
INDEX  userKey = 0x02 ‖ ten ‖ idxID  ‖ cols ‖ pk
```

Three properties follow directly:

1. **Durable.** The tenant is in the key, so it is persisted, replicated, and recovered by the
   same mechanism as the data. There is nothing extra to write and nothing that can be
   forgotten.
2. **Attested.** The key is built by the engine from `Txn.tenant`, which is set at `Begin` from a
   `TenantScope` value (§5.3). The row *contents* never participate in key construction. A row
   claiming `{"tenant": "acme"}` in its body lands wherever its transaction's scope says, and is
   read back only under that scope.
3. **Structurally isolated.** A scoped read is not a filter; it is an iterator bound. A
   transaction opened under tenant `T` can only construct iterators over
   `[0x01 ‖ enc(T) ‖ …, 0x01 ‖ enc(T) ‖ 0xFF…)`. There is **no API that accepts a tenant
   argument from data** — `Reader.Iterate`, `Txn.Scan`, and the planner all take their bounds
   from the transaction. Cross-tenant reads are impossible rather than filtered.

### 5.3 `TenantScope` — and the end of the empty default

```elm
type TenantScope
    = Tenant String     -- an ordinary tenant, from a verified session identity
    | System            -- the administrative scope; see below
```

- **In a Sky.Live handler**, the scope is the session's verified tenant. `Persist.conn` resolves
  it from the identity-stamped goroutine, exactly as the existing `currentLiveSession()` bridge
  does.
- **Off-session** (cron, CLI, `Sky.Http.Server` handler, webhook), there is **no default**. A
  write through the ambient `Persist.conn` in a multi-tenant app returns
  `Err (Error.unauthorized "persist: no tenant scope on this goroutine — wrap in Persist.asTenant, or Persist.asSystem with an admin capability")`.
  This is the fix for `RESUME.md` item 9: today's `""` produces rows that are silently invisible
  to their owner; here the call fails loudly at the call site.

```elm
asTenant : String -> (Conn -> Task Error a) -> Task Error a
asSystem : Persist.Admin -> (Conn -> Task Error a) -> Task Error a
```

- **`Persist.Admin`** is an unforgeable capability. It is not constructible by application code:
  the only producers are (a) the console authorization funnel's `Decide()` (§8), and (b)
  `Persist.adminFromEnv` which requires `SKY_DATA_ADMIN_TOKEN` and refuses in
  `ENV=production` unless `[data] allowEnvAdmin = true`. `System` scope reads across tenants by
  widening the iterator bound to the whole `0x01` namespace; it is the only thing that can, and
  it is audit-logged per operation.
- **Single-tenant apps pay nothing.** `[data] tenancy = "single"` (the default) makes the scope
  a compile-time constant; `asTenant` / `asSystem` are unnecessary and the off-session error
  never fires. Multi-tenancy is opt-in, and turning it on is what makes ambiguous writes an
  error — which is the right place for the friction.

**Background-job attribution.** `Persist.asTenant tid` gives a job the same key range as the
tenant's own sessions, so its writes are visible to that tenant's reads *and* to that tenant's
reactive subscriptions (§6) — a property the prior design could not have, because the reactive
partition keyed on a transient tag that background goroutines could not set.

### 5.4 Migration of existing data

Data written before v2 has no tenant component. `sky data migrate` rewrites keys into the
`0x01 ‖ ten ‖ …` layout, with `ten` taken from the collection's declared `tenantCol` when one
exists and from the single-tenant constant otherwise. This is a **key rewrite, not a comparer
change**: it is an ordinary bulk transaction, resumable, and verified by a post-migration
G2.3-style re-derivation. For an app that has never enabled multi-tenancy, it is a no-op beyond
the 3-byte prefix.

### 5.5 Gates

**G2.5 — cross-tenant structural impossibility.**

1. *Adversarial contents.* Write rows under tenant `T1` whose bodies contain
   `{"tenant":"T2"}`, `{"tenant":""}`, `{"tenant":"T2 T1"}`, and a body encoding the raw
   escaped key prefix of `T2`. Read under `T2` and under `System`-minus-`T2`. Assert zero rows
   in every scoped read.
2. *Property test.* For random tenant strings (including embedded `0x00`, `0xFF`, empty, 4 KiB),
   `keyRange(a)` and `keyRange(b)` are disjoint for `a ≠ b`. This is what the escaping in §3.3
   buys and it is worth proving rather than assuming.
3. *Structural.* An AST analysis over the `bluedb` package asserts that every construction of an
   iterator lower/upper bound flows from `Txn.tenant` or `Reader.tenant`, and from no other
   source. Grep is insufficient here (the property is dataflow, not lexical) — this reuses the
   dominance-analysis technique proven on `feat/bluedb`'s persist-before-ack tripwire, which
   emits its own site table so the inventory cannot drift.
4. *Attestation.* Dump raw pebble keys after a mixed workload; assert every key's tenant
   component equals the writing transaction's scope.

*Mutations:* add a `Txn.SetTenantUnchecked` used by the reader → arm 3 RED. Take the tenant from
the decoded row body in the index-key builder → arm 1 RED. Drop the escaping and concatenate raw
→ arm 2 RED.

---

## 6. A5 — reactivity that composes with scale

### 6.1 What is wrong

- Reactivity is **embedded-only** and **single-process** (`rt/bluedb_reactive.go:196-198`), and
  the cross-instance path does not exist at all (P15).
- The capability gate calls **`os.Exit(1)` on the first session**, under `sess.mu`
  (`bluedb_reactive_gate.go:172`, doc comment at `:156-159` — the exit under the lock is
  intentional). An app passes its health check and then dies when the first user loads a page.
- A dropped delivery latches a resync flag that **no production code reads** (P16), so a session
  can be permanently stale.
- Detection is query-scoped but **delivery is not**: the computed `Transition`/`Record`/
  `OrderChanged` are discarded and the consumer re-runs the whole query per session per
  notification (P17). So goal #4's "query/row-scoped" is true of the matcher and false of the
  wire.

### 6.2 The decision: reactivity at the Persist commit boundary

Move the emission point up one layer, from the embedded committer to `Persist.transact`'s commit
boundary. Persist knows every write the transaction performed (all writes go through it), so
after a successful commit it can emit a changeset on **any** backend:

```go
type Changeset struct {
    Tenant   string
    CommitTs uint64        // HLC on embedded; txid/LSN on SQL
    Changes  []RowChange
}
type RowChange struct {
    Coll   string
    PK     []byte
    Op     uint8      // put | delete
    Row    []byte     // encoded row, present for put
    Before []byte     // pre-image when the plan needs Leave/Stay classification
}
```

This is what makes goal #4 hold together with goal #2's "scalable": a Postgres deployment gets
reactivity, so an app does not have to choose between reactive and multi-replica.

The embedded engine keeps its changefeed — it is the more precise source (durable-before-notify,
ordered by `commitTs`, gap-recoverable via the changelog) — and the Persist layer consumes it
rather than reimplementing it. On SQL backends the changeset is captured in the transaction
wrapper. **Same `Changeset` type either way**, so the matcher above is backend-agnostic.

**Layering.** `bluedb` still may not import `rt` (it is a leaf). The bridge is the
`persistglue` package (§7.2) — the same shape as the *existing, in-production*
`console_app` → `rt.RegisterInlineConsoleCfgProvider` registration
(`runtime-go/rt/console_app/register_v3.go:33-35`), where a leaf package pushes a factory into
`rt`'s slot at blank-import time. That precedent is cited rather than invented.

### 6.3 Delivery: apply the delta, do not re-query

`RowChange` carries enough to maintain the bound list directly:

- `Enter` → decode and insert at the sorted position
- `Leave` → remove by pk
- `Stay` + `OrderChanged` → move
- `Stay` + `!OrderChanged` → replace in place

The full re-query becomes the **resync path**, taken only when a subscription's delta stream has
a gap. This is what P17 says was designed and not delivered; delivering it is what makes the
fan-out cost in §6.5 achievable at all.

**The resync latch gets a consumer** (P16). Every subscription has a `needsResync` flag; the rt
pump checks it on every wake *and* on a timer, and a set flag forces a full re-query. G4.5 proves
it: force a drop, assert the session converges. *Mutation:* remove the check → RED.

### 6.4 Cross-replica: the `ChangeBus`

```go
type ChangeBus interface {
    Publish(ctx context.Context, cs Changeset) error
    Subscribe(ctx context.Context, fn func(Changeset)) (cancel func(), err error)
    // Recover replays committed changesets after `since` for gap recovery.
    Recover(ctx context.Context, since uint64) ([]Changeset, error)
}
```

| `[data] changeBus` | Mechanism | Recovery | When it is the default |
|---|---|---|---|
| `local` | in-process | n/a | `driver = embedded` or `sqlite` (single instance by definition) |
| `postgres` | `LISTEN`/`NOTIFY` on `sky_changes`, payload = **summary only** (tenant, collections, `commitTs` range) | `_sky_changelog` table, GC'd at `[data] changelogRetention` (default 1 h) | `driver = postgres` |
| `redis` | pub/sub for the nudge + a Redis Stream for recovery | the stream | `SKY_LIVE_BROKER_URL` set |

**Why summary-only over `NOTIFY`.** The payload limit is 8 kB and delivery is not durable — a
subscriber disconnected during a commit misses it permanently. So the notification carries a
*watermark advance*, and a subscriber whose watermark is behind reads the durable
`_sky_changelog` to fill the gap. This is the same watermark + changelog contract the embedded
engine already implements, which is the point: **one recovery model, two transports.**
Row bodies never cross a shared channel, which also removes the cross-tenant body-leak class
(the prior B#1 finding) by construction rather than by topic naming.

Delivery is **at-least-once**; application is pk-keyed and idempotent, so a double delivery is a
no-op. (The prior design also landed on at-least-once, for the same reason: a subscription's
baseline snapshot cannot be pinned to the registration instant.)

### 6.5 What degrades, and how loudly

The prior gate's failure mode was a process that boots green and dies on the first page load.
Every check here happens **at startup, before the listener opens**:

| Situation | v2 behaviour |
|---|---|
| Reactive app, `driver = embedded`/`sqlite`, `[data] replicas = 1` (default) | runs |
| Reactive app, local-single-writer driver, `replicas > 1` | **startup fatal**, naming the fix (`driver = postgres` + `changeBus = postgres`, or `replicas = 1`) |
| Reactive app, `driver = postgres`, `changeBus = local`, `replicas > 1` | **startup fatal** |
| `changeBus = postgres` but `LISTEN` fails at boot | **startup fatal** |
| `ChangeBus` drops at runtime | reconnect with backoff; on reconnect every subscription is forced to resync; `sky_persist_changebus_reconnects_total` + a `WARN` |
| Subscription outbox overflows | latch `needsResync`; the pump converges the session; `sky_persist_resync_total` |

`replicas` is an explicit operator assertion, not a runtime probe — N processes each with their
own local store are indistinguishable from one process at runtime (the prior RG#2 finding, which
is correct). The improvement is not the detection method; it is **when** the check runs and that
it exits from `main` rather than from inside a session lock. `os.Exit` never appears on a
request path.

**G4.4** boots the matrix above and asserts exit codes and stderr, including that a
misconfigured app **never serves a single request**. *Mutation:* move the check back to the
first session → RED (the app serves `/healthz` before dying).

### 6.6 Honest fan-out cost

Subscriptions are indexed by `(collection, tenant)`. A commit touching one row in one collection
visits only that bucket, then evaluates the residual predicate once per *distinct* predicate
(the shared-predicate `matchCache` from `feat/bluedb`'s `reactive.go:121-131` is a genuinely
good idea and ports).

| Term | Cost | Notes |
|---|---|---|
| bucket lookup | `O(1)` | per changed row |
| predicate evaluation | `O(distinct predicates in the bucket)` | not `O(subscriptions)` — the cache is the reason |
| row decode | `O(1)` per changed row | decoded once, shared |
| **delivery** | `O(matched subscriptions)` | irreducible: each matched session gets a frame |
| point subscriptions (`where pk = …`) | `O(1)` | a pk-keyed side index, so a "watch this one row" case does not scan the bucket |

The **delivery** term is the wall, and it is linear in matched sessions. That is not a bug and
it is not removable; a broadcast to 10 000 interested sessions costs 10 000 frames. What §6.3
buys is that each frame is a *delta*, not a re-query — the difference between
`O(matched) × O(1)` and `O(matched) × O(collection scan + full decode)`, which is what ships on
`feat/bluedb`.

**G4.3** records measured numbers into `docs/bluedb/baselines.json` — changesets/second at
1 k / 10 k / 100 k subscriptions, with 1 / 10 / 100 distinct predicates, and the delivery cost
separated from the matching cost so a regression is attributable. CI fails on >20% regression
against the committed baseline. The N = 2 two-browser demo that "verified" the prior phase is
explicitly not a gate.

---

## 7. A6 + the DX surface

### 7.1 One `[data]` section, wired end to end

```toml
[data]
driver     = "embedded"        # embedded (default) | sqlite | postgres
path       = "data/app.blue"   # embedded file / sqlite file
# url      = "$DATABASE_URL"   # postgres
tenancy    = "single"          # single (default) | multi
durability = "full"            # full (default) | normal
replicas   = 1                 # operator assertion; >1 requires a shared driver + changeBus
changeBus  = "auto"            # auto (default: derived from driver) | local | postgres | redis

sessionCacheMaxBytes   = "64MiB"
sessionCacheMaxEntries = 10000
sessionMaxBytes        = "1MiB"
provisionalTTL         = "60s"
sessionTTL             = "30m"
sessionVersion         = 1      # developer-declared; bump on any Model semantic change
fullScanWarnRows       = 10000
```

`[data]` subsumes `[database]`, `[live] store`/`storePath`/`ttl`, and `[analytics] dbPath`.
Legacy sections keep working and emit **one** deprecation warning per project (not per section
per build), mapping onto the same code path. `[data]` wins over legacy — implemented correctly,
because `SetSkyDefault` is **first-wins** (`lower.rs:785`), so `[data]`-derived defaults are
pushed **first**. (The prior design asserted "pushed last wins" and was inverted; the fix is
already known and is ported.)

### 7.2 Why a key cannot be dead this time

The dead-`DB_DRIVER` class exists because config was written into an environment variable and a
reader was expected to appear. v2 makes the config **structurally load-bearing**: it decides
what code is generated.

```
runtime-go/
  bluedb/        pebble + stdlib ONLY. Never imports rt.
  persistglue/   imports BOTH rt and bluedb. The only adapter. Ordinary, tested code.
  rt/            never imports bluedb. Declares the DataBackend interface in stdlib types.
```

`sky build` emits `sky-out/sky_data.go` — modelled on the existing, proven
`write_embedded_migrations` (`rust/crates/project/src/build.rs:1129`), which already generates a
Go file whose `init()` sets a runtime variable:

```go
package main

import (
    _ "sky-app/persistglue" // its init() registers the embedded backend factory with rt
    rt "sky-app/rt"
)

// Generated by `sky build` from [data] in sky.toml — do not edit.
func init() {
    rt.SetDataConfig(rt.DataConfig{
        Driver: rt.DriverEmbedded, Path: "data/app.blue",
        Tenancy: rt.TenancySingle, Durability: rt.DurabilityFull,
        Replicas: 1, ChangeBus: rt.BusLocal,
        SessionCacheMaxBytes: 67108864, SessionCacheMaxEntries: 10000,
        Collections: []rt.CollDecl{ /* … derived from the Sky declarations … */ },
    })
}
```

Consequences:

- **`rt` never imports `bluedb`.** The prior arrangement (`rt/embedded_kernel.go` importing
  `sky-app/bluedb`) is what broke every non-Persist Sky app when a single import escaped the
  materialisation gate, and it forced a fragile per-filename prune list in `materialise_rt`. Here
  the prune is one directory decision (`persistglue/` + `bluedb/` are copied iff needed), which
  is the same shape as the existing `console_app` prune (`build.rs:1189`).
- **A non-Persist app links no pebble.** Nothing imports it, so nothing is built.
- **A dead key is impossible.** If `[data] driver` were not read, no glue would be emitted and
  `Persist.*` would have no backend — the app fails at build or boot, not silently.

`DB_DRIVER` is **deleted**, along with the test that pins it (`build.rs:1442`). The `[database]
driver` documentation at `docs/sky-toml.md:202` and `docs/skydb/overview.md:558` is corrected in
the same commit.

**G0.4 — no dead config, generalised.** `read_sky_toml_config`'s match arms become a
data-driven table:

```rust
pub const CONFIG_KEYS: &[ConfigKey] = &[
    ConfigKey { section: "data", keys: &["driver"],       env: "DATA_DRIVER",  reader: Reader::Glue },
    ConfigKey { section: "data", keys: &["path", "url"],  env: "DATA_PATH",    reader: Reader::Runtime },
    // …
];
```

The gate asserts, in both directions: every `env` with `Reader::Runtime` is actually read in
`runtime-go/` (an `os.Getenv`/`skyGetenv` scan), every `Reader::Glue` key actually influences the
generated glue (byte-diff the glue with the key flipped), and every DB/DATA-shaped getenv in
`runtime-go/` appears in the table. *Mutation:* add a key with no reader → RED. This closes the
whole class, not just `DB_DRIVER`.

**G0.5 — the zstd tag.** `run_go_build_once` (`build.rs:600`) passes `-tags pebblegozstd` on the
CGO=0 path **and** on the CGO=1 FFI-retry path (`build.rs:569`, `:576-595`). The gate builds a
Persist example on both paths and asserts the binary contains no DataDog cgo zstd symbols. This
is the real form of the mandate's non-negotiable; the `go test` form (P8) is already satisfied by
`CGO_ENABLED=0` in CI and is asserted separately.

### 7.3 One Persist API

Backend names leave application source. `connectKeyValue` / `connectKeyValueSync` /
`connectRelational` and the `Conn cap` phantom tag are removed: a phantom that is only obtainable
from a backend-specific constructor forces app code to name the backend, which is precisely what
goal #2's "UNIFIED APIs shareable across dbs" forbids, and what makes the
embedded→sqlite→postgres graduation a source edit instead of a config edit.

```elm
conn : Conn                                        -- from [data]; memoised (one shared pool — the correct CAF)

collection : String -> Codec a -> Collection a
key        : String -> Collection a -> Collection a
index      : String -> Collection a -> Collection a
indexOn    : List String -> Collection a -> Collection a   -- composite; legal in any column order (§3.3)
unique     : String -> Collection a -> Collection a
tenantCol  : String -> Collection a -> Collection a         -- §8 admin scoping (multi-tenant)
adminShow  : List String -> Collection a -> Collection a    -- §8 disclosure allow-list

get     : Conn -> Collection a -> String -> Task Error (Maybe a)
put     : Conn -> Collection a -> a -> Task Error ()
insert  : Conn -> Collection a -> a -> Task Error a
delete  : Conn -> Collection a -> String -> Task Error ()
count   : Conn -> Collection a -> Task Error Int
transact : Conn -> (Tx -> Task Error a) -> Task Error a     -- serializable, auto-retried (§2)

query   : Collection a -> Query a
where_  : Cond -> Query a -> Query a
orderAsc, orderDesc : String -> Query a -> Query a
limit, offset : Int -> Query a -> Query a
toList  : Conn -> Query a -> Task Error (List a)
explain : Conn -> Query a -> Task Error Plan                -- §3.4

asTenant : String -> (Conn -> Task Error a) -> Task Error a  -- §5.3
asSystem : Admin -> (Conn -> Task Error a) -> Task Error a

liveInto : Collection a -> Query a -> (List a -> model -> model) -> LiveBinding model
```

`Cond` keeps the shipped shape (`eq`/`neq`/`gt`/`gte`/`lt`/`lte`/`like`/`isNull`/`notNull`/
`inList`/`and_`/`or_`/`not_`), with the identifier allow-list (`[A-Za-z0-9_.]`) applied at
**`Collection` construction and query build**, not only in `toList`/`toCount` as today.

**G2.4 — transact-body replayability, at compile time.** `transact` retries, so its body must be
re-runnable. Sky's HM cannot express an effect restriction, but the compiler can check the one
that matters: a `sky check` **error** when a `Persist.transact` body statically calls a
non-replayable kernel (`Http.*`, `File.*`, `Time.now`, `Uuid.*`, `Random.*`, `Db.execRaw`,
`Task.perform` of an external effect). This is a HIR-level walk of the lambda; the reference is
static even though the backend is not. *Mutation:* an `Http.get` inside a fixture's transact body
must produce the diagnostic; removing the check makes the fixture compile → RED.

### 7.4 The escape hatch

Raw SQL is genuinely non-portable, so it lives in its own module and its use is a *static* fact:

```elm
-- Std.Persist.Sql
raw     : Conn -> Codec row -> String -> List SqlValue -> Task Error (List row)
execRaw : Conn -> String -> List SqlValue -> Task Error Int
```

If the program references `Std.Persist.Sql` and `[data] driver = "embedded"`, that is a **build
error** naming the call site (§2.6 mechanism 1) — not a runtime surprise. Joins, aggregates, and
window functions are the intended users; they are documented as the graduation trigger to
`driver = "postgres"`.

### 7.5 One migration story

`sky data migrate --gen | migrate | status | seed | reindex`, aliasing the existing `sky db`
verbs. The DB-free declared-vs-recorded diff, the checksummed `_sky_migrations` ledger, the
never-lossy quarantine, and the TTY rename prompts all port unchanged from
`Std.Db.Schema` / the file-based migration machinery on `main`. What is added:

- `_sky_sessions` participates (one store, one ledger) — the thing `[data]` was supposed to buy
  and did not (P20).
- `reindex` rebuilds index entries from data (§3.2), for an index-encoding version bump or a new
  index on an existing collection.
- The tenant key-prefix migration (§5.4) is an ordinary, resumable migration step.

### 7.6 The 10-line app

No `[data]` section. No connection management. No backend named. Data survives restart, and the
list stays live without a subscription being written by hand.

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Codec as Codec
import Std.Live as Live
import Std.Persist as P
import Std.Ui as Ui

type alias Todo = { id : String, title : String, done : Bool }

todos : P.Collection Todo
todos =
    P.collection "todos" (Codec.auto { id = "", title = "", done = False }) |> P.key "id"

main =
    Live.app
        (Live.config { init = init, update = update, view = view }
            |> Live.withLive [ P.liveInto todos (P.query todos |> P.orderAsc "title") setItems ]
        )
```

`sky run src/Main.sky` creates `data/app.blue`, migrates on first boot, serves, and keeps
`.items` live across tabs and restarts. Graduating to Postgres is three lines of `sky.toml` and
no source change — asserted by **G3.3**, which runs the identical source against all three
drivers.

### 7.7 Docs surface

- `docs/skypersist/overview.md` — the guide, gated by `scripts/doc-examples.sh` so a rotting
  example fails CI (**G3.2**).
- `sky doc Std.Persist` — generated from source, so the API cannot drift.
- `docs/sky-toml.md` `[data]` section, replacing the `[database]` section (whose `driver` row is
  corrected, not deleted, since the key still parses for compatibility).
- `docs/bluedb/v2-architecture.md` (this file) as the design reference;
  `docs/bluedb/STATUS.md` as the generated truth.
- `AGENTS.md` — the `[data]` default, the Persist pinned default replacing "BlueDB is WIP", and
  the removal of the phantom `kernel_api.rs` gate sentence (P5). Same commit, per the
  template + doc sync rule.

---

## 8. Goal #5 — console admin access to records

### 8.1 ⛔ The open decision — read-only vs read-write

Goal #5 verbatim is **"Built-in Sky Console admin access to records."** The words "read-only",
"CRUD", and "LIST/detail" appear nowhere in the user's goal; they originate in agent-authored
docs. The `goty.rs` collision long cited as blocking an edit form does not block it (P7). **This
document does not decide the scope.** §8.3 designs 5e-1 (read) as a complete, shippable
deliverable; §8.4 specifies 5e-2 (write) precisely enough to implement without redesign. A Judge
must return NOT ACHIEVED for goal #5 on read-only alone until the user rules.

### 8.2 What durable tenancy changes

`phase5e-closure-design-v2.md` v2.1 is twice-grilled and its authorization architecture is
sound. It has exactly one weakness it could only document, not fix, and it is stated in the
salvage branch's own source comment on `CollSchema.TenantCol`:

> *"It is an APPLICATION-WRITTEN column, not an engine-verified fact — the engine's write-time
> tenant tag is explicitly, by design, never durably written… So this is a VIEW filter over
> application-declared data, not an authorization boundary over application WRITES."*

§5 removes that weakness. The admin scope is no longer a `WHERE tenant = ?` over a column the
application wrote; it is the transaction's key range. `TenantCol` survives only as a *display*
hint (which column to show as the tenant), never as the enforcement mechanism. The consequence
is precise: **a malicious tenant poisoning its own row contents cannot make its rows appear in
another tenant's admin view**, which v2.1 explicitly could not promise.

### 8.3 5e-1 — the read surface, complete

**The funnel.** One decision point; no caller may assemble a decision from parts.

```go
// package consoledata — imports rt; never imported BY rt.
//
// Decide takes NO arguments. Every input is read INSIDE the funnel from an
// authenticated source, so a caller cannot pass a value that flatters it. This is the
// "zero trust inputs" rule: the funnel is not a policy helper the caller configures,
// it is the only thing that knows the answer.
func Decide(r *http.Request) Decision

type Decision struct {
    Allow      bool
    Scope      Scope        // ScopeDenied | ScopeTenant | ScopeSystem
    Tenant     string       // meaningful iff ScopeTenant
    Mode       Mode         // ModeRead | ModeReadWrite   (5e-2)
    Disclose   []string     // per-collection allow-list resolution, empty = disclose nothing
    Reason     string       // audit + operator diagnosis; never rendered to the browser
    Admin      rt.Admin     // the §5.3 capability — ONLY the funnel mints one
}
```

**Ordering, fail-closed.** Each step can only *narrow*; there is no step that re-widens. This
ordering is the fix for the prior fail-**open** defect where a verified session with no tenant
claim was treated as in-scope for every tenant (the `rejectCrossTenantSvc(_, "")` → IN-SCOPE
path):

1. Console access is enabled at all (`SKY_CONSOLE_AUTH` set; in `ENV=production`, unset ⇒ DENY).
2. The request carries a valid, mode-bound console session (`token` or `app` mode). Invalid,
   expired, or mode-mismatched ⇒ DENY.
3. The principal is reconciled to exactly one identity. Multiple candidate principals
   (cookie + bearer + the legacy embed JWT) ⇒ DENY, never "pick the strongest".
4. **Tenant resolution.** A verified tenant claim ⇒ `ScopeTenant`. **No claim ⇒ DENY** — never
   "all tenants". This is the inversion of the reused v0.16.6 pattern.
5. **`ScopeSystem` requires an explicit super-admin grant** (`SKY_CONSOLE_SUPERADMIN` listing the
   principal, or the app's `consoleAuth` callback returning the super-admin role). It is never
   inferred from the absence of a tenant.
6. Dev-mode unscoped access requires `ENV` to be explicitly a development value **and**
   `[data] tenancy = "single"`. It is not the default and it is logged on every use.
7. Disclosure: the collection's `adminShow` allow-list. **Empty ⇒ nothing disclosed.** An
   allow-list, never a deny-list — `stripe_sk`, `iban`, `dob`, `national_id`, and `backup_codes`
   all pass any plausible name deny-list.

**Funnel-internal predicates.** Helpers like "is this principal a super-admin", "is this
production", "is this tenant claim verified" are unexported within `consoledata` and take no
caller-supplied booleans. The prior `consoleDataAccess(prod, verified, superAdmin bool, tenant
string)` signature was correct in its *ordering* but wrong in its *shape*: a caller who computes
`superAdmin` wrongly gets an unscoped decision, so the security property depends on every call
site. Here there is one call site and it passes only the `*http.Request`.

**Reads.** All reads go through `Persist` under `Decision.Admin`, i.e. through §5's key-range
scoping. `ScopeTenant` opens the range for that tenant; `ScopeSystem` opens the whole namespace
and audit-logs each operation. There is no `adminReadRows` that scans everything and filters
afterwards — the prior implementation's unscoped all-scan, safe only because it was "reachable
only when the gate grants unscoped", is exactly the kind of coupling that breaks when the gate
changes.

**Enumeration.** Collections come from the registry, whose write-once/copy-on-write semantics and
deep-copying `SchemaOf` port from `salvage/p5e-foundation` (`de3e7431`). That fix matters here:
the registry's `Register` used to overwrite unconditionally and `cp := cs` was a *shallow* copy,
so a caller retained the `Cols`/`Indexes`/`Generated` backing arrays and could mutate the
registry — and therefore the resolver's, the indexer's, and every live subscription's view of the
schema — after `Register` returned. Write-once + deep copy makes every escaping `*CollSchema`
safe by construction.

**Binding.** `consoledata` cannot be imported by `rt` (cycle), so it registers itself into an
`rt` slot at blank-import time — the same shape as the in-production
`console_app` → `rt.RegisterInlineConsoleCfgProvider` seam. First-wins registration, consistent
with existing runtime practice.

**Surface.** A `Data` tab: collection list → row list (index-backed ordered range scan with
cursor pagination — §3 is what makes this affordable on a large collection) → row detail, with a
`Cond` filter builder. Values bounded, output HTML-escaped by `Std.Ui`, every access audit-logged
with the `Decision.Reason`.

**All three backends, not just embedded.** The prior 5e enumerated only embedded collections
(`adminEmbeddedCollections` walks `embeddedByID`), which would make goal #5 false for a Postgres
app. Because §7 makes `Persist` backend-agnostic, browse goes through `Persist` and works
everywhere.

Enumeration comes from `rt.DataConfig.Collections` — the **statically declared** collection list
the compiler emits into the generated glue (§7.2) from the Sky source. This is better than a
runtime creation registry in three ways: it is complete before any table exists, it is identical
across backends, and it is **default-deny by construction** — an undeclared table is
unbrowsable, so an `information_schema` walk is never needed and never offered.

> *Premise check:* `exp/bluedb`'s browse layer describes its allow-list as "tables created via
> `Std.Db.Store` (registered in `Db_createCols`)". On `main`, `Db_createCols`
> (`runtime-go/rt/db_codec.go:133`) renders and executes the DDL and **registers nothing** — the
> registry is `exp/bluedb`-only. Sourcing the allow-list from the declared collections avoids
> building it.

**Port the `exp/bluedb` browse hardening verbatim** — it is the most security-reviewed part of
either prior branch (`exp/bluedb:runtime-go/rt/console_data_sql.go`):

| Property | Why it is kept |
|---|---|
| **Default-deny table allow-list** from the `Store` creation registry — never an `information_schema` walk | an `information_schema` walk discloses other tenants', system, migration, and auth tables |
| **Separate read-only capped connection**, not the app's hot-path pool | a heavy operator browse cannot lock application traffic — and on sqlite it must not contend for the single writer connection (§2.3) |
| **SELECTs fully constructed in Go**; only allow-listed, quoted identifiers reach the query; values are never interpolated | there is no user-supplied SQL text at all |
| **Row caps, byte caps, statement timeout** | an unbounded admin scan is a self-DoS |
| **Opaque `sha256` source handle**, never the raw DSN | a Postgres DSN carries `user:PASSWORD@host` and must never reach discovery JSON, the audit log, or a client-echoed error |
| **Every read audit-logged** | — |
| **No loopback bypass** | behind a reverse proxy every request is loopback (this is why `isLoopbackRemoteAddr` is deleted, not merely unused) |

One deliberate **substitution**: `exp/bluedb` redacts by matching column names against a
sensitive-name pattern (`password`/`token`/`secret`/`hash`/`api_key`/`ssn`/…). That is a
**deny-list**, and a deny-list is incomplete by construction — `stripe_sk`, `iban`, `dob`,
`national_id`, and `backup_codes` all pass it. v2.1's `adminShow` **allow-list** replaces it:
outside an explicitly-declared dev environment only allow-listed fields render, everything else
is `***`, and an empty list discloses nothing. Keep the pattern matcher as a *second* filter
applied on top (defence in depth), never as the primary one.

One inherited defect **not** ported: `exp/bluedb`'s `dataAuthOK` accepts the per-boot internal
token as a data principal. That is a confused deputy — the internal token authenticates the
console *process*, not an *operator*. `Decide()` reconciles principals (step 3) and the internal
token is not among them.

Two cleanups the port should carry: `isLoopbackRemoteAddr` (`console.go:409-436`) has **zero
callers** — delete it rather than leave a re-wirable loopback bypass; and the console's loopback
self-fetches do not attach the internal token, so under `SKY_CONSOLE_AUTH=token` in production the
refresh ticks receive a 401 login page instead of JSON (first paint is unaffected — it is
populated in-process — so the symptom is "renders once, then freezes"). The Data tab must not
inherit that bug.

**Gates.**

- **G5.1 — decision matrix.** Every combination of {prod, dev} × {no auth, invalid, valid} ×
  {no tenant claim, tenant claim, super-admin} × {multiple principals} against the expected
  `Decision`. *Mutations:* make step 4 return `ScopeSystem` on a missing claim → RED; reorder
  step 5 before step 4 → RED; turn `adminShow` into a deny-list → RED (a fixture column named
  `stripe_sk` becomes disclosed).
- **G5.2 — scoped read cannot cross tenants.** Reuses G2.5's adversarial fixtures: rows under
  `T1` whose contents claim `T2`; an admin scoped to `T2` sees zero. This is the gate that only
  becomes *provable* because of §5 — under v2.1's forgeable column it could only be asserted.
- **G5.3 — e2e.** Playwright against a real app: authenticate, list collections, page a
  100 k-row collection, filter, open a row, confirm a non-`adminShow` field renders as `***`,
  confirm the audit log entry, and confirm the refresh tick does **not** 401.

### 8.4 5e-2 — the write surface, specified (not decided)

If the user rules for read-write, this is the delivery, and it requires no change to §8.3:

- **Capability.** `Decision.Mode == ModeReadWrite`, granted only by an explicit
  `SKY_CONSOLE_DATA=readwrite` **and** a super-admin or tenant-admin grant. Read-only is the
  default; a missing setting is read-only, never read-write.
- **Scope.** Writes go through `Persist.transact` under `Decision.Admin`, so they are
  serializable (§2) and confined to the decided tenant's key range (§5). A `ScopeTenant` admin
  physically cannot write into another tenant.
- **Form derivation.** Scalar fields only (`String`, `Int`, `Float`, `Bool`, `Time`, `Decimal`,
  `Money`), derived from the codec `Shape`. Relations, enums, nested records, and validation are
  out of scope for the generic form and render read-only with an explanatory note.
- **The `goty.rs` erased-`any` fieldset collision** is avoided by representing the form's
  field/value pairs as a **tuple**, not a named `{field, value}` record — the documented
  workaround from `record_fieldset_collision_erased`. This is a shape choice in the console's own
  Sky source, not a compiler dependency.
- **Excluded by construction:** `_sky_sessions` and `_sky_migrations` are never writable from the
  console (editing a session blob is a privilege-escalation primitive; editing the ledger breaks
  the checksum gate).
- **Every mutation** is audit-logged with principal, tenant, collection, pk, before-image, and
  after-image, and emits a `Changeset` (§6) so other operators' views update live.
- **Gates:** `G5.4` write authorization matrix (read-only decision + a POST ⇒ 403);
  `G5.5` a `ScopeTenant` admin's write into another tenant's pk is rejected *and* creates no row
  anywhere; `G5.6` audit completeness — every accepted write has exactly one log entry with both
  images.

---

## 9. RULE ZERO — the executable-state implementation

This section is a deliverable. It is the countermeasure to the failure mode the mandate names:
*"a fresh or compacted session inherits CLAIMS; claims survive compaction while the evidence
behind them evaporates."*

### 9.1 The one command

```bash
cargo run -p xtask -- bluedb-gates
```

Runs every numbered gate, prints a per-goal roll-up, **regenerates
`docs/bluedb/STATUS.md`**, and exits non-zero if any gate fails. Target wall time ≤ 60 s for the
fast tier; the slow tier (`--tier=full`: G1.1 at 50 k sessions, G2.3's crash corpus, G4.3's
benches) runs at phase boundaries and in CI.

```bash
cargo run -p xtask -- bluedb-gates --only=G2.2        # one gate
cargo run -p xtask -- bluedb-gates --json             # machine-readable
cargo run -p xtask -- bluedb-gates --check            # verify STATUS.md matches a fresh run
cargo run -p xtask -- bluedb-gates --verify-mutations # apply every recorded mutation, assert RED
cargo run -p xtask -- bluedb-gates --tier=full
```

### 9.2 The registry

`rust/crates/xtask/src/bluedb_gates.rs`, following the existing gate idiom
(`coerce_floor_gate.rs`, `s8_gate.rs`):

```rust
pub struct Gate {
    pub id:        &'static str,   // "G2.2"
    pub goal:      u8,             // 0 = cross-cutting, 1..5 = the numbered goal
    pub title:     &'static str,
    pub tier:      Tier,           // Fast | Full
    pub run:       fn(&Ctx) -> GateOutcome,
    pub budget_s:  u64,            // hard timeout; exceeding it is a FAIL, not a hang
    pub mutations: &'static [Mutation],
}

pub struct Mutation {
    pub id:     &'static str,      // "G2.2/force-full-scan"
    pub patch:  &'static str,      // docs/bluedb/mutations/G2.2.force-full-scan.patch
    pub expect: &'static str,      // which assertion must go RED, verbatim
}
```

The registry is the single source of truth. A gate that exists in code but not in the registry
does not count; a registry entry with no `run` does not compile.

### 9.3 `STATUS.md` is generated output

```markdown
<!-- GENERATED by `cargo run -p xtask -- bluedb-gates`. DO NOT EDIT. -->
<!-- commit: 5c0d0b7b  ran: 2026-08-09T14:02:11Z  host: darwin/arm64  tier: fast -->

# BlueDB v2 — STATUS

| Goal | Verdict | Gates |
|---|---|---|
| 1 — session-bounded Model state | **PASS** | G1.1 G1.2 G1.3 G1.4 |
| 2 — unified store, real SERIALIZABLE | **FAIL** | G2.1 G2.2 ✗G2.3 G2.4 G2.5 G2.6 |
| …

| Gate | Goal | Title | Verdict | Time | Mutation proof |
|---|---|---|---|---|---|
| G2.3 | 2 | index↔data consistency under crash | **FAIL** | 41.2s | PROVEN @ 9b1f0ac |
| …

## Failures
### G2.3 — index↔data consistency under crash
    orphan index entry: coll=todos idx=2 pk=t-8842 (crash seed 17)
    runtime-go/bluedb/committer.go:214

<!-- body-sha256: 3f2a…  -->
```

Three properties make it trustworthy:

1. **A goal's verdict is computed** from its gates' outcomes. No prose verdict exists anywhere.
2. **Hand edits are detected.** The trailing `body-sha256` covers the generated body;
   `--check` recomputes it and fails with *"STATUS.md is generated output; run
   `cargo run -p xtask -- bluedb-gates`"*. `--check` runs in CI and in a pre-commit hook.
3. **Staleness is detected.** The header records the commit; `--check` fails if `HEAD` has moved
   since the recorded run.

### 9.4 Mutation proof, recorded and re-verifiable

A gate does not count until it has been proven falsifiable **by mutation**. The proof is not a
paragraph; it is a patch plus two recorded outputs.

```
docs/bluedb/mutations/
  G2.2.force-full-scan.patch          # git-apply-able; reintroduces the defect
  G2.2.force-full-scan.expected.txt   # the RED output, verbatim
  G2.1.sqlite-deferred.patch
  …
```

`--verify-mutations` for each mutation:

1. creates a scratch git worktree (never the developer's tree),
2. `git apply`s the patch,
3. runs **only** that gate,
4. asserts a non-zero exit **and** that the recorded assertion string appears in the output,
5. discards the worktree,
6. records `PROVEN @ <sha>` in `STATUS.md`.

Failure modes and what they mean:

| Outcome | `STATUS.md` | Meaning |
|---|---|---|
| patch applies, gate goes RED with the expected string | `PROVEN @ <sha>` | the gate can fail |
| patch applies, gate stays GREEN | `VACUOUS` → **overall FAIL** | the gate is a green lie |
| patch no longer applies | `MUTATION-STALE` → **overall FAIL** | code moved; re-derive the proof |

`MUTATION-STALE` as a *failure* is the anti-rot mechanism: a refactor that invalidates a proof
cannot silently leave a gate un-proven.

**G0.6** is `--verify-mutations` itself, run at every phase boundary and in the nightly sweep.

### 9.5 What a fresh session does

`docs/bluedb/RESUME.md` on this branch is short by design, and its content is an instruction, not
a status:

```markdown
# BlueDB v2 — RESUME
1. Read .claude/AUTONOMOUS_GOAL.md (the mandate).
2. Read docs/bluedb/v2-architecture.md (this design).
3. Run: cargo run -p xtask -- bluedb-gates
   Its output IS the state. docs/bluedb/STATUS.md is that output, committed.
4. Do not trust any prose in any doc about what is done. Run the gates.
```

No phase table with ✅ marks exists anywhere on this branch. The prior branch's phase table is
precisely the artefact that survived compaction while the evidence behind it evaporated.

### 9.6 Gate ↔ goal traceability

Every gate declares its `goal` in the registry. Two static checks run inside
`bluedb-gates` itself:

- every goal 1–5 has **at least one** gate (a goal with no gate is a FAIL, not an omission);
- every gate maps to exactly one goal or is explicitly `goal = 0` (cross-cutting).

So "which gate proves goal #4?" is answered by the tool, not by reading this document.

---

## 10. Phase plan

Each phase is independently verifiable and shippable, and runs the full cycle:
**decide scope → design → grill (≥2 fresh-context adversaries) → implement (worktree) →
three-leg verify (unit `-race` + integration + a REAL app) → fresh-context Judge.** Only a Judge
closes a phase. Every agent brief opens with *"confirm `git log --oneline -1` equals `<base>`;
reset if not"* — 8 of 8 worktrees in the prior session were created off `main` instead of the
branch tip.

| Phase | Scope | Gates | Reused (from) | Net-new |
|---|---|---|---|---|
| **P0 — Rule Zero first** | The gate harness before there is anything to hide: `xtask bluedb-gates`, the registry, `STATUS.md` generation + checksum, `--verify-mutations`, the scratch-worktree runner. Plus the cross-cutting gates, which are implementable with zero BlueDB code. Fix `AGENTS.md:258` (P5) and the `docs/sky-toml.md` / `docs/skydb` `driver` rows (P3). | G0.1 G0.2 G0.3 G0.4 G0.5 G0.6 | gate idiom from `xtask/src/{coerce_floor,s8}_gate.rs` (main) | all |
| **P1 — Substrate port** | `runtime-go/bluedb/`: keys, comparer (`skydb.mvcc.v1`, `base.CheckComparer`), HLC + restart floor, single-writer committer + group commit, changelog, watermark + GC, readset, validate, txn, errorfs crash corpus. **`scanMaterialize` is NOT ported** (P13). Layering enforced from day one. | G2.6 (crash corpus, HLC floor, C1–C7 contracts) | `feat/bluedb` @ `5c1beb69`: `bluedb/{keys,comparer,hlc,committer,changelog,changefeed,watermark,gc,readset,validate,engine,pebble_engine,reader,crashsim_test}.go` | the `persistglue` seam; pebble in `go.mod` |
| **P2 — Tenancy + index keyspace** | User-key layout (§3.2), escaping + null tags + float total order (§3.3), the `0x02` index namespace, the planner (§3.4), seek-backed `ScanRange`, index maintenance in `buildReq` (§3.5), `Persist.explain`, `ScanStats`. Delete `checkCompositeLayout`. Invert `TestReactive_TenantNeverDurable`. | G2.2 G2.3 G2.5 | `index_key.go`'s encoder + `readset.go`/`validate.go`'s range contract (`feat/bluedb`) | index storage, planner, tenancy-in-key, escaping, float/time encodings, `ScanStats` |
| **P3 — One isolation contract** | Closed driver registry + `IsolationStrategy` (§2.4); sqlite split pool + `BEGIN IMMEDIATE` + `synchronous` policy; postgres `SERIALIZABLE` + typed `40001`/`40P01` retry; embedded SSI wiring; startup self-test; the discriminating conformance suite on all three; `sky check` transact-body purity. | G2.1 G2.4 | the retry/backoff shape + typed-`PgError` classification (`feat/bluedb` `db_auth.go:1608-1633`) | split pool, registry, self-test, the anomaly corpus, the purity check |
| **P4 — Persist API + `[data]` + migrations** | `Std.Persist` (§7.3) with no phantom tag and no backend-named connect; `Std.Persist.Sql` escape hatch; `[data]` parsing (first-wins), the generated `sky_data.go`, `persistglue`; `sky data` verbs; `reindex`; `docs/skypersist/`; `sky doc`. | G3.1 G3.2 G3.3 | `Std/Persist.sky`'s `Cond`/`Query`/builder shape + `guardIdents` (`feat/bluedb`); migration machinery (main) | glue emission, `[data]`, one-Conn API, the SQL-module build check |
| **P5 — Session-bounded Model state** | `_sky_sessions` collection + opaque-blob envelope; `chooseStore` `case "data"` as default; byte + count accounting in the funnel; deflation; provisional admission; coalescing outbox; gauges. Invert `TestTiered_SSEConnectedNeverEvicted`; wire `idleEvict` into `sky.toml` (currently unreachable). | G1.1 G1.2 G1.3 G1.4 | the persist-before-ack funnel `persistAndShipFrame` + the AST-dominance tripwire (`feat/bluedb` `e1f6eaf2`, `947cd114`); the 5c envelope (`27470bff`) | the cache, deflation, admission, accounting, metrics |
| **P6 — Reactivity at scale** | Changeset at the Persist commit boundary; delta application (not re-query); the resync consumer; `ChangeBus` local/postgres/redis + `_sky_changelog` gap recovery; startup-time capability assertions replacing the first-session `os.Exit`; the fan-out bench baseline. | G4.1 G4.2 G4.3 G4.4 G4.5 | the changefeed + the shared-predicate `matchCache` + the Enter/Leave/Stay truth table (`feat/bluedb` `reactive.go`) | the Persist-boundary emit, SQL-backend changesets, `ChangeBus`, the resync consumer, delta application |
| **P7 — Console admin (5e-1)** | `consoledata` package, `Decide()`, the registry write-once/deep-copy fix, the Data tab, audit logging. Delete `isLoopbackRemoteAddr`. Fix the console's self-fetch 401. | G5.1 G5.2 G5.3 | `salvage/p5e-foundation` @ `de3e7431` (registry write-once + deep copy, `SchemaOf`, `adminShow`/`TenantCol`, `embedded_admin_test.go` mutation proofs); `phase5e-closure-design-v2.md` v2.1 (authorization design); `exp/bluedb:runtime-go/rt/console_data_sql.go` (default-deny table allow-list, separate capped read-only pool, constructed SELECTs, caps + timeout, opaque DSN handle, audit) | the Data tab UI, the durable-tenancy scoping, `Decide()`'s no-argument shape, SQL-backend enumeration, `adminShow` replacing the name-pattern deny-list |
| **P7b — Console writes (5e-2)** | **Only if the user rules for read-write.** §8.4 as specified. | G5.4 G5.5 G5.6 | — | all |
| **P8 — Whole-goal close** | Full-tier gate run, `--verify-mutations`, example sweep, `verify-cli`, `verify-all-web`, conformance, cross-compile; fresh-context Judge against the verbatim five goals. | all | — | — |

**Ordering rationale.** P0 first is not ceremony: the prior attempt's three false-green gates
were all authored *after* the code they guarded, by the same context that wrote the code. Building
the harness while there is nothing to certify removes that coupling. P1 before P2 because the
comparer is irreversible. P2 before P3 because A-PH (the phantom-under-seek anomaly) cannot be
written until seeks exist. P4 before P5 because sessions-as-collection needs the Persist API. P6
after P5 because the funnel is the reactive apply point. P7 after P2/P5 because its security
property *is* §5's key scoping.

**Push discipline.** Local commits at verified sub-milestones; push once per phase boundary,
after that phase's Judge. Not per commit, not per green gate.

---

## 11. Irreducible floor and risk register

### 11.1 Cannot be fixed — and why

| # | Floor | Why |
|---|---|---|
| F1 | **SQLite: one writer, one machine.** | SQLite's write lock is per-file and serialises read-write transactions by design. Serializability is achieved *because* of it. Multi-writer serializable on one SQLite file is not a thing. WAL is also undefined on network filesystems, so "one machine" is a hard boundary. |
| F2 | **Embedded: one process.** | Pebble takes an exclusive directory lock. N replicas cannot share an embedded store. Multi-replica requires `driver = postgres`. |
| F3 | **Multi-replica topology is not runtime-detectable.** | N processes each with a private local store are indistinguishable from one process. `[data] replicas` is an operator assertion. The design can only choose *when* to check it (boot) and how loudly to fail. |
| F4 | **Postgres SERIALIZABLE is not strict serializable.** | SSI guarantees a serial equivalent, not agreement with real-time order. Sky promises serializable, not linearizable, and says so. |
| F5 | **Per-connection RAM is linear in connected clients.** | One goroutine, one socket, one coalesced frame per client. §4 bounds session *state*; it cannot bound the client count. Measured and published (§4.7), not hidden inside "bounded". |
| F6 | **No compile-time backend-capability typing.** | Sky is HM with no type classes and no HKT, and the backend is a runtime value injected at boot. A type cannot depend on it. §2.6 substitutes build-time static-reference checking + a boot self-test. |
| F7 | **Sticky sessions remain required.** | A session's Model has one owner under one mutex. Cross-instance frame fan-out does not fix a split Model. The `sky_sid` affinity requirement is unchanged by anything here. |
| F8 | **`rt.Coerce` at the wire boundary.** | Decoding a persisted row into a typed Sky record is the existing §8 "wire decode" floor category. BlueDB does not widen it and does not remove it. |
| F9 | **Text index order is byte order, not collation.** | Locale-aware collation would require ICU (a cgo dependency) or a large table. Byte order is documented; a case-insensitive index is achieved by indexing a derived normalised column. |

### 11.2 Deliberately bounded in v2

| # | Bound | Escape |
|---|---|---|
| B1 | No index seek on `Decimal` / `Money` / `Bytes` — **a build error**, not a silent full scan | index a derived integer minor-unit column; or accept it as a residual predicate |
| B2 | No covering indexes — a seek yields pks, then point gets | the point gets share the block cache; covering indexes are a later cycle |
| B3 | `CondOr` / `CondNot` do not produce spans (except single-column `CondIn`) | they become residual predicates; `explain` shows it |
| B4 | Cross-replica delivery is at-least-once, not exactly-once | application is pk-keyed and idempotent |
| B5 | `NOTIFY` carries a summary, not the row body | subscribers read `_sky_changelog` for the gap |
| B6 | Console admin writes are scalar-only, and only if the user rules for read-write | relations/enums render read-only |
| B7 | The session Model is an opaque blob, not a typed collection | this is deliberate (§4.2) — it dissolves P19 rather than assuming it away |

### 11.3 Risks

| # | Risk | Likelihood | Mitigation |
|---|---|---|---|
| R1 | **Pebble bloats every Sky app.** Binary +10–18 MB; pebble pulls sentry + prometheus transitively | high if wired wrongly | G0.3 asserts a non-Persist app links zero pebble symbols, ships no `bluedb/`, and builds cold-cache **offline**. The glue-file design (§7.2) is what makes this structural rather than a prune list. |
| R2 | **Replacing the session store breaks a working subsystem.** | medium | The funnel is a single seam — only what "persist" means inside `persistAndShipFrame` changes. Legacy stores stay as opt-outs. G1.2/G1.3 are the guard. |
| R3 | **Deflation adds a store read to the event path.** | medium | Measured in G1.1's latency arm; deflation is LRU-cold-first so hot sessions are unaffected; `sessionCacheMaxBytes` is tunable upward. |
| R4 | **Inverting two locked tests** (`TestTiered_SSEConnectedNeverEvicted`, `TestReactive_TenantNeverDurable`) | certain | Both are inverted deliberately and in the open, with the replacement test named in §4.4 / §5.2. A griller should check that the *replacement* is stronger, not merely different. |
| R5 | **The tenant key-prefix migration touches every key.** | medium | Resumable, verified by a G2.3-style post-migration re-derivation, and a no-op beyond 3 bytes for single-tenant apps. |
| R6 | **Mutation patches rot.** | high over time | `MUTATION-STALE` is a **FAIL** (§9.4), so rot surfaces immediately instead of leaving gates silently un-proven. |
| R7 | **The A-PH anomaly is the one that could be got wrong quietly.** A seek that reads fewer keys than a scan is exactly how a phantom escapes an incomplete read-set. | medium | A-PH is in G2.1, and §3.5 keeps the *recorded* predicate identical to the pre-seek design so the existing SSI proof still applies. Grillers should attack this first. |
| R8 | **Postgres poolers in transaction mode break SSI.** | medium | The boot self-test (§2.6) runs a real write-skew probe against the configured database, so a pooler misconfiguration fails at deploy. |
| R9 | **The `sky check` transact-purity rule produces false positives** (rejecting a legitimate body). | medium | It is a closed list of known-non-replayable kernels, not an effect inference; an escape hatch (`Persist.transactUnsafe`) exists, is named to discourage, and is excluded from the retry loop. |
| R10 | **`ScopeSystem` is a cross-tenant read primitive by design.** | inherent | Only the funnel mints `Admin`; every `System` operation is audit-logged; `adminFromEnv` refuses in production without an explicit opt-in. G5.1 covers the grant paths. |
| R11 | **Sky.Live runtime bugs land separately** (`handleEvent` session hijack, `sendBeacon` CSRF 403, the reactive gate's first-session `os.Exit`, `live.go`'s implicit lock contract) and this design touches adjacent code. | high | Out of scope here per the mandate; shipping off `main` on `fix/skylive-runtime-soundness`. P5 and P6 touch `live.go` and must rebase onto that work rather than re-fixing it. The one overlap this design *does* claim is the reactive gate's `os.Exit`, which §6.5 replaces with a startup check — coordinate, do not duplicate. |
| R12 | **`Persist.conn` is a memoised zero-arg binding (a CAF).** | low | Correct here — it is a shared pool handle, not a fresh value — but it is exactly the shape the compiler warns about for `Uuid`/`Random`/`Time`. Document it in `docs/skypersist/` so it is not "fixed" by a later reader. |

---

## Appendix — orientation in one screen

```
sky.toml [data]
   │  (read at BUILD time — decides what is generated)
   ▼
sky-out/sky_data.go            generated: blank-imports persistglue, sets rt.DataConfig
   │
   ├─► sky-app/persistglue     the ONLY package importing both rt and bluedb
   │        │
   │        ▼
   │   sky-app/bluedb          pebble + stdlib ONLY.  Never imports rt.
   │      L1  keys / comparer(skydb.mvcc.v1) / HLC / single-writer committer / changelog / GC
   │      L2  txn / read-set (points + index RANGES) / validate  →  SSI
   │      L3  index keyspace 0x02 / planner / seek                →  O(log n + k)
   │
   └─► sky-app/rt              never imports bluedb
          DataBackend (stdlib types only) · SessionStore · the persist-before-ack funnel
          ChangeBus · consoledata.Decide()

Sky source:  Std.Persist  (one Conn, no backend named)
             Std.Persist.Sql  (escape hatch; referencing it constrains [data] at BUILD time)

Truth:       cargo run -p xtask -- bluedb-gates   →   docs/bluedb/STATUS.md (generated)
```

Key facts a griller should check first, because everything else rests on them:

1. `userKey` is opaque to the comparer (`keys.go`, `Split` reads the trailing length byte) —
   therefore §3's index namespace and §5's tenancy prefix do **not** touch `skydb.mvcc.v1`.
2. Index entries are ordinary MVCC rows in the same `CommitReq` — therefore index maintenance is
   inside the single-writer commit path with no new machinery.
3. A seek records the same `indexRange` the current design records — therefore §3 preserves §2's
   SSI proof rather than re-deriving it. A-PH exists to test that claim, not to assume it.
4. The generated glue file means `rt` never imports `bluedb` — therefore the P0-class breakage of
   every non-Persist app cannot recur, and a dead config key cannot exist.
5. `STATUS.md` is generated and checksummed, and an un-provable gate is a FAIL — therefore a
   green lie cannot survive compaction.
