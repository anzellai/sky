// auth_sliding.go — opt-in rolling JWT re-issue with an absolute-lifetime cap.
//
// The problem this solves. A fixed-expiry auth token forces a hard choice:
// a SHORT expiry logs active users out mid-session, a LONG expiry means a
// stolen token is usable for the whole window. A SLIDING token re-issues a
// fresh short window on activity, so an active user stays signed in while an
// IDLE token still lapses on schedule — with an ABSOLUTE cap (`aexp`) so no
// amount of activity extends a session past a hard ceiling, and an optional
// per-user revocation hook consulted at re-issue so a revoked user's token
// stops sliding.
//
// Three pieces, all opt-in:
//
//   - Auth.signSlidingToken (db_auth.go) — stamps iat/exp/aexp/w at login.
//   - Live.withAuthSliding (live_config.go) — registers the middleware +
//     its cookie/secret/sameSite/revokedCheck config.
//   - AuthSlidingMiddleware (here) — per request, verifies the token FIRST,
//     then (past half-life, under the cap, not revoked) re-issues it, setting
//     the cookie through the SAME builder-owned attribute source the login
//     setter uses (buildSlidingAuthCookie) so the two cannot drift (G4).
//
// SECURITY ORDERING (the middleware step order IS the gate — see
// maybeSlideAuthToken): VERIFY happens before any claim is read, so an
// expired or tampered token can never be resurrected — verifyToken bails
// (db_auth.go:1942) before aexp/iat/exp/w are decodable. A missing/malformed
// aexp/iat/exp/w fails CLOSED (no slide). The absolute cap and the revocation
// hook are both checked before the re-sign.

package rt

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// authSlidingConfig is the runtime-side reflection of the Sky record passed to
// `Live.withAuthSliding`. Built once (parseAuthSlidingConfig) from the builder
// record and stored globally; the middleware and the cookie setter both read it
// so there is ONE source of the cookie name + SameSite + revocation hook.
type authSlidingConfig struct {
	cookie    string // the auth cookie name (e.g. "sky_auth")
	secretEnv string // the ENV-VAR NAME holding the HMAC secret (never the value)
	sameSite  string // "Strict" (default) / "Lax" / "None"
	// revokedCheck is the unwrapped Sky closure `sub -> Task Error Bool`, or nil
	// when the builder passed `Nothing`. Consulted at RE-ISSUE time only.
	revokedCheck any
}

// authSlidingCfg holds the process-wide sliding-auth config. nil ⇒ the feature
// is not registered and the middleware passes every request through untouched.
// Mirrors consoleAuthCallback's single-app global model.
var authSlidingCfg atomic.Pointer[authSlidingConfig]

// SetAuthSlidingConfig parses the `Live.withAuthSliding` builder record and
// installs it as the process sliding-auth config. A half-configured record
// (missing cookie or secretEnv) installs nothing — the feature stays inert
// (G5: signSlidingToken without a live middleware is just a fixed-exp token).
func SetAuthSlidingConfig(rec any) {
	cfg := parseAuthSlidingConfig(rec)
	authSlidingCfg.Store(cfg) // nil-tolerant: clears when cfg == nil
}

// ResetAuthSlidingConfig clears the config. Test-only; production never calls it.
func ResetAuthSlidingConfig() { authSlidingCfg.Store(nil) }

func getAuthSlidingConfig() *authSlidingConfig { return authSlidingCfg.Load() }

// parseAuthSlidingConfig reads the builder record. Returns nil when the record
// is absent or half-configured (no cookie / no secretEnv) so a partial opt-in
// never mints (G5).
func parseAuthSlidingConfig(rec any) *authSlidingConfig {
	if rec == nil {
		return nil
	}
	cookie := slidingStringOf(Field(rec, "Cookie"))
	secretEnv := slidingStringOf(Field(rec, "SecretEnv"))
	if cookie == "" || secretEnv == "" {
		return nil
	}
	sameSite := slidingStringOf(Field(rec, "SameSite"))
	if strings.TrimSpace(sameSite) == "" {
		sameSite = "Strict" // documented default, matches CSRF's default
	}
	return &authSlidingConfig{
		cookie:       cookie,
		secretEnv:    secretEnv,
		sameSite:     sameSite,
		revokedCheck: slidingUnwrapMaybe(Field(rec, "RevokedCheck")),
	}
}

// slidingStringOf coerces a Field value to a trimmed-away-nothing string.
func slidingStringOf(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// slidingUnwrapMaybe turns a Sky `Maybe f` into the wrapped `f` (Just) or nil
// (Nothing / absent). Reuses the generic ADT accessors used by the console-auth
// Maybe handling.
func slidingUnwrapMaybe(v any) any {
	if v == nil {
		return nil
	}
	if consoleIsMaybeNothing(v) {
		return nil
	}
	if j := consoleUnwrapMaybeJust(v); j != nil {
		return j
	}
	// Not a recognised Maybe wrapper (e.g. a bare closure) — treat as the value
	// itself so a caller that passed the function directly still works.
	return v
}

// parseSlidingSameSite maps the builder's SameSite string to the http enum.
// Empty / unknown → Strict (the secure default). "None" additionally forces
// Secure via cookieSecureFor (spec requirement).
func parseSlidingSameSite(s string) http.SameSite {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "lax":
		return http.SameSiteLaxMode
	case "none":
		return http.SameSiteNoneMode
	default: // "", "strict", or anything unrecognised
		return http.SameSiteStrictMode
	}
}

// buildSlidingAuthCookie is the SINGLE SOURCE of the sliding auth cookie's
// attributes (G4). BOTH the login setter (Auth.setSlidingCookie) and the
// re-issue path (maybeSlideAuthToken) build the cookie through here, so the
// two can never drift:
//
//   - Path=/           — same scope as the session / CSRF cookies.
//   - HttpOnly         — JS must never read the auth token.
//   - SameSite         — the builder's value (default Strict).
//   - Secure           — cookieSecureFor(r, name, sameSite): the SHARED helper
//     the session / CSRF / console cookies all use, so the
//     Secure decision cannot drift from them.
//   - MaxAge           — slidingCookieMaxAgeSeconds(resolveSessionTTL()): a
//     long floor that outlives the session it carries,
//     exactly like sky_sid / __sky_csrf.
func buildSlidingAuthCookie(r *http.Request, name, value, sameSite string) *http.Cookie {
	ss := parseSlidingSameSite(sameSite)
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   slidingCookieMaxAgeSeconds(resolveSessionTTL()),
		SameSite: ss,
		Secure:   cookieSecureFor(r, name, ss),
	}
}

// ─── The middleware ────────────────────────────────────────────────

// AuthSlidingMiddleware wraps the mux (mounted at live.go:4345, alongside CSRF)
// ONLY when Live.withAuthSliding registered a config. It re-issues a sliding
// auth token on activity; every other request passes straight through.
func AuthSlidingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg := getAuthSlidingConfig(); cfg != nil {
			// Set the re-issued cookie BEFORE the handler runs (like CSRF), so
			// the Set-Cookie header is on the response the handler completes.
			// The verified token subject is returned for PULL-model auto-bind:
			// stash it on the request context so the Sky.Live session handlers
			// bind the session to this user automatically — a token app never
			// has to remember Live.bindSessionUser.
			if sub := maybeSlideAuthToken(w, r, cfg); sub != "" {
				r = r.WithContext(withAutoBindSub(r.Context(), sub))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// slidingSecretWarnOnce ensures the "secret env unset" warning is emitted at
// most once per process — a per-request warn would flood the log.
var slidingSecretWarnOnce sync.Once

// maybeSlideAuthToken runs the ordered sliding decision. The ORDER is the
// security gate (G1). See the numbered steps. Returns the VERIFIED token
// subject (canonicalised) for PULL-model auto-bind — "" whenever the token is
// absent / unverifiable / carries no usable sub. The subject is returned even
// when the token does NOT re-issue (auto-bind must work on every authenticated
// request, not only past half-life), and is derived only from VERIFIED claims.
func maybeSlideAuthToken(w http.ResponseWriter, r *http.Request, cfg *authSlidingConfig) (verifiedSub string) {
	// 1. Auth cookie present? Absent ⇒ unauthenticated; not our job to mint.
	c, err := r.Cookie(cfg.cookie)
	if err != nil || c.Value == "" {
		return ""
	}
	// 2. Secret readable? Empty ⇒ fail-OPEN on the read (never mint), warn once.
	//    secretEnv is the operator-chosen env-var NAME, read verbatim (no prefix
	//    mangling) — it is not a Sky-config surface.
	secret := os.Getenv(cfg.secretEnv)
	if secret == "" {
		slidingSecretWarnOnce.Do(func() {
			fmt.Fprintf(os.Stderr,
				"[WARN] auth.sliding secretEnv=%s is unset; sliding re-issue disabled\n",
				cfg.secretEnv)
		})
		return ""
	}
	// 3. VERIFY FIRST via the existing Auth_verifyToken. An expired / tampered
	//    token fails here — BEFORE any claim is decodable — so it can never be
	//    resurrected (this ordering IS the no-resurrection guarantee).
	claims, ok := slidingVerifiedClaims(secret, c.Value)
	if !ok {
		return ""
	}
	// Capture the VERIFIED subject for auto-bind. Returned in every post-verify
	// path below, whether or not the token re-issues.
	if s, hasSub := slidingClaimString(claims, "sub"); hasSub {
		verifiedSub = canonicalSub(s)
	}
	// 4. Read aexp/iat/exp/w from the VERIFIED claims. Fail CLOSED (no slide)
	//    on any missing/malformed value. An old cap-less token has no aexp and
	//    correctly does not slide.
	aexp, ok1 := slidingClaimFloat(claims, "aexp")
	iat, ok2 := slidingClaimFloat(claims, "iat")
	_, ok3 := slidingClaimFloat(claims, "exp")
	wsec, ok4 := slidingClaimFloat(claims, "w")
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return verifiedSub
	}
	now := float64(time.Now().Unix())
	// 5. Absolute cap: at/after aexp, let the token lapse — never re-issue.
	if now >= aexp {
		return verifiedSub
	}
	// 6. Revocation hook (if configured), consulted at RE-ISSUE time only.
	if cfg.revokedCheck != nil {
		sub, hasSub := slidingClaimString(claims, "sub")
		if !hasSub {
			// Cannot identify the subject ⇒ cannot check revocation ⇒ fail
			// closed (no slide) rather than re-issue an unattributable token.
			return verifiedSub
		}
		if slidingIsRevoked(cfg.revokedCheck, sub) {
			return verifiedSub
		}
	}
	// 7. Past half-life? Re-issue only once per w/2 so a burst of requests does
	//    not re-sign on every hit (one HMAC + Set-Cookie per half-window, G6).
	if now < iat+wsec/2 {
		return verifiedSub
	}
	// Re-sign: iat'=now, exp'=min(now+w, aexp), aexp'=aexp (verbatim), w'=w,
	// carrying every other (user) claim unchanged.
	newExp := now + wsec
	if newExp > aexp {
		newExp = aexp
	}
	m := make(map[string]any, len(claims))
	for k, v := range claims {
		m[k] = v
	}
	m["iat"] = int64(now)
	m["exp"] = int64(newExp)
	m["aexp"] = int64(aexp) // verbatim — a whole-second unix value round-trips exactly
	m["w"] = int64(wsec)
	signed, ok := slidingResign(secret, m)
	if !ok {
		return
	}
	http.SetCookie(w, buildSlidingAuthCookie(r, cfg.cookie, signed, cfg.sameSite))
	return verifiedSub
}

// slidingVerifiedClaims verifies `token` with `secret` via the existing
// Auth_verifyToken kernel and returns the decoded claims on success. Any verify
// failure (expired / tampered / bad shape) returns ok=false — no claims escape.
func slidingVerifiedClaims(secret, token string) (map[string]any, bool) {
	res := Auth_verifyToken(secret, token)
	tag, okv, _ := anyResultView(res)
	if tag != 0 { // Err or unrecognised → no claims escape
		return nil, false
	}
	if m, ok := okv.(map[string]any); ok {
		return m, true
	}
	return nil, false
}

// slidingResign signs the re-issue claims with the same HMAC path as
// Auth_signToken (shared signHS256Claims), returning the token on success.
func slidingResign(secret string, m map[string]any) (string, bool) {
	keyBytes, errRes := coerceAuthSecret(secret, "signSlidingToken")
	if errRes != nil {
		return "", false
	}
	res := signHS256Claims(keyBytes, m, "signSlidingToken")
	tag, okv, _ := anyResultView(res)
	if tag != 0 {
		return "", false
	}
	if s, ok := okv.(string); ok && s != "" {
		return s, true
	}
	return "", false
}

// slidingClaimFloat reads a numeric JWT claim (they decode as float64). Returns
// ok=false when absent or not numeric — the fail-closed signal for step 4.
func slidingClaimFloat(claims map[string]any, key string) (float64, bool) {
	v, present := claims[key]
	if !present {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	default:
		return 0, false
	}
}

// slidingClaimString reads the `sub` claim as a string. A numeric sub (float64)
// is formatted to its integer text so an Int subject still identifies the user.
func slidingClaimString(claims map[string]any, key string) (string, bool) {
	v, present := claims[key]
	if !present {
		return "", false
	}
	switch s := v.(type) {
	case string:
		if s == "" {
			return "", false
		}
		return s, true
	case float64:
		return fmt.Sprintf("%d", int64(s)), true
	case int64:
		return fmt.Sprintf("%d", s), true
	case int:
		return fmt.Sprintf("%d", s), true
	default:
		return "", false
	}
}

// slidingIsRevoked consults the user's `sub -> Task Error Bool` hook. Returns
// true (BLOCK the re-issue) when the hook reports revoked, AND — fail closed —
// when the hook errors or panics: a transient revocation-check failure stops
// the slide (the token still lives to its current exp) rather than risk sliding
// a revoked user's token through the outage.
func slidingIsRevoked(cb any, sub string) (revoked bool) {
	defer func() {
		if rec := recover(); rec != nil {
			revoked = true // fail closed on panic
		}
	}()
	task := sky_call(cb, sub)
	if task == nil {
		return true
	}
	res := AnyTaskRun(task)
	tag, okv, _ := anyResultView(res)
	if tag != 0 { // Err or unrecognised → fail closed
		return true
	}
	b, ok := okv.(bool)
	if !ok {
		return true // non-bool payload → fail closed
	}
	return b
}

// ─── The builder-owned login setter ────────────────────────────────

// slidingSetterWarnOnce bounds the "no builder registered" warning to once.
var slidingSetterWarnOnce sync.Once

// Auth.setSlidingCookie : Request -> String -> Response -> Response
// (req, tokenValue, resp)
//
// Sets the sliding auth cookie on a response at LOGIN time with the SAME
// attributes the re-issue middleware will use — because both build the cookie
// through buildSlidingAuthCookie, reading the cookie name + SameSite from the
// ONE registered config (G4: no drift). The middleware CANNOT read cookie
// attributes off a later request (browsers send only name=value), so the login
// handler MUST use this setter, not a hand-rolled Server.withCookie.
//
// The Secure attribute is finalised at response-emission time
// (applySkyResponseHeaders → securifyCookieLine) against the REAL request being
// answered, exactly as every other Sky-set cookie is — so it matches the
// middleware's cookieSecureFor decision for the same deployment.
func Auth_setSlidingCookie(req any, tokenValue any, resp any) any {
	r, ok := asSkyResponse(resp)
	if !ok {
		return resp
	}
	cfg := getAuthSlidingConfig()
	if cfg == nil {
		// No builder registered — we cannot know the cookie name / attributes,
		// so we refuse to guess. Return the response unchanged and warn once.
		slidingSetterWarnOnce.Do(func() {
			fmt.Fprintf(os.Stderr,
				"[WARN] Auth.setSlidingCookie called with no Live.withAuthSliding builder registered; cookie not set\n")
		})
		return resp
	}
	httpReq := slidingReconstructRequest(req)
	cookie := buildSlidingAuthCookie(httpReq, cfg.cookie, slidingStringOf(tokenValue), cfg.sameSite)
	return addSetCookie(r, cookie.String())
}

// slidingReconstructRequest builds a minimal *http.Request from the Sky request
// carrying only what cookieSecureFor consults (the forwarded-proto headers), so
// the setter's own Secure guess honours a terminating TLS proxy. The final
// Secure decision is re-derived at emission with the real request, so this is a
// best-effort floor, never the last word.
func slidingReconstructRequest(req any) *http.Request {
	out := &http.Request{Header: http.Header{}, URL: &url.URL{}}
	if sr, ok := asSkyRequest(req); ok {
		for k, v := range sr.Headers {
			if s, ok := v.(string); ok {
				out.Header.Set(k, s)
			}
		}
	}
	return out
}
