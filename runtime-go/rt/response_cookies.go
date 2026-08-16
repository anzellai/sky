// response_cookies.go — the one place a SkyResponse's headers (and, in
// particular, its cookies) are turned into wire headers.
//
// Three dispatchers used to carry their own copy of
//
//	for k, v := range resp.Headers { w.Header().Set(k, v) }
//
// (`rt.go` buffered listener, `live.go` Sky.Live handler-route bridge,
// `server_stream.go` streaming path). `SkyResponse.Headers` is a
// `map[string]string` — ONE slot per header name — so a response could
// only ever carry a single `Set-Cookie`. Any second cookie silently
// overwrote the first, which is how `Middleware.withCsrf` destroyed a
// handler's session cookie on a visitor's first GET.
//
// Cookies therefore live in their own append-only field
// (`SkyResponse.Cookies`) and are emitted with `Header().Add`, which is
// what RFC 9110 §5.2 requires of Set-Cookie (repeated field, never
// comma-folded). `Headers["Set-Cookie"]` remains supported as a
// user-visible escape hatch — a Sky handler may still put a literal line
// in the `headers` Dict — and is emitted too, de-duplicated against
// `Cookies` so the common single-cookie case yields exactly one header.

package rt

import "net/http"

// setCookieLines returns every Set-Cookie line a response should emit,
// in issue order, with the user-set `Headers["Set-Cookie"]` escape hatch
// appended if it is not already among the runtime-minted cookies.
func setCookieLines(resp SkyResponse) []string {
	lines := make([]string, 0, len(resp.Cookies)+1)
	lines = append(lines, resp.Cookies...)
	if raw, ok := resp.Headers["Set-Cookie"]; ok && raw != "" {
		dup := false
		for _, l := range lines {
			if l == raw {
				dup = true
				break
			}
		}
		if !dup {
			lines = append(lines, raw)
		}
	}
	return lines
}

// addSetCookie appends a fully-formed Set-Cookie line to a response.
//
// The line is ALSO mirrored into `Headers["Set-Cookie"]` when that slot
// is still empty, so `resp.headers` keeps showing the first cookie to
// Sky code (and to the response-shape tests) exactly as before. Later
// cookies live only in `Cookies`; `setCookieLines` de-duplicates, so the
// mirror never doubles a header on the wire.
func addSetCookie(resp SkyResponse, line string) SkyResponse {
	resp.Cookies = append(append([]string(nil), resp.Cookies...), line)
	if resp.Headers == nil {
		resp.Headers = map[string]string{}
	}
	if existing, ok := resp.Headers["Set-Cookie"]; !ok || existing == "" {
		resp.Headers["Set-Cookie"] = line
	}
	return resp
}

// applySkyResponseHeaders writes a SkyResponse's headers onto an
// outgoing http.Header. Single source of truth for all three
// dispatchers.
//
// `r` is the request being answered, and it is what closes the last gap
// in the Secure story. `Server.withCookie` is called from Sky code with
// no request in hand, so at MINT time the only thing it can consult is
// the production gate — which left a user's auth cookie without Secure
// on a staging deploy served over HTTPS, while the runtime's own
// `sky_sid` (which does see the request) got it. The runtime was holding
// its own cookies to a stricter rule than the one it applied to the
// user's. Emission time is where the request exists, so the same
// predicate every other mint site uses is applied here, to every cookie
// on the response.
func applySkyResponseHeaders(h http.Header, r *http.Request, resp SkyResponse) {
	for k, v := range resp.Headers {
		if http.CanonicalHeaderKey(k) == "Set-Cookie" {
			continue // handled below, as a repeated field
		}
		h.Set(k, v)
	}
	for _, line := range setCookieLines(resp) {
		h.Add("Set-Cookie", securifyCookieLine(r, line))
	}
}

// securifyCookieLine adds `Secure` to a fully-formed Set-Cookie line
// when the shared predicate says the cookie needs it. The line is parsed
// with the STDLIB cookie parser — the browser's reading of it — so the
// name prefix and SameSite mode are the real ones, not a substring
// guess. An unparseable line is passed through untouched: emitting the
// user's bytes unchanged beats corrupting them.
func securifyCookieLine(r *http.Request, line string) string {
	parsed := (&http.Response{Header: http.Header{"Set-Cookie": []string{line}}}).Cookies()
	if len(parsed) != 1 {
		return line
	}
	c := parsed[0]
	if c.Secure {
		return line
	}
	if !cookieSecureFor(r, c.Name, c.SameSite) {
		return line
	}
	return line + "; Secure"
}
