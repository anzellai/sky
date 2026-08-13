package rt

// Pro+ Console auth — token-gated /_sky/console for production.
//
// Background: every Sky.Live binary auto-mounts /_sky/console in
// dev mode. Production turns it OFF (`maybeAutoMountConsole` early-
// returns) — for very good reason: the console exposes logs,
// traces, request rate, and live metrics that you don't want
// publicly accessible.
//
// For Pro+ tier in skydeploy, we want the console available IN
// production but gated so only the app owner (authenticated via
// dev.skydeploy.app) can reach it. The control-plane mints a
// short-lived HS256 JWT containing `{appId, userEmail, exp}`,
// signed with a SKY_CONSOLE_TOKEN_SECRET shared between the
// control-plane and the tenant Cloud Run service. The tenant
// runtime verifies the token on first access, sets an HttpOnly
// Secure SameSite=None session cookie, and strips the token from
// the URL.
//
// Why JWT + cookie rather than just JWT-in-cookie or HTTP Basic
// or session-from-control-plane:
//   * JWT in URL on first hit lets the control-plane open the
//     console in an iframe directly — no auth round-trip via
//     tenant.
//   * Cookie on subsequent hits avoids ?token=… leaking into
//     console-internal navigation, browser history, referer
//     headers.
//   * SameSite=None;Secure cookie is needed because the iframe
//     is cross-origin (dev.skydeploy.app embedding <slug>.skydeploy.app).
//     We already shipped the same SameSite policy in v0.14.16 for
//     the Sky.Live session cookies — same recipe.
//
// Token revocation: rotate SKY_CONSOLE_TOKEN_SECRET. All existing
// tokens + cookies fail signature check on next request. Coarse
// but sufficient pre-launch.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// adminTokenSecret returns the per-app admin secret that gates
// every privileged Sky.Live surface — /_sky/console (HS256 JWT
// signing), /_sky/metrics (Bearer auth), and any future admin-
// only endpoint. One secret per app, multiple surfaces, one
// trust domain.
//
// The canonical env var is SKY_ADMIN_TOKEN. Two legacy aliases
// are honoured for tenants seeded on earlier Sky versions:
//
//   - SKY_METRICS_TOKEN — v0.14.21's first-pass unification name
//     (kept "metrics" in the name; promoted to admin-wide).
//   - SKY_CONSOLE_TOKEN_SECRET — v0.14.20's console-specific
//     secret before any unification.
//
// Returns "" when nothing is set — admin auth is off and the
// production-mode console mount declines (see
// `MountEmbeddedConsole`).
//
// Moved from runtime-go/rt/subapp.go in v0.16.0 PR 2 when the
// subprocess + reverse-proxy mount path was deleted.
// adminTokenSecret stays here because every caller is part of
// the admin-auth surface (this file + observability.go metrics
// gate).
func adminTokenSecret() string {
	if s := os.Getenv("SKY_ADMIN_TOKEN"); s != "" {
		return s
	}
	if s := os.Getenv("SKY_METRICS_TOKEN"); s != "" {
		return s
	}
	return os.Getenv("SKY_CONSOLE_TOKEN_SECRET")
}

// consoleAdminSecret is a thin alias kept so callers that imported
// this name from v0.14.21 keep compiling. Use adminTokenSecret for
// new code.
func consoleAdminSecret() string {
	return adminTokenSecret()
}

// consoleAuthCookieName is the session cookie the tenant runtime
// issues after a successful URL-token verification. Same shape as
// the Sky.Live session cookie (sky_sid) — HttpOnly, Secure,
// SameSite=None (cross-origin iframe friendly). Path-scoped to
// the console so it never leaks into the app's own routes.
const consoleAuthCookieName = "sky_console_sid"

// consoleAuthSessionTTL bounds how long a single login lasts. The
// short-lived URL token (10 min, control-plane side) gates entry;
// the session cookie carries the user through console navigation
// without forcing re-token-fetch each time. 4 hours is long enough
// for sustained debugging, short enough to limit cookie-theft
// blast radius. User refreshes by clicking Console from the
// dashboard again, which mints a fresh token + cookie.
const consoleAuthSessionTTL = 4 * time.Hour

// consoleTokenAuth wraps an inner handler with the JWT-or-cookie
// auth gate. Used by MountConsoleAuth below; safe to compose with
// any http.Handler.
//
// Flow:
//   1. Read sky_console_sid cookie. If present + signature valid +
//      not expired → pass through.
//   2. Else read ?token=<JWT> query param. If valid → set cookie +
//      redirect to same path with token stripped (keep history /
//      Referer clean).
//   3. Else → 401 with a small landing page suggesting the user
//      open the console from their skydeploy.app dashboard.
//
// v0.16.0 PR 3 hardening:
//   - URL-handshake disabled by default. Set
//     `SKY_CONSOLE_EMBED_ORIGIN=<exact-origin>` to opt in (the iframe
//     embedder origin, matched against Origin/Referer at request
//     time). Closes the cookie/JWT-confusion attack surface from
//     the security review.
//   - `jti` claim required and consumed one-shot via the
//     `consumedJTI` sync.Map. Replays denied.
//   - `aud` claim must match the build-time commit hash; mismatch
//     denied. Caller-supplied tokens minted for a different binary
//     no longer redeem.
func consoleTokenAuth(secret string, inner http.Handler) http.Handler {
	keyBytes := []byte(secret)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Cookie path — most requests after the first
		if c, err := r.Cookie(consoleAuthCookieName); err == nil {
			if _, ok := verifyConsoleJwt(c.Value, keyBytes); ok {
				inner.ServeHTTP(w, r)
				return
			}
		}
		// First-hit path — verify ?token=
		//
		// Opt-in gate: SKY_CONSOLE_EMBED_ORIGIN must be set, and the
		// request's Origin (or Referer fallback) must match. Closes
		// the case where an HS256 token leaked from one origin gets
		// redeemed against a binary that doesn't expect URL tokens
		// at all.
		if !consoleEmbedAllowed() {
			consoleAuth401(w, r)
			return
		}
		if !requestOriginMatchesEmbed(r) {
			consoleAuth401(w, r)
			return
		}
		token := r.URL.Query().Get("token")
		if token == "" {
			consoleAuth401(w, r)
			return
		}
		claims, ok := verifyConsoleJwt(token, keyBytes)
		if !ok {
			consoleAuth401(w, r)
			return
		}
		// One-shot `jti` enforcement.
		jtiVal, _ := claims["jti"].(string)
		if jtiVal == "" {
			consoleAuth401(w, r)
			return
		}
		// Expiry already enforced by jwt.Parse, but read it back for
		// the JTI map's pruning window.
		var expUnix int64
		if expF, ok := claims["exp"].(float64); ok {
			expUnix = int64(expF)
		}
		if !rememberConsumedJTI(jtiVal, expUnix) {
			// Replay attempt
			consoleAuth401(w, r)
			return
		}
		startJTIJanitor()
		// `aud` claim must match the build's commit. SkyDeploy mints
		// these on the control-plane with the tenant's published
		// build ID; if the tenant is rolling out a new build the URL
		// minted before the rollout shouldn't authenticate against
		// the new binary.
		audClaim, _ := claims["aud"].(string)
		expectedAud := expectedConsoleAud()
		if expectedAud != "" && audClaim != expectedAud {
			consoleAuth401(w, r)
			return
		}

		// Mint a session cookie that carries the same identity for
		// the next 4 hours. Same secret signs both; we re-sign
		// rather than reusing the URL token so the URL token's
		// shorter expiry stays useful (it really is one-shot).
		sessionTok, err := mintConsoleSession(keyBytes, claims)
		if err != nil {
			consoleAuth401(w, r)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     consoleAuthCookieName,
			Value:    sessionTok,
			Path:     "/_sky/console",
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteNoneMode,
			MaxAge:   int(consoleAuthSessionTTL.Seconds()),
		})

		// Redirect to same path WITHOUT the token query so it
		// doesn't appear in browser history, referer, or any
		// link the user copies. 303 (See Other) is the right
		// status for a POST-or-anything → GET swap, but for a
		// GET → GET we use 302 (Found).
		u := *r.URL
		q := u.Query()
		q.Del("token")
		u.RawQuery = q.Encode()
		http.Redirect(w, r, u.RequestURI(), http.StatusFound)
	})
}

// requestOriginMatchesEmbed returns true when the incoming request's
// Origin / Referer matches the configured SKY_CONSOLE_EMBED_ORIGIN.
// Header matching is exact-string after a strings.TrimSpace; we
// don't loosen to suffix/wildcard because the security review's
// whole point was "no fuzzy origin matching".
func requestOriginMatchesEmbed(r *http.Request) bool {
	allowed := consoleEmbedOrigin()
	if allowed == "" {
		return false
	}
	if got := strings.TrimSpace(r.Header.Get("Origin")); got != "" {
		return got == allowed
	}
	// Origin is optional on same-site GETs; fall back to Referer
	// origin (scheme+host[:port]).
	if ref := r.Header.Get("Referer"); ref != "" {
		// Extract origin from the Referer URL.
		if i := strings.Index(ref, "://"); i > 0 {
			rest := ref[i+3:]
			if j := strings.IndexAny(rest, "/?#"); j >= 0 {
				rest = rest[:j]
			}
			candidate := ref[:i+3] + rest
			return candidate == allowed
		}
	}
	return false
}

// expectedConsoleAud returns the build's commit hash from
// `currentBuildInfo`. Falls back to "" on a dev build so the aud
// check passes when the operator hasn't wired -X buildCommit (the
// strict gate only activates on real release builds where the
// commit is injected).
func expectedConsoleAud() string {
	bi := currentBuildInfo()
	if bi.Commit == "" || bi.Commit == "dev" {
		return ""
	}
	return bi.Commit
}

// verifyConsoleJwt parses + checks an HS256 JWT. Returns claims +
// ok=true on success. Treats any verification failure (bad
// signature, wrong method, expired, malformed) as a uniform fail
// — no specific error path leaks to the caller.
func verifyConsoleJwt(tok string, key []byte) (jwt.MapClaims, bool) {
	parsed, err := jwt.Parse(tok, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errConsoleAuthBadMethod
		}
		return key, nil
	})
	if err != nil || !parsed.Valid {
		return nil, false
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return nil, false
	}
	return claims, true
}

// mintConsoleSession re-issues a fresh JWT for the session cookie
// off the URL token's claims. Same key, longer expiry. We carry
// `sub` (user identity) and `aud` (app id) through so audit logs
// downstream can attribute console actions.
func mintConsoleSession(key []byte, claims jwt.MapClaims) (string, error) {
	mc := jwt.MapClaims{
		"sub": claims["sub"],
		"aud": claims["aud"],
		"iss": "skydeploy-tenant",
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(consoleAuthSessionTTL).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, mc)
	return tok.SignedString(key)
}

// errConsoleAuthBadMethod is the sentinel returned to jwt.Parse's
// key callback when the signing method isn't HMAC. Surfaces as a
// generic parse error to the caller — never reaches the client.
var errConsoleAuthBadMethod = stringError("console-auth: unexpected signing method")

// stringError lets us declare named errors without importing
// "errors" (avoids the import-cycle / duplicate-import warnings
// some of Sky's runtime files trip).
type stringError string

func (e stringError) Error() string { return string(e) }

// consoleAuth401 writes a small landing page when the user hits
// /_sky/console with no valid token or cookie. The page is
// deliberately plain — no console branding, no app branding —
// it's an "open this from your dashboard" hint, nothing more.
func consoleAuth401(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(consoleAuth401Page))
}

const consoleAuth401Page = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Sky Console</title>
<style>
  html,body{margin:0;padding:0;height:100%;background:#0b0b0f;color:#e8e8ec;
    font:14px/1.5 -apple-system,Segoe UI,Roboto,sans-serif}
  main{height:100%;display:flex;align-items:center;justify-content:center}
  .card{max-width:420px;padding:24px 28px;border:1px solid #2a2a33;
    border-radius:10px;background:#15161c;text-align:center}
  h1{font-size:18px;margin:0 0 12px;font-weight:600}
  p{margin:0 0 8px;color:#a0a0aa}
  a{color:#3ECF8E;text-decoration:none}
  a:hover{color:#4ADE80}
</style></head><body><main><div class="card">
  <h1>Sky Console</h1>
  <p>Open this console from the <a href="https://skydeploy.app">Sky Deploy dashboard</a>
     — your app-owner session there generates the access token.</p>
  <p style="color:#5e5e6b;font-size:12px;margin-top:16px;font-family:ui-monospace,monospace">
     401 — no valid console token / session.</p>
</div></main></body></html>`

// ─── Helpers for the control-plane side ─────────────────────────────
//
// Skydeploy's control-plane uses Sky.Core.Jwt directly (via Std.Auth.signToken)
// — no Go-side helper needed here. These helpers exist so OTHER tooling
// (e.g. CLI tools that mint a token for local console access) can produce
// tokens with the same shape, and so the token format is documented in
// one place.

// MintConsoleUrlToken — Go-side helper for tooling. Produces an
// HS256 JWT with `{sub, aud, jti, iss, iat, exp}` claims using the
// same secret the tenant runtime verifies against. ttl typically 10
// minutes (URL token's expiry — short for theft-resistance).
//
// v0.16.0 PR 3: the `jti` claim is now a 16-byte random hex string,
// consumed one-shot by the tenant runtime via `consumedJTI`. Tokens
// are single-use even within their expiry window — replays denied.
// `aud` should be the tenant's deployed build-commit hash (or the
// appID for back-compat where the tenant runtime hasn't enabled
// the strict aud-claim check, which only fires when buildCommit is
// injected — see expectedConsoleAud).
//
// Usage: ConsoleURL = "https://<app>.skydeploy.app/_sky/console?token=" + MintConsoleUrlToken(...)
func MintConsoleUrlToken(secret, userEmail, appID string, ttl time.Duration) (string, error) {
	jtiBytes := make([]byte, 16)
	_, _ = rand.Read(jtiBytes)
	mc := jwt.MapClaims{
		"sub": userEmail,
		"aud": appID,
		"jti": hex.EncodeToString(jtiBytes),
		"iss": "skydeploy-control-plane",
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(ttl).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, mc)
	return tok.SignedString([]byte(secret))
}

// randomConsoleSecret is a 32-byte cryptographically-random
// base64 string suitable for SKY_CONSOLE_TOKEN_SECRET. Exposed
// for tooling that auto-generates per-app secrets.
func randomConsoleSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return strings.TrimRight(base64.URLEncoding.EncodeToString(b), "=")
}

// _ silences "unused" warnings during incremental development —
// randomConsoleSecret + MintConsoleUrlToken are exported helpers
// for control-plane use, not necessarily called from runtime-go itself.
var _ = randomConsoleSecret
var _ = MintConsoleUrlToken
