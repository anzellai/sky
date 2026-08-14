# P1 Stage 2 — the engine hub. Grilled plan.

> Status: **PLANNED, NOT STARTED.** Stage 1 (the irreversible key format) is
> committed. This document is the plan after two adversarial reviews — one on
> correctness, one on enforcement/vacuity — folded in. 25 findings; the ones
> that changed the plan are recorded inline with their reasoning, because a
> defect found and then forgotten costs more than one never found.

## What Stage 2 is

Port the engine hub from `feat/bluedb` into `runtime-go/bluedb/`:
`engine.go`, `keychange.go`, `readset.go`, `validate.go`, `reader.go`,
`watermark.go`, `recent_changes.go`, `hotkey.go`, `changefeed.go`,
`changelog.go`, `gc.go`, `committer.go`, `pebble_engine.go`, `txn.go`
plus tests `engine_test.go`, `crashsim_test.go`, `gc_test.go`, `lock_test.go`,
`bench_test.go`.

Go compiles a package as a unit, so this lands as ONE closed set. It cannot be
subdivided.

### Excision (verified line-exact against the prior art)

Delete when porting: `Txn.Scan` (`txn.go:171-178`), `Txn.ScanRange`
(`:183-186`), `Txn.ScanFallback` (`:192-195`), `Txn.scanMaterialize`
(`:562-609`), `Txn.rowMatches` (`:668-678`).

Keep: `WitnessCollection` (`:200`), `ScanCollection` (`:619-622`),
`scanPrefixMaterialize` (`:627-666`), `sliceCursor` (`:681-697`), `indexRange`
(`readset.go:20`).

Relocate `inRangeClosed` (`index_key.go:194-196`) verbatim into `readset.go`.
Verified: its only two consumers are `validate.go:65` (kept) and `rowMatches`
(excised); `IndexCoord`/`IndexID`/`CollID` live in `keychange.go:17-34`, not
`index_key.go`; `txn.go`'s only `ColType` reference is `ScanRange`'s parameter.
So this genuinely keeps `index_key.go` out of Stage 2 with nothing dangling —
which matters because its presence would trip `pending::p2_index` and detonate
seven P2 gates.

**Do NOT port `backend.go` or `cond.go`.** They force `index_key.go` in
(`backend.go:263` calls `encodeIndexKey`). The architecture doc (§10.1) says P1
ports `backend.go` to restore "the only working uniqueness enforcement" — the
doc is WRONG: `uniqUserKey` is a key *builder*; the enforcement is the
read-then-reserve pair at `embedded.go:295-298`, which no phase ports. Amend
the doc line in the same commit.

**Do NOT create `runtime-go/persistglue/`** — it is P4's probe; creating it
detonates five gates.

---

## The seven defects ported deliberately, then fixed one per commit

Every one was proven by running code against real Pebble. The port (C1) keeps
them so the falsification evidence survives; C2–C8 fix them.

### N1 — `Iterate` bounds misparse → cross-collection row leakage

`reader.go:68,81` build bounds `[]byte{tagData} ‖ prefix` ending in an
arbitrary user byte, which `skydbSplit` (`comparer.go:45-57`) reads as a suffix
length.

**The rule, stated correctly** (the first plan got this wrong twice):
a bound of length `L` ending in byte `v` is misread **iff `0 < v ≤ L-1`**.
The `v == 0` clause is not pedantry — it is precisely the mechanism the fix
relies on, and without it every well-formed bound in the codebase
(`[]byte{tagData}`, `dataKeyPrefix(...)`, `encodeChangelogKey(T)`) looks like it
detonates when all are safe. Content-independent failure begins at
`len(prefix) ≥ 255` for arbitrary bytes, or `≥ 127` for an ASCII tail — not the
"~126" first claimed.

With `prefix = collName ‖ 0x1F`: `n ≤ 29` correct; `n = 30` → lower bound's
prefix collapses to `[0x00]`, the whole data keyspace → **another collection's
rows are returned, decoded and predicate-matched as if they belonged**;
`n ≥ 31` (not just 31) → lower > upper → zero rows, `Err() == nil`. Pebble has
no production assertion on inverted bounds, so it is silent.

**Fix:** `lower = dataKeyPrefix(prefix)`; `upper = bytesSuccessor(tagData‖prefix) ‖ 0x00`.
Both end in `0x00` → `Split` returns `len` → bare prefixes → no misparse.

The upper bound was independently re-derived across all five edge cases and is
sound. The sharpest question — is appending `0x00` to a successor well-formed? —
resolves yes: `S`'s last byte is `(non-0xFF)+1 ∈ [1,0xFF]` so `S` never ends in
`0x00`, while every stored key's `Split`-prefix always does; therefore no valid
prefix can equal `S`, the interval `[S, S‖0x00)` contains no valid prefix, and
`S‖0x00` excludes exactly what bare `S` would.

**Keep the `bytesSuccessor == nil` guard** even though it is unreachable (byte 0
is always `tagData`), and keep `[]byte{tagChangelog}` for the empty-prefix case
rather than `[0x01,0x00]` — the latter is correct but by a non-obvious argument.

**On-disk bytes: UNCHANGED.** Verified by enumerating every persisted write in
all 39 prior-art files. The only persisted bound is `gc.go:124`'s
`DeleteRange(clLo, clHi)`, and both are already well-formed.

> **DO NOT fix this in `skydbSplit`.** It is the obvious move and it is
> irreversible: it changes SSTable ordering under a frozen
> `comparerName = "skydb.mvcc.v1"`, breaks the leading-byte-stripping invariant
> `base.CheckComparer` enforces, and requires `skydb.mvcc.v2` plus a full store
> rewrite. The bug is in the caller. **Stage 2 must not modify `comparer.go` or
> `keys.go` at all** — enforce with `git diff --exit-code` on both as a
> pre-commit check.

**N1b, same commit:** `scanPrefixMaterialize` (`txn.go:632-637`) never checks
`cur.Err()`, so the `n ≥ 31` regime returns an empty collection with no error.

### N3 — `quietLogger.Fatalf` panics on Pebble background goroutines

**The first plan's fix was not implementable.** `quietLogger` is a fieldless
value type installed into `opts.Logger` *before* `pebble.Open` and before the
engine struct exists — there is no `e` to store a latch into.

**Fix:** a separate `fatalLatch{ set atomic.Bool; msg atomic.Value }`
constructed before `opts`; `opts.Logger = &quietLogger{lat: lat}`; wire
`e.fatal = lat` after `Open` succeeds. Atomics are required, not optional —
background sites race.

**The soundness argument covers only one of ~36 sites.** `db.go:885` is
synchronous inside `Apply` (verified: `commit.go:298-354` confirms
`noSyncWait == false` is fully synchronous), so the latch is set before `Apply`
returns and a next-line read is race-free *for that path*. But
`version_state.go:191/196/202` (MANIFEST flush/sync/set-current) run on
flush/compaction goroutines; with a latch and no consumer there, **a broken
MANIFEST is swallowed and the engine keeps acking commits as durable.**

So the latch must be consumed at five points, not one:
after `pebble.Open` (fail `openWith`, `db.Close()`); in `Close()`; after
`committer.go:135`; after `committer.go:306`; and after **both** GC applies
(`gc.go:135`, `gc.go:152`) — the first plan named only "the committer", so a
GC-triggered fatal would be lost or mis-attributed.

Placement matters: the check must sit **before** the
`if err != nil {seal} else {advanceDurableHi; ring append; emit}` block, or a
lost write advances `durableHi` and fires the change feed.

`takeFatal` must **not** clear — a clear-on-read latch loses a second fatal and
charges a background fatal to an innocent batch.

**Keep both `recover` blocks** (`committer.go:94-103`, `:195-207`):
`db.go:955` is a raw `panic(err)`, not a `Fatalf`.

### N4 — `Close` races readers into a Pebble panic

`Snapshot` checks `isClosed()` then calls `NewSnapshot()`, which panics
unconditionally on a closed DB; `snapshotAt` does not check at all; `newIter`
panics too, so a live reader mid-`Iterate` panics as well.

**The first plan's fix self-deadlocks, deterministically.** Holding
`closeMu.Lock()` across the reader drain is a guaranteed hang: every
reader-release path runs through `Txn.Commit` → `e.Commit` → `isClosed()` →
`closeMu.RLock()`, which blocks behind the pending writer, so the reader never
releases, the refcount never decrements, and `Close` burns its full timeout
every time any transaction is open.

**Fix — three phases, lock released between them:**
1. `Lock()`; set `closed = true`; `close(e.ch)`; `close(e.reaperStop)`; `Unlock()`.
2. `wg.Wait()`; bounded refcount drain. The `closed` **flag**, not the lock,
   prevents new pins — so releasing here is safe.
3. `Lock()`; `e.db.Close()`; `Unlock()`.

The refcount decrement must be **lock-free** (atomic + signal) and must never
touch `closeMu`. `e.Commit`'s `isClosed()` must stay a short RLock and must not
be widened over `blindHotLeases` (`pebble_engine.go:391`), which blocks on
`<-t.granted`.

Two paths the first plan missed: `snapshotAt` constructs a reader with
`reg: nil` and no refcount — invisible to the drain; and `pebbleReader` has no
engine handle, so there is nowhere for `Close()` to decrement. Both must be
added.

**`closeOnce.Do` + a fallible drain is unrecoverable:** if the drain times out,
the `once` is consumed and `Close` can never be retried — the directory lock is
held forever, or the DB is closed under live readers. Either make `Close`
retryable with an explicit state machine, or force `db.Close()` and document
that in-flight readers get a hard error. Silence here ships a stuck state.

### N5 — `readMetaHLC` swallows corruption into the fresh-store sentinel

A truncated `hlc_hi` returns `{0,0}, nil`, indistinguishable from an empty
store, so `newHLCClock` restarts from wall-clock and can **re-issue a commitTs
already on disk** — two transactions sharing a data key, the later `Set`
silently overwriting the earlier committed version.

**Fix:** `len(v) != hlcEncodedLen` is corruption; return an error and refuse to
open. Verified: every writer emits exactly 12 bytes (`committer.go:122-124`,
`:293-295`, `gc.go:149`), absence returns `ErrNotFound` handled earlier, and
nothing ever `Set`s an empty meta value. Pair it with a documented recovery
path — this also makes a future 13-byte meta value a hard refuse-to-open for
old binaries, and there is no repair verb.

### C1 — `Commit` returns `Err: nil` on the `Close` race

When `e.ch <- job` panics on a closed channel, the deferred recover sends into
`job.done` — a buffered channel nobody will read, because `return <-job.done`
never executed. The function returns the zero `CommitResult`: **`Err: nil`**.
A commit against a closed engine reports success.

**The first plan's fix was a no-op.** "Named return so the recover can set it"
describes only the signature; adding `(res CommitResult)` without touching the
body changes nothing — the recover still writes to `job.done`, `res` stays
zero, and the bug survives behind a green-looking diff.

**Fix:** named return **and** the deferred body becomes
`res = CommitResult{Err: ErrClosed}`, with the `job.done <-` send deleted.

### H1 — `Register` hands out a readTs naming an in-flight commit

**Restate this finding before fixing it.** On the prior art, `Register`
(`watermark.go:35-47`) never calls `durableHi` — it calls `w.highWater()` inside
`w.mu`. And `advanceThreshold` *already* reads `durableHi` before `w.mu.Lock()`,
with a comment giving verbatim the reasoning the first plan presented as new. So
"Register must read durableHi before w.mu" is either restating shipped
discipline or predicated on an unstated change.

**The real defect is at the other site:** `Snapshot()`
(`pebble_engine.go:343-359`) calls `Register()` and *then* `e.db.NewSnapshot()`,
so the token and the snapshot pin are **non-atomic**. `beginSnapshot`
(`:259-274`) already documents and fixes exactly this for `Begin()`.

**Fix:** make `Snapshot()` adopt `beginSnapshot`'s construction — hold `durMu`
across `readTs := e.durableHiVal` → `NewSnapshot()` → `RegisterAt(readTs)`.
Simplest correct form: `Snapshot()` becomes a thin wrapper over
`beginSnapshot()`. Do **not** hoist a `durableHi` read into `Register`: a
stale-low value is conservative-safe for a *clamp* but wrong for a *readTs
choice*.

### H3 — `pebbleReader.Get` swallows all errors

`reader.go:42-44` discards the `NewIter` error and `:48` treats a failed
`SeekGE` as "absent". **Checking `err` fixes nothing**: `Snapshot.NewIter`
(`pebble/v2@v2.1.6/snapshot.go:62-69`) returns `nil` unconditionally and panics
if the snapshot is closed. The fix is `iter.Error()` after positioning.

Add `Err() error` to the `Reader` interface, mirroring `Cursor.Err()`. Fail
closed at the commit boundary: `Txn.Commit` returns `tx.reader.Err()` before
calling `e.Commit`, and `ensurePreimage` must not record a point read as
`present: false` when the underlying `Get` errored — otherwise an I/O error is
laundered into "the row is absent", which is how a swallowed error becomes an
unwanted INSERT.

---

## The four Stage-1 signature reconciliations — three different remedies

Stage 1 changed `decodeDataVersion` and `changelogTsOf` to `(HLC, bool)`. The
prior art has exactly four consumers, and the first plan prescribed `continue`
for all of them. That is wrong.

| Site | Remedy | Why |
|---|---|---|
| `gc.go:92` | `continue` **+ `stats.CorruptKeys++`** | Never delete a key you cannot parse. But `continue` alone leaks it permanently and invisibly — `GCStats` has no corruption counter and GC's bounds are the whole keyspace. Add the counter and a documented threshold at which GC errors rather than looping forever. |
| `changelog.go:36` | **`return` an error — MUST NOT `continue`** | `Tail` backs `changelogTailChanges`, the Fix-8 spill fallback that computes the SSI validation window. Skipping a malformed key silently drops a committed change from that window — under-rejection, i.e. a serializability break. `changelogTailChanges` already converts an error into `ErrConflict`, so failing closed is both correct and already plumbed. |
| `reader.go:55` | return **absent** | A `{0,0}` leaking into `pointRead.versionSeen` is exactly the fresh-store-sentinel confusion the `ok` flag exists to prevent. |
| `reader.go:161` | **skip this prefix** | Same reasoning for `Cursor.CommitTs()`. |

Never `_` the flag. A `grep -n 'decodeDataVersion\|changelogTsOf'` review gate on
the C1 diff: every call site must bind and check `ok`.

---

## N2 belongs to P2, not here

`Descending(ColText)` is not order-preserving, so SSI validation
under-rejects: `rangeOptimized` masks the descending flag off and returns true
even though `fixedWidthCol` already knows text is variable-width. Worked
example: a band over `["a","b"]` encodes `lo=[0x9D], hi=[0x9E]`; a phantom at
`"ab"` encodes `[0x9E,0x9D]`, which `inRangeClosed` rejects as longer — the
phantom is inside the value range and is not matched.

**It is unfixable in Stage 2 because there is nothing here to fix** —
`Descending`, `rangeOptimized`, `encodeScanRange` all live in `index_key.go`,
which must not enter. Recommended P2 fix is the conservative one:
`rangeOptimized` returns `false` for a descending non-fixed-width column, so
descending text degrades to the collection witness — over-rejects, never
under-rejects, and **no encoding change**. Re-encoding is rejected because
`IndexCoord.Key` is serialised into the durable changelog and would need a
`payloadFmtV1` bump.

Owning gate: **G2.12**. Three anchors so it cannot evaporate: an `AUDIT-N2`
comment in `readset.go`, the C1 commit message, and the Stage-2 test below.

## What Stage 2's serializability claim does NOT cover — state it plainly

Excising `Txn.Scan` and `Txn.ScanFallback` removes the **only** writers of
`ReadSet.ranges` and `ReadSet.indexWitness`. So Stage 2 ships an SSI validator
whose range-conflict and index-witness arms are **structurally unreachable**.

Stage 2's serializability claim covers **point reads and `collWitness` only**.
No Stage-2 gate may assert range-conflict or index-witness detection. Proof
obligation: mutating `inRangeClosed` to `return false` must **not** change any
Stage-2 gate — if it does, that gate is lying about what it covers.

---

## Enforcement — the part that makes the rest real

### The harness is off-CI (found by the vacuity grill; C0 closes it)

`grep -rin bluedb .github/ scripts/` returns **nothing**. Stage 1's 13 Go tests,
every gate body, `--check` and `--verify-mutations` are invoked by no
automation. Only the `#[cfg(test)]` unit tests inside `bluedb_gates/*` run.
`gate_manifest_test.rs` cannot notice: it scans CI→xtask, never the reverse.

C0 (landing first, independently) must therefore:
- run `go test ./rt/... ./bluedb/...` — **that order**, because
  `coverage_ledger.rs:644` substring-matches the literal `"cmd:go test ./rt/..."`
  and reversing it silently weakens two coverage surfaces;
- add `-tags pebblegozstd`, or CI certifies a build no app ships;
- put `-race` in its **own** step with `CGO_ENABLED=1` — reproduced:
  `CGO_ENABLED=0 go test -race` fails on linux/amd64 (`-race requires cgo`)
  while succeeding on darwin/arm64, the perfect works-locally-breaks-in-CI trap;
- wire `bluedb-gates --check` into CI and `--tier=full` + `--verify-mutations`
  into nightly;
- add the reverse (xtask→CI) assertion for `bluedb-gates`.

### Every gate body that runs `go test` must assert a count

**`go test -run 'TestNoSuchThing'` exits 0** — reproduced. A body classifying on
exit status alone reports PASS having executed nothing: the exact shape this
branch exists to eliminate. Reuse the mechanism the repo already has
(`harness/bodies.rs` `CLI_VERBS_EXPECTED`), whose own comment names the defect:
count `func TestAudit…` from source, require an EXACT count, run with
`-count=1` to defeat the result cache, and parse `-json` for exactly N passing
events. Any one of the three alone is insufficient.

### Seven gates, not one gate with seven mutations

`mutations.rs` checks only that **this** mutation's `expect` string is present —
never that the other six are **absent**, and no test requires `expect` strings to
be mutually discriminating. One C1-era defect breaking several properties at
once would mint seven PROVENs from one undifferentiated failure.

So: **G2.13a–g**, one mutation each, each body anchored `-run '^TestAuditN1$'`
with the exact-count assertion. This makes discrimination structural and gives
`STATUS.md` a row per property.

### Other enforcement gaps to close in sequence

- Gate bodies are spawned on a detached thread and never killed; the budget is
  advisory and orphans `go test` children into the tree the next probe measures.
  Fix with `process_group(0)` + `killpg`, as `harness/layer2.rs` already does,
  **before** the crash corpus lands.
- `--verify-mutations` has no budget at all; seven mutations on one gate is 14
  probes, each preceded by a `cargo build` in the worktree.
- G0.1's title claims "matches a fresh run"; its body checks only the banner and
  a `body-sha256`. The fresh-run comparison lives in `--check`, which nothing
  invokes. Narrow the title AND wire `--check` (both, not either).
- Nothing asserts a declared mutation's patch file exists — 39 are unauthored
  today. Add a test for non-pending gates only.

---

## N6 — found during C1, not by either grill: `decodePayload` fails open

`committer.go:352` silently returns `nil` on a decode error, and its docstring
justifies it: *"a malformed payload validates as 'no changes' for that job,
never a false accept of a later txn against garbage"*.

**That reasoning is inverted for the `pending` path.** A blind job's undecodable
payload contributes nothing to `pending`, so a later txn in the *same drain
window* validates against a window missing that job's committed changes. That is
under-rejection — the identical shape the plan rejects `continue` for in
`changelog.go`, one function over, with a comment arguing it is safe.

Two defects of one class, in one file, one reasoned about correctly and one not,
is the strongest evidence available that the class is worth a systematic sweep
rather than a per-site fix. **Before C8 closes, sweep every error path in the
commit/validation route for fail-open behaviour** — `committer.go`,
`changelog.go`, `validate.go`, `recent_changes.go` — and record each site as
fail-open-by-design (with the argument) or fail-closed.

Fix in the C2–C8 sequence: schedule as **C6b**, immediately after H1, since both
concern what the validation window is permitted to omit.

## Two more doc rows carry the `uniqUserKey` conflation

C1 amended §10.1's P1 row. `v2-architecture.md:73` (G-B4) and `:171` (P12) say
the same wrong thing — that adding `backend.go` to P1's port list restores
uniqueness enforcement. Line 73 is the sharper case: its *evidence* column
correctly locates the mechanism at `embedded.go`, while its *remedy* column
draws the opposite conclusion. Amend both in a later commit.

## Fault-injection fixtures: prove the fault was REACHED, not just armed

Found the hard way in C3. This document prescribed H3's fixture as "one row +
small `memTableSize` + `Flush()` + reopen, then inject on `*.sst` reads". That
fixture **passed against the unfixed reader** — a green test proving nothing.

Cause: a single row makes a one-block SSTable, and `openWith`'s own `hlc_hi` and
gc-threshold meta reads pull that block into the fresh cache. By the time the
armed `Get` ran, **zero filesystem operations occurred**. The injector was
armed at a door nobody walked through.

It was caught by instrumenting the injector to count ops, not by reading the
test. The fix is 400×2 KiB of padding plus a mid-keyspace target key, so the
meta reads land in a different block.

**Rule for every remaining injection fixture (C10's crash corpus especially):
assert the injected op actually fired.** Count invocations in the
`InjectorFunc` and fail if the count is zero. An injection test that cannot
prove it injected is indistinguishable from one that passes because nothing
happened — the same shape as `go test -run` matching no tests.

Second lesson from the same commit: use `t.Errorf`, not `t.Fatalf`, for the
first of several assertions. A `Fatalf` on "the flag went red" masks the
downstream assertion about whether anything CONSULTS the flag — so the mutation
proof would only ever demonstrate the flag, which is the uninteresting half.

## `audit_test.go` is shared

Every C-commit appends to `runtime-go/bluedb/audit_test.go`. Concurrent agents
must append only — never rewrite, never reorder — and stage the file by
composing the intended blob rather than `git add`-ing a working tree that holds
someone else's in-flight test.

## Commit sequence

`--verify-mutations` refuses to run against a tree that differs from HEAD
(`head_skew`), so **commit before verifying, every time**.

| # | Content |
|---|---|
| C0 | CI enforcement (above). Lands on Stage 1 and must pass immediately — if it does not, Stage 1 was never green. |
| C1 | The port, seven defects PRESERVED, plus: the excision, the `inRangeClosed` relocation, the four signature reconciliations, the `AUDIT-N2` note, the §10.1 doc amendment. Commit message enumerates the preserved defects by ID. |
| C2 | N1 + N1b |
| C3 | H3 |
| C4 | N5 |
| C5 | C1 (the `Commit` false ack) |
| C6 | H1 |
| C7 | N4 |
| C8 | N3 |
| C9 | G2.9a body + mutation (after C8 — N3 changes the mechanism it certifies) |
| C10 | G2.6 body + injection manifest + mutation (after C8 — N3 adds a new injection site) |
| C11 | G2.13a–g + seven mutations |
| C12 | `--verify-mutations`, regenerate `STATUS.md` + ledger |

C1's deliberately-defective commit is **inert**: verified on four independent
paths that nothing consumes `runtime-go/bluedb/` — `build.rs` materialises only
`runtime-go/rt` into an app, `rt` may not import `bluedb`, CI's Go step is
`./rt/...` only, and no script builds the whole module. A stop between C1 and C8
ships nothing to any consumer.

Expected: **G2.6 and G2.9a go RED at C1** and stay red until C9/C10. That is the
ratchet working. Do not silence it, and do not narrow `p1_engine` off
`committer.go` — that probe was already narrowed once, deliberately.

## Risks, ranked

1. **Someone fixes N1 in `skydbSplit`.** Irreversible under a frozen comparer
   name. Guard: `git diff --exit-code` on `comparer.go` and `keys.go`.
2. **N4's drain deadlocks** (D1) or leaves `Close` unretryable (D10).
3. **N3's latch mis-scopes** — silences too much (breaks acked⇒durable) or too
   little (still crashes on background fatals). Needs the two-sided test pair:
   one test fails if you silence too much, the other if you panic too much.
4. **C1's fix looks right and does nothing** — the named-return trap.
5. **`changelog.go` gets `continue`** by mechanical analogy with `gc.go`,
   silently breaking serializability.
6. **G2.13's mutations authored to pass rather than to falsify.** Every `expect`
   string must be copied from a real observed pre-fix failure, never composed.
   Spot-check by submitting one deliberate no-op mutation and confirming it
   reports VACUOUS.
7. **The Go tests stay unenforced** — nullifies everything above. C0 first.
