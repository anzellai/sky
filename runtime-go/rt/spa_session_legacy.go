//go:build !js

package rt

// spa_session_legacy.go — the Sky.Spa session cookie name (A-2b) and the
// one-time conversion of a pre-v0.27.0 session token (E-3).
//
// COOKIE NAME (A-2b). A Sky.Live app and a Sky.Spa backend on one host both
// used the cookie `sky_sid` (cookies ignore the port), so each overwrote the
// other's session. From v0.27.0 the Spa session token is the cookie
// `sky_spa`. The legacy `sky_sid` is still READ when it holds a Spa token
// (a signed JWT, never the 32-hex id Sky.Live mints), so a browser that holds
// one keeps its session; the response then moves it to `sky_spa` and expires
// the legacy cookie.
//
// CONVERSION (E-3). A token signed before v0.27.0 has no session id (`sid`),
// so Spa_verifySession refuses it, and the visitor was signed out at the
// upgrade and lost their locally saved model (a basket, say). The conversion
// accepts such a token ONCE, for a short window:
//
//   - the first request that presents it gets a new token with the SAME
//     claims and a fresh `sid` (and the token's own `exp`), and the record
//     `spa-legacy:<sha256(token)> -> {sid, graceUntil}` is written to the
//     sign-out record store for the token's lifetime;
//   - within spaLegacyGrace every presentation (the same request verifies the
//     token once per identity field; several tabs and parallel RPCs send the
//     same cookie) gets the same `sid`, so the conversion is idempotent. The
//     process keeps the converted token, so one request sees one token;
//   - after the window the record refuses it: a copy of the legacy cookie
//     taken earlier is dead from then on, and the new `sid` can be signed
//     out like any other.
//
// A forged or expired legacy token fails the signature or `exp` check and is
// never converted. When the store cannot answer, nothing is converted (fail
// closed): the visitor signs in again, as before this change.
//
// THE GENERATED BACKEND (rust/crates/project/src/spa_split.rs) calls:
//
//	Spa_sessionToken   : Secret -> Request -> String
//	    the request's effective session token ("" when none): `sky_spa`, else
//	    a Spa token in the legacy `sky_sid`, converted when it has no `sid`.
//	    It replaces `Server.getCookie "sky_sid"` everywhere.
//	Spa_sessionCookies : Secret -> Request -> Response -> Response
//	    on every response: when the request's token came from the legacy
//	    cookie, set `sky_spa` to the effective token (unless the response
//	    already sets `sky_spa`) and expire `sky_sid`.

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// spaSessionCookieName is the Sky.Spa session cookie from v0.27.0.
const spaSessionCookieName = "sky_spa"

// spaLegacySessionCookieName is the pre-v0.27.0 Spa session cookie, shared
// with Sky.Live's `sky_sid`.
const spaLegacySessionCookieName = "sky_sid"

// spaSessionCookieAttrs are the attributes the generated backend sets on the
// session cookie.
const spaSessionCookieAttrs = "Path=/; HttpOnly; SameSite=Lax"

// spaLegacyGrace is how long a converted legacy token keeps converting to the
// same session id. A variable so tests can shorten it.
var spaLegacyGrace = 60 * time.Second

// spaLegacyKeyPrefix namespaces a conversion record in the sign-out store's
// alias table.
const spaLegacyKeyPrefix = "spa-legacy:"

type spaLegacyEntry struct {
	token      string
	graceUntil time.Time
}

var (
	spaLegacyMu    sync.Mutex
	spaLegacyCache = map[string]spaLegacyEntry{}
)

func spaLegacyKey(legacy string) string {
	h := sha256.Sum256([]byte(legacy))
	return spaLegacyKeyPrefix + hex.EncodeToString(h[:])
}

// spaEffectiveToken is Spa_sessionToken over a cookie map. fromLegacy is true
// when the token came from the legacy `sky_sid` cookie.
func spaEffectiveToken(secret any, cookies map[string]string) (token string, fromLegacy bool) {
	if v := cookies[spaSessionCookieName]; v != "" {
		return v, false
	}
	legacy := cookies[spaLegacySessionCookieName]
	if legacy == "" || looksLikeLiveSID(legacy) {
		// No cookie, or a Sky.Live session id on the same host: not ours.
		return "", false
	}
	claims, ok := okClaims(Auth_verifyToken(secret, legacy))
	if !ok {
		return "", false // forged or expired
	}
	if claimString(claims, "sid") != "" {
		// A v0.27.0 token still under the old name: Spa_verifySession
		// checks it; the response moves it to `sky_spa`.
		return legacy, true
	}
	converted, ok := spaConvertLegacy(secret, legacy, claims, time.Now())
	if !ok {
		return "", false
	}
	return converted, true
}

// spaConvertLegacy converts a verified pre-v0.27.0 token (see the file
// comment). ok is false when the window has passed or the store cannot
// answer.
func spaConvertLegacy(secret any, legacy string, claims map[string]any, now time.Time) (string, bool) {
	key := spaLegacyKey(legacy)
	spaLegacyMu.Lock()
	defer spaLegacyMu.Unlock()
	for k, e := range spaLegacyCache {
		if !now.Before(e.graceUntil) {
			delete(spaLegacyCache, k)
		}
	}
	if e, ok := spaLegacyCache[key]; ok {
		return e.token, true
	}
	st, err := spaRevocationStore()
	if err != nil {
		return "", false
	}
	exp := claimExp(claims)
	if !exp.After(now) {
		return "", false
	}
	rec, has, err := st.lookupAlias(key)
	if err != nil {
		logStructured("error", "spa.legacy-session.failed",
			"detail", "the session store could not answer; the pre-v0.27 cookie is not converted (fail closed)",
			"error", err.Error())
		return "", false
	}
	var sid string
	var graceUntil time.Time
	switch {
	case has && rec.inGrace(now):
		sid, graceUntil = rec.New, time.Unix(0, rec.GraceUntil)
	case has:
		// Converted before, and the window has passed: a replayed copy.
		return "", false
	default:
		sid = generateSkySessionID()
		graceUntil = now.Add(spaLegacyGrace)
		if err := st.putAliasUntil(key, sessionAlias{New: sid, GraceUntil: graceUntil.UnixNano()}, exp); err != nil {
			logStructured("error", "spa.legacy-session.failed",
				"detail", "the conversion of a pre-v0.27 cookie could not be recorded; it is not converted (fail closed)",
				"error", err.Error())
			return "", false
		}
	}
	keyBytes, errRes := coerceAuthSecret(secret, "spaSessionToken")
	if errRes != nil {
		return "", false
	}
	m := map[string]any{}
	for k, v := range claims {
		if !spaReservedClaims[k] {
			m[k] = v
		}
	}
	m["sid"] = sid
	m["iat"] = now.Unix()
	m["exp"] = exp.Unix()
	tok, ok := okString(signHS256Claims(keyBytes, m, "spaSessionToken"))
	if !ok {
		return "", false
	}
	spaLegacyCache[key] = spaLegacyEntry{token: tok, graceUntil: graceUntil}
	return tok, true
}

// okString unwraps an `Ok string`.
func okString(res any) (string, bool) {
	r, ok := res.(SkyResult[any, any])
	if !ok || r.Tag != 0 {
		return "", false
	}
	s, ok := r.OkValue.(string)
	return s, ok
}

// Spa_sessionToken — `Spa_sessionToken : Secret -> Request -> String`.
func Spa_sessionToken(secret, req any) any {
	r, ok := asSkyRequest(req)
	if !ok {
		return ""
	}
	tok, _ := spaEffectiveToken(secret, r.Cookies)
	return tok
}

// Spa_sessionCookies — `Spa_sessionCookies : Secret -> Request -> Response ->
// Response`. Moves a legacy-cookie session to `sky_spa` on the response.
func Spa_sessionCookies(secret, req, resp any) any {
	r, ok := asSkyRequest(req)
	if !ok {
		return resp
	}
	legacy := r.Cookies[spaLegacySessionCookieName]
	if legacy == "" || looksLikeLiveSID(legacy) {
		return resp
	}
	out, ok := asSkyResponse(resp)
	if !ok {
		return resp
	}
	if r.Cookies[spaSessionCookieName] != "" {
		// sky_spa already carries the session; the Spa token left under the
		// old name (set by a path that did not expire it) only goes.
		return addSetCookie(out, spaLegacySessionCookieName+"=; "+securifyCookieAttrs(spaSessionCookieAttrs+"; Max-Age=0"))
	}
	tok, fromLegacy := spaEffectiveToken(secret, r.Cookies)
	if !fromLegacy {
		// Forged, expired, replayed after the window, or the store could not
		// answer: nothing to move. The cookie is left alone, so a store that
		// is back on the next request can still convert it.
		return resp
	}
	setsSpa := false
	for _, c := range out.Cookies {
		if strings.HasPrefix(c, spaSessionCookieName+"=") {
			setsSpa = true
		}
	}
	if !setsSpa {
		out = addSetCookie(out, spaSessionCookieName+"="+tok+"; "+securifyCookieAttrs(spaSessionCookieAttrs))
	}
	return addSetCookie(out, spaLegacySessionCookieName+"=; "+securifyCookieAttrs(spaSessionCookieAttrs+"; Max-Age=0"))
}
