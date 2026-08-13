# BlueDB v2 — architecture

> **Status:** architecture design **v2.1** for branch `feat/bluedb-v2` (off `origin/main` @ `fdbc398d`).
> This document is what the grillers attack and what the phases implement. It contains no
> production code — contracts, encodings, signatures, gates, and a phase plan.
>
> **Authority:** `.claude/AUTONOMOUS_GOAL.md` on this branch. The five goals and RULE ZERO
> there are the definition of done; this document is subordinate to it and may not narrow it.
> **Goal #5 is READ *AND* WRITE by user ruling (2026-08-09)** and closes only when writes work.
>
> **Relationship to `feat/bluedb`:** that branch is *research, not truth*. Its verified
> substrate (Pebble + MVCC-in-key + the `base.CheckComparer` gate, the single-writer committer
> with HLC floor, the changefeed, SSI read-set validation, the errorfs crash corpus) is kept.
> Everything above it is redesigned here. §0 lists every premise of the prior work that this
> design found to be false.

---

## Citation provenance — read this before checking any line number

Three code bases are cited in this document and **they are different trees**. v2.0 mixed them,
so several citations resolved against the wrong branch or against nothing.

| Tag | Resolves against | Contains |
|---|---|---|
| **`[main]`** | `origin/main` @ `fdbc398d` — also the current checkout `feat/bluedb-v2` | `runtime-go/rt/`, `rust/crates/`, `sky-stdlib/`, `docs/`, `.github/` |
| **`[bdb]`** | `feat/bluedb` @ `5c1beb69`…`9ad00daf` | `runtime-go/bluedb/` (35 files) + `runtime-go/rt/bluedb_reactive*.go`. **Absent from `main` and from this branch.** |
| **`[p5e]`** | `salvage/p5e-foundation` @ `de3e7431` | the console authorization funnel, `SchemaOf`, registry write-once |
| **`[exp]`** | `exp/bluedb` | `runtime-go/rt/console_data_sql.go` browse hardening |

Rules now in force: **(1)** every code citation in this document carries its tag; **(2)** the
engine path is `runtime-go/bluedb/…`, never `runtime-go/rt/bluedb/…`; **(3)** a citation without
a tag is a defect, not a detail — v2.0's untagged citations are why `db_auth.go`'s `[bdb]` lines
were checked against `[main]` and reported as rot.

**Commits that do not exist where v2.0 said they did** (verified with `git branch --contains`):
`e1f6eaf2` and `27470bff` live **only** on `feat/bluedb-backup-prerebase`; the rebased equivalent
on `feat/bluedb` is **`9ad00daf`** (the persist-before-ack funnel). `947cd114`, `5c1beb69` are on
`feat/bluedb`; `de3e7431` is on `salvage/p5e-foundation` only. All references corrected below.

---

## Changelog — v2.0 → v2.1

Two adversarial grillers returned **PROCEED WITH CHANGES**: 6 blocking (engine lens) and 10
blocking (goals/gates lens). Every finding is resolved below or pushed back on with evidence.
Line numbers in the Evidence column were re-verified against the branch named in the tag.

### Harness — fixed first, because nothing else is trustworthy until it is

| # | Finding | Resolution | Evidence |
|---|---|---|---|
| **H1** | A gate with zero recorded mutations passes silently; 12 of 26 gates had none | §9.2 `mutations` must be **non-empty**; zero ⇒ `UNPROVEN` ⇒ goal **FAIL**, enforced as the third static check in §9.6. **All missing mutations authored** in §9.7 | count confirmed: G0.1/0.2/0.3/0.5/0.6, G2.6, G3.1/3.2/3.3, G4.1/4.2/4.3 + G5.2/5.3/5.4/5.5/5.6 had no `Mutation` row in v2.0 |
| **H2** | Default command reports PASS while `--tier=full` gates never ran | §9.3: `STATUS.md` enumerates **every registered gate**; `NOT RUN` is a distinct non-PASS state; any NOT-RUN gate ⇒ goal renders **`UNKNOWN`**, never PASS; `--only` never writes `STATUS.md`; header records `full-tier-commit` separately and marks it `STALE` when `HEAD` moved past it | v2.0 §9.5 told a fresh session to run the plain command; §9.3's schema had only PASS/FAIL |
| **H3** | `--verify-mutations` is itself unfalsifiable (could apply the patch in the scratch worktree and run the gate against the dev tree) | §9.4: permanent canary pair `G0.C` (asserts `true`) + a no-op patch. `--verify-mutations` **must** report `VACUOUS` for the canary and **FAILS** if it reports `PROVEN` | new §9.4 |
| **MAJOR-17** | `PROVEN @ <sha>` sits un-revalidated because `--verify-mutations` is not in the default command | §9.4: render `PROVEN @ <sha>` **plus** an `UNVERIFIED-SINCE` non-PASS marker computed by diffing the patch's target paths against that sha | new §9.4 |

### Blocking — engine lens

| # | Finding | Resolution | Evidence |
|---|---|---|---|
| **E-B1** | Seek bound and read-set bound are different byte spaces; §3.4/§3.5 conflated them ⇒ phantoms admitted. Rule-4 formulas wrong once `pk` is appended | §3.4a: **two separately-named artefacts from one encoder pass** — `coordBounds` (recorded) and `seekBounds` (physical). All physical bounds **half-open** via `bytesSuccessor`. New **G2.11** asserts byte-equality of recorded bounds across the seek and pre-seek paths | `[bdb]` `txn.go:171-186` records `indexRange{index,lo,hi}` from `encodeScanRange` — pure coordinate space; `validate.go:65` `inRangeClosed(r.lo,r.hi,c.Key)` where `c.Key` is `IndexCoord.Key` (`keychange.go:34-37`); `bytesSuccessor` is at **`reader.go:91-100`** (both v2.0 and the griller cited `:100-110`, which is the `pebbleCursor` struct) |
| **E-B2** | Tenancy is in the key but not in the conflict domain or the changeset ⇒ global write serialization at 1000 tenants; `Changeset.Tenant` has no derivation | §3.2a: `IndexCoord.Key` is **tenant-prefixed**; `Txn.collWitness` becomes `map[tenantColl]bool`. §6.2: `Changeset.Tenant` is **decoded from the row key**, never from `CommitReq.Tenant`. New **G2.10** | `[bdb]` `keychange.go:16-20` — `CollID` "stable per-collection", `IndexID` "stable per-(collection,index)"; **zero** tenant references in `keychange.go` or `readset.go`; `readset.go:20-24` `indexRange{index,lo,hi}`; `txn.go:54` `collWitness map[CollID]bool`; `txn.go:200` `WitnessCollection`; tenant documented "TRANSIENT… NEVER durably written" (`txn.go:78-81`, `engine.go:123-130`) |
| **E-B3** | G2.1 is not discriminating three ways: auto-retry hides the signal, no plan-shape precondition, and G2.4 forbids the barrier G2.1 needs | §2.5 rewritten: assertion is **final state ∈ serial outcomes** + the exposed `validateCalls` counter; every anomaly asserts `Persist.explain`'s `Access` **first**; a `forceFullScan` arm whose expectation is that anomalies *still pass*, paired with a discriminating indexed arm; the barrier is resolved as an explicitly-exempt `Persist.Test.barrier` kernel | `[bdb]` `txn.go:14` `const maxTxnAttempts = 8`; `validate.go:8` `var validateCalls atomic.Int64`, documented at `:5-7` as "a test seam", incremented `:28`; `ScanCollection` (`txn.go:619`) calls `WitnessCollection` then `scanPrefixMaterialize`. **Partial pushback on the third leg — see below** |
| **E-B4** | `IsolationStrategy` cannot deliver `BEGIN IMMEDIATE`; `IsConflict(error)` cannot see the dominant path | §2.4 rewritten: `BeginWrite` returns a `WriteTx` obtained from a **pinned `*sql.Conn`**, and the IMMEDIATE knob is a **DSN parameter** on the writer DSN. §2.5's mutation retargeted to the DSN knob. `IsConflict` split into `IsConflict(error)` **and** `IsConflictValue(rt.SkyError) bool` | **VERIFIED**: `[main]` `runtime-go/rt/jobs/sqlite_store.go:78` uses `?…&_txlock=immediate`; literal `BEGIN IMMEDIATE` SQL appears **nowhere** in `runtime-go/` on `[main]` (only in comments at `:44`, `:76`) — so v2.0's mutation was MUTATION-STALE on day one. **Partial pushback on the interface-shape claim — see below** |
| **E-B5** | Index tombstones are immortal ⇒ complexity claim decays; G2.2 cannot see it | §3.5a: **tombstone reclamation** (a sole-remaining tombstone below `T` is dropped). G2.2 gains an **M-update-cycle arm** asserting `KeysVisited` is invariant in M; mutation: disable reclamation ⇒ RED at M = 10·N | `[bdb]` `gc.go:97-100` — `if !keptBelowT { keptBelowT = true; continue }`; the value marker (`markerTombstone`/`markerPut`) is **never read** in `gc.go` (only `decodeDataVersion(k)` timestamps are) |
| **E-B6** | Changing the column encoding changes **durable** changelog bytes; only the index keyspace was versioned | §3.3a: the changelog payload is versioned (`payloadFmtV2`), a coord-encoding bump **fails closed** on pre-bump entries, and `sky data reindex` gains a **drain-to-T barrier** | `[bdb]` `keychange.go:42` `const payloadFmtV1 byte = 0x01`; `EncodeChangelogPayload` `:49-63`; written durably at `committer.go:343-346` under `Apply(b, pebble.Sync)` (`:306` SSI path, `:135` blind path); read back by `changelogTailChanges` on the ring-spill validation path |

### Blocking — goals/gates lens

| # | Finding | Resolution | Evidence |
|---|---|---|---|
| **G-B4** | `unique` is in the API with no mechanism, no gate, and the key layout defeats it; **and** the P12 premise was half false — a working stored unique mechanism exists on `feat/bluedb` and `backend.go` was not in P1's port list | §3.2b: unique indexes get a **pk-free** entry key (`0x03` namespace) whose value is the owning pk; duplicate detection in `buildReq` is a **read-set point read**, so concurrent duplicate inserts conflict. `backend.go` **added to P1's port list**. New **G2.7** | **VERIFIED, griller correct**: `[bdb]` `backend.go:262` `func uniqUserKey(coll, indexName string, colType ColType, valBytes []byte) []byte` → `coll ‖ 0x1E ‖ indexName ‖ 0x1F ‖ encodeIndexKey(…)`, **no pk**, pk stored as the *value* (`:259-261`); maintained at `embedded.go:288` (old-value delete), `:294-298` (`tx.Get(uKey)` → `ErrUniqueViolation` → `tx.Put(uKey, []byte(pk))`), `:318` (delete). P12 corrected in §0.2 |
| **G-B5** | `Codec.Shape` cannot support §3.3's type-directed encodings or B1's build error | §3.3b: the encoding source is the **HIR type of the record literal passed to `Codec.auto`**, correlated with the `Collection` declaration and threaded into the generated glue as `CollDecl.Cols[i].SkyType`. §3.3 and B1 re-derived from that. New **G2.12** (Time-typed vs Int-typed columns encode differently) | **VERIFIED, griller correct**: `[main]` `sky-stdlib/Std/Codec.sky:67-73` — `type ColType = CText \| CInt \| CReal \| CBool \| CBlob \| CNull ColType` (a 5-way **storage** class: Decimal/Money/String all `CText`; Time/Int both `CInt`; Float/Decimal both `CReal`); `Shape` at `:77-80`. And `Shape` is a **runtime** value: `Codec_autoCols` (`runtime-go/rt/codec_auto.go:515`) derives it by `reflect.TypeOf(witness)` |
| **G-B6** | "lock-safe" is a verbatim goal-2 clause with zero gates, and §4.4 adds an ABBA cycle | §4.0: the **global lock order** is stated (cache lock is a leaf; deflation snapshots under the cache lock and releases before touching a session). New **G1.5 lock safety** (`go test -race`, timeout ⇒ goroutine dump); mutation: invert acquisition order ⇒ RED | v2.0 §4.3's `sessionCache` declared no mutex and §4.4 described deflation walking sessions while the funnel updates cache accounting. The prior attempt's worst failure was a deadlock on every page load |
| **G-B7** | G2.4's HIR walk is bypassed by anything beyond a literal lambda | **Adopted**: G2.4 becomes a **runtime poison flag** — a goroutine-local "inside transact" mark that every non-replayable kernel checks. Removes the compiler change from P3. **Extended**: the mark must **propagate across `rt`'s task-spawn seam**, or `Task.parallel` inside a transact body escapes it | `[main]` the mechanism already exists and is canonical: `runtime-go/rt/live_session_ctx.go` — `liveSessionByGoroutine sync.Map`, `currentLiveSession()`, and the `runWithLiveSession(sess, fn)` set/defer-clear wrapper, itself modelled on `goroutine_context.go`'s trace context |
| **G-B8** | G1.1 probably cannot run, and can pass while the app OOMs | §4.7 restructured: prove the bound the **cheap** way — `maxBytes`/`maxEntries` set LOW, assert the ceiling holds, deflation triggers and 503-refusal fires at **N ≈ 200**; body size **parameterised** (1 KB and 50 KB); an **admission bound on connections**; the 50 k arm demoted to a `--tier=full` capacity **REPORT**, not a correctness gate. `perConnFloor` becomes a committed constant in `baselines.json` | RAM arithmetic confirmed: 50 k × 50 KB ≈ 2.5 GB of bodies vs a 64 MiB `sessionCacheMaxBytes` — the asserted bound is ~2 % of the footprint. **Partial pushback on the fd-limit leg — see below** |
| **G-B9** | P3 depends on P4 | Dissolved by the **re-slice**: a minimal `Std.Persist` moves into **P2** | §10 |
| **G-B10** | Goal #4's verbatim "query/row-scoped" has no falsifier, and §6.3's central claim has none | New **G4.6** query-scoped **non**-delivery (subscribe `status="a"`, commit `status="b"`, assert zero wakes; mutation: broadcast on any commit ⇒ RED). New **G4.7** asserts the delta path **mechanically** with `ScanStats` (`KeysVisited == 0`, `RowsReturned == 0` per notification; mutation: replace delta application with `Persist.toList` ⇒ RED) | v2.0's G4.1/G4.2 prove delivery and tenant non-delivery only; §6.3's "apply the delta" was guarded by a **baseline** gate, and baselines seed from whatever ships first |

### Deliverability re-slice (third-abandonment risk rated HIGH by both grillers)

| # | Change | Where |
|---|---|---|
| **D1** | A **minimal `Std.Persist`** (`collection`/`key`/`get`/`put`/`query`/`toList`, embedded only, zero-config, no tenancy/index/reactivity) moves into **P2**, so the §7.6 app runs and persists at phase 2 and later phases harden a working thing. Dissolves G-B9 | §10 |
| **D2** | G2.4's HIR walk → runtime poison flag; the compiler change leaves P3 | §7.3, §10 |
| **D3** | The `redis` `ChangeBus` is **cut to §11.2** — `local` + `postgres` fully serve goal #4 | §6.4, §11.2 |
| **D4** | `Std.Persist.Sql` **and** its build-time static-reference check are **cut from v2**; raw SQL via the existing `Std.Db` becomes a **startup fatal** on embedded | §7.4, §2.7 |
| **D5** | The `[live]`/`[analytics]` subsumption + deprecation mapping moves to **post-P8** | §7.1, §11.2 |
| **D6** | G1.1 restructured per G-B8 | §4.7 |

### MAJORs resolved

| Finding | Resolution |
|---|---|
| Citation rot (`e1f6eaf2`/`27470bff`; `db_auth.go` lines; `[main]` numbers cited for `[bdb]` files) | The **Citation provenance** block above; every citation now tagged; commits corrected to `9ad00daf` |
| §1's "Fully delivered?" column pre-authorizes qualified PASS verdicts | Column is now bare **Yes/No**; every bound moved to §11.2 |
| Goal #1's verbatim word **"sync"** silently dropped | §4.0a **defines** sync and gates it: new **G1.7** (multi-connection convergence across a deflate) |
| "high-throughput" has no threshold | §2.7 states **absolute hand-committed floors** per backend, flagged for user review (a baseline seeded from first-ship is the G-B10 anti-pattern) |
| Contract clause 3 (power-loss durability) has no gate | New **G2.9** — `synchronous=FULL` / `pebble.Sync` verified by an fsync-counting harness + a kill-power simulation |
| The "unified API" is unified by **subsetting** | Disclosed as **B8** in §11.2, and named in §7.3 |
| `[data]` as default session store puts pebble in every Sky.Live app; G0.3 only tests non-Persist apps | §7.1: the `[data]` session-store default applies **only when the app already declares a Persist collection**; a pure Sky.Live app keeps `memory`/`sqlite`. G0.3 gains that arm |
| G0.4's Glue arm proves reachability, not liveness | §7.2: the Glue arm byte-diffs the glue **and** asserts an observable behaviour change at runtime |
| Reactivity emitted AFTER commit, not IN it; on SQL, who writes `_sky_changelog`? | §6.2a: on SQL the changelog row is written **inside the user's transaction**, so the changeset is commit-atomic and recoverable; new **G4.8** kills the process between commit and emit and asserts the changeset still arrives |
| `execRaw` writes produce no changeset | Dissolved by **D4** (no `Std.Persist.Sql`); raw `Std.Db` use is disclosed as **B9** — it bypasses reactivity |
| G2.5 arm 3 is scoped to `bluedb` but the tenant VALUE arrives via a bridge whose session-hijack bug is out of scope | §5.5: `fix/skylive-runtime-soundness` is a **DECLARED MERGE DEPENDENCY**; new gate arm **G2.5.5** fails if the fix is absent from the merge-base |
| Sessions-as-collection × persist-before-ack × single-writer = unpriced write amplification | New **G1.6** — fsync-counting under G1.1's own workload, asserting the group-commit amplification factor and a p99 ack-latency budget |
| G4.5 doesn't exclude alternative convergence paths | §6.3: G4.5 disables the timer and the reconnect resync, leaving only the latch |
| Neither migrations (dev pain #3) nor §5.4's key rewrite has a numbered gate | New **G3.4** (migration lifecycle) and **G2.8** (tenant key rewrite) |
| §5.4's migration OOMs (`Txn` buffers the whole write-set) / chunked isn't atomic / single→multi flip unaddressed | §5.4 rewritten: a **generation-stamped dual-read** migration — chunked, resumable, and correct while partially applied, because readers consult both generations until the barrier flips |
| `Reader.Iterate(prefix)` takes a raw prefix, so §5.2 property 3 is false | §5.2: the **raw-prefix API is removed** — `Iterate` takes a typed `Scope` the caller cannot forge. Proving a hard interprocedural dataflow property is replaced by deleting the hole |
| Split reader pool can starve WAL checkpointing | §2.3: explicit `wal_autocheckpoint` + `SetConnMaxLifetime` policy; **G2.9** gates WAL size under sustained write load |
| Long constant key prefixes defeat pebble's `AbbreviatedKey` | §3.2c: the tenant component is placed **after** a 4-byte `collID`/`idxID` for multi-tenant apps, and the trade-off is measured in G2.2 |
| `[data]` still doesn't unify the jobs store and the exporter spool | Disclosed as **B10**; unification scheduled post-P8 |
| `perConnFloor` must be a committed constant; `sessionCache.bytes` bounds an accounting variable, not RAM | §4.7: `perConnFloor` moves to `baselines.json`; a new mutation **undercounts `treeBytes`** and must go RED |
| Opaque-blob Model + hand-declared `sessionVersion` gets more dangerous once sessions are durable | §4.2a: `sessionVersion` is **derived from a compiler-computed structural hash** of the Model type and emitted into the glue; the hand-declared key is deleted |
| Deflation requires handler-id determinism across re-render | §4.4a: stated as a contract, gated by **G1.2**'s new arm; inflate-failure behaviour and lock ordering specified |
| G2.1 needs a rendezvous assertion | §2.5: `Persist.Test.barrier` + an assertion that both transactions were actually in flight simultaneously |
| §6.5's startup-fatal matrix must cover sessions | §6.5: `replicas > 1` with an embedded **session** store is a startup fatal |
| Schedule the goal-#5 question in P0 | Now **answered** (read + write) and recorded in §8.1 |

### MINORs resolved

`G0.5`'s "both paths" is **one** site — `run_go_build_once` (`[main]` `build.rs:708-735`) — called from **three** paths (`:578` FFI-detected CGO=1, `:590` CGO=0, `:600` CGO=1 retry); §7.2 corrected, and the single site is now cited as the reason the property is structural · `G0.3` gains its mechanism (temp `GOMODCACHE`, `GOPROXY=off`, `go tool nm`) · `G2.2` gains a compaction precondition · `sseChanBuffer` is a **var** (`[main]` `live.go:6540`), clamped from `sseChanBufferDefault=16` (`:6534`) — §4.1 corrected · `live_store.go:715` → **`:712`** · `rust-ci.yml:219` → **`:255`** (`:219` is the Postgres-only integration subset) · §7.6's app moves to `docs/skypersist/todo.md` where **G3.2** gates it, and is completed so it compiles · the Appendix no longer places `consoledata` inside `rt` · `or_` is disclosed as **B3** · P0 explicitly includes building the xtask **gate registry** (there is no existing registry idiom to reuse — only single-purpose gates) · **`Std.Money`/`Decimal` un-indexable in the default database is escalated to a user-attention item**, not a quiet B-row (§11.4), because `Std.Money` on `Std.Decimal` is `AGENTS.md`'s pinned currency default.

### PUSHBACK — three findings contested, with evidence

| # | Contested claim | Evidence | Net effect |
|---|---|---|---|
| **P-1** | **G-B8**: "macOS default fd limit 256" makes G1.1 unrunnable | Measured on the development host: `ulimit -n` ⇒ **1048576**. CI runners are similarly raised. The fd leg does not hold | The **finding is adopted in full anyway** — for the reasons that do hold: ephemeral-port exhaustion (~28 k per source-IP 4-tuple), the unspecified harness, the ~2.5 GB-vs-64 MiB arithmetic, and the "green while OOM" defect. Only the fd sentence is dropped |
| **P-2** | **E-B4**: "`BeginWrite(ctx) (Tx, error)` *implies* `database/sql`'s bare `BEGIN`, so `IsolationStrategy` **cannot** deliver `BEGIN IMMEDIATE`" | The signature constrains the *return*, not the implementation: an implementation is free to pin a `*sql.Conn` and `ExecContext` literal SQL, exactly as `[bdb]` `db_auth.go` does. "Cannot" is an inference the signature does not license | The **substantive halves are adopted in full**: (a) verified that on `[main]` the IMMEDIATE knob is a **DSN parameter** (`jobs/sqlite_store.go:78` `_txlock=immediate`) and literal `BEGIN IMMEDIATE` SQL exists **nowhere** in `runtime-go/`, so v2.0's §2.5 mutation was MUTATION-STALE on day one — retargeted; (b) a Sky `transact` conflict is a Sky `Error` **value**, not a Go `error` — `IsConflict` split in two. §2.4's interface is respecified to make the pinned-conn requirement explicit rather than implied |
| **P-3** | **E-B3(c)**: "G2.4 **forbids** what G2.1 requires" | v2.0's G2.4 list is a **closed enumeration** — `Http.*`, `File.*`, `Time.now`, `Uuid.*`, `Random.*`, `Db.execRaw`, `Task.perform` of an external effect. A test rendezvous barrier is in none of them, so there is no literal conflict in the shipped text | The **finding is adopted anyway**, because it becomes true under the G-B7 fix: with a runtime poison flag, a barrier *is* a kernel call inside `transact` and would be poisoned. §2.5 and §7.3 therefore name `Persist.Test.barrier` as an explicitly-exempt kernel, linked only under a test build tag, and G2.1 asserts the rendezvous actually happened |

**Meta-finding.** Both grillers' own citations rot at the same rate as the document's:
`bytesSuccessor` is at `[bdb]` `reader.go:91-100`, not `:100-110`; `IndexID`/`CollID` are at
`[bdb]` `keychange.go:16-20`, not `:25-31`; the coordinate-space bound recording is at `[bdb]` `txn.go:171-186`,
not `:493-501`; the bound predicate is `[bdb]` `validate.go:65`, not `:66-71`; `payloadFmtV1` is at
`[bdb]` `keychange.go:42`, not `:41`. This is not a complaint — it is the reason the **Citation
provenance** rules above are now part of the document rather than an assumed practice, and the
reason **G0.7** (new) machine-checks that every `file:line` cited in this document still resolves
to the quoted token on the tagged branch.

---

## 0. Premise audit — what the prior work asserted that is not true

The prior attempt repeatedly built on claims that did not hold. Every claim this design
relies on was re-verified against source. The following were found FALSE or materially
misstated. Each is cited so a griller can check it in one command.

### 0.1 About the current tree (`feat/bluedb-v2` = `origin/main` + the mandate doc)

| # | Claim | Reality |
|---|---|---|
| P1 | "`[data] driver` is a no-op" implies `[data]` exists | **`[data]` does not exist on `main` at all.** `read_sky_toml_config` (`[main]` `rust/crates/project/src/build.rs:885-996`) handles `[database]` only. `[data]` is `feat/bluedb`-only. v2 introduces it net-new. |
| P2 | `Std.Persist` shipped (memory `std_persist_unified_data.md`, 2026-08-04) | **Not on `main`.** `sky-stdlib/Std/` has no `Persist.sky`. It exists only on `feat/bluedb` / `exp/bluedb`. Net-new here. |
| P3 | `DB_DRIVER` is written and read by nobody | **CONFIRMED when written — and CLOSED on `main` since, in `6aa275bd`, before this branch was rebased onto it.** It was written at build.rs line 802, had zero readers, and was documented as a working knob at docs/sky-toml.md line 202 / docs/skydb/overview.md line 558 and pinned green by a test at build.rs line 1442. None of those four lines exists on `[main]` now. The successors: the emission is gone and a *negative* assertion pins its absence (`[main]` `rust/crates/project/src/build.rs:1735`), both doc rows now say the driver comes from the DSN's shape (`[main]` `docs/sky-toml.md:208`, `[main]` `docs/skydb/overview.md:561`), and the DSN rule itself is `detectDriver` (`[main]` `runtime-go/rt/db_auth.go:350`). **The four original lines still exist verbatim on `[bdb]`, `[p5e]`, `[exp]` and `backup/bluedb-v2-pre-rebase`** — so a check that only asks "does this line exist somewhere?" resolves them against the wrong tree and reports closed work as outstanding. **The sibling this row missed:** `[auth] driver`, the same defect shape in the same parser, survived that cleanup and was found by G0.4 here (`[main]` `docs/sky-toml.md:183`). |
| P4 | Goal #2's "real SERIALIZABLE" merely lacks cross-backend unity | **On `main` it does not exist at any backend.** `Std.Db` exposes exactly one transaction verb — `withTransaction : Db -> (Db -> Task Error a) -> Task Error a` (`[main]` `sky-stdlib/Std/Db.sky:216`) — implemented with a bare `d.conn.Begin()` (`[main]` `db_auth.go:1364-1407`) = driver default = **READ COMMITTED on Postgres**. No isolation type, no isolation argument, no `40001` retry anywhere (`grep -rn "40001" runtime-go/` → empty). `Conflict` is retryable nowhere: only `Timeout`/`Network`/`Unavailable` reach a `True` arm of `isRetryable` (`[main]` `Error.sky:192-209`), so a serialization conflict falls to `_ -> False`. |
| P5 | `kernel_api.rs` + the `kernel_api_covers_registered_kernel_functions` CI gate exist | **Both deleted** (commit `054f6d26`); the gate existed in no workflow, and AGENTS.md line 258 still documented it as current and "fails CI on drift" — a durable instruction file asserting a phantom enforcement mechanism. **CLOSED in P0 on this branch.** `AGENTS.md` now states what actually holds: kernel-only module docs are *derived*, not curated — the list is `hir::KERNEL_MODULES` (`[main]` `rust/crates/hir/src/kernel.rs:29`), rendered through `kernel_only_modules()` (`[main]` `rust/crates/project/src/doc.rs:53`) and covered by `kernel_only_module_is_queryable` (`[main]` `rust/crates/project/src/doc.rs:1053`). Adding a kernel module documents it by construction, which is a stronger property than the gate that was claimed. |
| P6 | A CI gate guards console drift | **NOT FOUND.** `grep -rni "drift\|console" .github/workflows/` → zero hits across all five workflow files. |
| P7 | The `goty.rs` record-fieldset collision lives in `codegen` and blocks the console edit form | **Path wrong, and it does not block.** The function is `select_record_candidate` at **`[main]` `rust/crates/lower/src/goty.rs:274-302`** (`crates/codegen/src/` contains only `lib.rs`). It selects by field *type* (landed `1a7142f6`, v0.19.1). The erased-`any` recurrence is real but is a *documented workaround* (use a tuple), not a blocker. |
| P8 | `-tags pebblegozstd` is a non-negotiable already in force | **The string appears nowhere in the repo** except the mandate doc. CI runs `CGO_ENABLED=0 go test ./rt/...` at `[main]` `rust-ci.yml:255` (macOS determinism job) — v2.0 cited `:219`, which is the **Postgres-only** integration subset (`go test -tags integration ./rt/ -run Postgres`). Either way CGO=0 forecloses the cgo-zstd link path *for tests*. The real exposure is different and still open: **`sky build`'s CGO_ENABLED=1 paths** would link cgo DataDog zstd into a *shipped app* while the CGO=0 path links pure-Go klauspost. There is exactly **one** `go build` invocation site — `run_go_build_once` (`[main]` `build.rs:708-735`) — reached from **three** call paths (`:578` FFI-detected CGO=1, `:590` CGO=0, `:600` CGO=1 retry). Adding the tag at the single site therefore covers all three *by construction*; v2.0's "on both paths" framing was wrong about the shape and is corrected in §7.2. |
| P9 | `SchemaOf` exists | **Did not exist** when the prior phases relied on it; it exists **only** on `salvage/p5e-foundation` (`runtime-go/bluedb/embedded.go`, added by that branch). It is a *deliverable of the salvage branch*, not a pre-existing facility. |
| P10 | Sessions are serialised as JSON | **gob.** `[main]` `docs/skylive/architecture.md:376` is wrong; `encodeSession` at `[main]` `runtime-go/rt/live_store.go:1278`. |
| P11 | `docs/skylive/tiered-session-cache.md` describes a proposal | It says `Status: PROPOSED` but the cache **shipped** (`a6b4c443`), and its `decodeSession` line citation no longer resolves. Stale doc. |

### 0.2 About `feat/bluedb`'s own claims (research read as research)

| # | Claim in the prior docs | Reality in the prior code |
|---|---|---|
| P12 | `P.index` declares a secondary index | **Half true, and v2.0 got the other half wrong.** There is no *ordered* secondary-index keyspace: `[bdb]` `keys.go:19-22` defines `tagData 0x00`, `tagChangelog 0x01`, `tagMeta 0x02`, and `index_key.go` is a *validation-coordinate* encoder whose output only lands in `IndexCoord.Key` or a read-set bound. **But a working stored UNIQUE mechanism does exist and is maintained in production on that branch** — `[bdb]` `backend.go:262` `uniqUserKey(coll, indexName, colType, valBytes)` builds a **pk-free** key (`coll ‖ 0x1E ‖ indexName ‖ 0x1F ‖ encodeIndexKey(…)`) whose *value* is the owning pk (`:259-261`), maintained at `embedded.go:288` (old-value delete), `:294-298` (`tx.Get(uKey)` → `ErrUniqueViolation` → `tx.Put(uKey, pk)`) and `:318` (delete on row delete). v2.0 omitted `backend.go` from P1's port list and shipped `unique` as a no-op — corrected in §3.2b and §10. |
| P13 | Scans are O(all rows in the collection) | **Worse.** The *precise* (declared-index) transactional path calls `tx.reader.Iterate(nil)` (`[bdb]` `txn.go:566`) — the whole data keyspace across every collection — and recomputes `tx.indexCoords(k,v)` per row. The *unindexed* fallback `ScanCollection` prefixes by collection and is strictly cheaper. **Declaring an index makes a transactional query slower.** |
| P14 | `Backend.Capabilities()` gates multi-replica reactivity at startup | `Capabilities()` returns `CrossInstanceReactive: true` (`[bdb]` `embedded.go:466-474`) — the opposite of the corrected matrix — **and has no production reader**. The real gate is a string classification of bindings behind a `sync.Once` on the *first session*. |
| P15 | The cross-instance reactive bridge exists and RG#1's fix ("empty tenant skips the broker publish") closes a leak | **The cross-instance path does not exist.** `grep "reactiveTenantTopic\|__bluedb:" runtime-go/` → nothing; `rt/bluedb_reactive.go` has no Broker reference. There is no publish to skip; RG#1's fix is vacuous, and the promised `Persist.withTenant` escape hatch was never built. |
| P16 | A dropped reactive delivery "self-corrects via the resync path, never a permanent silent loss" | **No production consumer reads the resync latch.** `NeedsResync()` / `ResyncPending()` are called only from tests; `markResyncAll` latches a flag nothing reads; `drainReactiveBurst` discards every `Change`. A drop while the rt loop is inside `reactiveRefreshOnce` leaves the session permanently stale. This is the same "gate that cannot fail" class the branch's own RESUME warns about — still open there. |
| P17 | Reactivity is query-scoped | Detection is query-scoped; **delivery is not**. The computed `Transition`/`Record`/`OrderChanged` are discarded and the consumer re-runs the full query (`Persist.toList` → full collection scan + full codec decode) **per session per notification**. |
| P18 | The index read-set range test is a biconditional (`⟺`) | Docs specify half-open `[lo, hi)`; shipped code uses **closed `[lo, hi]`** (`[bdb]` `index_key.go:107-108`, `validate.go` `inRangeClosed`). Direction is safe (over-reject) but the stated `⟺` is false at the upper boundary. |
| P19 | ADR-001: backing sessions with a collection is "not a blocker" because TEA Models are typed records | **Unshown.** `Persist.collection` requires a `Codec a` *value supplied from Sky*, while the funnel's persist point holds `sess.model` as untyped `any` on the Go side. There is no mechanism by which `rt` obtains a codec for the app's Model. §4 dissolves this rather than assuming it. |
| P20 | `[data]` collapses sessions + app data + analytics into one backend | `[data] path` seeds **only** `DB_PATH`; `sessionPath` and `analyticsPath` remain separate keys. And `backend = "embedded"` emits `DB_DRIVER=embedded`, which nothing reads, so `Db.connect ()` opens **SQLite**. There is no config-driven way to select the embedded engine at all. |

**What this list is for.** Nothing in §1–§11 depends on an unverified claim. Where this design
relies on a fact, the fact is cited. Where a mechanism must be built that the prior work
*described* but did not build, it is listed as NET-NEW in §10, not as a port.

---

## 1. Goal → mechanism → gate

The five goals are quoted verbatim from `.claude/AUTONOMOUS_GOAL.md`. For each: the mechanism
that delivers it and the numbered gates that prove it.

**The "Delivered?" column is bare Yes/No.** v2.0's version carried a bound in the same cell
("Yes for Model state", "Serializable: yes, all three. Bounds: …"), which pre-authorizes exactly
the qualified PASS the mandate forbids — a Judge reading "yes, with a stated degradation ladder"
has been handed the word "but" in advance. Every bound now lives in §11.2 and nowhere else. A
`No` here is not a failure of the design; it is the design refusing to launder a bound as a pass.

| # | Goal (verbatim) | Mechanism | Gates | Delivered? |
|---|---|---|---|---|
| 1 | *Session-bounded Model state sync.* | Sessions become a `_sky_sessions` Persist collection (§5); a two-part **count + bytes** ceiling over a resident cache; SSE-connected sessions become **deflatable** (spill Model/tree/bodies to the store, keep the connection) rather than immortal; provisional admission so a crawler GET does not mint a resident session; coalescing per-connection outbox; a stated global lock order (§4.0) | **G1.1** ceiling · **G1.2** correctness across spill/rehydrate · **G1.3** no acked-then-lost across spill · **G1.4** provisional admission · **G1.5** lock safety · **G1.6** durable-session write amplification · **G1.7** *sync* convergence | Yes |
| 2 | *Unified store: high-throughput lock-safe parallel + scalable + reliable + ACID (**real SERIALIZABLE**) + secure, with UNIFIED APIs shareable across dbs (sqlite/postgres/bluedb).* | One promise, no isolation knob (§2): embedded SSI, sqlite WAL + IMMEDIATE write-serialisation with a **split reader/writer pool**, postgres `SERIALIZABLE` + internal bounded retry; a closed driver registry that fails closed on an unknown driver; real index seeks with separated coordinate/physical bounds (§3); durable engine-attested tenancy in the key **and in the conflict domain** (§3.2a, §5) | **G2.1** isolation conformance, all three backends, discriminating · **G2.2** index-seek complexity · **G2.3** index↔data crash consistency · **G2.4** transact-body replayability · **G2.5** cross-tenant structural impossibility · **G2.6** substrate crash corpus · **G2.7** unique enforcement · **G2.8** tenant key rewrite · **G2.9** durability on ack · **G2.10** no cross-tenant conflicts · **G2.11** seek/read-set bound equality · **G2.12** per-Sky-type index encoding | Yes |
| 3 | *Easy + simple; low-level APIs only for the 0.001%.* | One `[data]` section wired end-to-end through a generated glue file (§7); one `Persist` API with no backend named in app code; zero-config default (embedded, `data/app.blue`); one migration story; `sky doc Std.Persist` generated from source | **G3.1** the zero-config app builds + runs + persists across restart · **G3.2** doc-examples gate over `docs/skypersist/` · **G3.3** graduation embedded→sqlite→postgres on identical source · **G3.4** migration lifecycle | Yes |
| 4 | *Notify clients of changesets (query/row-scoped, in the commit path).* | The changeset is derived from a **durable artefact written inside the committing transaction** (§6.2a), so emission is commit-atomic on every backend; a `ChangeBus` (local / postgres `LISTEN` + `_sky_changelog`) for multi-replica; the delta is **applied**, not used as a "go re-query" nudge | **G4.1** delivery on all backends · **G4.2** cross-tenant non-delivery · **G4.3** fan-out cost · **G4.4** startup fatal, never a first-session `os.Exit` · **G4.5** no permanently-stale session after a forced drop · **G4.6** query-scoped **non**-delivery · **G4.7** delta applied, not re-queried · **G4.8** changeset atomic with commit | Yes |
| 5 | *Built-in Sky Console admin access to records.* **Ruled by the user 2026-08-09: READ *AND* WRITE.** | The `[p5e]` authorization funnel (zero-trust-input `Decide()`, fail-closed ordering, allow-list disclosure) ported onto §5's durable tenancy — which converts the "forgeable tenant column" from a documented weakness into a structural impossibility — **plus** the write surface: mutations gated on the engine-attested tenant, a per-mutation audit trail, optimistic concurrency, and confirm/undo (§8.4) | **G5.1** funnel decision matrix · **G5.2** scoped read cannot cross tenants · **G5.3** read e2e · **G5.4** write authorization matrix · **G5.5** cross-tenant write rejected and creates no row · **G5.6** audit completeness · **G5.7** optimistic concurrency / no lost update · **G5.8** confirm + undo | Yes — **only when G5.4–G5.8 are green.** A read-only surface is `No`. |

**Goal #1's verbatim word "sync"** (§4.0a). v2.0 dropped it silently. It is defined here as a
two-part property and gated by **G1.7**: (a) every acked transition is reflected on **every** live
connection of that session, not only the originating one; (b) a rehydrated session is
**state-identical** to the pre-deflation session with the intervening transitions applied. Sync is
*within* a session identity, not across identities — cross-session convergence is goal #4's job.

**Goal #2's "high-throughput"** (§2.7). An adjective with no threshold cannot fail, so absolute
per-backend floors are hand-committed and flagged for user review in §11.4. They are *not* seeded
from the first run — a baseline seeded from whatever ships first records its own cost and stays
green forever, which is precisely the G-B10 defect.

**Goal #2's "lock-safe"** (§4.0). Also a verbatim clause, and v2.0 had zero gates for it.
**G1.5** is the falsifier.

Cross-cutting, serving all five:

| Gate | Proves |
|---|---|
| **G0.1** | `docs/bluedb/STATUS.md` is generated and matches a fresh run (hand edits detected) |
| **G0.2** | `rt` never imports `bluedb`; `bluedb` imports only pebble + stdlib |
| **G0.3** | A non-Persist app links no pebble, builds cold-cache offline, ships no `bluedb/`, and **keeps its non-`data` session store** |
| **G0.4** | No dead config key: every env suffix the compiler writes has a runtime reader, and vice versa — *and* every glue-affecting key changes observable behaviour, not just glue bytes |
| **G0.5** | `sky build` passes `-tags pebblegozstd` at the single `run_go_build_once` site, so all three call paths inherit it |
| **G0.6** | Every gate's recorded mutation still applies and still turns it red |
| **G0.7** | Harness self-integrity: every gate has ≥1 mutation, every goal has ≥1 gate, every gate maps to one goal, every `file:line` cited in this document still resolves to the quoted token on its tagged branch |
| **G0.C** | **The canary.** A deliberately vacuous gate + a no-op patch. `--verify-mutations` must report `VACUOUS`; reporting `PROVEN` is a harness failure (§9.4) |

---

## 2. A1 — one isolation contract across embedded / sqlite / postgres

### 2.1 What is wrong today

Three separate defects, on two different branches:

1. **On `main` there is no serializable path at all.** `Db.withTransaction` → `d.conn.Begin()`
   (`[main]` `db_auth.go:1364-1407`). Postgres gets READ COMMITTED. Write skew is available to every
   Sky app today.
2. **On `feat/bluedb`, sqlite's "serializable" is a pool clamp.** `dbSerializableTxAttempt`
   (`[bdb]` `db_auth.go:1529-1587`) emits `BEGIN IMMEDIATE` over a pinned `*sql.Conn`, but the actual
   serialisation comes from `conn.SetMaxOpenConns(1)` applied unconditionally at connect
   (`[bdb]` `db_auth.go:356`) for *every* SQLite pool. The branch's own test says so in its name:
   `TestWriteSkewSQLiteReadCommittedAlsoHolds_MaxConns1`
   (`[bdb]` `persist_writeskew_test.go:129-141`). The requested isolation level is decorative.
3. **The dispatch fails open.** `if serializable && d.driver != "pgx"` (`[bdb]` `db_auth.go:1535`) sends
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

  *WAL checkpointing must not starve.* `SetMaxOpenConns(1)` accidentally guaranteed that the
  single connection periodically found the database quiescent and could run a passive
  checkpoint. Splitting the pool removes that accident: long-lived readers hold WAL read marks,
  the passive checkpointer can never truncate past them, and the `-wal` file grows without
  bound. The policy is therefore explicit, not emergent:
  `PRAGMA wal_autocheckpoint = 1000` pages on the writer; `SetConnMaxLifetime(5 * time.Minute)`
  and `SetConnMaxIdleTime(1 * time.Minute)` on the **reader** pool so no read mark is immortal;
  a `TRUNCATE` checkpoint on the writer when `sky_persist_wal_bytes` crosses
  `[data] walCheckpointBytes` (default 64 MiB); and the reader-pool DSN sets
  `_pragma=busy_timeout(5000)`. **G2.9** gates WAL size under sustained write + long-read load —
  it is the falsifier for this whole paragraph.
- **Every read-write transaction takes SQLite's write lock at `BEGIN`** (IMMEDIATE semantics), so
  read-write transactions execute in a strict serial order, machine-wide (it is a file lock, so
  it holds across processes too).

  *How IMMEDIATE is actually obtained, and why v2.0 was wrong about it.* v2.0 assumed a literal
  `BEGIN IMMEDIATE` SQL string. On `[main]` that string **does not exist anywhere in
  `runtime-go/`** — the only occurrences are comments. Under `CGO_ENABLED=0` the driver is
  `modernc.org/sqlite`, and the supported knob is the **DSN parameter** `_txlock=immediate`,
  already in production use at `[main]` `runtime-go/rt/jobs/sqlite_store.go:78`. So: the writer
  is opened on its **own `*sql.DB` with its own DSN** carrying `_txlock=immediate`, and the
  reader pool is opened on a second `*sql.DB` whose DSN omits it. Two DSNs, one file. A pinned
  `*sql.Conn` plus literal SQL remains a legal implementation, but the DSN is the one that works
  under CGO=0 and is therefore what §2.5's mutation targets. Getting this wrong is not cosmetic:
  the entire §2.3 serializability argument rests on the knob actually being set, and a mutation
  aimed at a string that does not exist is `MUTATION-STALE` on day one — a gate that can never
  go red.
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
  Today's `NORMAL` (`[bdb]` `db_auth.go:356`) does not fsync per commit under WAL, so an acked
  transaction is durable only against process crash — the exact `A2` grill finding, deferred on
  `feat/bluedb`. It is not deferred here; it is a config key with a safe default.

  *Honest bound:* one global writer. Write throughput is one transaction at a time per
  database file, machine-wide. This is a **throughput** bound, not a correctness one, and it is
  published as a measured number by G2.1's throughput arm.

**Postgres.** `BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})` — per-transaction,
never a session default. Retry on `40001` (serialization_failure) and `40P01` (deadlock_detected),
classified from a typed `*pgconn.PgError`, not a substring match. (The prior implementation's
string fallback — `"could not serialize"`, `"database is locked"`, … at `[bdb]` `db_auth.go:1479-1487` —
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
//
// BeginWrite returns a WriteTx, NOT a database/sql *Tx. The distinction is
// load-bearing: a bare `db.BeginTx` on a shared pool cannot express SQLite's
// IMMEDIATE lock acquisition. A WriteTx is obtained from the strategy's OWN
// writer handle — for SQLite a second *sql.DB whose DSN carries
// `_txlock=immediate` (see §2.3); for Postgres a BeginTx with
// sql.LevelSerializable; for embedded a bluedb Txn via persistglue. The
// interface does not merely permit that, it REQUIRES it: constructing a
// strategy takes the writer handle, and OpenData never hands a strategy the
// shared read pool.
type IsolationStrategy interface {
    BeginWrite(ctx context.Context) (WriteTx, error) // serializable read-write
    BeginRead(ctx context.Context) (ReadTx, error)   // consistent snapshot, read-only

    // TWO classifiers, because conflicts arrive on two different channels.
    //
    // IsConflict sees the Go error from the driver: *pgconn.PgError 40001/40P01,
    // modernc SQLITE_BUSY/SQLITE_BUSY_SNAPSHOT, bluedb's ErrValidationFailed.
    //
    // IsConflictValue sees the DOMINANT path, which v2.0 missed entirely: a Sky
    // `transact` body returns a Sky Error VALUE, not a Go error. A user callback
    // that surfaces `Err (Error.conflict …)`, or a kernel that maps a driver
    // failure into a Sky Error before the retry loop sees it, never produces a
    // Go error at all. Today that case is classified by SUBSTRING on the Sky
    // error message — exactly what §2.3 says must never be the classifier. So
    // the retry loop consults BOTH, and neither is a substring match: the Sky
    // Error carries a typed `Error.Kind` discriminant.
    IsConflict(err error) bool
    IsConflictValue(e rt.SkyError) bool

    SelfTest(ctx context.Context) error // §2.7 — run at boot
}

var strategies = map[Driver]func(WriterHandle, ReaderHandle, DataConfig) (IsolationStrategy, error){}
```

`rt.Error`'s `Conflict` kind already exists (`[main]` `sky-stdlib/Sky/Core/Error.sky`), and
`isRetryable` returns `False` for it — correct at the *application* level (the app must never
retry; §2.2 clause 2 says the runtime does it) and exactly why the runtime needs its own typed
predicate rather than reusing the Sky-visible one.

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

**What each anomaly asserts — three corrections that decide whether G2.1 discriminates at all.**

v2.0 said "run the anomaly corpus" and left the assertion unstated. Three ways that fails:

*(a) The assertion is final-state legality, never "a `Conflict` was returned."* `Persist.transact`
auto-retries — `[bdb]` `txn.go:14` `const maxTxnAttempts = 8`, and §2.2 clause 2 makes invisible
retry a **promised** property. So a correct backend converges to a serial-legal state and returns
**no error**, while a snapshot-isolated backend converges to an illegal state and *also* returns
no error. An assertion on the error channel therefore cannot tell them apart. Each anomaly
declares the finite set of states reachable by *some* serial order of its transactions, and the
gate asserts membership:

```
assertFinalState(anomaly, oneOf: []State{ /* T1;T2 */, /* T2;T1 */ })
```

*(b) A validation counter is exposed, so "was the mechanism engaged?" is separately observable.*
The seam already exists and is already labelled as one: `[bdb]` `validate.go:8`
`var validateCalls atomic.Int64`, documented at `:5-7` as "a test seam", incremented at `:28`.
It is promoted to a strategy method `Stats() IsolationStats{Validations, Retries, Conflicts}`,
with the SQL backends reporting their own equivalents. Final-state legality with
`Validations == 0` on a discriminating anomaly is a **FAIL**, not a pass: it means the state was
legal by luck (or by serialisation the workload never contended), not by the mechanism.

*(c) Every anomaly asserts the plan shape FIRST.* This is the leg that silently voids the whole
suite. `ScanCollection` (`[bdb]` `txn.go:619`) calls `WitnessCollection` (`:200`,
`tx.collWitness[coll] = true`), a **collection-level** witness that conflicts with *any* write to
that collection. §3.4 rule 6 makes `FullScan` a legal plan. So if the planner falls back to a full
scan, **all twelve anomalies pass regardless of whether the seek, the span extraction, or the
read-set recording work at all** — the coarse witness over-rejects everything. Postgres has the
identical hazard via seq-scan `SIREAD` predicate locks. Therefore:

```
plan := Persist.explain(conn, q)
require(plan.Access == expectedAccess)   // asserted BEFORE the interleaving runs
```

and G2.1 carries **two paired arms** whose *expectations differ*:

| Arm | Plan | Expectation | What it proves |
|---|---|---|---|
| `forceFullScan` | `FullScan` forced via the build-tagged planner switch | **anomalies still pass** | that the anomaly outcomes are *not* what measures the seek — this arm is expected GREEN, and its going RED means the coarse witness is broken |
| `indexed` | `IndexSeek` asserted by `explain` | anomalies pass **and** `Stats().Validations > 0` **and** A-PH passes | that the *seek* path records a read-set fine enough to catch a phantom |

Only the second arm is discriminating. Stating both, with opposite roles, is what makes the
suite honest about which half of it is load-bearing.

**The rendezvous, and the G2.1/G2.4 collision.** A two-transaction interleaving needs a barrier
*inside* a transact body ("T1 has read; now let T2 write"). Under G2.4's runtime poison flag
(§7.3) any kernel call inside `transact` is checked, so the barrier must be an explicitly-exempt
kernel: **`Persist.Test.barrier : String -> Task Error ()`**, registered only under the
`sky_test_barrier` build tag, absent from release builds, and named in the poison flag's exempt
set as the *only* member. Because retry re-executes the body, the barrier is idempotent per
attempt and keyed by attempt number. G2.1 additionally asserts the rendezvous actually occurred
(`barrier.bothArrived == true`) — without that, a barrier that degenerates to a no-op turns every
anomaly into a sequential execution that trivially passes.

**Gate mechanics.**

```
cargo run -p xtask -- bluedb-gates --only=G2.1
  → embedded : in-process, temp dir
  → sqlite   : temp file, WAL, writer DSN _txlock=immediate
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
| `embedded`: stub `validate()` to always accept (build tag `bluedb_mutation`) | A-G2i, A-G2, A-PMPW, A-PH go RED **on the `indexed` arm** |
| `sqlite`: **drop `_txlock=immediate` from the writer DSN** (v2.0 targeted a literal `BEGIN IMMEDIATE` string that does not exist — see §2.3) | A-G2i, A-G2, A-PMPW go RED |
| `sqlite`: collapse the split pool back to a single shared `*sql.DB` | throughput arm shows read concurrency = 1 → RED |
| `postgres`: `LevelSerializable` → `LevelRepeatableRead` | A-G2i, A-G2, A-PMPW go RED |
| registry: reinstate a `default:` arm for unknown drivers | the unknown-driver arm goes RED (it must fatal) |
| **plan**: force `FullScan` on the `indexed` arm | the `indexed` arm's `explain` precondition goes RED **before** any anomaly runs |
| **counter**: hard-wire `Stats().Validations` to a positive constant | the "mechanism engaged" assertion can no longer fail ⇒ the *counter* mutation detects it via a control anomaly that must report `Validations == 0` |
| **barrier**: make `Persist.Test.barrier` a no-op | `bothArrived == false` → RED |
| **conflict-value**: delete `IsConflictValue` from the retry loop | the Sky-`Error`-value conflict arm exhausts retries and surfaces `Conflict` to the caller → RED |

Because each mutation turns a *different* subset red, the gate cannot be satisfied by a single
accidental invariant.

### 2.6 "High-throughput" — the number, not the adjective

Goal #2's verbatim clause is *"high-throughput lock-safe parallel"*. An adjective has no
falsifier, so v2.0's promise to publish "numbers, not adjectives" was itself unfalsifiable. The
floors below are **hand-committed absolute minima** in `docs/bluedb/baselines.json`, asserted by
G2.1's throughput arm on the fixed CI runner, and flagged for user review in §11.4. They are
deliberately *not* seeded from the first run: a self-seeded baseline records whatever ships and
stays green forever (the G-B10 defect).

| Backend | Serializable write commits/s | Concurrent read scaling | Notes |
|---|---|---|---|
| embedded | ≥ 2 000 sustained, `durability = "full"` | reads scale to `GOMAXPROCS` (MVCC snapshots, no reader lock) | group commit is what makes fsync-per-commit affordable; **G1.6** prices it |
| sqlite | ≥ 500 sustained, `synchronous = FULL` | reads scale to `min(4, GOMAXPROCS)` — the split pool is the point | one writer per file, machine-wide (F1) |
| postgres | ≥ 2 000 at 8 concurrent writers | reads scale with the pool | true multi-writer; retries counted separately |

"Lock-safe parallel" is gated by **G1.5** (`-race` over concurrent transitions, deflations,
evictions and rehydrations, with a timeout that dumps goroutines) and **G2.10** (two tenants on
identical contended workloads show **zero** cross-tenant conflicts).

**G2.9 (new) — durability on ack, and the WAL policy.** §2.2's contract **clause 3** —
*"A returned commit survives process crash on every backend, and host power loss when
`[data] durability = "full"` (the default)"* — had **no gate at all** in v2.0. It is the strongest
promise the contract makes and it was the only clause nothing tested.

| Arm | Assertion |
|---|---|
| a — fsync actually happens | with `durability = "full"`, an `errorfs`/`vfs` shim counts `Sync`/`fdatasync` calls; every acked commit is preceded by one (allowing for group commit — the assertion is that the ack **follows** a sync covering that commit's bytes, not one sync per commit) |
| b — process crash | `SIGKILL` mid-workload; every acked commit is present after recovery, no un-acked commit is |
| c — **power loss** | the same workload under a write-reordering, un-flushed-buffer-dropping fault harness (`errorfs` with the sync barrier honoured but everything after it discarded); every acked commit survives, and the store opens without repair |
| d — `durability = "normal"` is honestly weaker | the same power-loss arm **must lose** un-synced commits, and the docs must say so. A knob whose two settings behave identically is a lie in the config file |
| e — WAL does not grow without bound | sqlite, 10 minutes of sustained writes with a long-lived reader held open on the reader pool: `-wal` stays under `walCheckpointBytes` × 2. This is the falsifier for §2.3's checkpoint policy — the hazard the old `SetMaxOpenConns(1)` accidentally prevented |

*Mutations:* set `synchronous = NORMAL` on the sqlite writer while `durability = "full"` → (a) and
(c) RED; replace `Apply(pebble.Sync)` with an async apply → (a) and (c) RED; make
`durability = "normal"` fsync anyway → (d) RED; remove `wal_autocheckpoint` **and** the reader
`SetConnMaxLifetime` → (e) RED.

### 2.7 Making the bound visible before 3am

Compile-time backend typing is impossible (§2.2). Three mechanisms substitute, in order of
earliness — none of them a compiler change, which is what makes P3 shippable (§10):

1. **Startup, for raw SQL.** *(Changed in v2.1 — deliverability item D4.)* v2.0 proposed a new
   `Std.Persist.Sql` module plus a build-time static-reference check that would make referencing
   it under `driver = "embedded"` a compile error. Both are **cut from v2**: the module is a
   second data API to design, document, gate and migrate, and the static check is a new
   whole-program reachability analysis in the compiler — together a phase's worth of work
   defending a case the startup check already covers. Instead: raw SQL stays where it already is
   (`Std.Db`, on `[main]`), and an app that opens `Std.Db` while `[data] driver = "embedded"` is a
   **startup fatal** naming the module and the two fixes (`driver = "postgres"`, or move the
   query to `Persist`). Later than a compile error, still before the listener opens, and it costs
   nothing to build. The bound this concedes is disclosed as **B9** in §11.2: raw `Std.Db` writes
   bypass Persist and therefore emit no changeset.
2. **Startup, for isolation.** `IsolationStrategy.SelfTest` runs one A-G2i write-skew probe against the real
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
no index tag in the keyspace (`[bdb]` `keys.go:19-22`). Its output feeds `IndexCoord.Key` (changelog
witnesses) and `Txn` read-set bounds. The *storage* half was left as
`TODO(phase3b/4)` (`[bdb]` `embedded.go:349-352`) and never landed. Consequences measured in §0.2/P13:
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
**It is not, however, sufficient on its own — see §3.3a: the same encoder's output also lands in
the durable changelog, which the index-keyspace version does not cover.**

#### 3.2a Tenancy must be in the CONFLICT domain, not only in the key (E-B2)

Putting `ten` in the physical key (above) makes reads structurally isolated. It does **not** make
*writes* independent, and v2.0 stopped one layer too early.

The conflict domain is built from identifiers that are **shared across tenants**:

- `[bdb]` `keychange.go:16-20` — `CollID` is "a stable per-collection id"; `IndexID` is "a stable
  per-(collection,index) id". Neither is tenant-scoped.
- `[bdb]` `keychange.go` and `readset.go` contain **zero** tenant references.
  `[bdb]` `readset.go:20-24` is `indexRange{index IndexID; lo, hi []byte}`.
- `[bdb]` `txn.go:54` — `collWitness map[CollID]bool`, set by `WitnessCollection` (`:200`).
- The tenant exists only as `Txn.tenant` / `CommitReq.Tenant`, documented at `[bdb]` `txn.go:78-81` and
  `[bdb]` `engine.go:123-130` as a **transient reactive routing tag** that is *never durably written*.

Consequence: tenant T1's `indexRange` matches tenant T2's `IndexCoord` whenever the *values*
overlap — `status = "active"` encodes to identical coordinate bytes for every tenant. Combined
with the collection-level `WitnessCollection` (any write by any tenant conflicts) and §3.4 rule 6
making `FullScan` a legal plan, a 1 000-tenant deployment degrades to **global write
serialization with spurious `Conflict`s**. That is not a performance nit; it is a
correctness-adjacent failure of goal #2's verbatim *"high-throughput lock-safe parallel"*.

**Fix — the conflict domain is tenant-scoped at three places, from one source:**

```go
// The coordinate space gains the tenant as a leading component, so two tenants
// can never share a coordinate even when their column values are identical.
type IndexCoord struct {
    Index IndexID
    Key   []byte // = encTenant(ten) ‖ <column encodings>   (§3.3 escaping)
}

// The collection witness is tenant-scoped.
type tenantColl struct { Ten string; Coll CollID }
//   Txn.collWitness map[tenantColl]bool
```

1. `IndexCoord.Key` is tenant-prefixed by the same escaped encoder as `ten` in the physical key.
2. `indexRange.lo` / `.hi` are built from the *same* prefix, so validation compares like with
   like (this is also what §3.4a's `coordBounds` guarantees).
3. `collWitness` keys on `tenantColl`, so `ScanCollection` under T1 does not witness T2's writes.

An alternative — making `CollID`/`IndexID` themselves tenant-scoped — was rejected: id assignment
is a migration-ledger fact, and per-tenant ids would make the ledger grow with tenant count and
break `sky data reindex`'s ability to rebuild an index without enumerating tenants.

**G2.10 (new) — no cross-tenant conflicts.** Two tenants run identical, internally-contended
workloads (each producing a healthy internal conflict rate). Assert the **cross-tenant** conflict
count is exactly **zero**, and that each tenant's throughput is within 10 % of that tenant running
alone. *Mutation:* drop the tenant prefix from `IndexCoord.Key` → cross-tenant conflicts appear →
RED. *Mutation:* revert `collWitness` to `map[CollID]bool` → RED.

#### 3.2b `unique` — a mechanism, not a keyword (G-B4)

v2.0 listed `unique` in §7.3's API with **no mechanism, no gate, and a key layout that defeats
it**: appending `pk` (§3.2) makes two rows with the same indexed value into two *distinct* index
keys that collide with nothing.

Worse, the premise underneath was half false. `feat/bluedb` **has** a working stored unique
mechanism, and v2.0 both omitted its file from P1's port list and replaced it with a no-op:

- `[bdb]` `backend.go:262` — `func uniqUserKey(coll, indexName string, colType ColType, valBytes []byte) []byte`
  builds `coll ‖ 0x1E ‖ indexName ‖ 0x1F ‖ encodeIndexKey(0, colType, valBytes)`. **No pk** — so
  duplicates *do* collide — and the owning pk is stored as the entry's **value** (`:259-261`).
- Maintained in production at `[bdb]` `embedded.go:288` (delete the old value's entry on update),
  `:294-298` (`if owner, ok := tx.Get(uKey); ok && string(owner) != pk` → `ErrUniqueViolation`;
  else `tx.Put(uKey, []byte(pk))`), and `:318` (delete on row delete).
- `backend.go` was **not** in v2.0's P1 port list. It is now (§10).

v2 keeps that shape and fixes its two gaps — no tenant, and the wrong namespace:

```
UNIQUE   userKey = 0x03 ‖ ten ‖ idxID(BE4) ‖ cols            -- NO pk
         value   = pk                                        -- the current owner
```

Three properties follow:

1. **Duplicates collide** because the key is pk-free — the whole point, and the reason unique
   entries cannot live in the `0x02` namespace where `pk` is mandatory for multiplicity.
2. **Uniqueness is per tenant**, because `ten` is in the key. Cross-tenant uniqueness would be a
   tenancy leak (tenant A learns that tenant B holds an email address by failing to insert it).
3. **Concurrent duplicate inserts conflict**, because the `tx.Get(uKey)` probe in `buildReq` is
   an ordinary **point read recorded in the read-set**. Two transactions each probing the same
   absent unique key and each writing it form a write-write conflict that SSI rejects — this is
   the property that a naive "check then write" outside the read-set would *not* have, and it is
   the reason the probe must stay inside the transaction rather than becoming a pre-flight check.

**G2.7 (new) — unique enforcement.** (a) Sequential: a second insert of the same value returns
`ErrUniqueViolation` and creates no row. (b) **Concurrent**: N goroutines insert the same unique
value simultaneously; exactly **one** commits, the rest see `Conflict`-or-`UniqueViolation`, and
a final scan finds exactly one row. (c) Update moves the entry and frees the old value for reuse.
(d) Cross-tenant: T1 and T2 may both hold `"a@b.c"`. *Mutations:* append `pk` to the unique key →
(a) and (b) RED; remove the `tx.Get` probe from the read-set (probe outside the transaction) →
(b) RED while (a) stays green — which is exactly why (b) exists; drop `ten` from the unique key →
(d) RED.

#### 3.2c Key-prefix length vs pebble's `AbbreviatedKey`

`[bdb]` `comparer.go:110-112` — `skydbAbbrev(key)` returns
`pebble.DefaultComparer.AbbreviatedKey(key[:skydbSplit(key)])`, i.e. the first 8 bytes of the
*user key* as a `uint64` fast-path digest. With the layout `0x01 ‖ ten ‖ collID ‖ pk`, a
single-tenant app's `ten` is the 3-byte constant `0x01 0x00 0x01`, so bytes 0-7 are
`tag ‖ ten ‖ collID` — **constant for an entire collection**. Every abbreviated comparison ties
and falls through to the full comparator, losing the fast path exactly where scans are hottest.

Fix: for `tenancy = "single"` the tenant component is **elided entirely** (not encoded as a
constant), giving `0x01 ‖ collID(BE4) ‖ pk` — 5 constant bytes, 3 discriminating. For
`tenancy = "multi"` the order is `0x01 ‖ collID(BE4) ‖ ten ‖ pk`: `collID` first keeps the
per-collection prefix short, and `ten` occupies the remaining fast-path bytes, which discriminates
well because tenant ids differ early. **This changes §3.2's stated layout** — the version above
is superseded by:

```
ROW      single: 0x01 ‖ collID(BE4) ‖ pk            multi: 0x01 ‖ collID(BE4) ‖ ten ‖ pk
INDEX    single: 0x02 ‖ idxID(BE4)  ‖ cols ‖ pk     multi: 0x02 ‖ idxID(BE4)  ‖ ten ‖ cols ‖ pk
UNIQUE   single: 0x03 ‖ idxID(BE4)  ‖ cols          multi: 0x03 ‖ idxID(BE4)  ‖ ten ‖ cols
```

Tenant scoping is unaffected: a scoped iterator's bounds are still
`[0x01 ‖ collID ‖ enc(T), 0x01 ‖ collID ‖ bytesSuccessor(enc(T)))`, still constructed only from
`Txn.tenant`, and still disjoint across tenants (§5.2 property 2). What changes is that the
prefix is per-collection rather than per-tenant, so a `System`-scope cross-tenant scan of one
collection is now a **single** contiguous range instead of one range per tenant — a secondary
benefit for §8's console.

G2.2 measures the abbreviated-key hit rate on both layouts and records it in `baselines.json`;
a layout change that regresses it >20 % fails.

### 3.3 Order-preserving column encodings

*In this section:* **§3.3b** — where a column's type actually comes from (v2.0's answer was wrong,
and the failure is silent) · **§3.3c** — the encoding table itself · **§3.3a** — why the encoding
is *durable* and therefore needs its own version, independently of the index keyspace. The letters
match the findings they close (G-B5, E-B6) rather than reading order.

#### 3.3b Where the column's TYPE comes from — `Codec.Shape` cannot supply it (G-B5)

v2.0 said the encoding dispatch and B1's build error are "checkable because the collection
declaration is static and `Std.Codec.Shape` gives each column's type at compile time." **Both
halves of that are false**, and the failure mode is silent:

- `[main]` `sky-stdlib/Std/Codec.sky:67-73` —
  `type ColType = CText | CInt | CReal | CBool | CBlob | CNull ColType`. That is a five-way
  **storage** class, not a Sky type. `Decimal`, `Money` and `String` are **all** `CText`;
  `Time` and `Int` are **both** `CInt`; `Float` and `Decimal` are **both** `CReal`.
- `Shape` (`:77-80`) is a **runtime value**: `Codec.auto` resolves it via
  `[main]` `runtime-go/rt/codec_auto.go:515` `Codec_autoCols`, which walks
  `reflect.TypeOf(witness)`. Nothing about it is available to `sky check`.

So under v2.0 a `Time` column would take `Int`'s encoding (both `CInt` — harmless by luck, since
§3.3 gives them the same body) but a `Money` column would take `Float`'s or `String`'s depending
on which `ColType` the codec picked, **silently producing a wrongly-ordered index** — the worst
class of bug this design can ship, because every read still returns rows and only the *order* and
the *range bounds* are wrong.

**The real source: the HIR type of the record literal passed to `Codec.auto`.**

```elm
todos = P.collection "todos" (Codec.auto { id = "", title = "", done = False }) |> P.key "id"
```

`Codec.auto`'s argument is a record literal whose type the type-checker fully solves. The
compiler therefore knows, at `sky check` time, that `id : String`, `title : String`,
`done : Bool` — the actual Sky types, `Money` distinct from `Float`, `Time` distinct from `Int`.
Three steps make it usable:

1. **Correlate.** A `Persist.collection` application is recognised in HIR by callee `DefId`
   (not by name — `[main]` `rust_ty_alias_resolution_164` is the standing warning against
   name-based resolution). Its second argument must be a `Codec.auto` application whose argument
   is a record literal or a variable whose solved type is a record; anything else is a
   `sky check` **error** ("`Persist.collection` needs a codec the compiler can see the shape of").
   This keeps the analysis local — one application node, one solved type — rather than a
   whole-program dataflow.
2. **Thread.** Per-column Sky types are emitted into the generated glue (§7.2) as
   `CollDecl.Cols[i] = { Name, SkyType }`, where `SkyType` is a closed enum
   (`String | Int | Float | Bool | Time | Decimal | Money | Bytes | Json`). This is the same
   mechanism that already carries the collection list, so it costs a field, not a pass.
3. **Dispatch.** §3.3's table below is keyed on `SkyType`, never on `ColType`. `Codec.Shape`
   keeps its existing job — deriving *columns* for the SQL backends — and loses the job it could
   never do.

B1's build error is re-derived the same way: `P.index "price"` where `price : Money` is a
`sky check` error, decidable because `SkyType` is known, whereas under `ColType` it was
indistinguishable from `price : String`.

**G2.12 (new) — per-Sky-type index encoding.** A fixture collection with a `Time` column and an
`Int` column holding numerically identical values, and a `Money` column and a `String` column
likewise. Assert: (a) the two encode to **different** bytes wherever the design says they must;
(b) index order matches Sky's `compare` for each type over a randomized corpus including
negatives, zero and boundary values; (c) `P.index` on the `Money` column is a `sky check` error
naming the column and its type. *Mutations:* dispatch on `ColType` instead of `SkyType` → (a) and
(c) RED; drop the sign-bit flip from the `Int` body → (b) RED on negatives.

#### 3.3c Encoding table

Each component is `nullTag ‖ body`, `nullTag ∈ {0x00 null, 0x01 present}`, so NULLs sort first
and a NULL can never collide with a value. **Dispatch is on the `SkyType` from §3.3b**, not on
`Codec.ColType`.

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
`checkCompositeLayout`, which *panics at encode time* — `[bdb]` `index_key.go:144-164` — a latent trap
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
error**. This is decidable because §3.3b threads the *Sky* type — `Money` is distinguishable from
`String` and `Float`, which it is **not** under `Codec.ColType`. `sky check` rejects it naming
the column, the type, and the two supported alternatives (index a derived integer minor-unit
column; or accept it as a residual predicate). Loud beats silent.

> ⚠️ **This is a product decision, not a technical footnote — flagged for the user (§11.4).**
> `AGENTS.md` pins `Std.Money` on `Std.Decimal` as the currency default and forbids raw `Float`
> for currency. Shipping BlueDB as the default database with `Money` **un-indexable** means the
> canonical "orders sorted by total", "products priced under X" query is a full scan in the
> default configuration. v2.0 parked this as a quiet B-row (§11.2 B1). It is escalated.
> The workaround (index a derived `Int` minor-unit column) is real and cheap, but it is a
> workaround the *user* should choose to accept.

#### 3.3a The changelog payload is DURABLE, so the coordinate encoding needs its own version (E-B6)

§3.2 versions the index keyspace (`meta: "index_encoding"`) and calls a change "an index rebuild,
not a store rewrite". That is true of the `0x02`/`0x03` namespaces and **false of the changelog**,
because the same encoder's output escapes into durable bytes by a second route:

`IndexCoord` (`[bdb]` `keychange.go:34-37`) is a field of `KeyChange` → serialised by
`EncodeChangelogPayload` (`:49-63`, framed by `const payloadFmtV1 byte = 0x01` at `:42`) →
written durably at `[bdb]` `committer.go:343-346`
(`b.Set(encodeChangelogKey(commitTs), j.req.ChangelogPayload, nil)`) inside the batch that
`Apply(b, pebble.Sync)`s at `:306` (SSI path) / `:135` (blind path) → **read back** by
`changelogTailChanges` on the ring-spill validation path.

§3.3 changes `String` escaping and adds `Float`/`Time`/null tags. So during an upgrade window the
validator compares **old-encoding coordinates** (durable, written by the previous binary) against
**new-encoding bounds** (computed by the running binary). Mismatched bytes do not *hit*, so the
range test under-rejects — a **serializability break**, silent, and confined to the upgrade
window, which is exactly when nobody is looking.

**Fix — version the payload and fail closed:**

1. `payloadFmtV2` frames a payload whose header carries `coordEncodingVersion uint16`.
   `[bdb]` `keychange.go:42` is the existing hook; the constant becomes a small enum.
2. `changelogTailChanges` **fails closed** on any entry whose `coordEncodingVersion` differs from
   the running binary's: validation of a transaction whose window includes such an entry returns
   `ErrValidationFailed` (abort + retry), never "no hit". Over-rejection is safe; under-rejection
   is not. The abort is counted (`sky_persist_changelog_version_aborts_total`) and logged once.
3. A coordinate-encoding bump therefore requires a **drain-to-`T` barrier** in
   `sky data reindex`: advance the watermark past every pre-bump changelog entry (bounded by
   `[data] changelogRetention`), GC them, *then* flip `meta: "coord_encoding"`. Until the barrier
   completes, the store runs on the old encoding — the binary reads the meta key at open and
   selects the encoder, so both versions are implementable simultaneously and the flip is atomic.
4. **`skydb.mvcc.v1` is still untouched.** This is a payload format and a meta key, not a
   comparer change. The irreversible gate is unaffected — the point of §3.3a is that "not the
   comparer" was being used to mean "not durable", and those are different claims.

*Mutation (folded into G2.3):* write `payloadFmtV1` bytes and read them with the V2 decoder
without the version check → the crash/upgrade arm's re-derivation finds coordinate mismatches →
RED. *Mutation:* make the version mismatch return "no hit" instead of `ErrValidationFailed` →
A-PH under a simulated upgrade window → RED.

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
4. Bounds are produced by **one encoder pass yielding two separately-named artefacts** — see
   §3.4a. v2.0's rule 4 was wrong twice over and is superseded there.
5. Conjuncts not implied by the span become `Residual`. A conjunct on an unindexed column is
   always residual.
6. If `j == 0` and there is no range: `FullScan{Coll}` over `Iterate(rowPrefix(tenant, collID))`
   — **collection- and tenant-prefixed**, which alone fixes the `Iterate(nil)` whole-database
   scan of P13.

#### 3.4a `coordBounds` and `seekBounds` — two byte spaces, never one (E-B1)

**This is the finding that would have admitted phantoms.** v2.0's §3.4/§3.5 used one word,
"bounds", for two things that live in different byte spaces:

| | Recorded bound (SSI) | Physical bound (the seek) |
|---|---|---|
| Space | **coordinate**: `IndexCoord.Key` = tenant ‖ encoded column bytes, nothing else (`[bdb]` `keychange.go:34-37`) | **physical**: the full user key `0x02 ‖ idxID ‖ [ten ‖] cols ‖ pk` (§3.2c) |
| Produced at | `Txn.Scan` → `tx.ranges = append(tx.ranges, indexRange{index, lo, hi})` (`[bdb]` `txn.go:171-178`), values converted by `encodeScanRange` (`:183-186`) | the pebble iterator's `SetBounds` |
| Consumed at | `[bdb]` `validate.go:65` — `if r.index == c.Index && inRangeClosed(r.lo, r.hi, c.Key)` | `SeekGE` / `Next` |

Record a *physical* bound against a bare coordinate and the range test never matches: the
recorded `lo`/`hi` carry a `0x02 ‖ idxID` prefix the coordinate does not have, so
`inRangeClosed` is false for every witness. That is an **under-reject** — the direction that
loses serializability. Concurrent inserts into the seeked range would commit unwitnessed:
**phantoms admitted**, on the exact path §2.5's A-PH exists to catch, and A-PH would have caught
it only if A-PH itself ran on an `IndexSeek` plan (E-B3(c) — hence the `explain` precondition).

**Second defect, independent of the first.** Rule 4's formulas are wrong once `pk` is appended:

- `gt v` with `Lo = enc(v) ‖ 0x00`: every row equal to `v` has the physical key
  `… ‖ enc(v) ‖ pk`, and any `pk` whose first byte is ≥ `0x01` sorts **after** `enc(v) ‖ 0x00`.
  So `gt v` **returns rows equal to `v`**. Not an over-fetch the residual quietly repairs — it is
  wrong for `count`, wrong for `limit`, and wrong for the read-set.
- Inclusive `lte v` had **no formula at all**. The obvious `Hi = enc(v)` **misses every**
  `enc(v) ‖ pk`, since all of them sort after `enc(v)`. A silent under-fetch: rows exist,
  the query does not return them.

**Fix — one encoder pass, two named artefacts, all physical bounds half-open.**

```go
// Produced together by encodeSpan(); neither is derivable from the other after the fact.
type Bounds struct {
    // Recorded in the read-set. Coordinate space: tenant ‖ column encodings.
    // NO physical prefix, NO pk. Compared against IndexCoord.Key by validate.go.
    coordLo, coordHi []byte
    coordLoIncl      bool
    coordHiIncl      bool

    // Handed to the iterator. Physical space, ALWAYS half-open [seekLo, seekHi).
    seekLo, seekHi []byte
}

func encodeSpan(idx *IndexDecl, ten string, conj []Conjunct) Bounds {
    physPrefix := indexPrefix(idx.ID, ten)          // 0x02 ‖ idxID ‖ [ten]
    coordLo, coordHi, loIncl, hiIncl := encodeCoordBounds(idx, ten, conj)  // §3.3 encoder

    seekLo := concat(physPrefix, coordLo)
    if !loIncl {
        seekLo = bytesSuccessor(seekLo)             // exclude every  <prefix‖coordLo>‖pk
    }
    seekHi := concat(physPrefix, coordHi)
    if hiIncl {
        seekHi = bytesSuccessor(seekHi)             // INCLUDE every  <prefix‖coordHi>‖pk
    }
    return Bounds{coordLo, coordHi, loIncl, hiIncl, seekLo, seekHi}
}
```

`bytesSuccessor` already exists and is the right primitive — `[bdb]` `reader.go:91-100`
(v2.0 and the griller both cited `:100-110`, which is the `pebbleCursor` struct). It returns the
shortest byte string strictly greater than its argument and greater than every extension of it,
which is exactly "skip all `pk` suffixes of this coordinate".

Both defects fall out at once:

| Predicate | `coordLo`/`coordHi` (recorded) | `seekLo` … `seekHi` (physical, half-open) |
|---|---|---|
| `eq v` | `enc(v)` … `enc(v)`, both inclusive | `P‖enc(v)` … `succ(P‖enc(v))` |
| `gt v` | `enc(v)` excl … `+∞` | `succ(P‖enc(v))` … `succ(P)` |
| `gte v` | `enc(v)` incl … `+∞` | `P‖enc(v)` … `succ(P)` |
| `lt v` | `−∞` … `enc(v)` excl | `P` … `P‖enc(v)` |
| `lte v` | `−∞` … `enc(v)` incl | `P` … `succ(P‖enc(v))` |

`gt` no longer returns rows equal to `v`; `lte` no longer misses them. The recorded interval keeps
the **closed** `[lo, hi]` semantics the shipped validator implements (`inRangeClosed`,
`[bdb]` `validate.go:65`) — P18's correction stands, and the physical/half-open change does not touch it,
because they are now explicitly different artefacts.

**G2.11 (new) — the two spaces agree.** For a corpus of queries covering every predicate above,
every column type, both tenancy modes, and both index arities: run the query on the **seek** path
and on the **pre-seek `ScanRange`** path, and assert (a) the recorded `indexRange.lo`/`.hi` are
**byte-equal** between the two paths, (b) the returned pk sets are equal, (c) the recorded
coordinate interval **contains** the `IndexCoord` of every row a concurrent writer could insert
into the predicate's extent (a property test over random insert values). *Mutations:* record
`seekLo`/`seekHi` instead of `coordLo`/`coordHi` → (a) RED and (c) RED; restore v2.0's
`Lo = enc(v) ‖ 0x00` for `gt` → (b) RED (rows equal to `v` appear); use `Hi = enc(v)` for `lte` →
(b) RED (rows equal to `v` vanish); drop `bytesSuccessor` from `seekHi` → (b) RED.

Arm (c) is the one that matters most: it is the only assertion in the design that directly tests
"the recorded predicate covers the phantom", which is the property A-PH samples and G2.11 proves.

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
reads the pre-image via `ensurePreimage` (`[bdb]` `txn.go:243`). v2 changes what is done with them:

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

Unique-index maintenance rides the same path (§3.2b): `buildReq` probes `tx.Get(uniqKey)` — a
read recorded in the read-set — and either fails the transaction with `ErrUniqueViolation` or
emits the `0x03` entry into the same `CommitReq.Writes`.

**Read-set for a seek.** `ScanRange` continues to record `indexRange{index, lo, hi}` — the
existing `[bdb]` `readset.go:20-24` type, the existing `validate.go:65` `inRangeClosed` check —
and it records **`coordBounds`, never `seekBounds`** (§3.4a). That distinction is the whole of the
SSI-preservation claim: the *recorded* predicate is byte-identical to what the pre-seek design
recorded, so §3 **preserves** §2's proof rather than re-deriving it; only the way rows are
*fetched* changes. **G2.11** proves the byte-identity mechanically; **A-PH** (§2.5) samples the
consequence. v2.0 asserted the preservation without either.

One correction carried from P18: the *recorded* interval is **closed** `[lo, hi]` (safe
over-reject) while the *physical* seek range is **half-open** `[seekLo, seekHi)`. These are not in
tension — they are the two artefacts of §3.4a. The false `⟺` is deleted from the doc; neither the
code nor the closed recorded semantics change.

#### 3.5a Index tombstones must be reclaimable, or the complexity claim decays (E-B5)

`[bdb]` `gc.go:97-100` keeps the newest version below the GC watermark `T` unconditionally —
`if !keptBelowT { keptBelowT = true; continue }` — and the loop **never reads the value marker**
(`markerTombstone` / `markerPut`); only `decodeDataVersion(k)` timestamps are inspected. That is
correct for **data** rows: a reader at exactly `T` needs the newest version, tombstone or not,
to distinguish "deleted" from "never existed".

It is wrong for **index** entries, and §3.2 makes index entries ordinary MVCC rows precisely so
they inherit that GC. Every value a row's indexed column ever held leaves a permanent key: an
`OpDelete` index entry whose newest version is a tombstone below `T` is kept forever. `Next()`
skips a tombstone logically but the cursor still **visits** it physically, so seek cost becomes

```
O(k + distinct (value, pk) pairs ever written under this index prefix)
```

— unbounded in update history, not in live rows. And G2.2 as specified **cannot see it**: its
fixture is freshly built with no update history, so the gate passes on day one and forever while
production degrades. This is the "gate that cannot fail" class the mandate exists to prevent.

**Fix — reclaim sole-remaining index tombstones.** For a key in the `0x02`/`0x03` namespaces
(the GC already decodes the tag, so this is a branch, not a scan), if the newest version below
`T` is a tombstone **and** there is no version at or above `T`, drop it entirely. The
data-row asymmetry is deliberate and stated: an index entry has no "deleted vs never existed"
distinction to preserve — its absence *is* its meaning — so the reader-at-`T` argument that
protects data tombstones does not apply. Unique (`0x03`) entries reclaim under the same rule,
which is what lets a freed unique value be reused without accumulating a key per release.

**G2.2 gains an update-history arm.** Build the fixture, then run **M** update cycles that move
each row's indexed value (`M ∈ {0, 10·N}`), force a compaction (the precondition — without it
the reclamation has not run and the measurement is of the memtable, not the LSM), then measure:

| Assertion | Rationale |
|---|---|
| `KeysVisited(M = 10·N) ≤ 1.1 × KeysVisited(M = 0)` | seek cost is invariant in update history — the actual claim |
| live index-entry count is exactly `N` after compaction | reclamation happened, not just skipping |

*Mutation:* disable tombstone reclamation → `KeysVisited` grows with M → **RED at M = 10·N**.
*Mutation:* reclaim the newest tombstone below `T` for **data** rows too → a delete-then-read-at-`T`
returns the pre-delete row → G2.3 RED. The second mutation exists so the asymmetry is proven
deliberate rather than assumed.

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

### 4.0 The global lock order — stated, and gated (G-B6)

*"Lock-safe"* is a verbatim clause of goal #2 and v2.0 had **zero gates for it**. Worse, §4.4
introduced a fresh ABBA cycle without noticing: deflation was described as running "over *all*
sessions, connected or not, after persisting through the funnel" — i.e. acquiring session mutexes
and calling the funnel — while the funnel holds `sess.mu` **and** updates cache accounting. That
is cache-lock → session-lock in one direction and session-lock → cache-lock in the other. The
prior attempt's single worst failure was a deadlock on every page load; shipping a design that
re-creates the shape without a lock order is not a risk worth taking twice.

**The order. Acquire strictly left to right; never acquire leftward while holding rightward.**

```
storeMu  >  memMu (registry)  >  sess.mu  >  sess.conns[i].mu  >  cacheMu (LEAF)
```

Four rules make it checkable rather than aspirational:

1. **`cacheMu` is a leaf.** Nothing is called while it is held — no funnel, no store I/O, no
   session mutex, no channel send. Its critical sections are pure accounting: adjust
   `entries`/`bytes`, move an LRU node, read a candidate list.
2. **Deflation snapshots, then releases.** It takes `cacheMu`, copies the LRU-cold candidate
   `sid`s into a local slice, **releases `cacheMu`**, and only then acquires each session's `mu`
   and calls the funnel. The funnel's own accounting update re-takes `cacheMu` as a leaf. This is
   the same shape `[main]` `live_store.go:708-716` already uses for idle eviction — build a
   `cands` slice under `memMu.RLock()`, `memMu.RUnlock()`, then walk the candidates taking
   `c.sess.mu.Lock()`. v2 does not invent an idiom here; it names the existing one and makes it
   mandatory.
3. **A deflation candidate can vanish.** Between snapshot and acquisition a session may be
   evicted, promoted, or already deflated. Every candidate is re-validated under `sess.mu` and
   skipped if stale. Deflation is best-effort by construction; the ceiling is maintained by
   *retrying with a fresh snapshot*, never by holding a lock across the walk.
4. **No lock is held across a store write or a channel send.** The funnel persists under
   `sess.mu` (that is its existing contract and the reason persist-before-ack is sound), but it
   must not hold `memMu` — so the funnel is never called from inside a registry walk.

**G1.5 (new) — lock safety.** `go test -race` driving concurrent transitions, deflations,
rehydrations, evictions, provisional promotions and SSE connect/disconnect across 200 sessions
for a bounded duration, under a hard `budget_s`. Exceeding the budget is a **FAIL** that dumps
all goroutine stacks (`SIGQUIT` behaviour) so the cycle is diagnosable from CI output alone,
never a hang. Assertions: zero race reports, zero deadlock, the cache ceiling holds throughout.
*Mutations:* invert the acquisition order in deflation (take `sess.mu` then `cacheMu`) → RED
(deadlock inside the budget); hold `cacheMu` across the funnel call → RED; remove the candidate
re-validation of rule 3 → RED (use-after-evict panic under `-race`).

### 4.0a What "sync" means — the verbatim word v2.0 dropped

Goal #1 is *"Session-bounded Model state **sync**."* v2.0 designed the bound and silently dropped
the word. Defined here, so it has a falsifier:

> **Sync** = (a) every acked transition is reflected on **every** live connection of that session
> — not only the connection that originated it — and (b) a rehydrated session is **state-identical**
> to the pre-deflation session with the intervening transitions applied.

Scope: sync is *within* one session identity (its tabs, its reconnects, its spill cycles).
Convergence *across* identities is goal #4's job and is gated by G4.1/G4.6/G4.7. Saying so
explicitly is what stops goal #1 and goal #4 from each assuming the other covers multi-tab.

**G1.7 (new) — sync convergence.** Two SSE connections on one `sky_sid`. Drive a transition on
connection A; assert B observes the same post-state. Then force a deflate between the transition
and B's delivery; assert B still converges and that its post-state equals A's byte-for-byte after
rehydration. Then reconnect a third tab mid-cycle and assert it renders the same state.
*Mutations:* deliver the frame only to the originating connection → RED; skip the rehydrate and
re-render from a zero Model → RED; drop the outbox coalescing invariant so a superseded frame
wins → RED.

### 4.1 What exists today, measured

- The Model lives in `liveSession` (`[main]` `runtime-go/rt/live.go:2078`): `model any`,
  `handlers`, `prevTree *VNode` (the full rendered tree, with a `map` per node), **two** full HTML
  body strings (`lastComputedBody`, `lastShippedBody`), an ingress channel and a per-connection
  channel each of `sseChanBuffer` `sseFrame`s — where a patch frame's `data` is a **whole body**.
  `sseChanBuffer` is a **`var`, not a constant** (`[main]` `live.go:6540`), initialised from
  `sseChanBufferDefault = 16` (`:6534`) and clamped to `[1, 1024]` by `loadSseChanBuffer()` from
  `SKY_LIVE_SSE_BUFFER`. So the per-connection worst case is operator-tunable up to **1024**
  frames, and any RAM arithmetic that hard-codes 16 is a lower bound, not a bound.
- Measured size: **~37 KB per session** (`[main]` `docs/skylive/tiered-session-cache.md:3-9`, from the
  real OOM incident). Per SSE connection the worst case is ~17 × body size; at a 50 KB body that
  is ~850 KB **per connection**.
- Eviction is **100% time-based** (`idleEvictPass`, `[main]` `live_store.go:696-746`). There is **no
  count cap and no byte cap** anywhere in `runtime-go/`.
- The **default** store is `memory`, which has no idle-evict tier at all — locked by
  `TestTiered_MemoryStoreNoOp` (`[main]` `live_tiered_cache_test.go:304`).
- SSE-connected sessions are **immortal twice over**: the explicit
  `!sess.hasSSEConnOtherThan("")` guard (`[main]` `live_store.go:712` — v2.0 cited `:715` —
  re-checked under lock in the candidate walk, locked by `TestTiered_SSEConnectedNeverEvicted`)
  *and* the 15-second heartbeat that calls `touchLastSeen()` (`[main]` `live.go:6296`), which defeats the
  TTL reap as well.
- Admission control is a static path list (`isBrowserNoisePath`, `[main]` `live.go:3977`). **Any routed
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

`chooseStore` (`[main]` `live_store.go:1550`, two callers only — `live.go:3610`,
`[main]` `subapp_inprocess.go:402`) gains `case "data"`. `memory`, `sqlite`, `redis`, `postgres` remain as
explicit opt-outs so no existing app breaks (ADR-001's non-breaking constraint is kept).

**`data` is the default only for apps that already use Persist.** v2.0 made it *the* default,
which would put pebble — and its transitive sentry/prometheus pull, +10–18 MB of binary — into
**every Sky.Live app**, including ones that never touch a database. G0.3 asserted "a non-Persist
app links no pebble" while the session default guaranteed the opposite for the largest class of
Sky apps. The rule is therefore conditional on a fact the compiler already knows:

| App declares ≥1 `Persist.collection`? | Default session store |
|---|---|
| yes | `data` (one backend, one ledger, one migration story — the whole point of `[data]`) |
| no | unchanged from `[main]` (`memory`, or whatever `[live] store` says) |

The condition is evaluated at build time from the same HIR scan that produces `CollDecl` (§3.3b),
so it is not a runtime probe. **G0.3 gains an arm** asserting that a Sky.Live app with no Persist
collection links zero pebble symbols *and* still selects a non-`data` store — the two halves of
the same claim, which v2.0 tested only the first of.

#### 4.2a `sessionVersion` must be computed, not declared

The envelope is `"SKS1" ‖ BE32 schemaVersion ‖ gob`, and v2.0 left `sessionVersion` as a
hand-declared `sky.toml` key ("bump on any Model semantic change"). That was already the weakest
part of the tiered cache; making sessions **durable by default** makes it dangerous, because gob
decodes *partially and silently*: a Model that gains a field, or changes a field's type in a
gob-compatible direction, decodes into a struct with zero values where the old blob had none — no
error, no log, a user whose cart silently empties after a deploy. The developer must remember to
bump an integer, and the failure of remembering is invisible.

**Fix.** `sessionVersion` is **derived by the compiler** as a structural hash over the Model type:
field names, field order, and each field's fully-resolved Sky type, computed transitively over
named types and emitted into the generated glue as `DataConfig.SessionVersion uint32`. A Model
change that gob would decode wrongly necessarily changes the hash, so the envelope mismatches and
the session is discarded and re-initialised — the safe outcome — instead of decoding into a lie.
The `sky.toml` key is **deleted**, not deprecated: a hand-declared value can only disagree with
the computed one, and there is no case where the human is right.

This reuses the mechanism §3.3b already builds (solved HIR types threaded into the glue), so it
is a second consumer of one pass rather than new machinery. *Mutation (in G1.2):* freeze the hash
to a constant, add a field to the fixture's Model, and assert the old blob is **rejected** rather
than partially decoded → RED.

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

#### 4.4a Three preconditions deflation has, that v2.0 did not state

**(1) Handler-id determinism across re-render.** Deflation discards `handlers`. Rehydration
re-renders to rebuild them, and the client meanwhile holds handler ids minted by the *pre*-deflation
render. If a re-render of the same Model can produce different ids — because ids are minted from a
counter, an allocation order, or a map iteration — then every in-flight click on a deflated session
resolves to the wrong handler or to none. This is not hypothetical: map iteration order is
randomised in Go by design, and `[main]` `VNode` carries a `map` per node.

The contract: **`handlerID = f(stable path in the VNode tree, event name)`, a pure function of the
rendered tree, with no counter, no allocation identity, and no map-iteration dependence.** Where a
map must be walked to emit attributes, it is sorted first — the same discipline `AGENTS.md`
already mandates for record-field emission (`_fieldIndex` sorting). **G1.2 gains an arm**: render,
deflate, rehydrate, re-render, and assert the handler-id set is **byte-identical**; then dispatch
a pre-deflation handler id and assert it resolves. *Mutation:* mint ids from a counter → RED.

**(2) Inflate failure is a defined outcome, not a panic.** Rehydration can fail: the blob is
missing (GC'd, or the store lost it), the envelope version mismatches (§4.2a), gob decode errors,
or the store is unreachable. The behaviour is specified per cause:

| Cause | Behaviour |
|---|---|
| blob absent, or envelope `sessionVersion` mismatch | treat as a **new session**: re-run `init`, keep the SSE connection, push a full render, log `session.inflate.reset` with the cause |
| gob decode error on a matching version | same as above **plus** a `WARN` and `sky_live_session_inflate_errors_total` — this combination indicates corruption, not a deploy |
| store unreachable | **do not reset.** Fail the event with a typed `Error`, leave the session deflated, retry on the next event with backoff. Resetting here would destroy recoverable state because of a transient outage |

The distinction in the third row is the one that matters: v2.0 had no inflate-failure story at
all, and the naive "reset on any failure" turns a five-second store blip into every user being
logged out. *Mutation (G1.2):* collapse the third row into the first → RED (a fault-injected
store outage resets sessions that later prove recoverable).

**(3) Lock ordering.** Deflation and rehydration both obey §4.0. Rehydration additionally must not
hold `sess.mu` across the store read — it takes `sess.mu`, observes `deflated`, releases, reads
the blob, re-acquires, and re-checks (another session may have rehydrated it meanwhile). The
re-check is what makes concurrent rehydration idempotent rather than a double-decode.

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
drop-and-resync path (`sseConn.outOfSync`, `[main]` `live.go:2711`) for the case where a frame is lost;
a *coalesced replacement* is strictly better than a drop and needs no resync.

Result: per-connection RAM ≈ 1 body + goroutine stack ≈ tens of KB, not ~850 KB.

**The irreducible part, stated:** each connected client costs one goroutine, one HTTP connection,
and one coalesced frame. That is linear in *connected clients* and cannot be removed by any data
layer. G1.1 measures it and publishes it as a floor rather than folding it into the "bound".

### 4.7 Gates

**G1.1 — the ceiling holds.** *Restructured in v2.1 (G-B8).* v2.0 specified `N = 50 000` sessions
each with an open SSE connection, and no harness. Two things were wrong with that, and the second
is the serious one:

1. **It probably could not run.** 50 000 concurrent SSE connections from one load generator
   exhausts the ephemeral port range (~28 k per source-IP/dest 4-tuple) long before the assertion
   is reached, and needs multiple source addresses to get past it. An unrunnable gate gets
   skipped, and under v2.0's `STATUS.md` schema a skipped gate rendered as *absent* — H2's exact
   defect. *(The fd-limit objection raised alongside this does not hold: measured `ulimit -n` on
   the development host is 1 048 576, not 256. Ports and RAM are the real walls.)*
2. **It could go green while the app OOMs.** With §4.1's measured 50 KB bodies, 50 000
   connections carry ~2.5 GB of body strings plus ~400 MB of goroutine stacks, against a 64 MiB
   `sessionCacheMaxBytes`. The asserted bound covers **~2 %** of the footprint. And `perConnFloor`,
   measured on the tiny todo app of §7.6, under-reports the real per-connection cost by ~50×. So
   G1.1 as written could pass while the app OOMs a 4 GB container — which is **dev pain #4,
   verbatim**, the thing goal #1 exists to kill.

**Restructured: prove the bound cheaply, and report the capacity separately.**

*Arm A — the ceiling (fast tier, correctness).* Set `sessionCacheMaxBytes` and
`sessionCacheMaxEntries` **low** (1 MiB / 50) and drive `N ≈ 200` sessions. A small ceiling makes
the same property observable in seconds on a laptop, and a bound is a bound at any scale:

- `sessionCache.bytes ≤ maxBytes` and `entries ≤ maxEntries` at **every** sample
- deflation actually fires (`sky_live_sessions_deflated_total > 0`) — a ceiling held by never
  reaching it proves nothing
- with a **non-durable** store, 503 + `Retry-After` refusal fires (§4.5) and no established
  session is destroyed
- `sky_live_sessions_resident` and `sky_live_session_bytes` gauges exist and are non-zero

*Arm B — body size is a parameter, not a constant.* Arm A runs at **1 KB and 50 KB** body sizes.
The 50 KB arm is where the outbox coalescing of §4.6 either works or does not, and it is the size
the real OOM incident actually had.

*Arm C — an admission bound on CONNECTIONS.* §4.4 bounds session *state*; nothing in v2.0 bounded
the connection count, so the irreducible per-connection term (F5) was unbounded in practice.
`[data] maxLiveConnections` (default `0` = unlimited, and set by G1.1) makes the SSE upgrade
return 503 + `Retry-After` past the limit, with `sky_live_connections_refused_total`. Assert the
process refuses rather than accepting past the bound. Without this arm F5 is a floor with no
ceiling, which is not a bound at all.

*Arm D — capacity REPORT, `--tier=full`, not a correctness gate.* The 50 000-session run, with a
multi-source-address harness, publishing measured RSS, per-connection bytes and the ceiling's
share of the footprint into `docs/bluedb/baselines.json`. It **reports**; it does not gate. An
honest number nobody can fake beats a green tick on an assertion covering 2 % of the problem.

`perConnFloor` is **a committed constant in `baselines.json`**, not a value the run measures and
then compares itself against — a self-measured floor makes the RSS assertion tautological. It is
updated by an explicit `--bless`, which is a reviewable diff.

*Mutations:* restore the `!hasSSEConnOtherThan("")` immunity → Arm A RED. Remove the `maxBytes`
check, keeping only `maxEntries` → Arm A RED at 50 KB bodies (a few large sessions blow the byte
budget while the count is fine — which is why Arm B parameterises size). **Undercount
`treeBytes`** in the funnel's accounting (return 0 for child nodes) → Arm A must go RED: without
this mutation the `sessionCache.bytes` assertion bounds *an accounting variable*, not RAM, and an
accounting bug makes the gate green and the machine dead. Set `maxLiveConnections` to 0 while
driving past it → Arm C RED.

**G1.6 (new) — durable sessions have a write cost, and it must be priced.** Sessions-as-collection
(§4.2) × persist-before-ack (§1.3) × one writer (F1/F2) is an unpriced multiplication: at G1.1's
own workload — 50 000 sessions with a periodic transition — a naive implementation issues ~5 000
`Apply(pebble.Sync)` calls per second, which no single disk sustains. v2.0 asserted the
architecture and never counted the fsyncs.

The mechanism that makes it affordable already exists in the substrate and is ported in P1: the
single-writer committer's **group commit** batches concurrent transactions into one `Apply` and
one fsync. G1.6 measures whether it actually does, under the session workload specifically:

- count `fsync`s (via the committer's own counter, cross-checked against an `errorfs`-instrumented
  `vfs`) and commits over a 60 s run at 2 000 session-transitions/s
- assert **amplification** `commits / fsyncs ≥ 8` at that rate — i.e. group commit is engaging
- assert **p99 ack latency ≤ 25 ms**, because group commit trades latency for throughput and an
  unbounded trade is a different bug
- assert the durable-session throughput floor of §2.6 is met with `durability = "full"`

*Mutations:* disable group commit (one `Apply` per transaction) → amplification = 1 → RED.
Widen the group-commit window to 500 ms → p99 latency → RED. Both directions must be red, or the
gate is measuring one side of a trade-off and calling it a bound.

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
   transaction opened under tenant `T` can only construct iterators over that tenant's range
   (§3.2c gives the exact layout), with the upper bound from `bytesSuccessor`, never a `0xFF`
   sentinel. Cross-tenant reads are impossible rather than filtered.

   **The hole v2.0 left, and how it is closed.** v2.0 asserted "there is **no API that accepts a
   tenant argument from data** — `Reader.Iterate`, `Txn.Scan`, and the planner all take their
   bounds from the transaction." That is **false as written**: `[bdb]` `reader.go:67` is
   `func (r *pebbleReader) Iterate(prefix []byte) Cursor` — a **caller-supplied raw `[]byte`**,
   used as `lower := append([]byte{tagData}, prefix...)` with no collection or tenant
   enforcement whatsoever. Callers happen to build it via `[bdb]` `backend.go:252 dataCollPrefix`, but
   "happen to" is not a structural property, and §5.2 property 3 (and G2.5 arm 3) would have been
   proving something the API contradicts.

   The fix is **not** a better dataflow analysis. Proving that no path anywhere ever constructs a
   forged prefix is a hard interprocedural property, and a gate that must be that clever is a gate
   that will be quietly weakened. **Delete the hole instead**: `Iterate` takes a typed scope the
   caller cannot forge.

   ```go
   // A Scope is only obtainable from a Txn or a Reader, and only carries the
   // tenant that transaction was opened under. There is no exported constructor
   // and no []byte-accepting variant. `System` scope is minted only by §5.3's
   // capability, which only the console funnel can produce.
   type Scope struct{ ten string; coll CollID; sys bool }   // unexported fields

   func (tx *Txn)         CollScope(coll CollID) Scope
   func (r  *pebbleReader) Iterate(s Scope) Cursor
   func (r  *pebbleReader) IterateRange(s Scope, b Bounds) Cursor
   ```

   G2.5 arm 3 becomes a **lexical** check (no `Iterate` overload takes `[]byte`; the `Scope`
   fields are unexported; no exported `Scope` constructor exists) rather than a dataflow proof —
   which is both stronger and cheaper. That is the general lesson worth keeping: when a structural
   property is expensive to prove, the right move is usually to remove what makes it expensive.

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

### 5.4 Migration of existing data — generation-stamped dual-read

Data written before v2 has no tenant component. v2.0 described the rewrite as *"an ordinary bulk
transaction, resumable"*. That sentence contains three defects and they compound:

1. **It OOMs.** `[bdb]` `txn.go:57-59` — the write-set is `writes map[string]*bufferedWrite` plus
   an `order []string`, buffered in RAM and applied atomically at `Commit` (struct doc `:44-45`:
   "buffers a write-set with read-your-writes overlay"). A single transaction rewriting every key
   in the store holds every key **and every value** in memory. On any store worth migrating, that
   is the process.
2. **Chunking breaks atomicity.** Split it into chunks and it is no longer one transaction, so
   between chunks the store is **partially migrated** — some rows under the old layout, some under
   the new. A reader scoped to the new prefix silently **misses every un-migrated row**. Silent
   under-read is the same failure class as E-B1, arriving by a different door.
3. **`single` → `multi` is not covered at all.** Flipping `[data] tenancy` re-keys *every* row
   (§3.2c changes the layout, not just a prefix) and re-keys every index and unique entry. v2.0
   treated it as a config edit.

**The mechanism: a generation stamp plus dual-read, so a partial state is correct.**

```
meta: "keygen"        = { active: 1, migrating_to: 2, cursor: <last migrated key>, started: … }
```

- Keys carry no generation byte. The *store* carries a generation record, and the layout for each
  generation is a pure function the binary knows.
- While `migrating_to` is set, **every read consults both generations**: the planner issues the
  seek under gen 2 and, for the key range **at or after `cursor`**, also under gen 1, merging by
  pk with gen 2 winning. Below `cursor` only gen 2 is consulted, because everything there is
  migrated. So a partially-migrated store returns **exactly** the same rows as a fully-migrated
  one — no silent misses at any point.
- The migrator walks in key order, moving a bounded chunk per transaction (default 1 000 rows,
  `[data] migrateChunkRows`), advancing `cursor` **in the same transaction as the chunk**, so a
  crash resumes exactly where it stopped and a chunk is never half-applied.
- **Writes during migration** go to gen 2 only, and delete the gen-1 twin in the same
  transaction — so the migrator never has to reconcile concurrent writes.
- When the walk completes, `active` flips to 2 and `migrating_to` clears, in one transaction.
  Dual-read stops. Rollback before the flip is: clear `migrating_to`, drop gen 2's keyspace.
- **Cost, stated:** during migration every read in the un-migrated range costs two seeks. That is
  a documented, bounded, self-limiting degradation, and it is the price of never returning a
  wrong answer. `sky_persist_migration_progress` exports `cursor` as a fraction.

The `single` → `multi` flip uses the identical mechanism — it is just a different gen-2 layout
function — so there is one migration engine, not two. Index and unique entries are **not**
migrated: they are dropped and rebuilt by `reindex` after the flip, because they are derivable
(§3.2) and rebuilding is cheaper than rewriting.

**G2.8 (new) — tenant key rewrite.** A store with N rows across several collections, indexes and
unique constraints, in both tenancy modes. (a) Migrate to completion; assert a G2.3-style
re-derivation of every index and a full row-set equality against a pre-migration snapshot.
(b) **Kill the process at a random chunk boundary** and at a random point *within* a chunk;
restart; assert resumption and identical final state. (c) **Read during migration**: at every
10 % of progress, run a query corpus and assert results are identical to the fully-migrated
store's. (d) Write during migration, then assert the write survives the flip. (e) Assert peak RSS
during migration is `O(chunk)`, not `O(store)`.
*Mutations:* drop the dual-read (query gen 2 only) → (c) RED — rows vanish mid-migration; advance
`cursor` in a separate transaction from the chunk → (b) RED; raise the chunk size to the whole
store → (e) RED; skip the index rebuild after the flip → (a) RED.

### 5.5 Gates

**G2.5 — cross-tenant structural impossibility.**

1. *Adversarial contents.* Write rows under tenant `T1` whose bodies contain
   `{"tenant":"T2"}`, `{"tenant":""}`, `{"tenant":"T2\x00T1"}`, and a body encoding the raw
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

5. **The tenant VALUE's provenance — a declared merge dependency.** Arms 1–4 all test the
   `bluedb` package, and inside that package the design is sound. But the tenant *value* arrives
   from `rt` across the `currentLiveSession()` bridge (`[main]` `live_session_ctx.go`), and the
   mandate places `rt`'s **`handleEvent` session hijack** out of scope on
   `fix/skylive-runtime-soundness`. A hijack that hands a handler the *wrong session* hands the
   engine a correctly-attested key range for the **wrong tenant** — and every gate in G2.5 still
   passes, because the engine did exactly what it was told. Scoping the gate to `bluedb` while the
   input is unsound is precisely the coupling that makes a security property look proven when it
   is not.

   Therefore `fix/skylive-runtime-soundness` is a **DECLARED MERGE DEPENDENCY** of this branch,
   not merely adjacent work. **G2.5.5**: assert the fix is present in the merge base — by a
   behavioural probe, not a commit-sha check (a sha rots across rebases): interleave two sessions'
   events through `handleEvent` and assert each handler observes its own session's identity. The
   gate **FAILS** if the fix is absent, so BlueDB cannot be declared closed on top of a runtime
   that can misattribute the tenant. *Mutation:* revert the hijack fix in the scratch worktree →
   RED.

*Mutations:* add a `Txn.SetTenantUnchecked` used by the reader → arm 3 RED. Take the tenant from
the decoded row body in the index-key builder → arm 1 RED. Drop the escaping and concatenate raw
→ arm 2 RED. Add an exported `Scope` constructor taking `[]byte` → arm 3 RED (§5.2 property 3).

---

## 6. A5 — reactivity that composes with scale

### 6.1 What is wrong

- Reactivity is **embedded-only** and **single-process** (`[bdb]` `rt/bluedb_reactive.go:196-198`), and
  the cross-instance path does not exist at all (P15).
- The capability gate calls **`os.Exit(1)` on the first session**, under `sess.mu`
  (`[bdb]` `bluedb_reactive_gate.go:172`, doc comment at `:156-159` — the exit under the lock is
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
    // DECODED FROM THE ROW KEY (§3.2c), never from CommitReq.Tenant.
    //
    // v2.0 left this field with no stated derivation, and the only value
    // available at that layer today is CommitReq.Tenant — the TRANSIENT tag
    // documented at [bdb] txn.go:78-81 / engine.go:123-130 as "NEVER durably
    // written", and the very thing §5.1 deletes. Deriving the changeset's
    // tenant from a tag that no longer exists is not a small gap: §5.3's
    // asTenant claim (a background job's writes reach that tenant's reactive
    // subscriptions) would have had no mechanism at all, and §6.4's
    // per-tenant partitioning would have keyed on "".
    //
    // Since §5 puts the tenant IN the key, every RowChange already carries it
    // durably. The changeset decodes it, which makes the reactive partition
    // exactly as attested as the read path — one source of truth, not two.
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

#### 6.2a "In the commit path" is verbatim — so the changeset must be commit-ATOMIC

Goal #4 says *"Notify clients of changesets (query/row-scoped, **in the commit path**)."* v2.0
moved emission to "the Persist commit boundary", which on SQL means **after** the backend's
`COMMIT` returns, from an in-memory hand-off. That is emission *near* the commit path, not in it,
and the difference is observable: kill the process in the window between `COMMIT` and the emit and
the change is durable while the changeset is **gone forever**. Every session subscribed to that
query is permanently stale, with no gap for the resync path to detect — the watermark never
advanced, so nothing knows anything was missed. On embedded this cannot happen (the changelog
entry is in the same atomic batch, `[bdb]` `committer.go:343-346`); v2.0 silently gave SQL a
weaker property under the same sentence.

**Fix — the changeset is derived from a durable artefact written INSIDE the committing
transaction.** This also answers the question v2.0 left open ("on SQL who writes
`_sky_changelog`, inside the user's txn or not?"): **inside**, always.

| Backend | Where the changeset becomes durable | Atomic with the data? |
|---|---|---|
| embedded | `ChangelogPayload` in the same Pebble batch, behind the same `Apply(pebble.Sync)` (`[bdb]` `committer.go:343-346`, `:306`) | yes, already |
| sqlite | an `INSERT INTO _sky_changelog` on the **writer connection, inside the same transaction** as the user's writes | yes |
| postgres | same, plus `NOTIFY` issued **after** commit as a pure *nudge* — the durable row is the truth, the notification is only a latency optimisation | yes |

Emission to subscribers therefore becomes a **tail of a durable log**, on every backend, not a
hand-off. A crash anywhere loses at most latency: on restart the tailer resumes from its
watermark and delivers what it missed. `NOTIFY` being lossy (§6.4) stops being a special case and
becomes the general mechanism — one recovery model, three backends.

Cost, stated: one extra row written per transaction on SQL, GC'd at
`[data] changelogRetention`. That is the honest price of the verbatim clause, and it is cheaper
than the alternative (logical decoding / a WAL reader), which would need replication privileges
Sky cannot assume.

**G4.8 (new) — changeset atomic with commit.** Commit a transaction and `SIGKILL` the process in
the window between the backend's `COMMIT` returning and the subscriber emit (injected via a fault
point, exercised across a randomized set of injection offsets). Restart. Assert (a) the data
change is present, (b) the changeset is **still delivered** to a subscriber that reconnects, and
(c) the subscriber's watermark advances exactly once — a double delivery is acceptable
(at-least-once, B4), a lost one is not. *Mutations:* move the `_sky_changelog` insert outside the
user's transaction → (b) RED; make the postgres path emit from memory and skip the durable row →
(b) RED; treat `NOTIFY` as the source of truth rather than a nudge → (b) RED when the notification
is dropped.

**Layering.** `bluedb` still may not import `rt` (it is a leaf). The bridge is the
`persistglue` package (§7.2) — the same shape as the *existing, in-production*
`console_app` → `rt.RegisterInlineConsoleCfgProvider` registration
(`[main]` `runtime-go/rt/console_app/register_v3.go:33-35`), where a leaf package pushes a factory into
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
pump checks it on every wake *and* on a timer, and a set flag forces a full re-query.

**G4.5 excludes the alternative convergence paths.** v2.0 said "force a drop, assert the session
converges" — but a session also converges via the timer, via the SSE reconnect resync, and via the
next unrelated commit's delta. Any of those makes the gate green with the latch consumer deleted,
which is exactly the vacuity class the mandate names. So G4.5 **disables the timer and the
reconnect resync for the duration of the assertion**, drives no further commits, and asserts
convergence happens **and** that it happened through the latch (`sky_persist_resync_total`
incremented by exactly 1, attributed to the latch cause). *Mutation:* remove the latch check →
RED. *Mutation:* re-enable the timer inside the gate → the gate must **still** be RED with the
latch check removed; if it goes green, the gate is measuring the timer and is rejected.

**G4.6 (new) — query-scoped NON-delivery.** Goal #4's verbatim clause is
*"(query/row-scoped, in the commit path)"*, and v2.0's gate set proved only the positive
direction: G4.1 proves a matching commit **is** delivered, G4.2 proves another tenant's is not.
Nothing asserted that a **non-matching commit in the same tenant** does not wake a session — so
**a broadcast-everything implementation passes goal #4's entire gate set**. That is not a
hypothetical implementation; it is what re-query-on-any-nudge degenerates to.

Subscribe to `status = "a"`. Commit a row with `status = "b"` in the same collection and the same
tenant. Assert **zero** wakes, zero frames, and zero predicate evaluations attributed to that
session beyond the single bucket-level match test. Repeat for: a commit to a *different*
collection; a commit to a row that was matching and still matches with an unindexed field changed
(must wake — `Stay`); a row leaving the predicate (must wake — `Leave`). *Mutations:* broadcast on
any commit → RED; drop the `Leave` classification so a departing row does not wake → RED (the
negative gate must not be satisfiable by never waking at all).

**G4.7 (new) — the delta is applied, not re-queried, and it is proven mechanically.** §6.3's
central claim — "apply the delta, do not re-query" — is the fix for the prior branch's discarded-
deltas defect (P17), and v2.0 guarded it **only with a baseline** (G4.3's ≤20 % regression check).
A baseline is seeded from whatever ships first, so a re-querying implementation records its own
cost as the baseline and stays green forever. The claim needs a mechanical falsifier, and §3.6
already built one: `ScanStats`.

Per notification delivered to a subscriber, assert on the engine's counters:
`KeysVisited == 0`, `RowsReturned == 0`, `PointGets == 0`. A delta application reads **nothing**
from the store — it decodes the `RowChange` it was handed and splices the bound list. Any non-zero
counter means a query ran. *Mutation:* replace delta application with `Persist.toList` → counters
non-zero → RED. *Mutation:* apply the delta but also refresh "just to be safe" → RED. The resync
path is exempt and is asserted separately (it *must* show non-zero counters, or it is not
re-querying — the inverse assertion, so neither path can impersonate the other).

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

> **`redis` is cut from v2** *(deliverability item D3)* and moves to §11.2. Goal #4's verbatim
> requirement is that clients are notified of changesets; `local` serves single-instance
> deployments and `postgres` serves multi-replica ones, and F2 already makes `driver = postgres`
> mandatory for multi-replica. A redis bus is therefore a *third* transport for a topology
> already covered — a whole extra recovery model (streams, trimming, consumer groups, its own
> gap semantics, its own gate matrix) buying zero additional goal coverage. `SKY_LIVE_BROKER_URL`
> keeps working for `[main]`'s existing Sky.Live broker; it simply does not become a `ChangeBus`
> backend in v2. `ChangeBus` stays an interface, so adding it later is additive.

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
| **Any app**, `replicas > 1`, session store resolves to `data` with `driver = embedded`/`sqlite` | **startup fatal** — v2.0's matrix covered only *reactive* apps, but §4.2 makes sessions a Persist collection, so a multi-replica app on a single-machine driver now silently splits its **session** state too: each replica owns a private `_sky_sessions`, and a user bounced between replicas loses their session. Naming reactivity as the trigger would have missed the larger blast radius |
| `replicas > 1` with `[live] store = "memory"` (explicit opt-out) | **startup fatal** — pre-existing hazard on `[main]`, now checkable because `replicas` exists |
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
(the shared-predicate `matchCache` from `feat/bluedb`'s `[bdb]` `reactive.go:121-131` is a genuinely
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
changeBus  = "auto"            # auto (default: derived from driver) | local | postgres

sessionCacheMaxBytes   = "64MiB"
sessionCacheMaxEntries = 10000
sessionMaxBytes        = "1MiB"
maxLiveConnections     = 0      # 0 = unlimited; §4.7 arm C
provisionalTTL         = "60s"
sessionTTL             = "30m"
fullScanWarnRows       = 10000
walCheckpointBytes     = "64MiB"
migrateChunkRows       = 1000
changelogRetention     = "1h"
```

`sessionVersion` is **not** a key — it is computed by the compiler (§4.2a). `redis` is not a
`changeBus` value in v2 (§6.4, D3).

**`[data]` subsumes `[database]` only, in v2.** *(Changed in v2.1 — deliverability item D5.)*
v2.0 also had it subsume `[live] store`/`storePath`/`ttl` and `[analytics] dbPath`, with a
deprecation mapping and one warning per project. That is a compatibility surface across three
config sections, each with its own precedence, legacy-env, and warning semantics — and it buys no
goal coverage: §4.2's session-store default already routes Persist apps to `[data]`, which is the
substance. The subsumption and the deprecation mapping move to **post-P8**. `[live]` and
`[analytics]` keep working exactly as on `[main]` and are untouched by this branch.

`[data]` wins over `[database]` — implemented correctly, because `SetSkyDefault` is
**first-wins** (`[main]` `lower.rs:785`), so `[data]`-derived defaults are pushed **first**. (The
prior design asserted "pushed last wins" and was inverted; the fix is already known and is
ported.)

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
`write_embedded_migrations` (`[main]` `rust/crates/project/src/build.rs:1354`), which already generates a
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
  is the same shape as the existing `console_app` prune (`[main]` `rust/crates/project/src/build.rs:1418`).
- **A non-Persist app links no pebble.** Nothing imports it, so nothing is built.
- **A dead key is impossible.** If `[data] driver` were not read, no glue would be emitted and
  `Persist.*` would have no backend — the app fails at build or boot, not silently.

`DB_DRIVER` is **already deleted on `main`** — this is no longer work for a BlueDB phase. It was
planned here as a P0 item against `origin/main` @ `fdbc398d`; `main` closed it independently
before this branch was rebased onto it, so the citations that pointed at the *defect* now point
at nothing. They are replaced with the successor evidence:

| Was cited (pre-rebase, the defect) | Now (`[main]`, the fix) |
|---|---|
| build.rs line 1442 — `assert!(has("DB_DRIVER", "sqlite"))` | `[main]` `rust/crates/project/src/build.rs:1735` — `"DB_DRIVER must not be emitted — nothing reads it"` |
| docs/sky-toml.md line 202 — a `driver` row promising `<PREFIX>_DB_DRIVER` selects the driver | `[main]` `docs/sky-toml.md:208` — "The driver is derived from the connection string, not configured." |
| docs/skydb/overview.md line 558 — `driver = "sqlite"  # SKY_DB_DRIVER` | `[main]` `docs/skydb/overview.md:561` — "The driver comes from the connection string's shape, not from a config key." |

The old lines still exist verbatim on `[bdb]`, `[p5e]`, `[exp]` and
`backup/bluedb-v2-pre-rebase`, which is why a line-existence check alone would have "resolved"
them against the wrong tree and reported the work as outstanding. **The sibling defect this
paragraph did not know about — `[auth] driver`, the same shape one section down in the same
parser — was found by G0.4 on this branch and closed here**
(`[main]` `docs/sky-toml.md:183`). `[auth] tokenTtl` and `[auth] cookieName` are NOT dead: their reader is
app code, per `[main]` `docs/sky-toml.md:161`.

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
`runtime-go/` (an `os.Getenv`/`skyGetenv` scan), and every DB/DATA-shaped getenv in `runtime-go/`
appears in the table.

**The `Reader::Glue` arm proves LIVENESS, not reachability.** v2.0 asserted it by byte-diffing the
generated glue with the key flipped. A byte-diff proves the key *reached the generator* — it does
not prove anything *reads* what was generated, which is the exact failure mode `DB_DRIVER` had
(written faithfully, read by nobody). So each `Reader::Glue` key additionally declares a
**behavioural probe**: flip the key, build a fixture app, and assert an *observable* difference —
`driver` changes which backend the process opens (asserted from `/_sky/console` discovery or a
startup log line), `tenancy` changes the key layout (asserted from a raw key dump),
`sessionCacheMaxBytes` changes the ceiling at which deflation fires. A key whose flip changes glue
bytes but changes no behaviour is a **FAIL**. *Mutations:* add a key with no reader → RED; add a
key that reaches the glue but is never consumed at runtime → RED (this is the one v2.0 would have
passed). This closes the whole class, not just `DB_DRIVER`.

**G0.5 — the zstd tag.** There is exactly **one** `go build` invocation in the compiler —
`run_go_build_once` (`[main]` `build.rs:708-735`) — reached from **three** call paths:
`:578` (FFI-detected CGO=1 first attempt), `:590` (the preferred CGO=0 static build), and `:600`
(the CGO=1 retry). v2.0 described this as passing the tag "on **both** the CGO=0 and the CGO=1
retry path", which is wrong twice: there are three paths, and the tag is not passed per-path at
all — it is passed at the single site, so **all three inherit it by construction**. That is a
stronger property than v2.0 claimed, and the gate should assert the structure rather than
enumerate paths: (a) `run_go_build_once` is the sole `Command::new(go).arg("build")` site in the
crate (a lexical check — genuinely lexical, so grep is defensible here per RULE ZERO clause 5);
(b) it passes `-tags pebblegozstd`; (c) a Persist example built through **each** of the three call
paths contains no DataDog cgo zstd symbols, checked with `go tool nm`. *Mutations:* drop the tag
from `run_go_build_once` → (b) and (c) RED; add a second `go build` site without the tag → (a)
RED — this is the mutation that matters, because a second site is exactly how the property would
regress. The `go test` form (P8) is already satisfied by `CGO_ENABLED=0` in CI
(`[main]` `rust-ci.yml:255`) and is asserted separately.

**G0.3 — mechanism, not aspiration.** "Builds cold-cache offline" needs a procedure or it is
untestable: the gate builds in a scratch `HOME` with a fresh `GOMODCACHE`, `GOFLAGS=-mod=mod`,
and **`GOPROXY=off`**, so any module not already vendored fails the build rather than silently
fetching. "Links no pebble" is checked with **`go tool nm`** on the output binary
(`! nm(binary) contains "pebble"`), not by grepping source imports — a transitive import would
pass a source grep. Arms: (a) a non-Persist Sky.Live app links zero pebble symbols; (b) it ships
no `bluedb/` directory into `sky-out/`; (c) it selects a **non-`data`** session store (§4.2);
(d) its binary is within 1 MB of the same app built on `[main]`. *Mutations:* make `persistglue`
imported unconditionally → (a) RED; make `data` the unconditional session default → (c) and (a)
RED; remove the `bluedb/` prune → (b) RED.

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

**The API is unified by SUBSETTING — disclosed, not glossed.** Goal #2's verbatim clause is
*"UNIFIED APIs shareable across dbs (sqlite/postgres/bluedb)"*, and the way this surface achieves
it is by offering only what all three backends can do: **no joins, no aggregates, no
`GROUP BY`, no window functions, no subqueries.** That is a real and defensible design (it is why
the same source graduates embedded→sqlite→postgres with a config edit, which G3.3 proves), but
calling it "unified" without naming the subset would let a Judge read goal #2 as satisfied by an
API that cannot express a report. It is disclosed as **B8** in §11.2 and named here. The escape is
`Std.Db` raw SQL on a SQL driver, with the startup fatal of §2.7 on embedded.

**G2.4 — transact-body replayability, at RUNTIME.** *(Changed in v2.1 — G-B7, deliverability item
D2.)* `transact` retries, and §2.2 clause 2 promises the retry is **invisible**, so a
non-replayable effect inside the body silently double-executes: a slipped `Http.post` double-charges
a card and nothing in the system knows.

v2.0's mechanism was a HIR walk of the `transact` lambda. It is **bypassed by anything beyond a
literal lambda** — `transact conn (\tx -> myHelper tx)`, a `transact` applied to a function
*value*, a captured `Task`, a call inside a `List.map` closure. Each of those is ordinary Sky, and
each defeats the check silently, which is the worst property a safety check can have.

**The mechanism is a runtime poison flag**, and the repo already has the exact idiom in
production: `[main]` `runtime-go/rt/live_session_ctx.go` stamps a goroutine-local value
(`liveSessionByGoroutine sync.Map` keyed by goroutine id) around user code via
`runWithLiveSession(sess, fn)` — a set/defer-clear wrapper explicitly designed so the defer cannot
be forgotten, itself modelled on `goroutine_context.go`'s trace context. v2 adds a parallel mark:

```go
// rt — mirrors live_session_ctx.go exactly.
func runInTransact(fn func())        // stamps, defers clear
func insideTransact() bool
```

Every non-replayable kernel (`Http.*`, `File.*`, `Time.now`, `Uuid.*`, `Random.*`,
`Std.Db.execRaw`, external `Task.perform`) begins with `if insideTransact() { return typed error }`.
Three consequences:

1. **Indirect calls are caught by construction.** The check is at the *callee*, so it does not
   matter how the call was reached — helper, closure, function value, or `List.map`. This is
   strictly stronger than the HIR walk on the dimension that actually failed.
2. **The compiler change leaves P3.** No new HIR pass, no new diagnostic infrastructure, no
   whole-program analysis. This is a large part of why P3 becomes shippable (§10).
3. **The trade, stated honestly.** The check is *dynamic*, so a rarely-taken branch containing an
   `Http.post` ships green and fails in production the first time that branch runs — where the
   HIR walk (for the literal-lambda case it did cover) would have failed the build. The typed
   error is loud and names the kernel and the fix, and the failure is a clean transaction abort
   rather than a double charge, so the outcome is safe in both designs; only the *discovery time*
   differs. A lint-level HIR warning for the literal-lambda case would recover the early signal
   and is deferred to §11.2 — deliberately *not* an error, so it can never be the thing anyone
   relies on.

**The mark must propagate across goroutine spawn.** This is the hole in the poison-flag design and
it must be closed or the mechanism inherits an indirection gap of its own: `Task.parallel` /
`Cmd.batch` inside a transact body run on *new* goroutines, which carry no stamp. Since every Sky
concurrency primitive is an `rt` kernel, `rt` owns every spawn site, and each one propagates the
mark to the child exactly as trace-context propagation must. **G2.4 arm 2** asserts it:
`Task.parallel` inside `transact`, whose branch calls `Http.get`, is rejected.

*Mutations:* an `Http.get` inside a fixture's transact body must produce the typed error; removing
the check from the kernel → RED. An `Http.get` reached **via a helper function** → RED without the
fix (this is the arm v2.0's design could not have passed). Remove mark propagation at the spawn
seam → arm 2 RED. Poison `Persist.Test.barrier` along with the rest → G2.1 RED (the barrier is the
single explicitly-exempt kernel, §2.5, and it is linked only under the `sky_test_barrier` build
tag so it cannot exist in a release binary).

### 7.4 The escape hatch — reuse `Std.Db`, do not build a second one

*(Changed in v2.1 — deliverability item D4.)* v2.0 introduced a new `Std.Persist.Sql` module
(`raw` / `execRaw`) plus a build-time static-reference check making its use under
`driver = "embedded"` a compile error. **Both are cut.**

The reasoning is the no-parallel-implementations rule: `Std.Db` already ships on `[main]` with raw
SQL, typed parameters, the `guardIdents` allow-list, and a documented surface. `Std.Persist.Sql`
would be a second raw-SQL API to design, document, `sky doc`, gate, and keep in sync — and the
static-reference check is a new whole-program reachability analysis in the compiler, which is
precisely the kind of "one more compiler pass" that has ended two prior attempts. Together they
cost most of a phase and defend a case the startup fatal of §2.7 already covers.

So: raw SQL is `Std.Db`, unchanged. Opening `Std.Db` while `[data] driver = "embedded"` is a
**startup fatal** naming the module and the two fixes. Joins, aggregates, and window functions are
the intended users, and they are documented as the graduation trigger to `driver = "postgres"`.

Two bounds this concedes, both disclosed rather than absorbed:

- **B9** — raw `Std.Db` writes bypass Persist, so they emit **no changeset** and do not drive
  reactivity. v2.0 had the identical hole via `Std.Persist.Sql.execRaw` and did not state it.
  Documented in `docs/skypersist/`, and `Persist` exposes `Persist.touch : Collection a -> String
  -> Task Error ()` for an app that must nudge subscribers after a raw write.
- The error arrives at boot rather than at build. Later, still before any request.

### 7.5 One migration story

`sky data migrate --gen | migrate | status | seed | reindex`, aliasing the existing `sky db`
verbs. The DB-free declared-vs-recorded diff, the checksummed `_sky_migrations` ledger, the
never-lossy quarantine, and the TTY rename prompts all port unchanged from
`Std.Db.Schema` / the file-based migration machinery on `main`. What is added:

- `_sky_sessions` participates (one store, one ledger) — the thing `[data]` was supposed to buy
  and did not (P20).
- `reindex` rebuilds index entries from data (§3.2), for an index-encoding version bump or a new
  index on an existing collection, and carries the **drain-to-`T` barrier** of §3.3a when the
  coordinate encoding changes.
- The tenant key migration (§5.4) is a generation-stamped dual-read migration, gated by **G2.8**.

**G3.4 (new) — the migration lifecycle.** Dev pain #3 is *"data migration"* verbatim, and v2.0
gave it no numbered gate at all — it appeared only as prose asserting the machinery "ports
unchanged". Ported machinery running against a new engine is exactly the thing that needs a gate.

Arms, on all three drivers: (a) `--gen` produces a migration from a declared-vs-recorded diff with
**no database connection**; (b) applying it twice is a no-op (the checksummed `_sky_migrations`
ledger); (c) a hand-edited migration file fails the checksum gate; (d) a **destructive** diff
(dropped column) is quarantined, never silently applied; (e) `status` reports pending/applied
correctly against a partially-migrated store; (f) a migration interrupted by `SIGKILL` mid-apply
resumes to a consistent state; (g) adding an index to an existing populated collection triggers
`reindex` and the new index is immediately usable by the planner (asserted via `explain`);
(h) `seed` is idempotent. *Mutations:* remove the checksum verification → (c) RED; allow the
destructive diff through → (d) RED; skip the post-migration `reindex` → (g) RED; make `--gen`
require a connection → (a) RED.

### 7.6 The flagship app — and where it lives

**It does not live in this document.** v2.0 put a "10-line app" here that was ~20 lines, referenced
`init`, `update`, `view` and `setItems` without defining them, would not compile, and sat in
`docs/bluedb/` where **G3.2 (the `scripts/doc-examples.sh` live-docs gate) does not reach it** —
an aspirational snippet in a design doc, which is the exact artefact class RULE ZERO exists to
eliminate.

The complete, compiling app moves to **`docs/skypersist/todo.md`**, inside the tree
`scripts/doc-examples.sh` `sky check`s, so it **cannot rot**. `docs/skypersist/overview.md`
links it. This document states only the properties it must demonstrate, which is what a design
doc is for:

| Property | Gated by |
|---|---|
| **No `[data]` section at all** — zero config | G3.1 |
| No connection management, no backend named in source | G3.1, G3.3 |
| `sky run src/Main.sky` creates `data/app.blue` and migrates on first boot | G3.1 |
| Data survives a restart | G3.1 |
| The bound list stays live across tabs with no hand-written subscription | G4.1, G1.7 |
| The whole file compiles under `sky check` as written in the docs | **G3.2** |
| Graduating to Postgres is a `sky.toml` edit and **no source change** | G3.3 |
| Every line of it is reachable from `sky doc Std.Persist` | G3.2 |

**G3.1's mutations** (v2.0 had none): delete the zero-config default so the app needs a `[data]`
section → RED; make the store non-durable so restart loses data → RED; require an explicit
`P.connect` call in source → RED (the app no longer compiles as documented).
**G3.2's mutations:** break one Sky example in `docs/skypersist/` → RED; move an example outside
the gated tree → RED (the gate must notice its own coverage shrinking, which is how a doc example
silently stops being checked). **G3.3's mutations:** introduce a driver-conditional branch in the
app source → RED; make one behavioural assertion pass on sqlite but not postgres → RED.

### 7.7 Docs surface

- `docs/skypersist/overview.md` — the guide, gated by `scripts/doc-examples.sh` so a rotting
  example fails CI (**G3.2**).
- `docs/skypersist/todo.md` — **the flagship app** (§7.6), complete and compiling, living inside
  the gated tree rather than as a snippet in this design doc.
- `docs/skypersist/` also documents the bounds an app author actually meets: **B8** (the API is
  unified by subsetting — no joins/aggregates), **B9** (raw `Std.Db` writes emit no changeset),
  **B3** (`or_` is a residual full scan; use `inList`), **B1/U1** (`Money` is not indexable), and
  **R12** (`Persist.conn` is a memoised CAF and is correct as one).
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

### 8.1 ✅ DECIDED — goal #5 is READ *AND* WRITE

Goal #5 verbatim is **"Built-in Sky Console admin access to records."** v2.0 correctly refused to
decide the scope (the words "read-only", "CRUD" and "LIST/detail" appear nowhere in the user's
goal — they originate in agent-authored docs, and the `goty.rs` collision long cited as blocking
an edit form does not block it, per P7). **The user has now ruled**, and `.claude/AUTONOMOUS_GOAL.md`
records it:

> Asked directly whether "admin access to records" means read-only or read+write, the user chose
> **"Read + write, both in scope"**.

The consequences, all binding:

1. **The Console can view AND edit/delete records.**
2. **Goal #5 CLOSES ONLY WHEN WRITES WORK.** A Judge **MUST return NOT ACHIEVED** for goal #5 on
   a read-only surface. §1's table renders goal #5 as `No` until **G5.4–G5.8** are green.
3. **§8.4 is no longer conditional.** 5e-2 is a required deliverable, and P7b is no longer
   "only if the user rules" — it is a numbered phase (§10, **P8**) on the critical path to close.
4. **It is a user decision, not an agent's reading.** Do not re-narrow it, and do not cite any
   prior doc's "read-only" wording as authority — that wording came from agent-authored docs, and
   the doc most often cited as mandating read-only in fact *recommends shipping writes*.
5. **The `goty.rs` record-fieldset collision does not block the edit form** and may not be
   reinstated as an excuse: it was fixed in v0.19.1, `Std.Live` never imports `Std.Analytics`,
   and `EventProp` appears **0 times** in the generated console.

§8.4 therefore carries four requirements the mandate names explicitly: writes gated on the
**engine-attested tenant** (§5's durable tenancy, not a forgeable app-written column), a
**per-mutation audit trail**, **optimistic concurrency** so the console cannot cause a lost
update, and a **confirm/undo** story.

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
> (`[main]` `runtime-go/rt/db_codec.go:133`) renders and executes the DDL and **registers nothing** — the
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

Two cleanups the port should carry: `isLoopbackRemoteAddr` (`[main]` `console.go:409-436`) has **zero
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
  becomes *provable* because of §5 — under `[p5e]`'s forgeable column it could only be asserted.
  *Mutations:* scope the admin read with a `WHERE tenant = ?` residual over `TenantCol` instead of
  the key range → RED (the adversarial rows appear); widen `ScopeTenant` to the whole namespace →
  RED; take the tenant from the request query string → RED.
- **G5.3 — read e2e.** Playwright against a real app: authenticate, list collections, page a
  100 k-row collection, filter, open a row, confirm a non-`adminShow` field renders as `***`,
  confirm the audit log entry, and confirm the refresh tick does **not** 401.
  *Mutations:* turn `adminShow` into a deny-list → RED (a fixture column named `stripe_sk`
  renders); remove the internal token from the console's loopback self-fetch → RED (the refresh
  tick 401s — this is the inherited bug §8.3 names, and the mutation is what stops the Data tab
  re-acquiring it); make the row list an unindexed scan → RED (the 100 k page exceeds the
  gate's `budget_s`, which is how a performance regression becomes a correctness signal here).

### 8.4 5e-2 — the write surface, REQUIRED

Per §8.1 this is a required deliverable, not a contingency. It requires no change to §8.3.

- **Capability.** `Decision.Mode == ModeReadWrite`, granted only by an explicit
  `SKY_CONSOLE_DATA=readwrite` **and** a super-admin or tenant-admin grant. Read-only is the
  default; a missing setting is read-only, never read-write. The mode is decided **inside** the
  funnel, from the same zero-trust inputs as the scope — a caller cannot pass a mode.
- **Scope — the engine-attested tenant, not a column.** Writes go through `Persist.transact` under
  `Decision.Admin`, so they are serializable (§2) and confined to the decided tenant's **key
  range** (§5). A `ScopeTenant` admin physically cannot construct a key outside its range, so a
  cross-tenant write is not rejected by a check — it is unconstructible. This is the property that
  did not exist under `[p5e]`'s `TenantCol`, whose own source comment concedes it is "a VIEW
  filter over application-declared data, not an authorization boundary over application WRITES"
  (§8.2). Goal #5's write surface is the reason §5 had to be built at all.
- **Optimistic concurrency — no lost update.** Every row rendered in the edit form carries the
  version it was read at (`commitTs` on embedded; `xmin`/a `_version` column on SQL). The write
  is a **compare-and-set inside the transaction**: re-read the row at the transaction's snapshot,
  compare the version, and abort with a typed `Error Conflict` if it moved. Without this, two
  operators editing the same row silently overwrite each other — the classic admin-console lost
  update, and precisely the anomaly (A-P4) §2's own corpus is built to detect. The UI re-renders
  with both versions and asks the operator to re-apply. Note this is *stronger* than relying on
  serializable isolation alone: SSI would let a blind full-row `put` win legitimately, because a
  blind write reads nothing and conflicts with nothing.
- **Confirm, and undo.**
  - **Confirm** is required for `delete` and for any update touching a field the collection marks
    `adminConfirm` (default: the primary key and any field not in `adminShow`). The confirm step
    re-displays the resolved before/after diff and carries a **single-use, funnel-minted token
    bound to (principal, collection, pk, version)** — so a confirm cannot be replayed, cannot be
    forged, and cannot be applied to a row that changed after it was issued.
  - **Undo** is a first-class inverse write, not a UI affordance: because the audit trail records
    the complete before-image (below), the console can re-apply it as an ordinary CAS write within
    `[data] adminUndoWindow` (default 15 min). Undo is itself audited, is *not* itself undoable
    (no infinite chain), and is refused if the row moved since the mutation being undone — the
    same CAS rule, so undo cannot resurrect a state someone else has since edited past.
- **Form derivation.** Scalar fields only (`String`, `Int`, `Float`, `Bool`, `Time`, `Decimal`,
  `Money`), derived from the **`SkyType`** the compiler threaded into `CollDecl` (§3.3b) — *not*
  from `Codec.Shape`, which cannot distinguish `Money` from `String` (G-B5) and would render a
  currency field as free text. Relations, enums, nested records, and validation are out of scope
  for the generic form and render read-only with an explanatory note.
- **The `goty.rs` erased-`any` fieldset collision** is avoided by representing the form's
  field/value pairs as a **tuple**, not a named `{field, value}` record — the documented
  workaround from `record_fieldset_collision_erased`. This is a shape choice in the console's own
  Sky source, not a compiler dependency, and per §8.1 it is not a blocker.
- **Excluded by construction:** `_sky_sessions` and `_sky_migrations` are never writable from the
  console (editing a session blob is a privilege-escalation primitive — it is the app's Model,
  and §4.2's blob is opaque, so a hand-edited blob is arbitrary state injection; editing the
  ledger breaks the checksum gate).
- **Every mutation** is audit-logged with principal, tenant, collection, pk, version-before,
  version-after, **before-image and after-image**, and the `Decision.Reason`. The audit record is
  written **in the same transaction as the mutation** — the §6.2a rule, applied here for the same
  reason: an audit trail that can be lost while the write survives is not an audit trail. And it
  emits a `Changeset` (§6), so other operators' views update live.

**Gates.**

- **G5.4 — write authorization matrix.** Every combination of {read-only decision, read-write
  decision} × {GET, POST, DELETE} × {scope tenant, scope system, denied}. A read-only decision
  plus a mutating request ⇒ **403 and no write**. *Mutations:* default `Mode` to `ModeReadWrite`
  when `SKY_CONSOLE_DATA` is unset → RED; check the mode in the handler rather than in `Decide()`
  → RED (a second call site can then disagree with the funnel).
- **G5.5 — cross-tenant write is unconstructible.** A `ScopeTenant` admin for `T2` attempts to
  write `T1`'s pk, including via an adversarial pk that encodes `T1`'s escaped key prefix.
  Assert: rejected, **and a raw key dump shows no row created anywhere** — not in `T1`, not in
  `T2`, not under a mangled key. *Mutations:* build the write key from the request's pk instead of
  from `Decision.Admin`'s scope → RED; drop the §3.3 escaping → RED (the adversarial pk lands in
  `T1`).
- **G5.6 — audit completeness.** Every accepted write has exactly one log entry carrying both
  images and both versions; a `SIGKILL` between the data write and the audit write leaves
  **neither** (same-transaction property). *Mutations:* write the audit record after the
  transaction commits → RED under the kill arm; omit the before-image → RED, and G5.8 also RED
  (undo has nothing to restore — the two gates are deliberately coupled so the before-image
  cannot be dropped as "just logging").
- **G5.7 (new) — optimistic concurrency / no lost update.** Two admin sessions load the same row;
  A writes; B writes with the stale version. Assert B is rejected with a version conflict, A's
  value survives, and B's UI receives both versions. Run the same interleaving through a *blind*
  full-row `put` to prove the CAS is what rejects it, **not** serializable isolation — without
  that arm the gate is measuring §2 rather than §8. *Mutations:* drop the version from the form →
  RED; compare the version outside the transaction → RED (the check-then-write race reappears);
  use the row's `updatedAt` instead of the engine version → RED (two writes in the same
  millisecond).
- **G5.8 (new) — confirm and undo.** (a) A delete without a confirm token ⇒ 403. (b) A replayed
  confirm token ⇒ 403. (c) A confirm token for a row whose version moved ⇒ 403. (d) A token minted
  for principal X used by principal Y ⇒ 403. (e) An undo within the window restores the exact
  before-image, is audited, and is itself not undoable. (f) An undo of a row edited since ⇒
  rejected. *Mutations:* make the confirm token reusable → (b) RED; drop the version binding →
  (c) RED; drop the principal binding → (d) RED; make undo a blind write rather than a CAS →
  (f) RED.

---

## 9. RULE ZERO — the executable-state implementation

This section is a deliverable. It is the countermeasure to the failure mode the mandate names:
*"a fresh or compacted session inherits CLAIMS; claims survive compaction while the evidence
behind them evaporates."*

> **v2.1 note.** The closing judgement of the gates grill was that, as specified in v2.0, *"the
> harness built to stop green lies is itself the most efficient green-lie generator on the
> branch."* That is correct, and it is the highest-priority defect in the document: every other
> claim below is only as trustworthy as this section. §9.2–§9.4 are rewritten accordingly, and
> the three defects (H1 empty mutations, H2 absent outcomes, H3 an unfalsifiable verifier) are
> each closed by a *mechanism*, not by a rule someone must remember.

### 9.1 The one command

```bash
cargo run -p xtask -- bluedb-gates
```

Runs the fast tier, prints a per-goal roll-up, **regenerates `docs/bluedb/STATUS.md`** listing
**every registered gate including the ones it did not run**, and exits non-zero if any gate FAILs
**or if any goal renders `UNKNOWN`**. Target wall time ≤ 60 s for the fast tier; the full tier
(`--tier=full`: G1.1 arm D, G2.3's crash corpus, G4.3's benches, G2.8's kill arms) runs at phase
boundaries and in CI.

```bash
cargo run -p xtask -- bluedb-gates --only=G2.2        # one gate — NEVER writes STATUS.md
cargo run -p xtask -- bluedb-gates --json             # machine-readable
cargo run -p xtask -- bluedb-gates --check            # verify STATUS.md matches a fresh run
cargo run -p xtask -- bluedb-gates --verify-mutations # apply every recorded mutation, assert RED
cargo run -p xtask -- bluedb-gates --tier=full        # the only invocation that can clear STALE
cargo run -p xtask -- bluedb-gates --bless            # update baselines.json (a reviewable diff)
```

**`--only` never writes `STATUS.md`.** v2.0 did not say so, and the omission is a green-lie
generator in its own right: a developer debugging one gate would regenerate `STATUS.md` from a
run of that gate alone, and every other gate would render as… whatever the schema did with
absent outcomes. Under §9.3 that is now `NOT RUN`, but the cleaner rule is that a partial run may
not author the status file at all.

### 9.2 The registry

`rust/crates/xtask/src/bluedb_gates.rs`, following the existing gate idiom
(`coerce_floor_gate.rs`, `s8_gate.rs`):

There is **no existing gate-registry idiom to reuse**. `coerce_floor_gate.rs` and `s8_gate.rs` are
single-purpose gates with hard-coded bodies; neither enumerates anything. P0 therefore *builds* a
registry — the tiering, the `STATUS.md` renderer, the scratch-worktree mutation runner, the
canary, and the static checks. §10's P0 scope is sized accordingly; v2.0's "reuse the idiom"
understated it.

```rust
pub struct Gate {
    pub id:        &'static str,   // "G2.2"
    pub goal:      u8,             // 0 = cross-cutting, 1..5 = the numbered goal
    pub title:     &'static str,
    pub tier:      Tier,           // Fast | Full
    pub run:       fn(&Ctx) -> GateOutcome,
    pub budget_s:  u64,            // hard timeout; exceeding it is a FAIL, not a hang
    pub mutations: Mutations,      // NOT a slice — see below
}

/// H1: a plain `&'static [Mutation]` accepts `&[]`, and an empty slice iterates
/// zero elements and "succeeds". Twelve of v2.0's twenty-six gates declared no
/// mutation at all, so twelve gates would have been PROVEN-by-vacuum: the
/// verifier loops over nothing, finds no failure, and the gate reports green.
/// The harness built to stop green lies would have manufactured twelve of them.
///
/// The type makes the empty case unrepresentable rather than merely forbidden.
pub struct Mutations(&'static [Mutation]);   // field private to this module

impl Mutations {
    /// The ONLY constructor. Panics at registry-construction time (i.e. at the
    /// start of every run, including in CI) if the slice is empty.
    pub const fn new(m: &'static [Mutation]) -> Mutations {
        assert!(!m.is_empty(), "every gate must declare at least one mutation");
        Mutations(m)
    }
}

pub struct Mutation {
    pub id:      &'static str,     // "G2.2/force-full-scan"
    pub patch:   &'static str,     // docs/bluedb/mutations/G2.2.force-full-scan.patch
    pub expect:  &'static str,     // which assertion must go RED, verbatim
    /// Paths the patch touches. Used by MAJOR-17's UNVERIFIED-SINCE check to
    /// decide whether a recorded PROVEN is still meaningful.
    pub targets: &'static [&'static str],
}
```

The registry is the single source of truth. A gate that exists in code but not in the registry
does not count; a registry entry with no `run` does not compile; a registry entry with no
mutation does not *construct*.

A belt-and-braces **runtime** check backs the type-level one (§9.6 check 3), because a future
refactor could reintroduce a permissive constructor: any gate whose `mutations` is empty renders
**`UNPROVEN`**, and `UNPROVEN` makes its goal **FAIL**. `UNPROVEN` is deliberately a distinct
state from `VACUOUS` — "nobody ever tried to falsify this" and "someone tried and it could not
fail" are different diagnoses and want different fixes.

### 9.3 `STATUS.md` is generated output

**H2 — the defect this schema exists to close.** In v2.0, plain `bluedb-gates` ran the **fast**
tier; G1.1, G2.3 and G4.3 were `--tier=full`; §9.5 told a fresh session to run *the plain one*;
and §9.3's schema had only PASS and FAIL with **no rule for an absent outcome**. So the default
command — the one command the mandate points every fresh session at — would print a green
roll-up while the hardest gates in the design had never executed. A green lie generated by the
anti-green-lie harness, on the exact path designed to be trusted.

Four states, not two. `PASS` is the only one that is not a failure:

| State | Meaning | Effect on the goal |
|---|---|---|
| `PASS` | ran, all assertions held | contributes PASS |
| `FAIL` | ran, an assertion broke, or `budget_s` exceeded | goal **FAIL** |
| **`NOT RUN`** | registered but not executed in this run (wrong tier, `--only`, harness error) | goal **`UNKNOWN`** |
| **`UNPROVEN`** | no mutation declared (§9.2) | goal **FAIL** |

and a goal's verdict is computed by a total function over its gates, with `UNKNOWN` **never
collapsing to PASS**:

```
FAIL     if any gate FAIL or UNPROVEN          (a broken or unfalsifiable gate is decisive)
UNKNOWN  else if any gate NOT RUN              (absence of evidence is NOT evidence)
PASS     else                                  (every registered gate ran and passed)
```

```markdown
<!-- GENERATED by `cargo run -p xtask -- bluedb-gates`. DO NOT EDIT. -->
<!-- commit:      eac3e8d2  ran: 2026-08-09T14:02:11Z  host: darwin/arm64  tier: fast -->
<!-- full-tier:   9ece376c  ran: 2026-08-08T22:10:04Z  host: linux/amd64   STALE (HEAD is 7 commits ahead) -->

# BlueDB v2 — STATUS

⚠️  FULL-TIER RESULTS ARE STALE — 7 commits behind HEAD. Goals 1, 2, 4 render UNKNOWN.
    Run: cargo run -p xtask -- bluedb-gates --tier=full

| Goal | Verdict | Gates |
|---|---|---|
| 1 — session-bounded Model state sync | **UNKNOWN** | G1.1 G1.2 G1.3 G1.4 G1.5 G1.6 G1.7 |
| 2 — unified store, real SERIALIZABLE  | **FAIL**    | G2.1 G2.2 ✗G2.3 … |
| 5 — console admin access (read+write) | **FAIL**    | G5.1 G5.2 G5.3 ⊘G5.4 ⊘G5.5 ⊘G5.6 ⊘G5.7 ⊘G5.8 |
| …

Legend: (blank)=PASS  ✗=FAIL  ⊘=NOT RUN  ⊗=UNPROVEN

| Gate | Goal | Title | Verdict | Tier | Time | Mutation proof |
|---|---|---|---|---|---|---|
| G1.1 | 1 | ceiling holds                        | PASS      | fast | 8.1s  | PROVEN @ eac3e8d2 |
| G1.1d| 1 | capacity report (arm D)              | **NOT RUN** | full | —   | UNVERIFIED-SINCE 9ece376c |
| G2.3 | 2 | index↔data consistency under crash   | **FAIL**  | full | 41.2s | PROVEN @ 9b1f0ac |
| G5.4 | 5 | write authorization matrix           | **NOT RUN** | fast | —   | — |
| G0.C | 0 | CANARY (must report VACUOUS)         | PASS      | fast | 0.2s  | VACUOUS ✔ |
| …

## Failures
### G2.3 — index↔data consistency under crash
    orphan index entry: coll=todos idx=2 pk=t-8842 (crash seed 17)
    runtime-go/bluedb/committer.go:214

<!-- body-sha256: 3f2a…  -->
```

Five properties make it trustworthy:

1. **Every registered gate appears**, whether or not it ran. The table is rendered from the
   **registry**, not from the run's results — so a gate cannot disappear by not executing. This
   is the single mechanical change that closes H2.
2. **A goal's verdict is computed** by the total function above. No prose verdict exists anywhere,
   and `UNKNOWN` is unreachable from a partial run because the renderer sees the whole registry.
3. **Hand edits are detected.** The trailing `body-sha256` covers the generated body; `--check`
   recomputes it and fails with *"STATUS.md is generated output; run
   `cargo run -p xtask -- bluedb-gates`"*. `--check` runs in CI and in a pre-commit hook.
4. **Two staleness clocks, tracked separately.** The header records the fast-tier commit **and**
   the last **full-tier** commit. `--check` fails if `HEAD` moved past the fast-tier commit; and
   whenever `HEAD` is ahead of the full-tier commit, every full-tier gate renders `NOT RUN`,
   the banner says so, and the affected goals render `UNKNOWN`. Only `--tier=full` clears it.
   One clock would have let a fast run "refresh" a status whose hard gates were weeks old.
5. **`--only` cannot author this file** (§9.1).

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
2. `git apply`s the patch **in the worktree**,
3. runs **only** that gate — **built from, and executed against, the worktree**,
4. asserts a non-zero exit **and** that the recorded assertion string appears in the output,
5. discards the worktree,
6. records `PROVEN @ <sha>` in `STATUS.md`.

Step 3's emphasis is the whole of H3. If the runner applies the patch in the scratch worktree but
builds or runs the gate against the **developer's** tree — a one-line mistake, e.g. an absolute
`CARGO_TARGET_DIR`, an inherited `cwd`, or a Go build that resolves `runtime-go/` from the repo
root — then the mutated code never executes, every gate stays green under mutation, and
**every mutation reports `PROVEN` forever**. The verifier that certifies every other gate would
itself be unfalsifiable, and nothing in v2.0 could have detected it.

**The canary makes the verifier falsifiable.** A permanent pair lives in the registry:

```rust
Gate {
    id: "G0.C", goal: 0, tier: Tier::Fast,
    title: "CANARY — deliberately vacuous; --verify-mutations MUST report VACUOUS",
    run: |_| GateOutcome::pass_if(true),        // asserts nothing. Cannot fail.
    mutations: Mutations::new(&[Mutation {
        id: "G0.C/noop", patch: "docs/bluedb/mutations/G0.C.noop.patch",
        expect: "<never>", targets: &["docs/bluedb/mutations/CANARY_TOUCHED"],
    }]),
    budget_s: 30,
}
```

`G0.C` asserts `true`; the paired patch is a no-op. A correct verifier reports **`VACUOUS`** for
it. Therefore:

> **`--verify-mutations` FAILS if `G0.C` reports anything other than `VACUOUS`** — and in
> particular it fails if `G0.C` reports `PROVEN`, because a gate that asserts `true` cannot have
> gone red, so a `PROVEN` verdict proves the runner is not measuring what it claims.

The canary is the one place in the design where a **passing** gate is the failure signal, and
that inversion is deliberate: it is the only construction that can catch a verifier whose every
answer is "green". The patch also touches a sentinel path so a second arm can assert the worktree
was actually modified and then discarded.

Failure modes and what they mean:

| Outcome | `STATUS.md` | Meaning |
|---|---|---|
| patch applies, gate goes RED with the expected string | `PROVEN @ <sha>` | the gate can fail |
| patch applies, gate stays GREEN | `VACUOUS` → **overall FAIL** | the gate is a green lie |
| patch no longer applies | `MUTATION-STALE` → **overall FAIL** | code moved; re-derive the proof |
| gate declares no mutation | `UNPROVEN` → **overall FAIL** | nobody ever tried to falsify it (H1) |
| **`G0.C` reports anything but `VACUOUS`** | **harness FAIL** | the verifier is not verifying (H3) |

`MUTATION-STALE` as a *failure* is the anti-rot mechanism: a refactor that invalidates a proof
cannot silently leave a gate un-proven.

**`PROVEN @ <sha>` decays — `UNVERIFIED-SINCE` (MAJOR-17).** `--verify-mutations` is expensive
(a worktree, a build and a gate run per mutation) and therefore does **not** run in the default
command. So a `PROVEN @ <sha>` recorded weeks ago sits in `STATUS.md` looking like current
evidence while the code it certified has moved underneath it — the same "claims survive,
evidence evaporates" failure the mandate names, reproduced inside the countermeasure.

The default command cannot re-run the mutations, but it *can* cheaply detect that the proof may
no longer hold. For each mutation it diffs the mutation's declared `targets` between the recorded
sha and `HEAD`. If any target changed, the cell renders **`UNVERIFIED-SINCE <sha>`**, which is a
**non-PASS** state: the gate itself may still be PASS, but its *proof* is no longer known-good,
and the goal renders `UNKNOWN` exactly as it does for `NOT RUN`. It is not `FAIL` — the proof is
not known to be broken, only unrevalidated — and the distinction matters, because conflating
"unknown" with "broken" trains people to ignore the signal.

`targets` is what makes this cheap and precise, and it is why `Mutation` carries the field: a
whole-tree "has anything changed" check would mark everything unverified after every commit, and
a signal that always fires is a signal nobody reads.

**G0.6** is `--verify-mutations` itself, run at every phase boundary and in the nightly sweep.
**G0.7** is the harness's self-integrity check (§9.6).

### 9.5 What a fresh session does

`docs/bluedb/RESUME.md` on this branch is short by design, and its content is an instruction, not
a status:

```markdown
# BlueDB v2 — RESUME
1. Read .claude/AUTONOMOUS_GOAL.md (the mandate).
2. Read docs/bluedb/v2-architecture.md (this design).
3. Run: cargo run -p xtask -- bluedb-gates --tier=full
   Its output IS the state. docs/bluedb/STATUS.md is that output, committed.
   The FAST tier alone leaves the hardest gates NOT RUN, which renders their
   goals UNKNOWN — never PASS. If you only have time for the fast tier, read
   the UNKNOWNs as "not yet known", not as "fine".
4. A goal is closed only when its row says PASS with zero ⊘ and zero ⊗.
5. Do not trust any prose in any doc about what is done. Run the gates.
```

Step 3 changed in v2.1: v2.0 told a fresh session to run the **plain** command, which runs the
fast tier only — the instruction that turned H2 from a schema gap into an operational lie.

No phase table with ✅ marks exists anywhere on this branch. The prior branch's phase table is
precisely the artefact that survived compaction while the evidence behind it evaporated.

### 9.6 Static checks — the harness auditing itself (G0.7)

Every gate declares its `goal` in the registry. **Three** static checks run inside
`bluedb-gates` itself, before any gate executes:

1. Every goal 1–5 has **at least one** gate (a goal with no gate is a FAIL, not an omission).
2. Every gate maps to exactly one goal, or is explicitly `goal = 0` (cross-cutting).
3. **Every gate declares at least one mutation.** Zero ⇒ that gate is `UNPROVEN` ⇒ its goal
   **FAILs**. This is H1's runtime backstop behind §9.2's type-level `Mutations::new` assertion —
   two independent mechanisms, because this is the check whose absence would have manufactured
   twelve green lies.

**G0.7** additionally verifies this document against the code, since a design doc that cites
line numbers is a rot surface (see the Citation provenance block — v2.0, both grillers, and this
amendment all produced stale citations): every `file:line` cited in
`docs/bluedb/v2-architecture.md` is parsed together with its branch tag, resolved with
`git show <branch>:<path>`, and asserted to still contain the quoted token. A moved line is a
**warning**; a *missing* token is a **FAIL**. *Mutations:* delete check 3 and register a
mutation-less gate → RED; alter a cited token in `[bdb]` → G0.7 RED.

So "which gate proves goal #4?" and "is this document still true?" are both answered by the tool,
not by reading.

### 9.7 The mutations v2.0 never wrote (H1)

Twelve of v2.0's gates declared no mutation and would have been `UNPROVEN`. Authoring them is
part of this amendment, not of implementation — a mutation that cannot be described in the design
is usually a sign the gate has no falsifiable content. Each is now specified with its gate above;
collected here as the P0 checklist:

| Gate | Falsifying mutation | Expected RED assertion |
|---|---|---|
| G0.1 | hand-edit `STATUS.md`'s body without regenerating | `--check` reports the `body-sha256` mismatch |
| G0.2 | add `import "sky-app/bluedb"` to an `rt` file | the layering scan reports the edge `rt → bluedb` |
| G0.3 | import `persistglue` unconditionally from the runtime prelude | `go tool nm` finds pebble symbols in a non-Persist app |
| G0.4 | add a `[data]` key with no runtime reader / with glue bytes but no behaviour | the reader scan / the behavioural probe reports the dead key |
| G0.5 | add a second `go build` site without `-tags pebblegozstd` | the sole-site check reports two sites |
| G0.6 | corrupt one recorded `.expected.txt` | the expected-string assertion fails for that mutation |
| G0.7 | register a gate with `Mutations::new(&[])` | construction panics; and with the panic removed, check 3 reports `UNPROVEN` |
| **G0.C** | *(the canary — a no-op patch)* | **`VACUOUS`**, and `PROVEN` is a harness FAIL |
| G2.6 | disable one errorfs injection point in the crash corpus | the corpus reports fewer injection sites than the recorded manifest |
| G3.1 | remove the zero-config default | the app requires a `[data]` section and no longer builds as documented |
| G3.2 | break a Sky example in `docs/skypersist/`; and move one outside the gated tree | `doc-examples.sh` fails; the coverage count drops |
| G3.3 | add a driver-conditional branch to the app source | the identical-source assertion fails |
| G4.1 | drop the changeset emit on the sqlite path | no delivery on sqlite while embedded still passes |
| G4.2 | remove the tenant from the subscription bucket key | a T1 commit wakes a T2 subscriber |
| G4.3 | replace delta application with a full re-query | the fan-out cost exceeds the committed baseline (**and** G4.7 goes RED — the mechanical proof, since a baseline alone is not one) |
| G5.2 | scope the admin read with a `WHERE tenant = ?` residual instead of the key range | the adversarial rows appear |
| G5.3 | turn `adminShow` into a deny-list | the `stripe_sk` fixture column renders |
| G5.4 | default `Mode` to `ModeReadWrite` when `SKY_CONSOLE_DATA` is unset | a POST succeeds under a read-only decision |
| G5.5 | build the write key from the request pk rather than `Decision.Admin`'s scope | a row appears under `T1` |
| G5.6 | write the audit record after the transaction commits | the `SIGKILL` arm finds a write with no audit entry |

---

## 10. Phase plan

Each phase is independently verifiable and shippable, and runs the full cycle:
**decide scope → design → grill (≥2 fresh-context adversaries) → implement (worktree) →
three-leg verify (unit `-race` + integration + a REAL app) → fresh-context Judge.** Only a Judge
closes a phase. Every agent brief opens with *"confirm `git log --oneline -1` equals `<base>`;
reset if not"* — 8 of 8 worktrees in the prior session were created off `main` instead of the
branch tip.

### 10.0 The re-slice — why the v2.0 ordering was the biggest delivery risk

Both grillers rated third-abandonment risk **HIGH**, for the same structural reason: under v2.0,
**nothing user-visible shipped until P4 — the fifth phase** — with a realistic clean-run estimate
of 20–25 sessions assuming zero rework. Both prior attempts died *shorter than that*, on this same
bottom-up shape: substrate, then keyspace, then isolation, then finally an API someone can run.
A plan whose first demonstrable artefact is five phases away is a plan that gets abandoned before
its first artefact, and the correct response is not more discipline — it is a different slicing.

Six changes, in the order the grillers prioritised them:

1. **Thin-slice, don't stack.** A **minimal `Std.Persist`** — `collection` / `key` / `get` / `put`
   / `query` / `toList`, embedded only, zero-config, **no** tenancy, indexes, reactivity,
   migrations or `[data]` parsing — moves into **P2**. The §7.6 todo app runs and persists at
   **phase 2**, and every later phase hardens something that already works instead of extending
   something nobody has run. This also dissolves **G-B9** (P3's G2.1 and G2.4 are specified
   *through* `Persist`, which under v2.0 did not exist until P4 — P3 literally depended on P4).
2. **Runtime poison flag instead of the HIR walk** (G-B7 / D2) — removes the compiler change from
   P3, which was its largest and least predictable item.
3. **Cut the `redis` `ChangeBus`** (D3) — `local` + `postgres` fully serve goal #4.
4. **Cut `Std.Persist.Sql` + its build-time static-reference check** (D4) — raw SQL stays
   `Std.Db`, with a startup fatal on embedded.
5. **Cut the `[live]`/`[analytics]` subsumption + deprecation mapping** to post-P8 (D5).
6. **Restructure G1.1** (D6 / G-B8) — the correctness arm runs at N ≈ 200 in seconds.

Net effect on the critical path: a runnable, persisting Sky app at the end of **P2**; goal #2's
discriminating proof no longer blocked on an unbuilt API; and roughly a phase of scope removed
from P3 and P4 combined.

### 10.1 The phases

| Phase | Scope | Gates | Reused (from) | Net-new |
|---|---|---|---|---|
| **P0 — Rule Zero first** | The gate harness before there is anything to hide: `xtask bluedb-gates`, **the registry itself** (there is no registry idiom on `[main]` to reuse — `coerce_floor_gate.rs` / `s8_gate.rs` are single-purpose gates with hard-coded bodies, so P0 builds tiering, the `STATUS.md` renderer, the four-state model, the two staleness clocks, the scratch-worktree mutation runner, `UNVERIFIED-SINCE`, and the three static checks). **The canary `G0.C` ships in P0 or the harness is not trusted.** Plus the cross-cutting gates, implementable with zero BlueDB code. **Record the goal-#5 ruling** (§8.1 — answered, so P0 only writes it down). Fix `AGENTS.md:258` (P5) and the `docs/sky-toml.md:202` / `docs/skydb/overview.md:558` `driver` rows (P3). | G0.1 G0.2 G0.3 G0.4 G0.5 G0.6 G0.7 **G0.C** | nothing but the `xtask` crate scaffold | all |
| **P1 — Substrate port** | `runtime-go/bluedb/`: keys, comparer (`skydb.mvcc.v1`, `base.CheckComparer`), HLC + restart floor, single-writer committer + **group commit**, changelog, watermark + GC, readset, validate, txn, errorfs crash corpus, **and `backend.go`** — v2.0 omitted it, which deleted `uniqUserKey`, the only working uniqueness enforcement on either branch (G-B4 / P12). **`scanMaterialize` is NOT ported** (P13). Layering enforced from day one. | G2.6 G2.9 | `[bdb]` @ `5c1beb69`: `bluedb/{keys,comparer,hlc,committer,changelog,changefeed,watermark,gc,readset,validate,engine,pebble_engine,reader,backend,crashsim_test}.go` | the `persistglue` seam; pebble in `go.mod` |
| **P2 — Tenancy + index keyspace + MINIMAL Persist** | *(Re-sliced — D1.)* User-key layout (§3.2c), escaping + null tags + float/time total order (§3.3), `SkyType` threading (§3.3b), the `0x02` index and `0x03` unique namespaces, the planner (§3.4), **`coordBounds`/`seekBounds`** (§3.4a), seek-backed `ScanRange`, index + unique maintenance in `buildReq` (§3.5), tenant in the conflict domain (§3.2a), tombstone reclamation (§3.5a), changelog payload versioning (§3.3a), `Persist.explain`, `ScanStats`. Delete `checkCompositeLayout`; remove the raw-prefix `Iterate` (§5.2). Invert `TestReactive_TenantNeverDurable`. **Plus the minimal `Std.Persist` + `persistglue` + a hardcoded embedded default — so the §7.6 app runs at the end of this phase.** | G2.2 G2.3 G2.5 G2.7 G2.10 G2.11 G2.12 | `index_key.go`'s encoder + `readset.go`/`validate.go`'s range contract + `backend.go`'s unique mechanism (`[bdb]`) | index + unique storage, planner, bound separation, tenancy-in-key-and-conflict-domain, escaping, `SkyType` dispatch, `ScanStats`, minimal Persist |
| **P3 — One isolation contract** | Closed driver registry + `IsolationStrategy` (§2.4) with the pinned writer handle and the two conflict classifiers; sqlite split pool + `_txlock=immediate` writer DSN + `synchronous`/WAL-checkpoint policy; postgres `SERIALIZABLE` + typed `40001`/`40P01` retry; embedded SSI wiring; startup self-test; the discriminating conformance suite on all three (§2.5), driven through **P2's** Persist; the **runtime poison flag** + spawn-seam propagation; `Persist.Test.barrier`. **No compiler change in this phase.** | G2.1 G2.4 | the retry/backoff shape + typed-`PgError` classification (`[bdb]` `db_auth.go`, the `dbSerializableTxAttempt` region) | split pool, registry, self-test, the anomaly corpus, the poison flag |
| **P4 — Full Persist API + `[data]` + migrations** | The rest of §7.3 (`transact`, `insert`, `count`, `orderAsc/Desc`, `limit/offset`, `asTenant`/`asSystem`, `liveInto`); `[data]` parsing (first-wins) subsuming `[database]` **only** (D5); the generated `sky_data.go`; `sky data` verbs; `reindex` + the drain-to-`T` barrier; the §5.4 generation-stamped migration; `docs/skypersist/` incl. the flagship app; `sky doc`. Raw SQL stays `Std.Db` + the startup fatal (D4). | G3.1 G3.2 G3.3 G3.4 G2.8 | `Std/Persist.sky`'s `Cond`/`Query`/builder shape + `guardIdents` (`[bdb]`); migration machinery (`[main]`) | glue emission, `[data]`, one-Conn API, dual-read migration |
| **P5 — Session-bounded Model state** | `_sky_sessions` collection + opaque-blob envelope + **compiler-computed `sessionVersion`** (§4.2a); `chooseStore` `case "data"`, default **only for Persist apps** (§4.2); the **stated lock order** (§4.0); byte + count accounting in the funnel; deflation + handler-id determinism + inflate-failure policy (§4.4a); provisional admission; coalescing outbox; connection admission bound; gauges. Invert `TestTiered_SSEConnectedNeverEvicted`; wire `idleEvict` into `sky.toml`. | G1.1 G1.2 G1.3 G1.4 G1.5 G1.6 G1.7 | the persist-before-ack funnel `persistAndShipFrame` + the AST-dominance tripwire (`[bdb]` **`9ad00daf`** — *not* `e1f6eaf2`, which exists only on `feat/bluedb-backup-prerebase`; and `947cd114`); the 5c envelope | the cache, deflation, admission, accounting, lock order, metrics |
| **P6 — Reactivity at scale** | Changeset derived from a **durable artefact written inside the committing transaction** (§6.2a); `Changeset.Tenant` decoded from the row key; delta application (not re-query); the resync consumer; `ChangeBus` **local + postgres** (no redis, D3) + `_sky_changelog` gap recovery; startup-time capability assertions replacing the first-session `os.Exit`; the fan-out bench baseline. | G4.1 G4.2 G4.3 G4.4 G4.5 G4.6 G4.7 G4.8 | the changefeed + the shared-predicate `matchCache` + the Enter/Leave/Stay truth table (`[bdb]` `reactive.go`) | commit-atomic emit, SQL-backend changesets, `ChangeBus`, the resync consumer, delta application |
| **P7 — Console admin READ (5e-1)** | `consoledata` package, `Decide()`, the registry write-once/deep-copy fix, the Data tab, audit logging. Delete `isLoopbackRemoteAddr`. Fix the console's self-fetch 401. | G5.1 G5.2 G5.3 | `[p5e]` @ `de3e7431` (registry write-once + deep copy, `SchemaOf`, `adminShow`/`TenantCol`, `embedded_admin_test.go` mutation proofs); `phase5e-closure-design-v2.md` v2.1 (authorization design); `[exp]` `console_data_sql.go` (default-deny allow-list, separate capped read-only pool, constructed SELECTs, caps + timeout, opaque DSN handle, audit) | the Data tab UI, durable-tenancy scoping, `Decide()`'s no-argument shape, SQL-backend enumeration, `adminShow` replacing the deny-list |
| **P8 — Console admin WRITE (5e-2)** | **REQUIRED — not conditional.** §8.4: `ModeReadWrite` in the funnel, writes under the engine-attested tenant, `SkyType`-derived scalar forms, optimistic CAS concurrency, same-transaction audit with both images, confirm tokens, undo. Goal #5 is `No` until these are green (§8.1). | G5.4 G5.5 G5.6 G5.7 G5.8 | `[p5e]`'s funnel (extended with `Mode`) | the write path, CAS, confirm/undo, the audit trail |
| **P9 — Whole-goal close** | **Full-tier** gate run, `--verify-mutations` (incl. the canary), example sweep, `verify-cli`, `verify-all-web`, conformance, cross-compile; fresh-context Judge against the verbatim five goals, with goal #5 read **and** write. | all | — | — |

**Ordering rationale.** P0 first is not ceremony: the prior attempt's three false-green gates
were all authored *after* the code they guarded, by the same context that wrote the code. Building
the harness while there is nothing to certify removes that coupling — and under v2.1 the harness
must also be able to catch *itself* (G0.C), which is only cheap to build before there is anything
it would be embarrassing to fail. P1 before P2 because the comparer is irreversible. P2 carries
the minimal API because a design nobody can run is a design nobody can grill (D1). P2 before P3
because A-PH cannot be written until seeks exist, **and** because G2.1's plan-shape precondition
(E-B3(b)) needs `explain` to exist. P4 after P3 because the full API's `transact` is the isolation
contract. P5 after P4 because sessions-as-collection needs migrations and `[data]`. P6 after P5
because the funnel is the reactive apply point. P7 after P2/P5 because its security property *is*
§5's key scoping. **P8 after P7** because writes reuse P7's funnel unchanged.

**Merge dependency.** `fix/skylive-runtime-soundness` must be merged before P5 completes
(§5.5 / G2.5.5): the `handleEvent` session hijack can hand the engine the wrong tenant, and no
gate inside `bluedb` can see it. The reactive gate's first-session `os.Exit` is the one item of
that set that lands **here** (§6.5) — verified BlueDB-only and absent from `main`.

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
| F5 | **Per-connection RAM is linear in connected clients.** | One goroutine, one socket, one coalesced frame per client. §4 bounds session *state*; it cannot make the per-client term zero. What it **can** do — and v2.0 did not — is bound the client **count**: `[data] maxLiveConnections` (§4.7 arm C) refuses past the limit with 503 + `Retry-After`, so the linear term has a ceiling even though its slope is irreducible. `perConnFloor` is a committed constant in `baselines.json`, measured and published (§4.7 arm D), not folded into "bounded". |
| F6 | **No compile-time backend-capability typing.** | Sky is HM with no type classes and no HKT, and the backend is a runtime value injected at boot. A type cannot depend on it. §2.6 substitutes build-time static-reference checking + a boot self-test. |
| F7 | **Sticky sessions remain required.** | A session's Model has one owner under one mutex. Cross-instance frame fan-out does not fix a split Model. The `sky_sid` affinity requirement is unchanged by anything here. |
| F8 | **`rt.Coerce` at the wire boundary.** | Decoding a persisted row into a typed Sky record is the existing §8 "wire decode" floor category. BlueDB does not widen it and does not remove it. |
| F9 | **Text index order is byte order, not collation.** | Locale-aware collation would require ICU (a cgo dependency) or a large table. Byte order is documented; a case-insensitive index is achieved by indexing a derived normalised column. |

### 11.2 Deliberately bounded in v2

**This is the only place bounds live.** §1's table is bare Yes/No precisely so that a bound
cannot be smuggled into a verdict cell (v2.0's "Yes, with a stated degradation ladder" is the
shape a PASS verdict must never take).

| # | Bound | Escape |
|---|---|---|
| B1 | No index seek on `Decimal` / `Money` / `Bytes` — **a build error**, not a silent full scan. **Escalated to §11.4** — `Std.Money` is `AGENTS.md`'s pinned currency default, so this is a product decision, not a technical footnote | index a derived integer minor-unit column; or accept it as a residual predicate |
| B2 | No covering indexes — a seek yields pks, then point gets | the point gets share the block cache; covering indexes are a later cycle |
| B3 | `CondOr` / `CondNot` do not produce spans (except single-column `CondIn`), so an `or_` is a **residual full scan** — the common "status is a *or* b written with `or_`" is silently linear unless written as `inList` | `explain` shows it; `Persist.Test.assertNoFullScan` fails an app's own suite; `inList` is decomposed into a multi-span union |
| B4 | Cross-replica delivery is at-least-once, not exactly-once | application is pk-keyed and idempotent |
| B5 | `NOTIFY` carries a summary, not the row body | subscribers read `_sky_changelog` for the gap |
| B6 | Console admin writes are **scalar-only** (relations, enums, nested records render read-only). Writes themselves are **required**, not optional (§8.1) | edit relations through the app; a typed admin form is a later cycle |
| B7 | The session Model is an opaque blob, not a typed collection | deliberate (§4.2) — it dissolves P19 rather than assuming it away; `sessionVersion` is compiler-computed (§4.2a) so the blob cannot silently mis-decode |
| **B8** | **The "unified API" is unified by SUBSETTING** — no joins, no aggregates, no `GROUP BY`, no window functions, no subqueries. This is *how* one API spans three backends, and goal #2's "UNIFIED APIs shareable across dbs" is satisfied at that subset, not at SQL's full expressiveness | `Std.Db` raw SQL on a SQL driver; the startup fatal on embedded (§2.7) names it |
| **B9** | **Raw `Std.Db` writes bypass Persist**, so they emit no changeset and drive no reactivity | `Persist.touch` nudges subscribers after a raw write; or route the write through `Persist` |
| **B10** | `[data]` does **not** unify the job store (`[main]` `rt/jobs/sqlite_store.go:86`) or the exporter spool (`rt/exporter_spool.go:462`) — a Persist app can still open three SQLite files | scheduled post-P8; both already work and neither is on a goal's critical path |
| **B11** | `redis` is not a `ChangeBus` backend in v2 (D3) | `local` covers single-instance, `postgres` covers multi-replica — and F2 already forces `driver = postgres` for multi-replica, so no topology is uncovered |
| **B12** | `[live]` / `[analytics]` are **not** subsumed by `[data]` in v2 (D5) | they keep working exactly as on `[main]`; subsumption is post-P8 |
| **B13** | The transact-replayability check is **dynamic**, so a rarely-taken non-replayable branch ships green and fails on first execution (§7.3) | the failure is a clean typed abort, never a double effect; a lint-level HIR warning for the literal-lambda case is a later cycle |
| **B14** | During a §5.4 key migration, reads in the un-migrated range cost **two seeks** | bounded, self-limiting, and exported as `sky_persist_migration_progress`; correctness is unaffected (dual-read) |

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
| R13 | **The harness is the highest-value attack surface on this branch.** The gates grill's closing judgement was that v2.0's harness was "the most efficient green-lie generator on the branch" — twelve gates unprovable by construction (H1), a default command that reported PASS while the hardest gates never ran (H2), and a mutation verifier that could not be caught not verifying (H3). | was **certain** under v2.0 | §9 rewritten: `Mutations::new` makes the empty case unrepresentable; `STATUS.md` renders from the **registry** so a gate cannot vanish by not running; four outcome states with `UNKNOWN` never collapsing to PASS; two staleness clocks; and **G0.C**, the canary whose *passing* is the failure signal. A griller should attack §9 before anything else — everything else in this document is only as true as §9 is. |
| R14 | **47 gates is a large surface** (v2.0 had 26), and the fast tier must stay ≤ 60 s or nobody runs it. Every addition is traceable to a griller finding, but a suite nobody can afford to run is a suite that renders `UNKNOWN` forever. | high | Tiering is per-gate with a hard `budget_s`; the correctness arms are deliberately small (G1.1 at N ≈ 200, not 50 k); capacity and crash work is `--tier=full`. If the fast tier exceeds its budget, the fix is to move an arm to `full` **and accept the `UNKNOWN`** it produces — never to delete an assertion. |
| R15 | **P2 now carries both the keyspace and a minimal API** (D1), so it is the widest phase. | medium | Deliberate: it is also the first phase that produces something runnable, which is what makes P3–P9 verifiable against a real app rather than a fixture. The API half is genuinely minimal (six verbs, embedded only, no config) and is additive to the keyspace half, so it can slip a sub-milestone without blocking P3's start. |
| R11 | **Sky.Live runtime bugs land separately** (`handleEvent` session hijack, `sendBeacon` CSRF 403, the reactive gate's first-session `os.Exit`, `live.go`'s implicit lock contract) and this design touches adjacent code. | high | Out of scope here per the mandate; shipping off `main` on `fix/skylive-runtime-soundness`. P5 and P6 touch `live.go` and must rebase onto that work rather than re-fixing it. The one overlap this design *does* claim is the reactive gate's `os.Exit`, which §6.5 replaces with a startup check — coordinate, do not duplicate. |
| R12 | **`Persist.conn` is a memoised zero-arg binding (a CAF).** | low | Correct here — it is a shared pool handle, not a fresh value — but it is exactly the shape the compiler warns about for `Uuid`/`Random`/`Time`. Document it in `docs/skypersist/` so it is not "fixed" by a later reader. |

### 11.4 ⚠️ Needs the user's attention — product decisions, not technical ones

Two items are surfaced here rather than parked as bounds, because they change what the product
*is* and the design should not decide them silently.

| # | Decision | Why it is the user's, not the design's |
|---|---|---|
| **U1** | ✅ **RULED 2026-08-13 — fund the order-preserving encoding. B1 is retired.** | The user's ruling: `Money`/`Decimal` become indexable, via **two columns and each backend's native numeric type**, not a JSON/text blob. See §11.4a below for the full decision and its evidence. |
| **U2** | **The throughput floors of §2.6** — embedded ≥ 2 000 serializable commits/s, sqlite ≥ 500, postgres ≥ 2 000 at 8 writers. | These are hand-committed absolute minima, and they define what goal #2's verbatim *"high-throughput"* means. They are a product promise, not a measurement: seeding them from the first run is the G-B10 anti-pattern, and inventing them without review means the design graded its own homework. Confirm, raise, or lower them before P3 commits `baselines.json`. |

Goal #5's read-vs-write question, which v2.0 correctly listed here, is **answered** (read **and**
write, §8.1) and is no longer open.

### 11.4a U1 — RULED: `Money`/`Decimal` are indexable (2026-08-13)

**The decision.** A `Money` field maps to **two columns** — currency and amount — and the amount
uses each backend's native exact numeric type. Not a single JSON/text blob: a blob is opaque to
every backend's indexer, so it would guarantee full scans *everywhere and permanently*, and it
throws away the one backend that already solves this properly.

| Backend | Amount column | Ordering / index |
|---|---|---|
| PostgreSQL | `NUMERIC(p,s)` | native, exact, correct |
| SQLite | `INTEGER` minor units | native, exact, correct |
| BlueDB | canonical value in the row body | key `(currency, scaled int)` — sign-biased BE, exactly what `ColInt` already does |
| Redis | canonical string member | same BE bytes for `ZRANGEBYLEX` |
| Parquet / Arrow / DuckDB / ClickHouse / BigQuery | `DECIMAL(p,s)` | 1:1 lossless export |

**Why this is cheap rather than a new subsystem.** `ColMoney` is *already a declared column type*
(`[bdb]` `index_key.go:20`); it is merely routed to the non-order-preserving fallback that resolves
via a residual predicate (`:87`). The work is moving it onto the treatment `ColInt` already gets —
`out[0] ^= 0x80` sign-bias over big-endian bytes (`[bdb]` `index_key.go:70-77`) — inside the same
`SkyType`-keyed dispatch §3.3 already defines for `Int`, `Time` and `Float`. It does **not** touch
the comparer, which is the irreversible artefact. On Postgres and SQLite it needs no custom encoder
at all: it uses machinery that is already correct.

**Declared scale is not a BlueDB wart — it is the price of interop.** Parquet sizes its physical
encoding from precision (int32 / int64 / fixed-length byte array, two's-complement big-endian);
Arrow carries precision+scale+bit-width in the schema; DuckDB (`WIDTH 1-38`) and ClickHouse
(`P 1-76`) pick their integer width from it; BigQuery offers exactly two fixed envelopes
(`NUMERIC(38,9)`, `BIGNUMERIC(76.76,38)`). A decimal that never declares a scale cannot land
losslessly in any of them without re-deriving scale from data at export time. A value exceeding the
declared scale is a **typed error at insert**, never a silent truncation.

**Currency first in the key** is load-bearing, not decoration. ISO 4217 minor units are not always
2 (JPY/KRW 0, USD/EUR/GBP 2, BHD/KWD/OMR 3, CLF 4, and Sky's own table gives BTC 8 / ETH 18), so a
global "always 2 decimals" assumption is wrong. With currency as the key prefix the scale is fixed
*within* each currency, so per-currency minor units order correctly and cross-currency comparison
is partitioned rather than meaningless.

**Precedent, for the record.** This problem has two industry answers and we are taking the harder
one deliberately: Google Spanner refuses it (`NUMERIC` is disallowed in primary keys, foreign keys
and secondary indexes) and FoundationDB reserves decimal typecodes while explicitly disclaiming
ordering guarantees — whereas CockroachDB solved it with an exponent-classed base-100 mantissa
scheme inherited from SQLite4. Ours is simpler than Cockroach's because a declared scale removes
the variable-exponent case.

**Separate the two encodings.** The canonical VALUE form (what `Codec`/JSON/DB columns round-trip)
and the ORDER-PRESERVING KEY form are distinct artefacts with distinct names. CockroachDB does not
reuse `apd`'s marshaling for its keys, and neither should we.

**Landing zone:** P2 (index keyspace + `SkyType` dispatch), alongside `Float`'s net-new total-order
encoding. B1 moves out of §11.2 and its `sky check` build error is deleted with it.

**Prerequisite found while ruling this — `Std.Codec` cannot persist `Money`/`Decimal` at all
today.** `Codec.auto` rejects any data-carrying ADT (`[main]` `runtime-go/rt/codec_auto.go:130`),
and both types are exactly that shape, so the codec path this ruling assumes does not yet exist;
today's persistence is the raw `SqlDecimal`/`SqlMoney` TEXT path, and every example that sorts by
money uses an `Int` cents column instead. There is also no `NUMERIC`/`DECIMAL` DDL kind anywhere —
`schemaTypeName` falls through to `TEXT` on both dialects, so even Postgres receives money as text.
Both are P2 prerequisites, and neither is a migration risk: because `Codec.auto` has never
persisted these types, there is no codec-encoded installed base to convert.

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
   │      L2  txn / read-set (points + index RANGES, coordBounds) / validate  →  SSI
   │      L3  index 0x02 + unique 0x03 / planner / seek (seekBounds)          →  O(log n + k)
   │
   ├─► sky-app/rt              never imports bluedb
   │      DataBackend (stdlib types only) · SessionStore · the persist-before-ack funnel
   │      ChangeBus · the transact poison flag · rt.Admin (minted only by the funnel)
   │
   └─► sky-app/consoledata     imports rt; NEVER imported BY rt (registers into an rt slot
          Decide()             at blank-import time, like console_app's existing seam)

Sky source:  Std.Persist  (one Conn, no backend named)
             Std.Db       (raw SQL escape hatch — startup fatal on driver = "embedded")

Truth:       cargo run -p xtask -- bluedb-gates --tier=full  →  docs/bluedb/STATUS.md (generated)
```

*(v2.0's appendix listed `consoledata.Decide()` under `sky-app/rt`, contradicting §8.3's "imports
rt; never imported BY rt". The direction matters — it is what keeps the import cycle broken —
so it is drawn as its own node.)*

Key facts a griller should check first, because everything else rests on them:

1. **`userKey` is opaque to the comparer** (`[bdb]` `comparer.go:45-57` `skydbSplit`, doc `:38-44`:
   it "reads the TRAILING LENGTH BYTE arithmetically and NEVER scans for `0x00`", proven against
   an adversarial corpus incl. `{0x00}`, `{0xFF}` and a prefix pair at `[bdb]` `comparer_test.go:22-53`) —
   therefore §3's index/unique namespaces and §5's tenancy component do **not** touch
   `skydb.mvcc.v1`. This is the document's most load-bearing premise and both grillers confirmed
   it.
2. **Index entries are ordinary MVCC rows in the same `CommitReq`** — therefore index maintenance
   is inside the single-writer commit path with no new machinery, no second GC, no torn index.
   *But* index tombstones need explicit reclamation or the complexity claim decays (§3.5a).
3. **A seek records `coordBounds`, not `seekBounds`** (§3.4a) — therefore §3 preserves §2's SSI
   proof rather than re-deriving it. **G2.11** proves the byte-identity mechanically and A-PH
   samples the consequence; v2.0 asserted the preservation with neither, and conflating the two
   byte spaces would have admitted phantoms.
4. **The tenant is in the conflict domain, not only the key** (§3.2a) — otherwise every tenant
   conflict-checks against every other and goal #2's "high-throughput lock-safe parallel" fails
   at scale while every correctness gate stays green.
5. **The generated glue file means `rt` never imports `bluedb`** — therefore the P0-class breakage
   of every non-Persist app cannot recur, and a dead config key cannot exist. The session-store
   default is conditional on the app declaring a collection, or this claim and G0.3 contradict
   each other (§4.2).
6. **The changeset is durable inside the committing transaction** (§6.2a) — that is what makes
   goal #4's verbatim "in the commit path" true on SQL and not only on embedded.
7. **`STATUS.md` renders from the registry, an un-provable gate is a FAIL, a not-run gate makes
   its goal UNKNOWN, and the canary catches a verifier that cannot verify** (§9) — therefore a
   green lie cannot survive compaction. **Attack §9 first**: if it is wrong, nothing else in this
   document can be trusted, including the parts that are right.
