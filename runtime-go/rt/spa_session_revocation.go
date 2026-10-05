//go:build !js

// spa_session_revocation.go — server-side sign-out for the Sky.Spa signed
// session (v0.27.0).
//
// The auto-split's backend signs the session projection (the model's `Session`
// fields) into the httpOnly `sky_sid` cookie and verifies that cookie on every
// RPC, SSR render, console check and /_sky/sub request (spa_session_secret.go,
// rust/crates/project/src/spa_split.rs). Before v0.27.0 the token was checked
// only by its signature and its 30-day `exp`, so sign-out removed the cookie
// from the browser but NOT from the server's point of view: a copy of the cookie
// taken before sign-out still signed the user in until it expired. Sky.Live
// closed the same class in Phase 1A (session-id rotation on a change of user,
// and a revoked id is dead in the shared store).
//
// The Spa equivalent, three kernels the generated backend calls:
//
//   - Spa_signSession stamps a session id (`sid`) into every token. The id is
//     kept while the projection is unchanged and replaced when it changes (a
//     sign-in, a switch of account, a server-side sign-out); a replaced id is
//     ended, so the pre-change cookie stops working at once.
//   - Spa_verifySession is Auth.verifyToken plus two checks: the token has a
//     `sid`, and that `sid` has not ended.
//   - Spa_endSession (the framework sign-out endpoint POST /_rpc/__spaSignOut)
//     ends the cookie's `sid`.
//
// "Ended" is a record in the configured SESSION STORE — the same store and the
// same alias table Sky.Live uses for a retired session id (an alias whose `New`
// is empty is an ended id, live_session_rotation.go), under the key prefix
// "spa-sid:". It lives exactly as long as the token it ends (its `exp`), then
// the store's own alias sweep removes it. A store shared by the replicas
// (postgres / redis / one sqlite file) makes a sign-out on one replica refuse
// the cookie on every replica. With no store configured the record goes to a
// sqlite file in the data dir, beside the auto-minted signing secret, so a
// single node keeps its sign-outs across a restart.
//
// The check FAILS CLOSED: when the store cannot answer, the cookie is treated
// as signed out (and logged), never as a valid session.

package rt

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// spaSessionLifetimeSeconds is the signed session token's lifetime (its `exp`).
// It was the literal `2592000` in the generated `signedResponse_` before the
// signing moved here.
const spaSessionLifetimeSeconds = 30 * 24 * 60 * 60

// spaRevocationKeyPrefix namespaces a Spa sign-out record in the session
// store's alias table, so it can never be read as a Sky.Live session alias.
const spaRevocationKeyPrefix = "spa-sid:"

// spaReservedClaims are the claims the runtime owns. A projection claim never
// uses them (the projection claims are p0, p1, …), and they are left out of
// the "same projection" comparison.
var spaReservedClaims = map[string]bool{"sid": true, "iat": true, "exp": true}

// spaRev holds the one sign-out record store of this process, opened on first
// use. err non-nil means the configured store is unusable in production: every
// check then fails closed.
type spaRevState struct {
	opened bool
	store  SessionStore
	err    error
	// retryAt: when err is set by a failed open, the next open attempt is made
	// after this time, so a store that was down at first use is picked up once
	// it is back (zero: never retry, the test helper's injected error).
	retryAt time.Time
}

// spaRevRetryAfter is how long a failed store open is kept before the next
// attempt. Until then every signed session is refused (fail closed).
const spaRevRetryAfter = 30 * time.Second

var (
	spaRevMu sync.Mutex
	spaRev   spaRevState
)

// spaRevocationStore returns the sign-out record store, opening it on first
// use (and again, spaRevRetryAfter after a failed open).
func spaRevocationStore() (SessionStore, error) {
	spaRevMu.Lock()
	defer spaRevMu.Unlock()
	retry := spaRev.err != nil && !spaRev.retryAt.IsZero() && time.Now().After(spaRev.retryAt)
	if !spaRev.opened || retry {
		st, err := openSpaRevocationStore()
		spaRev = spaRevState{opened: true, store: st, err: err}
		if err != nil {
			spaRev.retryAt = time.Now().Add(spaRevRetryAfter)
		}
	}
	return spaRev.store, spaRev.err
}

// setSpaRevocationStoreForTest installs a store (or an error) and returns a
// func that restores the previous state. Tests only.
func setSpaRevocationStoreForTest(st SessionStore, err error) func() {
	spaRevMu.Lock()
	prev := spaRev
	spaRev = spaRevState{opened: true, store: st, err: err}
	spaRevMu.Unlock()
	return func() {
		spaRevMu.Lock()
		spaRev = prev
		spaRevMu.Unlock()
	}
}

// openSpaRevocationStore resolves the store the same way Sky.Live does
// (<PREFIX>_LIVE_STORE / _LIVE_STORE_PATH, the operator env over the seeded
// sky.toml `[live] store` / `storePath`). With nothing configured it uses a
// sqlite file in the data dir.
func openSpaRevocationStore() (SessionStore, error) {
	// Resolve the signing secret first: it tells whether the operator runs
	// several replicas (spaSessionSecretShared).
	_ = Spa_sessionSecret(nil)
	kind := resolveStoreKind("")
	path := resolveStorePath("")
	implicit := kind == ""
	prod := productionFromEnv()
	if implicit {
		dir := spaSecretDataDir()
		if err := os.MkdirAll(dir, 0o700); err == nil {
			kind, path = "sqlite", filepath.Join(dir, "spa-sessions.db")
		} else if prod {
			// A-6: production never keeps sign-outs in memory without a
			// word. A copy of a signed-out cookie would work again after the
			// next restart. Refuse every signed session instead.
			e := fmt.Errorf("no session store is configured and the data dir %s is not writable (%v); set SKY_LIVE_STORE, or SKY_DATA_DIR to a writable directory; see docs/migration/v0.27.md#spa-sign-out-store", dir, err)
			log.Printf("[sky.spa] ERROR: sign-outs cannot be recorded: %v. Every signed session is refused until this is fixed.", e)
			return nil, e
		} else {
			kind = "memory"
			log.Printf("[sky.spa] WARNING: the data dir %s is not writable (%v): sign-out records are kept in memory and are lost on restart", dir, err)
		}
	}
	var refusal error
	st := selectStoreAs(spaSignOutStoreBanner, kind, path, resolveSessionTTL(), 0, func(format string, args ...any) {
		if refusal == nil {
			refusal = fmt.Errorf(format, args...)
		}
	})
	if refusal != nil && implicit && prod {
		// A-6: the implicit sqlite file in the data dir cannot open (a
		// read-only image layer, a lock held by another replica). Production
		// refuses rather than falling back to memory in silence.
		_ = st.Close()
		e := fmt.Errorf("the sign-out record file in the data dir cannot open (%v); set SKY_LIVE_STORE, or SKY_DATA_DIR to a writable directory; see docs/migration/v0.27.md#spa-sign-out-store", refusal)
		log.Printf("[sky.spa] ERROR: sign-outs cannot be recorded: %v. Every signed session is refused until this is fixed.", e)
		return nil, e
	}
	if refusal != nil && !implicit {
		// A store the operator configured is unusable in production (a Sky.Live
		// app would refuse to start here). A per-process memory fallback would
		// make a sign-out on one replica invisible to the others, so refuse
		// every signed session instead: loud and safe.
		_ = st.Close()
		log.Printf("[sky.spa] ERROR: the configured session store cannot record sign-outs: %v. Every signed session is refused until the store is reachable (next attempt in %s).", refusal, spaRevRetryAfter)
		return nil, refusal
	}
	RegisterResourceCloser("spa.signOutRecords", func() {
		if err := st.Close(); err != nil {
			log.Printf("[sky.spa] sign-out record store close: %v", err)
		}
	})
	if spaSessionSecretShared && (implicit || kind == "memory") {
		log.Printf("[sky.spa] WARNING: SKY_SPA_SESSION_SECRET is set (several replicas?) but no shared " +
			"session store is configured, so a sign-out is recorded only on the replica that served it " +
			"and a copy of the signed-out cookie still works on the others. Set [live] store / " +
			"SKY_LIVE_STORE to postgres or redis (docs/skyspa/auto-split.md §25).")
	}
	return st, nil
}

// spaSignOutStoreBanner opens the store line of the sign-out record store, the
// one session store a Sky.Spa backend keeps (see appStoreBanner).
const spaSignOutStoreBanner = "[sky.spa] session store (sign-out records): "

// spaNoSessionStoreBanner opens the boot line of a backend with no session store.
const spaNoSessionStoreBanner = "[sky.spa] session store: none. "

// reportNoSpaSessionStore says, at boot, that a Sky.Spa backend with no session
// projection keeps no server-side session, and that a configured store is not
// used. Such a backend never opens a session store, so without this line the
// log said nothing about one, and the inline console's own memory store was the
// only store line an operator saw.
func reportNoSpaSessionStore() {
	msg := spaNoSessionStoreBanner + "This backend keeps no server-side session: " +
		"the model lives in the browser, and no server branch writes a `Session` / " +
		"`Maybe Session` model field, so there is no signed session to sign out."
	// Name the store AND who set it (resolveStoreKindSource): the app's own
	// `config` binding, sky.toml, or the operator's environment.
	if kind, source := resolveStoreKindSource(); kind != "" {
		msg += fmt.Sprintf(" The session store %q, set by %s, is not used.", kind, source)
	}
	log.Print(msg)
}

// Spa_sessionBoot — `Spa_sessionBoot : Bool -> Task Error ()`. The generated
// `main` of every Sky.Spa backend runs it before the server starts; the Bool
// says whether the app has a session projection. Without one it only reports
// that the backend keeps no server-side session (reportNoSpaSessionStore).
// With one (A-6) it resolves the signing key (A-7: a production key
// that cannot be persisted is reported in the start-up report) and opens
// the sign-out record store eagerly. In production with no store configured
// and no writable data dir it is an Err, so the backend refuses to start
// instead of recording sign-outs nowhere. A store the operator configured
// that is down at boot is NOT an Err: every signed session is refused until
// it answers, and the open is retried (spaRevRetryAfter).
func Spa_sessionBoot(withSession any) any {
	return func() any {
		if b, ok := withSession.(bool); ok && !b {
			reportNoSpaSessionStore()
			return Ok[any, any](struct{}{})
		}
		_ = Spa_sessionSecret(nil)
		_, err := spaRevocationStore()
		if err != nil && productionFromEnv() && resolveStoreKind("") == "" {
			return Err[any, any](ErrUnavailable("Sky.Spa did not start: sign-outs cannot be recorded: " + err.Error()))
		}
		return Ok[any, any](struct{}{})
	}
}

// spaSessionEnded reports whether a session id was signed out.
func spaSessionEnded(sid string) (bool, error) {
	st, err := spaRevocationStore()
	if err != nil {
		return false, err
	}
	a, ok, err := st.lookupAlias(spaRevocationKeyPrefix + sid)
	if err != nil {
		return false, err
	}
	return ok && a.New == "", nil
}

// spaEndSid records a session id as ended until exp (the token's own expiry).
// An id whose token has already expired needs no record.
func spaEndSid(sid string, exp time.Time) error {
	if sid == "" || !exp.After(time.Now()) {
		return nil
	}
	st, err := spaRevocationStore()
	if err != nil {
		return err
	}
	return st.putAliasUntil(spaRevocationKeyPrefix+sid, sessionAlias{}, exp)
}

// okClaims unwraps an `Ok claims` from Auth_verifyToken.
func okClaims(res any) (map[string]any, bool) {
	r, ok := res.(SkyResult[any, any])
	if !ok || r.Tag != 0 {
		return nil, false
	}
	m, ok := r.OkValue.(map[string]any)
	return m, ok
}

func claimString(claims map[string]any, key string) string {
	s, _ := claims[key].(string)
	return s
}

// claimExp reads the numeric `exp` claim (a JWT number decodes as float64).
func claimExp(claims map[string]any) time.Time {
	switch v := claims["exp"].(type) {
	case float64:
		return time.Unix(int64(v), 0)
	case int64:
		return time.Unix(v, 0)
	case int:
		return time.Unix(int64(v), 0)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return time.Unix(n, 0)
		}
	}
	return time.Time{}
}

// spaSameProjection reports whether a verified token carries exactly the
// projection claims `next` (the reserved claims aside).
func spaSameProjection(prev, next map[string]any) bool {
	count := 0
	for k, pv := range prev {
		if spaReservedClaims[k] {
			continue
		}
		count++
		nv, ok := next[k]
		if !ok || !jsonEqual(pv, nv) {
			return false
		}
	}
	return count == len(next)
}

func jsonEqual(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

// Spa_verifySession : Secret -> String -> Result Error a
//
// Auth.verifyToken, then: the token must carry a session id, and that id must
// not have been signed out. A store that cannot answer refuses the token.
func Spa_verifySession(secret, token any) any {
	res := Auth_verifyToken(secret, token)
	claims, ok := okClaims(res)
	if !ok {
		return res
	}
	sid := claimString(claims, "sid")
	if sid == "" {
		// A token signed before v0.27.0 has no session id and cannot be
		// signed out, so it is not accepted: the visitor signs in once more.
		return Err[any, any](ErrPermissionDenied("sky_sid: the token has no session id; sign in again"))
	}
	ended, err := spaSessionEnded(sid)
	if err != nil {
		logStructured("error", "spa.session-check.failed",
			"detail", "the session store could not answer the sign-out check; the cookie is refused (fail closed)",
			"error", err.Error())
		return Err[any, any](ErrUnavailable("sky_sid: the session store is unavailable"))
	}
	if ended {
		return Err[any, any](ErrPermissionDenied("sky_sid: the session was signed out"))
	}
	return res
}

// Spa_signSession : Secret -> String -> a -> Result Error String
//
// (secret, the request's current `sky_sid` value or "", the projection claims)
// Signs the projection with a session id. When the current cookie is valid and
// carries the same projection, its id is kept. Otherwise a new id is minted,
// and the current cookie's id (if any) is ended: the identity changed, so the
// old cookie must stop working now, not at its expiry.
func Spa_signSession(secret, prevToken, claims any) any {
	keyBytes, errRes := coerceAuthSecret(secret, "spaSignSession")
	if errRes != nil {
		return errRes
	}
	m := authClaimsToMap(claims)
	for k := range spaReservedClaims {
		delete(m, k)
	}
	sid := ""
	if prev, ok := prevToken.(string); ok && prev != "" {
		if pc, ok := okClaims(Spa_verifySession(secret, prev)); ok {
			psid := claimString(pc, "sid")
			if spaSameProjection(pc, m) {
				sid = psid
			} else if err := spaEndSid(psid, claimExp(pc)); err != nil {
				logStructured("error", "spa.session-rotate.failed",
					"detail", "the previous session id could not be ended; its cookie stays valid until it expires",
					"error", err.Error())
			}
		}
	}
	if sid == "" {
		sid = generateSkySessionID()
	}
	now := time.Now().Unix()
	m["sid"] = sid
	m["iat"] = now
	m["exp"] = now + spaSessionLifetimeSeconds
	return signHS256Claims(keyBytes, m, "spaSignSession")
}

// Spa_endSession : Secret -> String -> Task Error ()
//
// (secret, the request's `sky_sid` value or "") Ends the cookie's session id
// for the rest of its lifetime. A missing, forged or expired cookie has nothing
// to end and succeeds. A store write that fails is an Err, so the sign-out
// endpoint can say so instead of reporting a sign-out that did not happen.
func Spa_endSession(secret, token any) any {
	capSecret, capToken := secret, token
	return func() any {
		tok, _ := capToken.(string)
		if tok == "" {
			return Ok[any, any](struct{}{})
		}
		claims, ok := okClaims(Auth_verifyToken(capSecret, tok))
		if !ok {
			return Ok[any, any](struct{}{})
		}
		if err := spaEndSid(claimString(claims, "sid"), claimExp(claims)); err != nil {
			logStructured("error", "spa.sign-out.failed",
				"detail", "the session id could not be recorded as ended; a copy of the cookie stays valid until it expires",
				"error", err.Error())
			return Err[any, any](ErrUnavailable("sign-out: the session store could not record the sign-out"))
		}
		return Ok[any, any](struct{}{})
	}
}
