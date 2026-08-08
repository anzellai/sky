# Std.Db + Sky.Live driver-agnostic transient-error retry

> **Status**: v0.18.0 design (Architecture-Consult, 2026-07-17).
> **Scope**: Runtime-layer classification + retry for Std.Db and
> Sky.Live's SessionStore. Reuses `Sky.Core.Error` — **no new error
> ADT.**  No compiler touch. Does not touch the §8 irreducible floor.

## 1. The problem

Sky.Live apps (sky-diagram, mini-notion, and every downstream tenant)
lose requests under concurrent write load because the current Std.Db
runtime folds every driver-side error into `ErrIo("db.exec: …")`:

* **SQLite** — no `busy_timeout` set + rollback-journal default; the
  second concurrent writer trips `SQLITE_BUSY`/`SQLITE_LOCKED` and
  the Task fails immediately.
* **PostgreSQL** — under SERIALIZABLE/REPEATABLE READ isolation,
  `40001 serialization_failure` and `40P01 deadlock_detected` are
  the driver's contract for "retry the transaction". Sky treats
  them as opaque IO errors.
* **MySQL** — `1213 deadlock` and `1205 lock_wait_timeout` same.
* **Redis** — `MOVED`/`ASK` (cluster redirect), `LOADING`
  (server booting), or bare connection resets currently surface as
  IO errors; the caller has no path to recover.
* **Firestore** — `ABORTED`/`UNAVAILABLE`/`RESOURCE_EXHAUSTED`
  are Google's "retry with backoff" contract; Sky drops them on the
  floor.

The user surface: `Task.retryWith defaultRetryPolicy body` **does not
help** because `Sky.Core.Error.isRetryable` currently only fires for
`Timeout | Network | Unavailable` (`Sky/Core/Error.sky:186`) — every
DB error is `Io`, so `isRetryable` returns `False`, and retry never
runs.

## 2. Non-goals

* **No new error ADT.** Reuse `Sky.Core.Error`. Every DB / session-
  store failure maps to one of the existing 11 `ErrorKind`s.
* **No compiler surgery.** All work lives in `runtime-go/rt/` +
  `sky-stdlib/` + `src/Sky/Sky/Toml.hs`. §6/§7/§8 do not apply
  because we are not emitting new lowered code.
* **No wire-protocol change.** Existing gob-encoded sessions +
  Sky wire-decoded events remain byte-identical.
* **No forced idempotency.** We document the idempotency rule; we
  do not synthesise idempotency keys.

## 3. Architecture citations (CLAUDE.md §0.3 gate)

| Concern | Reference | Verdict |
|---|---|---|
| §6 rt.Coerce origin | `docs/architecture/sky-compiler-architecture.md:384-408` | **N/A** — pure runtime + stdlib work; no `rt.Coerce` sites created or removed. Does not enter the Compile.hs lowering path. |
| §7 architectural lever | `sky-compiler-architecture.md:412-451` | **N/A** — no lowering lever activated. Pattern precedent: `runtime-go/rt/exporter.go:856 classifyPushResult` (HubExporter status → retry bucket) is the exact runtime-side classification shape we extend to DB / session-store. |
| §8 irreducible floor | `sky-compiler-architecture.md:455-489` | **Not touched.** §8.1 (Go FFI return) already returns `any`; we operate on the `error` values passed to `ErrIo(err.Error())` — pure Go-side classification before wrapping. §8.2 (wire decode) unchanged. §8.3 (TEA MakeFunc) unchanged. |
| Std.Db invariants | `sky-stdlib-correctness.md:1076-1204` (§5.1-§5.5) | Extends v0.16.26 typed-parameter contract; preserves identifier allowlist + tenant SQL gate + migration checksum. Retry lives *above* the parameter-binding + WHERE-clause layer, not inside it. |
| Sky.Live TEA contract | `sky-stdlib-correctness.md:866-1073` (§4.2-§4.10) | Session-store retry is transparent to `update`/`view` — same `(Model, Cmd Msg)` return, same SSE-patch protocol. |

**Verdict: PROCEED. Zero floor touch, zero lowering touch.**

## 4. Two-layer retry model

The design layers two retry loops with **explicit ownership**:

```
┌──────────────────────────────────────────────────────────────────┐
│                       User Sky code                              │
│  ────────────────────────────────────────────                    │
│  Task.retryWith                                                  │
│    (Task.defaultRetryPolicy                                      │
│       |> Task.retryOn Error.isRetryable)          ← Layer 2      │
│    (Db.withTransaction db (\c -> …))                             │
└──────────────────────────────────────────────────────────────────┘
                          │
                          ▼
┌──────────────────────────────────────────────────────────────────┐
│                    runtime-go/rt/db_auth.go                      │
│  ────────────────────────────────────────────                    │
│  Db_exec / Db_query / SessionStore.Get                           │
│    ├─ classify(err) → ErrorKind + RetryClass                     │
│    ├─ if RetryClass == Reconnect: drop conn, refresh, retry once │
│    ├─ if RetryClass == Retryable AND kind ∈ Reconnect/Unavailable│
│    │   family AND not inside tx: bounded internal retry (≤3)  ← Layer 1
│    └─ else: return Err (typed by ErrorKind)                      │
└──────────────────────────────────────────────────────────────────┘
                          │
                          ▼
                driver: sqlite / pgx / redis / firestore
```

### Layer 1 — runtime-transparent retry

**Owned by the Sky runtime**. Handles the tightly-bounded transient
error classes where the *individual statement* can be retried without
observing intermediate state:

* `SQLITE_BUSY` — writer waiting on lock; retry after busy_timeout.
* `Redis LOADING` — replica booting; retry after brief delay.
* Driver `ErrBadConn` — stale conn from the pool; drop, get a new
  one, retry once.
* Firestore `INTERNAL` — Google's SDK contract says retry.

**Budget** (defaults, override per-driver in `sky.toml`):

| Driver | maxAttempts | baseDelayMs | maxDelayMs | jitter |
|---|---|---|---|---|
| SQLite | 3 | 20 | 500 | true |
| PostgreSQL | 3 | 100 | 2000 | true |
| MySQL | 3 | 100 | 2000 | true |
| Redis | 3 | 50 | 500 | true |
| Firestore | 5 | 250 | 5000 | true |
| **SessionStore** (any backend) | 2 | 50 | 200 | true |

Session-store retry budget is tighter because the operation is on
the request hot path: 2 attempts × 50ms base × ~1.5 jitter ceiling ≈
worst-case ~150ms added latency vs an immediate hard fail. The
tradeoff is deliberate; SSE keeps the client informed via the
existing reconnecting-banner protocol.

**Guard**: Layer 1 retry is **skipped inside `Db_withTransaction`**.
Retrying a single `INSERT` inside an open tx doesn't help if the tx
itself is doomed — the caller must re-run the whole tx (Layer 2). We
signal this by threading a tx-context flag through a per-goroutine
`context.Context` value.

### Layer 2 — Sky-visible retry

**Owned by user code**. Uses `Task.retryWith` (already shipped
v0.15.44) with a policy scoped by `Error.retryClass`:

```elm
Task.retryWith
    (Task.defaultRetryPolicy
        |> Task.withMaxAttempts 5
        |> Task.withJitter
        |> Task.retryOn (\e -> Error.retryClass e /= Error.Permanent))
    (Db.withTransaction db saveOrder)
```

Handles the **serialization/deadlock class**: `40001` /
`40P01` / `55P03` / Firestore `ABORTED`. The transaction body must
be re-run from the top; the runtime cannot do this transparently
because it has no view of user Sky code inside the tx callback.

## 5. Sky-side classification surface

**One addition to `Sky.Core.Error`**, no new ADT — this is a
classification helper over the existing kinds:

```elm
-- sky-stdlib/Sky/Core/Error.sky — additive
type RetryClass
    = Retryable   -- transient; safe to re-run the whole operation
    | Reconnect   -- connection is dead; drop + refresh + re-run once
    | Permanent   -- do not retry; surface to user

retryClass : Error -> RetryClass
retryClass err =
    case err of
        Error kind _ ->
            case kind of
                Conflict     -> Retryable  -- deadlock / serialization / lock timeout
                Unavailable  -> Retryable  -- SQLITE_BUSY / Redis LOADING / PG 57P03 / Firestore UNAVAILABLE
                Timeout      -> Retryable  -- statement_timeout / DEADLINE_EXCEEDED
                Network      -> Reconnect  -- ErrBadConn / dial errors / conn reset / cluster MOVED
                _            -> Permanent

-- isRetryable stays backward-compatible; now delegates:
isRetryable : Error -> Bool
isRetryable err = retryClass err /= Permanent
```

The existing `Error.isRetryable` gains `Conflict` (previously
`False`). This IS a semantic change to `isRetryable`. It is a
strictly-widening change (never turns True into False), and
`retryOn Error.isRetryable` composes with existing `Task.retryWith`
calls so no user code needs to update to benefit.

## 6. Driver-level native-error mapping

The mapping table lives in `runtime-go/rt/db_errors.go` (new file).
Each driver has an unwrap + classify function. The returned
`(ErrorKind, RetryClass)` pair is then packaged as the standard
`makeError` value with the original driver text preserved in the
message so ops teams don't lose the SQLSTATE.

### 6.1 SQLite (modernc.org/sqlite)

The driver exposes error code via `*sqlite.Error.Code()` (extended
result code, higher 8 bits are the primary code). Primary codes:

| Primary code | Symbol | ErrorKind | RetryClass |
|---|---|---|---|
| 5 | `SQLITE_BUSY` | `Unavailable` | `Retryable` |
| 6 | `SQLITE_LOCKED` | `Conflict` | `Retryable` |
| 8 | `SQLITE_READONLY` | `PermissionDenied` | `Permanent` |
| 9 | `SQLITE_INTERRUPT` | `Timeout` | `Retryable` |
| 11 | `SQLITE_CORRUPT` | `Unexpected` | `Permanent` |
| 13 | `SQLITE_FULL` | `Unavailable` | `Permanent` (disk full — retry won't help) |
| 19 | `SQLITE_CONSTRAINT` | `InvalidInput` | `Permanent` |
| 21 | `SQLITE_MISUSE` | `Ffi` | `Permanent` |
| 23 | `SQLITE_AUTH` | `PermissionDenied` | `Permanent` |
| 26 | `SQLITE_NOTADB` | `InvalidInput` | `Permanent` |
| — | `sql.ErrConnDone` | `Network` | `Reconnect` |
| — | `sql.ErrTxDone` | `Conflict` | `Permanent` (tx already committed/rolled back) |

Additionally: on `Db_connect`, unconditionally issue
`PRAGMA busy_timeout = <sky.toml [database] busyTimeoutMs, default 5000>`
+ `PRAGMA journal_mode = WAL` (already done in `newSQLiteStore` for
the session store; not yet for user Db — this design ships that
consistency).

### 6.2 PostgreSQL (jackc/pgx/v5)

Errors implement `*pgconn.PgError` with `.Code` = 5-char SQLSTATE.
Classify by SQLSTATE class (first 2 chars) + specific well-known
codes:

| SQLSTATE | Symbol | ErrorKind | RetryClass |
|---|---|---|---|
| `08006` | connection_failure | `Network` | `Reconnect` |
| `08001` | sqlclient_unable_to_establish_sqlconnection | `Network` | `Reconnect` |
| `08003` | connection_does_not_exist | `Network` | `Reconnect` |
| `08004` | sqlserver_rejected_establishment_of_sqlconnection | `Unavailable` | `Retryable` |
| `08007` | transaction_resolution_unknown | `Unexpected` | `Permanent` (in doubt — user decides) |
| `40001` | serialization_failure | `Conflict` | `Retryable` |
| `40P01` | deadlock_detected | `Conflict` | `Retryable` |
| `55P03` | lock_not_available | `Conflict` | `Retryable` |
| `57014` | query_canceled (statement_timeout) | `Timeout` | `Retryable` |
| `57P03` | cannot_connect_now (starting up) | `Unavailable` | `Retryable` |
| `53100` | disk_full | `Unavailable` | `Permanent` |
| `53200` | out_of_memory | `Unavailable` | `Retryable` |
| `53300` | too_many_connections | `Unavailable` | `Retryable` |
| `23xxx` | integrity_constraint_violation | `InvalidInput` | `Permanent` |
| `22xxx` | data_exception | `InvalidInput` | `Permanent` |
| `42xxx` | syntax_error_or_access_rule_violation | `InvalidInput` | `Permanent` |
| `28xxx` | invalid_authorization_specification | `PermissionDenied` | `Permanent` |
| `42501` | insufficient_privilege | `PermissionDenied` | `Permanent` |
| `25P02` | in_failed_sql_transaction | `Conflict` | `Permanent` (must rollback) |
| `XX000` | internal_error | `Unexpected` | `Retryable` (per PG docs) |
| — | `driver.ErrBadConn` | `Network` | `Reconnect` |

Fallback: SQLSTATE class `08xxx` → `Network / Reconnect`; class `53xxx`
→ `Unavailable / Retryable`; class `57xxx` → `Timeout / Retryable`;
class `23xxx / 22xxx / 42xxx` → `InvalidInput / Permanent`.

### 6.3 MySQL (go-sql-driver/mysql — future)

Not currently wired into `detectDriver`, but the classifier ships to
close the door on regressions when MySQL is added. Errors implement
`*mysql.MySQLError` with `.Number` = error code:

| Number | Symbol | ErrorKind | RetryClass |
|---|---|---|---|
| 1213 | ER_LOCK_DEADLOCK | `Conflict` | `Retryable` |
| 1205 | ER_LOCK_WAIT_TIMEOUT | `Timeout` | `Retryable` |
| 2006 | CR_SERVER_GONE_ERROR | `Network` | `Reconnect` |
| 2013 | CR_SERVER_LOST | `Network` | `Reconnect` |
| 1040 | ER_CON_COUNT_ERROR (too many connections) | `Unavailable` | `Retryable` |
| 1062 | ER_DUP_ENTRY | `InvalidInput` | `Permanent` |
| 1451/1452 | FK constraint | `InvalidInput` | `Permanent` |
| 1044/1045 | access denied | `PermissionDenied` | `Permanent` |

### 6.4 Redis (redis/go-redis/v9)

`go-redis` returns typed errors + raw response strings from Redis:

| Sentinel / prefix | ErrorKind | RetryClass |
|---|---|---|
| `redis.Nil` | `NotFound` | `Permanent` (already handled — GET miss) |
| `MOVED …` / `ASK …` (cluster redirect) | `Network` | `Reconnect` |
| `LOADING …` | `Unavailable` | `Retryable` |
| `READONLY …` (failover) | `Unavailable` | `Retryable` |
| `BUSY …` | `Unavailable` | `Retryable` |
| `NOSCRIPT …` | `InvalidInput` | `Permanent` |
| `CROSSSLOT …` | `InvalidInput` | `Permanent` |
| `OOM …` | `Unavailable` | `Permanent` (server out of memory — retry won't clear it) |
| `WRONGTYPE …` | `InvalidInput` | `Permanent` |
| `NOAUTH …` / `WRONGPASS …` | `PermissionDenied` | `Permanent` |
| `net.OpError` / `io.EOF` / dial errors | `Network` | `Reconnect` |
| `context.DeadlineExceeded` | `Timeout` | `Retryable` |

Cluster `MOVED`/`ASK` — go-redis handles the redirect internally on
its next request; our Reconnect class just triggers Layer 1 retry
which lets that internal redirect happen.

### 6.5 Firestore (cloud.google.com/go/firestore, google.golang.org/grpc/codes)

Errors implement `status.Status` with `.Code() codes.Code`:

| Code | Name | ErrorKind | RetryClass |
|---|---|---|---|
| 1 | Cancelled | `Timeout` | `Permanent` (client cancelled) |
| 2 | Unknown | `Unexpected` | `Retryable` |
| 3 | InvalidArgument | `InvalidInput` | `Permanent` |
| 4 | DeadlineExceeded | `Timeout` | `Retryable` |
| 5 | NotFound | `NotFound` | `Permanent` |
| 6 | AlreadyExists | `Conflict` | `Permanent` |
| 7 | PermissionDenied | `PermissionDenied` | `Permanent` |
| 8 | ResourceExhausted | `Unavailable` | `Retryable` |
| 9 | FailedPrecondition | `Conflict` | `Permanent` |
| 10 | Aborted | `Conflict` | `Retryable` |
| 11 | OutOfRange | `InvalidInput` | `Permanent` |
| 12 | Unimplemented | `Ffi` | `Permanent` |
| 13 | Internal | `Unexpected` | `Retryable` (Google contract) |
| 14 | Unavailable | `Unavailable` | `Retryable` |
| 15 | DataLoss | `Unexpected` | `Permanent` |
| 16 | Unauthenticated | `PermissionDenied` | `Permanent` |

**Coordination with Google's SDK**: the Google-cloud Go SDK does
its OWN retry internally for a subset of codes. We must not
double-retry. Solution: pass `gax.WithRetry(gax.Backoff{})` = empty
policy on outbound calls, take full control at Layer 1 with the same
budget the SDK would have applied.

## 7. Runtime changes — `runtime-go/rt/`

### 7.1 New file: `runtime-go/rt/db_errors.go`

```go
package rt

type RetryClass int
const (
    RetryPermanent RetryClass = 0
    RetryRetryable RetryClass = 1
    RetryReconnect RetryClass = 2
)

// classifyDbError returns (ErrorKind constructor, RetryClass, prefix).
// prefix is the operation label ("db.exec" / "db.query" / …).
// The caller wraps the (mapped-kind, "<prefix>: <driver-error>") pair.
func classifyDbError(driver string, err error) (errKind func(string) any, cls RetryClass) {
    switch driver {
    case "sqlite":  return classifySqliteError(err)
    case "pgx":     return classifyPostgresError(err)
    case "mysql":   return classifyMysqlError(err)
    default:        return ErrIo, RetryPermanent   // safe default
    }
}

// retryDbOp runs body up to policy.maxAttempts times, respecting the
// RetryClass of each returned error. Skipped when inTx is true.
func retryDbOp(policy retryPolicy, driver string, inTx bool,
               body func() (any, error)) any { … }
```

* SQLite classifier uses `errors.As` with `*sqlite.Error` (modernc)
  or `*sqlite3.Error` (mattn — check whichever is imported).
* Postgres classifier uses `errors.As` with `*pgconn.PgError`.
* Redis classifier uses string-prefix checks + `errors.Is(err,
  redis.Nil)` + `net.OpError` detection.
* Firestore classifier uses `status.FromError(err)` + `.Code()`.

### 7.2 `runtime-go/rt/db_auth.go` mutations

Every kernel body currently shaped as

```go
res, err := d.conn.Exec(q, goArgs...)
if err != nil {
    return Err[any, any](ErrIo("db.exec: " + err.Error()))
}
```

becomes

```go
res, err := d.conn.Exec(q, goArgs...)
if err != nil {
    return classifyAndWrap(d.driver, "db.exec", err, d)
}
```

where `classifyAndWrap` calls the classifier, wraps in the correct
`ErrXxx`, and (if RetryClass ≠ Permanent + not in tx) delegates to
`retryDbOp` with the driver's Layer 1 policy.

`Db_withTransaction` (`db_auth.go:1199`) marks the goroutine as
in-tx via a `context.WithValue` bag so nested `Db_exec` /
`Db_query` calls skip Layer 1 retry. The tx boundary itself
propagates the classified error unmodified to the caller (Layer 2
then sees `Conflict` on `40001`/`40P01`/`ABORTED` and re-runs).

`Db_connect` (`db_auth.go:147`) sets driver defaults:
* `PRAGMA busy_timeout = <cfg or 5000>` on SQLite.
* `PRAGMA journal_mode = WAL` on SQLite (already for sessions).
* `SetMaxOpenConns` / `SetMaxIdleConns` / `SetConnMaxLifetime`
  from `[database]` section on every driver.

### 7.3 `runtime-go/rt/live_store.go` mutations

Every store's `Get` / `Set` / `Delete` gains the same
classify-and-retry wrapper (`live_store.go:429` sqlite,
`:578` postgres, `:732` redis). Session-store retry uses the tighter
Layer 1 budget (2 × 50ms base).

`newSQLiteStore` (`live_store.go:400`) already runs
`PRAGMA journal_mode=WAL`; we ADD `PRAGMA busy_timeout=5000`
so single-writer contention on the session table doesn't fail-fast.

`SessionStore.Get` currently returns `(nil, false)` on error — this
silently hides `SQLITE_BUSY` / `Redis LOADING` / PG connection loss
as a session-not-found result, and the runtime mints a fresh
session (§4.3 in stdlib doc — "session bootstrap"). Post-v0.18 the
Get contract widens to distinguish "not found" from "transient
error"; the runtime treats transient errors as retry-and-then-fresh-
session (bounded), preserving the current UX floor while
recovering the common case.

### 7.4 `runtime-go/rt/rt.go` (no changes needed)

All `ErrConflict` / `ErrUnavailable` / `ErrTimeout` / `ErrNetwork`
constructors already exist (`rt.go:3895-3910`). We only start using
them from `db_auth.go` / `live_store.go`.

## 8. Stdlib surface — `sky-stdlib/`

### 8.1 `Sky/Core/Error.sky` — additive

Add `RetryClass` ADT + `retryClass` helper (§5 above). Change the
body of `isRetryable` to `retryClass err /= Permanent`. Export
`RetryClass(..)` and `retryClass`.

### 8.2 `Std/Db.sky` — docstring only

No API change. Docstring updates on `exec` / `query` /
`withTransaction` explain the two-layer retry model. Add a
canonical example:

```elm
-- Sky-visible retry over a whole transaction — the correct shape
-- for handling deadlocks and serialization failures.
saveOrder : Db -> Order -> Task Error Int
saveOrder db order =
    Task.retryWith
        (Task.defaultRetryPolicy
            |> Task.withMaxAttempts 5
            |> Task.withJitter
            |> Task.retryOn (\e -> Error.retryClass e /= Error.Permanent))
        (Db.withTransaction db (\c ->
            Db.insertRow c "orders" (orderRow order)
                |> Task.andThen (\id -> …)))
```

Add an **idempotency note**: "Retry is safe for idempotent
operations. `SELECT` and `UPDATE ... WHERE id = ?` are always safe.
`INSERT` is safe only when the target table has a UNIQUE constraint
that would fail the second attempt; otherwise callers must
themselves supply an idempotency key (row `id` you generate, not
autoincrement) so a retried INSERT is a no-op on re-run."

## 9. `sky.toml` schema

Additive to the existing `[database]` and `[live]` sections. Two
new tables:

```toml
[database]
driver = "sqlite"                # unchanged
url    = "app.db"                # unchanged
# v0.18 additions
busyTimeoutMs   = 5000           # SQLite PRAGMA busy_timeout
maxOpenConns    = 10             # database/sql SetMaxOpenConns
maxIdleConns    = 5              # database/sql SetMaxIdleConns
connMaxLifetime = "5m"           # Go duration; SetConnMaxLifetime

[database.retry]
# Layer 1 (runtime-transparent) retry budget for Db_exec / Db_query.
# Skipped inside Db_withTransaction — those callers own Layer 2.
maxAttempts = 3                  # driver default per §4
baseDelayMs = 20                 # driver default per §4
maxDelayMs  = 500                # driver default per §4
jitter      = true

[live.session]
# Existing keys (relabelled under [live.session] for grouping;
# [live] port/ttl/static remain in [live]).
store     = "sqlite"
storePath = "sessions.db"
ttl       = "30m"

[live.session.retry]
# Layer 1 budget for SessionStore Get/Set/Delete. Kept tight
# because it's on the request hot path.
maxAttempts = 2
baseDelayMs = 50
maxDelayMs  = 200
jitter      = true
```

**Env-var mirrors** (existing precedence: process env > .env >
sky.toml — same as `[live] port` etc.):

* `SKY_DB_BUSY_TIMEOUT_MS`, `SKY_DB_MAX_OPEN_CONNS`,
  `SKY_DB_MAX_IDLE_CONNS`, `SKY_DB_CONN_MAX_LIFETIME`
* `SKY_DB_RETRY_MAX_ATTEMPTS`, `SKY_DB_RETRY_BASE_DELAY_MS`,
  `SKY_DB_RETRY_MAX_DELAY_MS`, `SKY_DB_RETRY_JITTER`
* `SKY_LIVE_SESSION_RETRY_MAX_ATTEMPTS` etc.

## 10. Parser changes — `src/Sky/Sky/Toml.hs`

Additive fields on `SkyConfig`:

```haskell
    , _dbBusyTimeoutMs   :: !Int         -- [database] busyTimeoutMs
    , _dbMaxOpenConns    :: !Int         -- [database] maxOpenConns
    , _dbMaxIdleConns    :: !Int         -- [database] maxIdleConns
    , _dbConnMaxLifeSecs :: !Int         -- [database] connMaxLifetime (parsed as duration)
    , _dbRetryMaxAtt     :: !Int         -- [database.retry] maxAttempts
    , _dbRetryBaseMs     :: !Int
    , _dbRetryMaxMs      :: !Int
    , _dbRetryJitter     :: !Bool
    , _liveSessRetryMaxAtt  :: !Int
    , _liveSessRetryBaseMs  :: !Int
    , _liveSessRetryMaxMs   :: !Int
    , _liveSessRetryJitter  :: !Bool
```

Handling in `applyKeyValue` adds two new section arms:

```haskell
"database.retry" -> …    -- maxAttempts / baseDelayMs / maxDelayMs / jitter
"live.session.retry" -> …
```

`parseDurationSeconds` already handles `"5m"` / `"1h"` / bare-int
form; reuse for `connMaxLifetime`. Zero / negative sentinel means
"use runtime default" (do not emit env-var setter — runtime picks
driver-specific default from §4 table).

Compile-time: `src/Sky/Build/Compile.hs` gains a small `emitConfigEnv`
extension that mirrors the existing `SKY_LIVE_*` env-seeder pattern.
No new lowering logic, no new IORef. This is the SAME pattern
`[log] format` uses (`Toml.hs:132`).

## 11. Backward compatibility

* **Error messages preserved.** The mapped kind changes; the message
  string does not. `db.exec: pq: could not serialize access due to
  concurrent update` still contains the driver text — just wrapped
  as `ErrConflict(...)` instead of `ErrIo(...)`. Callers that
  string-match on `err.Error()` continue to work; callers that
  case-match on `ErrorKind` see a widened surface (previously
  everything was `Io`).
* **`isRetryable` semantic widening** — now returns `True` on
  `Conflict`. Composable with existing `Task.retryWith` calls; no
  Sky code needs to change to benefit. Any user who INTENDED to
  reject `Conflict` from retry can switch to
  `retryOn (\e -> Error.retryClass e == Reconnect || kind e == Timeout)`.
* **`Db.query` current `(nil, false)` semantics unchanged** on
  actual not-found; only transient errors now trigger retry.
* **Session lookup fallthrough preserved**: if all retries exhaust,
  the runtime STILL mints a fresh session (current v0.16 UX). We
  never fail the request because the session store is temporarily
  unavailable; the user just loses in-flight state, same as today.

## 12. Verification gates

### 12.1 New Rust test

a Rust `cargo test` gate (rust/crates) — golden pairs of
`(driver, native error) → (ErrorKind, RetryClass)` derived from
the tables in §6. Executes via a small Go test bridge that returns
mock driver errors from a fixture list.

### 12.2 Go race tests

* `runtime-go/rt/db_errors_test.go` — unit table-driven classifier
  tests, one row per §6 code.
* `runtime-go/rt/db_retry_test.go` — bounded retry respects
  `maxAttempts`; jitter stays in `[0.5×, 1.5×]`; `RetryClass`
  gates properly; in-tx flag skips Layer 1.
* `runtime-go/rt/live_store_retry_test.go` — SessionStore retry
  under injected transient errors on all four backends; verifies
  session-not-found fallthrough behaviour is preserved.
* `runtime-go/rt/db_errors_norace_test.go -race` — 100-goroutine
  contention smoke: 100 concurrent writers on the same SQLite file
  succeed with default policy; log-once cap on retry surfaces
  fires at most once per session.

### 12.3 Example-level gate

`examples/16-session-crud` (existing) grows a `-race` contention
scenario driven by `scripts/verify-cli.sh`: spawn N=50 concurrent
`Db.withTransaction` bodies against a single SQLite; assert 0
lost writes + 0 unclassified errors + all attempts complete under
policy budget.

### 12.4 Doc updates

* `docs/stdlib.md` — Error section: new `RetryClass` + updated
  `isRetryable`.
* `docs/skydb/overview.md` — new "Handling contention" section
  with the canonical `Task.retryWith + withTransaction` shape.
* `docs/skylive/overview.md` — session-store retry note.
* `docs/sky-toml.md` — new `[database.retry]` +
  `[live.session.retry]` tables + duration format reminder.
* `CLAUDE.md` — Environment variables table: new `SKY_DB_RETRY_*`
  + `SKY_LIVE_SESSION_RETRY_*` rows.
* `templates/CLAUDE.md` — same, for `sky init`ed projects.

## 13. Risks + mitigations

| Risk | Mitigation |
|---|---|
| Retry storms overwhelm a struggling DB | Bounded max delay (500ms default) + jitter default true; explicit `maxAttempts` ceiling per policy; Layer 1 budget documented as "small, fast" and NOT tunable to 100× via env alone (hard cap at 30s per attempt = existing `retryDelayCapMs` in `task_retry.go:28`). |
| Idempotency footgun: retrying a non-idempotent INSERT doubles rows | Doc gate in §8.2 stdlib docstring + `docs/skydb/overview.md`; example includes UNIQUE-constraint gate; `sky doctor` gains a new lint (`db.retry-without-unique-key`) that warns on `INSERT` inside `Task.retryWith` without a UNIQUE column reference. |
| Double-retry against Google-cloud SDKs (Firestore) | Explicitly disable SDK-internal retry via `gax.WithRetry(gax.Backoff{})` on outbound calls; single-source-of-truth = Sky's Layer 1. |
| Semantic change to `isRetryable` (Conflict now retryable) breaks callers who relied on the old surface | Widening only; documented in release notes as v0.18.0 BEHAVIOUR CHANGE; call sites can pin the old semantics with `retryOn (\e -> kind e == Timeout || kind e == Network || kind e == Unavailable)` for the one-line rollback. |
| Layer 1 inside a tx eats the deadlock resolution the tx needs | In-tx flag threaded via `context.Context` value on the goroutine; `retryDbOp` skips when set. Regression test in `db_retry_test.go`. |
| Session-store retry budget too small for slow Postgres | Env-var override (`SKY_LIVE_SESSION_RETRY_MAX_ATTEMPTS`) documented; default sized for SSE reconnect window (~150ms). |
| Existing callers string-match error text and now see different kind | Preserved: error text is verbatim driver text, only the wrapping `ErrorKind` changes. Regression: `db_errors_test.go` asserts `.Error()` output contains full driver text. |
| Firestore `INTERNAL` marked Retryable, but repeated INTERNAL might be a broken payload | Bounded Layer 1 attempts (5); `sky_db_retry_exhausted_total{kind,driver}` counter emits on exhaustion so ops sees the pattern in the console. |

## 14. Files to touch (summary)

| File | Change |
|---|---|
| `runtime-go/rt/db_errors.go` | **NEW** — classifier + retry loop, driver-multiplex |
| `runtime-go/rt/db_auth.go` | Route every `ErrIo(err.Error())` through `classifyAndWrap`; PRAGMA busy_timeout + WAL on `Db_connect`; `context.WithValue` in-tx flag in `Db_withTransaction` |
| `runtime-go/rt/live_store.go` | Wrap Get/Set/Delete of every backend in bounded Layer 1 retry with the tighter session budget; PRAGMA busy_timeout on session-SQLite |
| `runtime-go/rt/db_errors_test.go` | **NEW** — golden native-error → (Kind, Class) tests |
| `runtime-go/rt/db_retry_test.go` | **NEW** — retry semantics + in-tx skip |
| `runtime-go/rt/live_store_retry_test.go` | **NEW** — session-store retry |
| `sky-stdlib/Sky/Core/Error.sky` | Add `RetryClass` + `retryClass`; rewrite `isRetryable` in terms of it; export new symbols |
| `sky-stdlib/Std/Db.sky` | Docstring updates; canonical two-layer example; idempotency note |
| `src/Sky/Sky/Toml.hs` | Parse `[database.retry]` + `[live.session.retry]` + `[database] busyTimeoutMs / maxOpenConns / …` fields |
| `src/Sky/Build/Compile.hs` | Emit `SKY_DB_RETRY_*` + `SKY_LIVE_SESSION_RETRY_*` env-seeding in the config-env prologue (existing pattern) |
| Rust `cargo test` gate (rust/crates) | **NEW** — cross-check the Go table |
| `examples/16-session-crud/tests/Contention.sky` | **NEW** — 50-goroutine contention smoke |
| `docs/stdlib.md`, `docs/skydb/overview.md`, `docs/skylive/overview.md`, `docs/sky-toml.md`, `CLAUDE.md`, `templates/CLAUDE.md` | Doc sync per §12.4 |

## 15. Out of scope (deferred)

* **Idempotency-key primitive** in `Std.Db.insertFields` — could
  auto-generate `id` on client side + upsert-on-conflict. Ship as
  v0.18.1 if user demand surfaces.
* **Adaptive backoff** (measure actual RTT + tune baseMs). v0.18
  ships fixed defaults; adaptive is a v0.19 candidate.
* **Circuit-breaker** on repeated Reconnect (like the HubExporter
  precedent). Sky.Live already has the SSE reconnecting-banner
  protocol which is the user-facing analogue; the internal
  circuit-breaker adds complexity without a clear user need in v0.18.
* **MySQL driver wiring in `detectDriver`** — the classifier ships
  so switching wire is a one-line change, but the actual
  `_ "github.com/go-sql-driver/mysql"` import + DSN detection is
  a separate v0.18.x task.
