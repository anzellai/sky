//go:build !js

package rt

// telemetry_url.go — the ONE place the runtime turns a request URL, an
// outbound URL or a client address into a value that telemetry records.
//
// Spans (server and client), the access log, metrics labels, the Console
// trace ring, panic reports, Middleware.withLogging and Std.Analytics page
// views all go through these helpers, so none of them records a secret or
// personal data that arrived in a path, a query string, userinfo, a fragment
// or a full client IP. docs/observability.md, "What is captured", states the
// rules; TestTelemetryURL_NoRawURLInTelemetrySinks fails when a sink reads a
// raw request value outside this file.
//
// The rules:
//
//   - A path is the route TEMPLATE when one is known (a Sky.Live route such as
//     `/reset/:token`, a Sky.Http.Server pattern such as `/blog/{slug}`). The
//     app stamps it during dispatch (noteTelemetryRoute / the mux wrapper), so
//     every in-process sub-app records its own templates.
//   - With no template, each path segment is kept only when it looks like a
//     route name (telemetrySafeSegment) and is replaced by `:redacted`
//     otherwise. IDs, tokens, UUIDs, emails, postcodes and percent-encoded
//     text never pass.
//   - A query string is dropped, except the keys an app names in
//     SKY_TELEMETRY_QUERY_KEYS (or Sky.Config.withTelemetryQueryKeys).
//   - Userinfo and the fragment of an outbound URL are never recorded.
//   - A client IP is anonymised (the last IPv4 octet, the last 80 bits of an
//     IPv6 address), the rule Std.Analytics already applies.
//
// Operator overrides (env, each also readable under a custom `[env]` prefix):
//
//	SKY_TELEMETRY_URL        redacted (default) | path (the raw path, no
//	                         query) | full (path and query; never userinfo)
//	SKY_TELEMETRY_QUERY_KEYS comma-separated query keys kept with their values
//	SKY_TELEMETRY_CLIENT_IP  anonymised (default) | full

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
)

// telemetryRedacted replaces a path segment that may carry data.
const telemetryRedacted = ":redacted"

type telemetryURLMode int

const (
	telemetryURLRedacted telemetryURLMode = iota
	telemetryURLPath
	telemetryURLFull
)

// telemetryURLModeNow reads SKY_TELEMETRY_URL. An unknown value is the
// redacted default: a typo never widens what telemetry records.
func telemetryURLModeNow() telemetryURLMode {
	switch strings.ToLower(strings.TrimSpace(skyGetenv("TELEMETRY_URL"))) {
	case "path":
		return telemetryURLPath
	case "full":
		return telemetryURLFull
	default:
		return telemetryURLRedacted
	}
}

// telemetryClientIPFull reports the SKY_TELEMETRY_CLIENT_IP=full opt-in.
func telemetryClientIPFull() bool {
	return strings.EqualFold(strings.TrimSpace(skyGetenv("TELEMETRY_CLIENT_IP")), "full")
}

// telemetryQueryKeysParsed keeps the parsed allow-list for the last raw
// value, so a request does not split the list again.
type telemetryQueryKeysParsed struct {
	raw  string
	keys map[string]bool
}

var telemetryQueryKeysCache atomic.Pointer[telemetryQueryKeysParsed]

// telemetryQueryKeys is the SKY_TELEMETRY_QUERY_KEYS allow-list (nil = none).
func telemetryQueryKeys() map[string]bool {
	raw := skyGetenv("TELEMETRY_QUERY_KEYS")
	if c := telemetryQueryKeysCache.Load(); c != nil && c.raw == raw {
		return c.keys
	}
	var keys map[string]bool
	for _, k := range strings.Split(raw, ",") {
		if k = strings.TrimSpace(k); k != "" {
			if keys == nil {
				keys = map[string]bool{}
			}
			keys[k] = true
		}
	}
	telemetryQueryKeysCache.Store(&telemetryQueryKeysParsed{raw: raw, keys: keys})
	return keys
}

// ─── The per-request route slot ─────────────────────────────────────

// telemetryRouteSlot holds the route template the app found for one request.
// The observability middleware puts it on the request context BEFORE
// dispatch; the app writes it during dispatch; the middleware reads it after.
// A pointer in the context is needed because Go's mux sets r.Pattern on its
// own copy of the request, which the middleware never sees.
type telemetryRouteSlot struct{ route atomic.Pointer[string] }

type telemetryRouteKey struct{}

// withTelemetryRouteSlot returns r with an empty slot, or r itself when it
// already has one (an outer wrapper put it there).
func withTelemetryRouteSlot(r *http.Request) *http.Request {
	if telemetrySlotOf(r.Context()) != nil {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), telemetryRouteKey{}, &telemetryRouteSlot{}))
}

func telemetrySlotOf(ctx context.Context) *telemetryRouteSlot {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(telemetryRouteKey{}).(*telemetryRouteSlot)
	return s
}

// noteTelemetryRoute records an app route template (a Sky.Live route). The
// latest call wins: an in-process sub-app's own dispatch is the most specific.
func noteTelemetryRoute(r *http.Request, template string) {
	if template == "" {
		return
	}
	if s := telemetrySlotOf(r.Context()); s != nil {
		s.route.Store(&template)
	}
}

// noteTelemetryPattern records a mux pattern when no app template is known.
// The catch-all "/" says nothing about the route, so it is ignored.
func noteTelemetryPattern(r *http.Request, pattern string) {
	p := telemetryPatternPath(pattern)
	if p == "" || p == "/" {
		return
	}
	if s := telemetrySlotOf(r.Context()); s != nil {
		s.route.CompareAndSwap(nil, &p)
	}
}

// serveMuxWithTelemetry serves r through mux, then records the pattern the
// mux matched in the route slot. The mux sets r.Pattern on the request it is
// given, so the pattern is read after the call. skyAssetGuard, which wraps
// the route mux of both Sky.Live and Sky.Http.Server, serves through it.
func serveMuxWithTelemetry(mux *http.ServeMux, w http.ResponseWriter, r *http.Request) {
	mux.ServeHTTP(w, r)
	noteTelemetryPattern(r, r.Pattern)
}

// telemetryPatternPath strips the method and host from a mux pattern
// ("GET example.com/blog/{slug}" → "/blog/{slug}").
func telemetryPatternPath(pattern string) string {
	p := strings.TrimSpace(pattern)
	if i := strings.IndexByte(p, ' '); i >= 0 {
		p = strings.TrimSpace(p[i+1:])
	}
	if p != "" && p[0] != '/' {
		if i := strings.IndexByte(p, '/'); i >= 0 {
			p = p[i:]
		} else {
			p = ""
		}
	}
	return p
}

func telemetryTemplateFromContext(ctx context.Context) string {
	if s := telemetrySlotOf(ctx); s != nil {
		if p := s.route.Load(); p != nil {
			return *p
		}
	}
	return ""
}

// telemetryPageTemplate is the Sky.Live page route that urlPath matches, with
// the app's base path in front (an in-process sub-app keeps its own), or ""
// when no page route matches.
func (app *liveApp) telemetryPageTemplate(urlPath string) string {
	p := trimBasePathPrefix(urlPath, app.basePath)
	for _, rt := range app.routes {
		if _, ok := matchRoute(rt.path, p); ok {
			return telemetryJoinBase(app.basePath, rt.path)
		}
	}
	if len(app.routes) == 0 && p == "/" {
		return telemetryJoinBase(app.basePath, "/")
	}
	return ""
}

func telemetryJoinBase(base, path string) string {
	if base == "" {
		return path
	}
	if path == "" || path == "/" {
		return base + "/"
	}
	return base + path
}

// ─── Server side: route, path, target ───────────────────────────────

// isSkyInternalRequest reports a /_sky/* runtime endpoint, which the
// observability middleware does not meter.
func isSkyInternalRequest(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/_sky/")
}

// telemetryRoute is the bounded route label: the template the app stamped,
// else routeLabelFor's first-segment fallback.
func telemetryRoute(r *http.Request) string {
	if t := telemetryTemplateFromContext(r.Context()); t != "" {
		return t
	}
	return routeLabelFor(r)
}

// telemetryPath is the request path telemetry may record (no query).
func telemetryPath(r *http.Request) string {
	return telemetryPathFor(r.Context(), r.URL.Path)
}

// telemetrySkyPath is telemetryPath for a Sky handler that holds only the
// Sky request's path: the template comes from the request goroutine's trace
// context, which carries the route slot.
func telemetrySkyPath(path string) string {
	return telemetryPathFor(CurrentTraceContext(), path)
}

func telemetryPathFor(ctx context.Context, path string) string {
	switch telemetryURLModeNow() {
	case telemetryURLPath, telemetryURLFull:
		return path
	}
	if t := telemetryTemplateFromContext(ctx); t != "" {
		return t
	}
	return redactPathSegments(path)
}

// telemetryTarget is the path plus the query telemetry may keep.
func telemetryTarget(r *http.Request) string {
	p := telemetryPath(r)
	if q := telemetryQueryOf(r); q != "" {
		return p + "?" + q
	}
	return p
}

// telemetryQueryOf is the part of r's query telemetry may keep.
func telemetryQueryOf(r *http.Request) string {
	return telemetryQuery(r.URL.RawQuery)
}

// telemetryQuery keeps only the allow-listed keys of a raw query ("" when
// none is kept). In `full` mode the whole query is kept.
func telemetryQuery(raw string) string {
	if raw == "" {
		return ""
	}
	if telemetryURLModeNow() == telemetryURLFull {
		return raw
	}
	keys := telemetryQueryKeys()
	if len(keys) == 0 {
		return ""
	}
	vals, err := url.ParseQuery(raw)
	if err != nil {
		return ""
	}
	kept := url.Values{}
	for k, v := range vals {
		if keys[k] {
			kept[k] = v
		}
	}
	return kept.Encode()
}

// redactPathSegments keeps each segment that looks like a route name and
// replaces every other one with `:redacted`. Empty segments (a leading,
// trailing or double slash) are kept as they are.
func redactPathSegments(p string) string {
	if p == "" {
		return "/"
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if s != "" && !telemetrySafeSegment(s) {
			segs[i] = telemetryRedacted
		}
	}
	return strings.Join(segs, "/")
}

// telemetrySafeSegment: a letter, then up to 31 letters, '_', '.' or '-'
// (no digits: an ID or a postcode has some), or an API version such as `v1`,
// `v2beta` or `v1alpha1`.
func telemetrySafeSegment(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	if isAPIVersionSegment(s) {
		return true
	}
	if !isASCIILetter(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !isASCIILetter(c) && c != '_' && c != '.' && c != '-' {
			return false
		}
	}
	return true
}

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func isASCIIDigit(c byte) bool { return c >= '0' && c <= '9' }

// isAPIVersionSegment matches v<1-2 digits>, optionally followed by alpha or
// beta and up to 2 more digits.
func isAPIVersionSegment(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	i := 1
	for i < len(s) && i <= 2 && isASCIIDigit(s[i]) {
		i++
	}
	if i == 1 || (i < len(s) && isASCIIDigit(s[i])) {
		return false
	}
	rest := s[i:]
	if rest == "" {
		return true
	}
	for _, tag := range []string{"alpha", "beta"} {
		if strings.HasPrefix(rest, tag) {
			tail := rest[len(tag):]
			if len(tail) > 2 {
				return false
			}
			for j := 0; j < len(tail); j++ {
				if !isASCIIDigit(tail[j]) {
					return false
				}
			}
			return true
		}
	}
	return false
}

// routeLabelFor returns the cardinality-bounded route label for metrics when
// the app stamped no template. Priority:
//
//  1. Go 1.22+ r.Pattern (the registered ServeMux pattern,
//     including any {wildcard} placeholders) when non-empty AND
//     non-"/" (the bare "/" pattern means "catch-all" and would
//     collapse every dynamic URL into one label).
//  2. The FIRST path segment, under telemetryPath's segment rule
//     (telemetrySafeSegment): a segment that looks like a route name is
//     kept, and any other one becomes `:redacted`.
//
// This used to take the first TWO raw segments, which put raw path
// IDs into label values: /users/12345 minted a fresh series per
// user, so 10k unique IDs exhausted the per-name series cap in 10k
// requests and froze the request metrics for the process lifetime
// (first-N-win, no eviction). It then kept a first segment with
// digits (`/Abc123Token` on a 404) in the span name and http.route,
// a rule looser than telemetryPath's. Both now apply one rule.
//
// The first segment is still requester-chosen for unrouted paths,
// which is why the series cap remains per-name with a global
// backstop (telemetry/store.go) — this label merely stops handing
// the requester a fresh series per URL.
func routeLabelFor(r *http.Request) string {
	if p := r.Pattern; p != "" && p != "/" {
		return p
	}
	path := r.URL.Path
	if path == "" || path == "/" {
		return "/"
	}
	seg := strings.TrimPrefix(path, "/")
	if i := strings.IndexByte(seg, '/'); i >= 0 {
		seg = seg[:i]
	}
	if telemetrySafeSegment(seg) {
		return "/" + seg
	}
	return "/" + telemetryRedacted
}

// ─── Outbound URLs and their errors ─────────────────────────────────

// telemetryOutboundURL returns what telemetry records for an outbound URL:
// full is scheme, host, port and the path (redacted by the rules above) plus
// any query the mode keeps. It never holds userinfo or a fragment. host is
// the server address and path the recorded path.
func telemetryOutboundURL(raw string) (full, host, path string) {
	u, err := url.Parse(raw)
	if err != nil {
		return "(invalid URL)", "", ""
	}
	path = u.EscapedPath()
	if telemetryURLModeNow() == telemetryURLRedacted && path != "" {
		path = redactPathSegments(path)
	}
	var b strings.Builder
	if u.Scheme != "" {
		b.WriteString(u.Scheme)
		b.WriteString("://")
	}
	b.WriteString(u.Host)
	b.WriteString(path)
	if q := telemetryQuery(u.RawQuery); q != "" {
		b.WriteString("?")
		b.WriteString(q)
	}
	return b.String(), u.Hostname(), path
}

// redactURLError is the text of an outbound HTTP error with the URL redacted.
// Go's *url.Error prints the whole URL ("Get \"https://h/x?key=…\": …"), so a
// secret in the query used to reach the Sky Error and the app's logs. Any
// other copy of the raw URL, of its query or of its userinfo in the text is
// replaced too.
func redactURLError(err error, raw string) string {
	if err == nil {
		return ""
	}
	var ue *url.Error
	var msg string
	if errors.As(err, &ue) {
		safe, _, _ := telemetryOutboundURL(ue.URL)
		msg = ue.Op + " \"" + safe + "\": " + ue.Err.Error()
	} else {
		msg = err.Error()
	}
	if raw == "" {
		return msg
	}
	safe, _, _ := telemetryOutboundURL(raw)
	msg = strings.ReplaceAll(msg, raw, safe)
	if u, perr := url.Parse(raw); perr == nil {
		if u.RawQuery != "" && telemetryURLModeNow() != telemetryURLFull {
			msg = strings.ReplaceAll(msg, u.RawQuery, "(query redacted)")
		}
		if u.User != nil {
			msg = strings.ReplaceAll(msg, u.User.String()+"@", "")
		}
	}
	return msg
}

// ─── Client address ────────────────────────────────────────────────

// clientIP best-effort extracts the client IP. Honours
// X-Forwarded-For (first IP — the original client; subsequent
// IPs are proxies) and X-Real-IP, falling back to r.RemoteAddr.
// Not trying to be cryptographically authoritative. Telemetry never
// records it directly: it records telemetryClientIP.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	return r.RemoteAddr
}

// telemetryClientIP is the client address telemetry records: anonymised by
// default, the full address (port removed) under
// SKY_TELEMETRY_CLIENT_IP=full.
func telemetryClientIP(r *http.Request) string {
	ip := clientIP(r)
	if telemetryClientIPFull() {
		if h, _, err := net.SplitHostPort(ip); err == nil {
			return h
		}
		return ip
	}
	return anonymiseIP(ip)
}

// anonymiseIP truncates a client IP to its network prefix: the last octet
// of an IPv4 (1.2.3.4 -> 1.2.3.0), the last 80 bits of an IPv6 (keep the /48) —
// the standard "IP anonymisation" GA/Matomo apply. Returns "" for an
// unparseable address, so nothing personal leaks by accident.
func anonymiseIP(remoteAddr string) string {
	host := strings.TrimSpace(remoteAddr)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		v4[3] = 0
		return v4.String()
	}
	if v6 := ip.To16(); v6 != nil {
		for i := 6; i < 16; i++ {
			v6[i] = 0
		}
		return v6.String()
	}
	return ""
}

// ─── Std.Analytics page views ──────────────────────────────────────

// analyticsTrackPageViewFor records the Sky.Live auto page view for r with
// the telemetry path (the route template when known) and a referrer with no
// query, userinfo or fragment.
func analyticsTrackPageViewFor(r *http.Request) {
	ref := ""
	if raw := r.Header.Get("Referer"); raw != "" {
		ref, _, _ = telemetryOutboundURL(raw)
	}
	analyticsTrackPageView(telemetryPath(r), ref)
}

// analyticsSetContextFor records the device and the anonymised client IP
// (analyticsSetContext anonymises the address before it is stored).
func analyticsSetContextFor(r *http.Request) {
	analyticsSetContext(r.Header.Get("User-Agent"), r.RemoteAddr)
}
