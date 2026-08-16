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
func applySkyResponseHeaders(h http.Header, resp SkyResponse) {
	for k, v := range resp.Headers {
		if http.CanonicalHeaderKey(k) == "Set-Cookie" {
			continue // handled below, as a repeated field
		}
		h.Set(k, v)
	}
	for _, line := range setCookieLines(resp) {
		h.Add("Set-Cookie", line)
	}
}
