//go:build !js

// cookie_secure.go — ONE decision about the `Secure` cookie attribute,
// used by every Set-Cookie mint site in the runtime.
//
// There used to be three different answers to "does this cookie get
// Secure?":
//
//  1. `securifyCookieAttrs` — appended "; Secure" when `isProd()`, and
//     decided "already present?" with
//     `strings.Contains(strings.ToLower(attrs), "secure")`. That is true
//     for `Path=/secure`, `Domain=secure.example.com` and
//     `Path=/insecure-area`, none of which is the Secure ATTRIBUTE, so
//     any app whose cookie path or domain happened to contain those six
//     letters shipped a production session cookie without Secure — and
//     nothing said so. It reached `Server.withCookie name value attrs
//     resp`, documented as giving "full control over attribute string".
//
//  2. `isProd()` itself was a SECOND production predicate:
//     `<PREFIX>_ENV == "prod"`, exact match, namespaced variable only.
//     The documented production gate (AGENTS.md, `productionFromEnv`)
//     is "ENV, else SKY_ENV, set to any non-dev value". Under the gate
//     users are actually told to set — `ENV=production` — the cookie
//     path was therefore in DEV mode and appended no Secure at all.
//
//  3. The `http.SetCookie` sites each rolled their own: the Sky.Live
//     session cookie was `secure = false` unless SKY_LIVE_FRAME_ANCESTORS
//     was set, so the session id of a production Sky.Live app — the
//     PINNED DEFAULT app shape — rode without Secure.
//
// The rules now, in one place:
//
//   - A cookie whose NAME carries the `__Host-` or `__Secure-` prefix
//     MUST be Secure (RFC 6265bis §4.1.3.1–2). Unconditional.
//   - A cookie sent `SameSite=None` MUST be Secure (§5.4.7). Unconditional.
//   - Otherwise: Secure when the request arrived over HTTPS, or when the
//     production gate is on. `productionFromEnv()` is that gate — the
//     same predicate the console/metrics auth uses, so there is exactly
//     one notion of "this is production" in the runtime.

package rt

import (
	"net/http"
	"strings"
)

// cookiesMustBeSecure reports whether cookies minted without a request
// in hand (the `SkyResponse` path — `Server.withCookie`,
// `Server.csrfIssue`) must carry Secure. Single production predicate;
// see productionFromEnv.
func cookiesMustBeSecure() bool { return productionFromEnv() }

// isProd reports whether this process is running in production.
//
// It is a thin alias for productionFromEnv() — there is exactly ONE
// production predicate in this runtime, deliberately.
//
// It used to be `skyGetenv("ENV") == "prod"`, which disagreed with
// productionFromEnv() in two ways that silently disabled cookie
// hardening:
//
//   - It read <PREFIX>_ENV only, never plain `ENV`.
//   - It matched only the literal string "prod".
//
// `ENV=production` is what `sky init` scaffolds and what the docs
// promise, and it satisfied NEITHER condition — so production
// deployments got session cookies with no `Secure` attribute. Keeping
// two predicates in agreement by convention had already failed once;
// the alias makes divergence impossible.
func isProd() bool { return productionFromEnv() }

// requestIsHTTPS reports whether the browser reached us over TLS —
// either directly (r.TLS) or through a terminating reverse proxy that
// announced the original scheme in X-Forwarded-Proto / X-Forwarded-Ssl.
//
// This is the most accurate Secure-cookie signal there is: it describes
// the connection actually in front of us rather than an operator's guess
// about the environment. X-Forwarded-Proto is attacker-controllable when
// the app is exposed directly, but the only thing a forged header can do
// here is turn Secure ON, which is fail-safe.
//
// Nil-tolerant so callers without a request in hand can pass nil.
func requestIsHTTPS(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Ssl")), "on") {
		return true
	}
	return r.URL != nil && strings.EqualFold(r.URL.Scheme, "https")
}

// cookieNameMandatesSecure reports whether the cookie's NAME prefix
// requires the Secure attribute regardless of environment
// (RFC 6265bis §4.1.3.1 `__Secure-`, §4.1.3.2 `__Host-`). A client
// REJECTS such a cookie without Secure, so this is the one place a
// hardcoded `true` is correct.
func cookieNameMandatesSecure(name string) bool {
	return strings.HasPrefix(name, "__Host-") || strings.HasPrefix(name, "__Secure-")
}

// cookieSecureFor is the decision for every `http.SetCookie` site.
// `sameSite` is the mode the caller is about to set: SameSite=None is
// only honoured on a Secure cookie (RFC 6265bis §5.4.7).
func cookieSecureFor(r *http.Request, name string, sameSite http.SameSite) bool {
	if cookieNameMandatesSecure(name) {
		return true
	}
	if sameSite == http.SameSiteNoneMode {
		return true
	}
	return requestIsHTTPS(r) || cookiesMustBeSecure()
}

// cookieAttrsHaveSecure reports whether a Set-Cookie attribute list
// already carries the Secure ATTRIBUTE — parsed as a `;`-separated
// attribute list, case-insensitively, tolerating the valued form
// (`Secure=`, which clients accept and ignore the value of) and empty
// segments. NOT a substring test: `Path=/secure` is not Secure.
func cookieAttrsHaveSecure(attrs string) bool {
	for _, seg := range strings.Split(attrs, ";") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		if eq := strings.IndexByte(seg, '='); eq >= 0 {
			seg = strings.TrimSpace(seg[:eq])
		}
		if strings.EqualFold(seg, "secure") {
			return true
		}
	}
	return false
}

// securifyCookieAttrs appends "; Secure" to a Set-Cookie attribute
// string when the production gate is on and the attribute is not
// already present. Idempotent.
func securifyCookieAttrs(attrs string) string {
	if !cookiesMustBeSecure() {
		return attrs
	}
	if cookieAttrsHaveSecure(attrs) {
		return attrs
	}
	if strings.TrimSpace(attrs) == "" {
		return "Secure"
	}
	return attrs + "; Secure"
}
