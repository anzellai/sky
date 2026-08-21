//go:build !js

// auth_revocation.go — PULL-model user revocation + suspension for Std.Auth.
//
// The model (user-confirmed, single gate, PULL). Revocation and suspension are
// STATE in one shared table on the application `Db` (NOT the session store —
// the session store defaults to memory and would not be shared across
// replicas). Enforcement is ONE boolean gate each live session evaluates about
// ITSELF, answered by a FRESH shared-DB read (or a ≤TTL per-replica cache) that
// every replica performs independently. No broker, no session index, no
// cross-replica fan-out. On a Revoked/Disabled verdict the session is EVICTED
// (markDone + store.Delete), not merely 404'd, and the browser receives the
// existing session-lost signal.
//
// Two pieces of state, both keyed by the CANONICAL string form of the user id
// (canonicalSub — force string-keying so an Int id and its text spelling never
// diverge, and a float64 JWT sub never misformats through %v):
//
//   - sky_revocations(user_id TEXT PRIMARY KEY, revoked_at BIGINT) — "kill the
//     user's existing sessions/tokens". A session/token is revoked when its
//     issue time (iat / boundAt) predates revoked_at. A FRESH login after the
//     revoke is NOT revoked (revoke != ban).
//   - users.disabled_at BIGINT (nullable) — "ban the user". Checked in login
//     BEFORE verifyPassword (the re-login lock-out) and, first, by the session
//     gate.
//
// Neither field is ever written onto the session blob (storableSession) — a
// stored copy would let replica B serve a stale verdict; the gate always reads
// the shared table (or a ≤TTL cache). boundAt is immutable (set once at bind)
// and DOES ride on the session.

package rt

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// ─── canonicalSub ──────────────────────────────────────────────────
//
// canonicalSub forces a subject/user-id of any runtime shape to ONE canonical
// string, used on BOTH the write side (revokeUser/disableUser) and the read
// side (the gate + isRevoked) so the key can never diverge between them.
//
//   - string  → verbatim (an OAuth sub, or an Int id already spelled as text;
//     a large id passed as a String is exact — no float64 round-trip).
//   - int / int64 / int32 → strconv.FormatInt (never %v).
//   - float64 (the shape a JWT numeric claim decodes as) → FormatInt of the
//     integral value; a NON-integral or out-of-range float is REJECTED ("").
//     A JWT number above 2^53 has already lost precision in the wire decode —
//     document the caveat and pass large ids as Strings.
//
// A "" return is the "cannot identify the subject" signal; callers treat it as
// a no-op (never a silent match).
func canonicalSub(v any) string {
	switch s := v.(type) {
	case nil:
		return ""
	case string:
		return s
	case int:
		return strconv.FormatInt(int64(s), 10)
	case int64:
		return strconv.FormatInt(s, 10)
	case int32:
		return strconv.FormatInt(int64(s), 10)
	case uint:
		return strconv.FormatUint(uint64(s), 10)
	case uint64:
		return strconv.FormatUint(s, 10)
	case float64:
		if math.IsNaN(s) || math.IsInf(s, 0) || s != math.Trunc(s) {
			return "" // non-integral → not a valid id
		}
		if s < math.MinInt64 || s >= 9223372036854775807.0 {
			return "" // out of int64 range
		}
		return strconv.FormatInt(int64(s), 10)
	case float32:
		return canonicalSub(float64(s))
	default:
		return ""
	}
}

// ─── DDL: self-create + idempotent migrate ─────────────────────────

// ensureRevocationsTable self-creates sky_revocations on first use, exactly as
// Auth_register self-creates users. Dialect-safe (no dialect-specific column
// types). No auto-prune — the row count is bounded by the number of revoked
// users, and a TTL-parameterised prune is a footgun (a short app-side TTL would
// resurrect a still-revoked user).
func ensureRevocationsTable(d *SkyDb) error {
	_, err := d.conn.Exec(`CREATE TABLE IF NOT EXISTS sky_revocations (
		user_id TEXT PRIMARY KEY,
		revoked_at BIGINT NOT NULL
	)`)
	return err
}

// ensureUsersDisabledColumn adds users.disabled_at idempotently to a table that
// predates this feature. Dialect-safe:
//   - Postgres has ADD COLUMN IF NOT EXISTS.
//   - SQLite has no IF NOT EXISTS for ADD COLUMN, so we swallow the
//     "duplicate column" error a second call raises.
//
// A users table created AFTER this feature already carries the column (see
// Auth_register), so this is a no-op there.
func ensureUsersDisabledColumn(d *SkyDb) error {
	if d.driver == "pgx" {
		_, err := d.conn.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS disabled_at BIGINT`)
		return err
	}
	_, err := d.conn.Exec(`ALTER TABLE users ADD COLUMN disabled_at BIGINT`)
	if err != nil && isDuplicateColumnErr(err) {
		return nil // already present — idempotent
	}
	return err
}

// isDuplicateColumnErr recognises the SQLite "duplicate column name" error so a
// re-run of ensureUsersDisabledColumn is a no-op rather than a hard failure.
func isDuplicateColumnErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return containsFold(msg, "duplicate column") || containsFold(msg, "already exists")
}

// containsFold is a tiny case-insensitive substring test (avoids importing
// strings.Contains + strings.ToLower churn at every call).
func containsFold(haystack, needle string) bool {
	h := []byte(haystack)
	n := []byte(needle)
	if len(n) == 0 {
		return true
	}
	lower := func(b byte) byte {
		if b >= 'A' && b <= 'Z' {
			return b + 32
		}
		return b
	}
	for i := 0; i+len(n) <= len(h); i++ {
		ok := true
		for j := 0; j < len(n); j++ {
			if lower(h[i+j]) != lower(n[j]) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// ─── Go-side verdict helpers (used by BOTH the kernels and the live gate) ──

// authRevokedAt reads the revoked_at epoch for a canonical user id from the
// shared table, or (0, false) when the user has no revocation row. FRESH read —
// no session-blob copy.
func authRevokedAt(d *SkyDb, uid string) (int64, bool, error) {
	if err := ensureRevocationsTable(d); err != nil {
		return 0, false, err
	}
	var revokedAt int64
	q := fmt.Sprintf("SELECT revoked_at FROM sky_revocations WHERE user_id = %s", d.placeholder(1))
	err := d.conn.QueryRow(q, uid).Scan(&revokedAt)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return revokedAt, true, nil
}

// authIsRevoked answers "is a token/session issued at `iat` for `uid` revoked?"
// — true iff a revocation row exists AND iat < revoked_at. A fresh bind AFTER
// the revoke (boundAt >= revoked_at) is NOT revoked (revoke != ban).
func authIsRevoked(d *SkyDb, uid string, iat int64) (bool, error) {
	revokedAt, has, err := authRevokedAt(d, uid)
	if err != nil || !has {
		return false, err
	}
	return iat < revokedAt, nil
}

// authIsDisabled answers "is `uid` disabled (banned)?" — true iff
// users.disabled_at is non-null. FRESH read. Migrates the column in on first
// use so a pre-feature users table answers correctly. An app that does NOT use
// the built-in Std.Auth users table (OAuth-only, or a custom user store) has no
// `users` table at all — that is "not banned via this mechanism", NOT an error,
// so a missing table reads false.
func authIsDisabled(d *SkyDb, uid string) (bool, error) {
	if err := ensureUsersDisabledColumn(d); err != nil {
		if isMissingTableErr(err) {
			return false, nil // no built-in users table → disable-ban not in play
		}
		return false, err
	}
	var disabledAt sql.NullInt64
	// CAST(id AS TEXT) makes the integer built-in users.id comparable to the
	// canonical STRING uid on BOTH dialects (Postgres would reject
	// `integer = text` directly; SQLite is loose but we stay explicit).
	q := fmt.Sprintf("SELECT disabled_at FROM users WHERE CAST(id AS TEXT) = %s", d.placeholder(1))
	err := d.conn.QueryRow(q, uid).Scan(&disabledAt)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		if isMissingTableErr(err) {
			return false, nil
		}
		return false, err
	}
	return disabledAt.Valid && disabledAt.Int64 > 0, nil
}

// isMissingTableErr recognises "no users table exists" across SQLite
// ("no such table") and Postgres ("relation \"users\" does not exist").
func isMissingTableErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return containsFold(msg, "no such table") ||
		(containsFold(msg, "does not exist") && containsFold(msg, "users")) ||
		containsFold(msg, "undefined table")
}

// ─── Kernels: admin write APIs (STRING userId — canonicalSub'd) ─────

// Auth.revokeUser : Db -> String -> Task Error ()
// == invalidateSessions: upsert revoked_at = now. Every session/token for the
// user whose iat/boundAt predates now stops passing the gate. A fresh login
// after this call mints a later iat and is unaffected (revoke != ban).
func Auth_revokeUser(db any, userId any) any {
	capDb, capUid := db, userId
	return func() any {
		return WithAuthSpan("revokeUser", func() any {
			d, ok := capDb.(*SkyDb)
			if !ok {
				return Err[any, any](ErrInvalidInput("auth.revokeUser: not a Db"))
			}
			uid := canonicalSub(capUid)
			if uid == "" {
				return Err[any, any](ErrInvalidInput("auth.revokeUser: empty / non-identifying userId"))
			}
			if err := ensureRevocationsTable(d); err != nil {
				return Err[any, any](ErrFfi("auth.revokeUser create: " + err.Error()))
			}
			now := time.Now().Unix()
			q := fmt.Sprintf(
				"INSERT INTO sky_revocations (user_id, revoked_at) VALUES (%s, %s) "+
					"ON CONFLICT (user_id) DO UPDATE SET revoked_at = excluded.revoked_at",
				d.placeholder(1), d.placeholder(2))
			if _, err := d.conn.Exec(q, uid, now); err != nil {
				return Err[any, any](ErrFfi("auth.revokeUser: " + err.Error()))
			}
			noteRevocationUsed()
			invalidateAccessCache(uid)
			return Ok[any, any](struct{}{})
		})
	}
}

// Auth.disableUser : Db -> String -> Task Error ()
// Lock-out (ban): sets users.disabled_at = now. The login path checks this
// BEFORE verifyPassword, so a disabled user cannot re-authenticate, and the
// session gate treats Disabled as an eviction verdict (checked first).
func Auth_disableUser(db any, userId any) any {
	capDb, capUid := db, userId
	return func() any {
		return WithAuthSpan("disableUser", func() any {
			d, ok := capDb.(*SkyDb)
			if !ok {
				return Err[any, any](ErrInvalidInput("auth.disableUser: not a Db"))
			}
			uid := canonicalSub(capUid)
			if uid == "" {
				return Err[any, any](ErrInvalidInput("auth.disableUser: empty / non-identifying userId"))
			}
			if err := ensureUsersDisabledColumn(d); err != nil {
				return Err[any, any](ErrFfi("auth.disableUser migrate: " + err.Error()))
			}
			now := time.Now().Unix()
			q := fmt.Sprintf(
				"UPDATE users SET disabled_at = %s WHERE CAST(id AS TEXT) = %s",
				d.placeholder(1), d.placeholder(2))
			if _, err := d.conn.Exec(q, now, uid); err != nil {
				return Err[any, any](ErrFfi("auth.disableUser: " + err.Error()))
			}
			noteRevocationUsed()
			invalidateAccessCache(uid)
			return Ok[any, any](struct{}{})
		})
	}
}

// Auth.enableUser : Db -> String -> Task Error ()
// Reverses disableUser: clears users.disabled_at. (Does NOT clear a revocation —
// revoke and disable are independent; call the user's fresh login to move past a
// revoke.)
func Auth_enableUser(db any, userId any) any {
	capDb, capUid := db, userId
	return func() any {
		return WithAuthSpan("enableUser", func() any {
			d, ok := capDb.(*SkyDb)
			if !ok {
				return Err[any, any](ErrInvalidInput("auth.enableUser: not a Db"))
			}
			uid := canonicalSub(capUid)
			if uid == "" {
				return Err[any, any](ErrInvalidInput("auth.enableUser: empty / non-identifying userId"))
			}
			if err := ensureUsersDisabledColumn(d); err != nil {
				return Err[any, any](ErrFfi("auth.enableUser migrate: " + err.Error()))
			}
			q := fmt.Sprintf(
				"UPDATE users SET disabled_at = NULL WHERE CAST(id AS TEXT) = %s",
				d.placeholder(1))
			if _, err := d.conn.Exec(q, uid); err != nil {
				return Err[any, any](ErrFfi("auth.enableUser: " + err.Error()))
			}
			invalidateAccessCache(uid)
			return Ok[any, any](struct{}{})
		})
	}
}

// Auth.isRevoked : Db -> String -> Int -> Task Error Bool
// True iff a token/session issued at `iat` for `userId` predates the user's
// revoked_at. This is also the default slide-stopper the sliding middleware's
// revokedCheck can be wired to.
func Auth_isRevoked(db any, userId any, iat any) any {
	capDb, capUid, capIat := db, userId, iat
	return func() any {
		return WithAuthSpan("isRevoked", func() any {
			d, ok := capDb.(*SkyDb)
			if !ok {
				return Err[any, any](ErrInvalidInput("auth.isRevoked: not a Db"))
			}
			uid := canonicalSub(capUid)
			if uid == "" {
				// Cannot identify the subject → cannot claim revoked. Report
				// false (not an error) so a caller loop stays simple; the
				// admin write path is where an empty id is rejected.
				return Ok[any, any](false)
			}
			revoked, err := authIsRevoked(d, uid, int64(AsInt(capIat)))
			if err != nil {
				return Err[any, any](ErrFfi("auth.isRevoked: " + err.Error()))
			}
			return Ok[any, any](revoked)
		})
	}
}

// Auth.isDisabled : Db -> String -> Task Error Bool
// True iff `userId` is disabled (users.disabled_at non-null). Primitive behind
// userAccessState + the login lock-out.
func Auth_isDisabled(db any, userId any) any {
	capDb, capUid := db, userId
	return func() any {
		return WithAuthSpan("isDisabled", func() any {
			d, ok := capDb.(*SkyDb)
			if !ok {
				return Err[any, any](ErrInvalidInput("auth.isDisabled: not a Db"))
			}
			uid := canonicalSub(capUid)
			if uid == "" {
				return Ok[any, any](false)
			}
			disabled, err := authIsDisabled(d, uid)
			if err != nil {
				return Err[any, any](ErrFfi("auth.isDisabled: " + err.Error()))
			}
			return Ok[any, any](disabled)
		})
	}
}

// ─── Access-state verdict (Go) — Disabled checked first ─────────────

const (
	accessActive   = 0
	accessRevoked  = 1
	accessDisabled = 2
)

// authAccessState is the single Go verdict the live gate consults. Disabled is
// checked FIRST (a ban outranks a session-kill), then revocation against the
// session's immutable boundAt. Any DB error fails OPEN (accessActive) rather
// than evicting every session on a transient outage — the eviction is a
// security convenience layered over the login lock-out + token revokedCheck,
// and a DB blip must not log every user out.
func authAccessState(d *SkyDb, uid string, boundAt int64) (int, error) {
	disabled, err := authIsDisabled(d, uid)
	if err != nil {
		return accessActive, err
	}
	if disabled {
		return accessDisabled, nil
	}
	revoked, err := authIsRevoked(d, uid, boundAt)
	if err != nil {
		return accessActive, err
	}
	if revoked {
		return accessRevoked, nil
	}
	return accessActive, nil
}

// ─── The live-app revocation gate config (process-global) ───────────

// revocationGateConfig is installed by Live.withRevocation. It carries the
// application Db the gate reads (the shared table lives here, NOT on the
// session store) and the per-replica cache TTL. Absent ⇒ the gate is inert and
// every dispatch runs unchanged (public/anonymous apps pay nothing).
type revocationGateConfig struct {
	db  *SkyDb
	ttl time.Duration // per-replica cache window; 0 = fresh read every eval
}

var revocationGateCfg atomic.Pointer[revocationGateConfig]

// setRevocationGate installs the gate. Called from Live.withRevocation wiring.
func setRevocationGate(db *SkyDb, ttl time.Duration) {
	if db == nil {
		return
	}
	revocationGateCfg.Store(&revocationGateConfig{db: db, ttl: ttl})
}

// ResetRevocationGate clears the gate. Test-only.
func ResetRevocationGate() {
	revocationGateCfg.Store(nil)
	accessCache.Range(func(k, _ any) bool { accessCache.Delete(k); return true })
	revocationUsed.Store(false)
	unboundWarned.Store(false)
	unboundGateHits.Store(0)
	noGateWarned.Store(false)
}

func getRevocationGate() *revocationGateConfig { return revocationGateCfg.Load() }

// revocationGateEnabled reports whether enforcement is active — i.e. the app
// opted in via Live.withRevocation (the only source of the Db the gate needs).
func revocationGateEnabled() bool { return getRevocationGate() != nil }

// ─── "revocation used but not wired" detection (loud-on-misconfig) ──

var revocationUsed atomic.Bool
var noGateWarned atomic.Bool

// noteRevocationUsed records that revokeUser/disableUser has run at least once.
// If the app never registered Live.withRevocation, the session gate cannot
// enforce — warn once so the operator is never silently unprotected.
func noteRevocationUsed() {
	revocationUsed.Store(true)
	if !revocationGateEnabled() && noGateWarned.CompareAndSwap(false, true) {
		fmt.Fprintf(os.Stderr,
			"[WARN] Auth.revokeUser/disableUser was called but Live.withRevocation "+
				"is not registered — live sessions will NOT be evicted. Add "+
				"`|> Live.withRevocation db` to your Live.config, and bind sessions "+
				"with Live.bindSessionUser (or auto-bind via Live.withAuthSliding).\n")
	}
}

// ─── Per-replica ≤TTL cache (scale knob; default fresh) ─────────────

type accessCacheEntry struct {
	state int
	at    time.Time
}

var accessCache sync.Map // uid(string) -> accessCacheEntry

// accessStateCached resolves the verdict for uid, honouring the gate's TTL. At
// ttl == 0 (the shipped default) every call is a FRESH shared-table read — the
// property gate #8 (cross-replica) relies on. A positive TTL trades ≤ttl of
// revocation latency for fewer reads on the hot path; document the latency.
func (g *revocationGateConfig) accessStateCached(uid string, boundAt int64) (int, error) {
	if g.ttl > 0 {
		if v, ok := accessCache.Load(uid); ok {
			if e, ok := v.(accessCacheEntry); ok && time.Since(e.at) < g.ttl {
				return e.state, nil
			}
		}
	}
	state, err := authAccessState(g.db, uid, boundAt)
	if err != nil {
		return state, err
	}
	if g.ttl > 0 {
		accessCache.Store(uid, accessCacheEntry{state: state, at: time.Now()})
	}
	return state, nil
}

// invalidateAccessCache drops a user's cached verdict so a same-replica
// revoke/disable takes effect immediately even under a positive TTL.
func invalidateAccessCache(uid string) {
	accessCache.Delete(uid)
}

// revocationCacheTTLFromEnv reads the optional per-replica cache window from
// SKY_LIVE_REVOCATION_CACHE_TTL (whole seconds). Default 0 = a fresh shared-DB
// read on every gate eval (instant cross-replica revocation, the safe default).
// A positive value trades ≤TTL of revocation latency for fewer reads on the hot
// path — document that latency to operators who set it.
func revocationCacheTTLFromEnv() time.Duration {
	// skyGetenv applies the project's `[env] prefix`, so an app with a custom
	// prefix reaches this setting (SKY_LIVE_REVOCATION_CACHE_TTL by default).
	raw := skyGetenv("LIVE_REVOCATION_CACHE_TTL")
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}
