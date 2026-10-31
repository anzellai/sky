//go:build !js

package rt

// tracing_url_test.go — OTel spans, metrics, access logs, panic reports,
// Middleware.withLogging and Std.Analytics page views never record a secret
// or personal data from a request path, a query string, userinfo, a fragment
// or a full client IP (v0.29.1). See telemetry_url.go.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"sky-app/rt/procenv"
	"sky-app/rt/telemetry"
)

// spanAttr returns the string value of one attribute, "" when absent.
func spanAttr(s tracetest.SpanStub, key string) string {
	for _, kv := range s.Attributes {
		if string(kv.Key) == key {
			return kv.Value.Emit()
		}
	}
	return ""
}

// spanText is the span's name plus every attribute, for "contains" checks.
func spanText(s tracetest.SpanStub) string {
	var b strings.Builder
	b.WriteString(s.Name)
	for _, kv := range s.Attributes {
		b.WriteString(" ")
		b.WriteString(string(kv.Key))
		b.WriteString("=")
		b.WriteString(kv.Value.Emit())
	}
	return b.String()
}

func clearTelemetryURLEnv(t *testing.T) {
	t.Helper()
	for _, s := range []string{"TELEMETRY_URL", "TELEMETRY_QUERY_KEYS", "TELEMETRY_CLIENT_IP"} {
		resetEnvFor(t, s)
	}
}

func logsText(entries []telemetry.LogEntry) string {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Message)
		b.WriteString(" ")
		b.WriteString(e.Route)
		for k, v := range e.Fields {
			b.WriteString(" " + k + "=" + v)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// ─── Outbound (client) spans ──────────────────────────────────────

func TestTelemetryURL_ClientSpanDropsQueryUserinfoFragment(t *testing.T) {
	clearTelemetryURLEnv(t)
	exporter := installSpyTracer(t)
	WithHTTPClientSpan("GET", "https://u:p@h.example/a/b?key=s#f", func() any { return nil })
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	s := spans[0]
	if got := spanAttr(s, "url.full"); got != "https://h.example/a/b" {
		t.Errorf("url.full = %q, want https://h.example/a/b", got)
	}
	if got := spanAttr(s, "http.url"); got != "https://h.example/a/b" {
		t.Errorf("http.url = %q, want https://h.example/a/b", got)
	}
	if got := spanAttr(s, "server.address"); got != "h.example" {
		t.Errorf("server.address = %q", got)
	}
	if got := spanAttr(s, "url.path"); got != "/a/b" {
		t.Errorf("url.path = %q", got)
	}
	text := spanText(s)
	for _, bad := range []string{"key=", "u:p", "#f", "=s"} {
		if strings.Contains(text, bad) {
			t.Errorf("client span carries %q: %s", bad, text)
		}
	}
}

func TestTelemetryURL_ClientSpanRedactsDataSegments(t *testing.T) {
	clearTelemetryURLEnv(t)
	exporter := installSpyTracer(t)
	WithHTTPClientSpan("GET", "https://api.postcodes.example/postcodes/SW1A1AA", func() any { return nil })
	s := exporter.GetSpans()[0]
	if got := spanAttr(s, "url.full"); got != "https://api.postcodes.example/postcodes/:redacted" {
		t.Errorf("url.full = %q", got)
	}
	if strings.Contains(spanText(s), "SW1A1AA") {
		t.Errorf("postcode reached the span: %s", spanText(s))
	}
}

func TestTelemetryURL_QueryAllowList(t *testing.T) {
	clearTelemetryURLEnv(t)
	t.Setenv("SKY_TELEMETRY_QUERY_KEYS", "page, sort")
	exporter := installSpyTracer(t)
	WithHTTPClientSpan("GET", "https://h.example/list?page=2&key=s&sort=asc", func() any { return nil })
	s := exporter.GetSpans()[0]
	if got := spanAttr(s, "url.full"); got != "https://h.example/list?page=2&sort=asc" {
		t.Errorf("url.full = %q, want the allowed keys only", got)
	}
	if strings.Contains(spanText(s), "key=s") {
		t.Errorf("a key outside the allow-list reached the span: %s", spanText(s))
	}
}

func TestTelemetryURL_FullModeKeepsQueryNeverUserinfo(t *testing.T) {
	clearTelemetryURLEnv(t)
	t.Setenv("SKY_TELEMETRY_URL", "full")
	exporter := installSpyTracer(t)
	WithHTTPClientSpan("GET", "https://u:p@h.example/a/123?key=s#f", func() any { return nil })
	s := exporter.GetSpans()[0]
	if got := spanAttr(s, "url.full"); got != "https://h.example/a/123?key=s" {
		t.Errorf("url.full = %q", got)
	}
	if strings.Contains(spanText(s), "u:p") || strings.Contains(spanText(s), "#f") {
		t.Errorf("full mode leaked userinfo or the fragment: %s", spanText(s))
	}
}

func TestTelemetryURL_PathModeKeepsRawPathNoQuery(t *testing.T) {
	clearTelemetryURLEnv(t)
	t.Setenv("SKY_TELEMETRY_URL", "path")
	exporter := installSpyTracer(t)
	WithHTTPClientSpan("GET", "https://h.example/a/123?key=s", func() any { return nil })
	if got := spanAttr(exporter.GetSpans()[0], "url.full"); got != "https://h.example/a/123" {
		t.Errorf("url.full = %q", got)
	}
}

// ─── Outbound error text ──────────────────────────────────────────

func TestTelemetryURL_UnreachableErrorHasNoSecret(t *testing.T) {
	clearTelemetryURLEnv(t)
	raw := "http://127.0.0.1:1/x?key=SECRET123"
	render := func(v any) string {
		if sr, ok := v.(SkyResult[any, any]); ok && sr.Tag != 0 {
			msg, _ := renderSkyError(sr.ErrValue)
			return msg
		}
		return fmt.Sprintf("not an Err: %#v", v)
	}
	cases := map[string]string{
		"Http.get":     render(Http_get(raw).(func() any)()),
		"Http.post":    render(Http_post(raw, "{}").(func() any)()),
		"Http.request": render(Http_request("GET", raw, "", nil).(func() any)()),
	}
	if r := Http_getT(raw)(); r.Tag != 0 {
		cases["Http_getT"] = r.ErrValue
	} else {
		t.Fatalf("Http_getT reached a closed port")
	}
	for name, msg := range cases {
		if msg == "" {
			t.Errorf("%s: empty error", name)
		}
		if strings.Contains(msg, "SECRET123") || strings.Contains(msg, "key=") {
			t.Errorf("%s: error text carries the query: %q", name, msg)
		}
		if !strings.Contains(msg, "127.0.0.1:1") {
			t.Errorf("%s: error text lost the host: %q", name, msg)
		}
	}
}

func TestTelemetryURL_HttpRequestHasClientSpan(t *testing.T) {
	clearTelemetryURLEnv(t)
	exporter := installSpyTracer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	Http_request("PUT", srv.URL+"/thing?key=SECRET", "", nil).(func() any)()
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "http PUT" {
		t.Fatalf("Http.request must open one client span, got %d", len(spans))
	}
	if strings.Contains(spanText(spans[0]), "SECRET") {
		t.Errorf("Http.request span carries the query: %s", spanText(spans[0]))
	}
}

// ─── Server spans + access log ────────────────────────────────────

func TestTelemetryURL_ServerSpanUnknownRouteRedacted(t *testing.T) {
	resetTelemetry(t)
	withServerlessEnv(t, nil)
	clearTelemetryURLEnv(t)
	exporter := installSpyTracer(t)
	h := ObservabilityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest(http.MethodGet, "/reset/3f9a77c2?token=xyz", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	text := spanText(spans[0])
	for _, bad := range []string{"xyz", "3f9a77c2", "token"} {
		if strings.Contains(text, bad) {
			t.Errorf("server span carries %q: %s", bad, text)
		}
	}
	if got := spanAttr(spans[0], "url.path"); got != "/reset/:redacted" {
		t.Errorf("url.path = %q, want /reset/:redacted", got)
	}
	logs := logsText(telemetry.Default().RecentLogs(0))
	if !strings.Contains(logs, "/reset/:redacted") {
		t.Errorf("access log should show the redacted path: %s", logs)
	}
	for _, bad := range []string{"xyz", "3f9a77c2"} {
		if strings.Contains(logs, bad) {
			t.Errorf("access log carries %q: %s", bad, logs)
		}
	}
}

func TestTelemetryURL_ServerSpanUsesLiveTemplate(t *testing.T) {
	resetTelemetry(t)
	withServerlessEnv(t, nil)
	clearTelemetryURLEnv(t)
	exporter := installSpyTracer(t)
	app := &liveApp{api: []apiRoute{{
		method:  "GET",
		pattern: "/reset/:token",
		handler: func(req any) any {
			return func() any { return Ok[any, any](SkyResponse{Status: 200, Body: "ok"}) }
		},
	}}}
	h := ObservabilityMiddleware(http.HandlerFunc(app.dispatchRoot))
	req := httptest.NewRequest(http.MethodGet, "/reset/3f9a77c2?token=xyz", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	s := exporter.GetSpans()[0]
	if got := spanAttr(s, "url.path"); got != "/reset/:token" {
		t.Errorf("url.path = %q, want the Live route template", got)
	}
	if got := spanAttr(s, "http.route"); got != "/reset/:token" {
		t.Errorf("http.route = %q, want the Live route template", got)
	}
	if s.Name != "GET /reset/:token" {
		t.Errorf("span name = %q", s.Name)
	}
	if strings.Contains(spanText(s), "xyz") || strings.Contains(spanText(s), "3f9a77c2") {
		t.Errorf("span carries request data: %s", spanText(s))
	}
}

func TestTelemetryURL_LivePageTemplate(t *testing.T) {
	app := &liveApp{routes: []liveRoute{{path: "/"}, {path: "/reset/:token"}}}
	if got := app.telemetryPageTemplate("/reset/abc"); got != "/reset/:token" {
		t.Errorf("root app template = %q", got)
	}
	if got := app.telemetryPageTemplate("/nope/abc"); got != "" {
		t.Errorf("unrouted path must have no template, got %q", got)
	}
	sub := &liveApp{basePath: "/admin", routes: []liveRoute{{path: "/users/:id"}}}
	if got := sub.telemetryPageTemplate("/admin/users/42"); got != "/admin/users/:id" {
		t.Errorf("sub-app template = %q, want its own base path + template", got)
	}
}

func TestTelemetryURL_ServerSpanUsesMuxPattern(t *testing.T) {
	resetTelemetry(t)
	withServerlessEnv(t, nil)
	clearTelemetryURLEnv(t)
	exporter := installSpyTracer(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/blog/{slug}", func(w http.ResponseWriter, r *http.Request) {})
	h := serverListenerHandler(mux, nil, "")
	req := httptest.NewRequest(http.MethodGet, "/blog/my-secret-draft-42?preview=tok", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	s := exporter.GetSpans()[0]
	if got := spanAttr(s, "url.path"); got != "/blog/{slug}" {
		t.Errorf("url.path = %q, want the mux pattern", got)
	}
	if got := spanAttr(s, "http.route"); got != "/blog/{slug}" {
		t.Errorf("http.route = %q", got)
	}
	if strings.Contains(spanText(s), "draft") || strings.Contains(spanText(s), "tok") {
		t.Errorf("span carries request data: %s", spanText(s))
	}
}

func TestTelemetryURL_ServerTargetAllowListAndFull(t *testing.T) {
	resetTelemetry(t)
	withServerlessEnv(t, nil)
	clearTelemetryURLEnv(t)
	t.Setenv("SKY_TELEMETRY_QUERY_KEYS", "page")
	exporter := installSpyTracer(t)
	h := ObservabilityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/list?page=2&token=abc", nil))
	if got := spanAttr(exporter.GetSpans()[0], "http.target"); got != "/list?page=2" {
		t.Errorf("http.target = %q, want /list?page=2", got)
	}

	t.Setenv("SKY_TELEMETRY_QUERY_KEYS", "")
	t.Setenv("SKY_TELEMETRY_URL", "full")
	exporter.Reset()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/a/123?token=abc", nil))
	if got := spanAttr(exporter.GetSpans()[0], "http.target"); got != "/a/123?token=abc" {
		t.Errorf("full mode http.target = %q", got)
	}
}

func TestTelemetryURL_ServerlessAccessLogIsValidJSON(t *testing.T) {
	resetTelemetry(t)
	withServerlessEnv(t, map[string]string{"K_SERVICE": "svc"})
	clearTelemetryURLEnv(t)
	t.Setenv("SKY_TELEMETRY_URL", "path")
	h := ObservabilityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	out := captureStderr(t, func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, `/a%22b%5Cc`, nil))
	})
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, `"http_request"`) {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no access log line on stderr: %q", out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("access log line is not valid JSON (%v): %s", err, line)
	}
	if m["path"] != `/a"b\c` {
		t.Errorf("path = %v", m["path"])
	}
}

// ─── Client IP ────────────────────────────────────────────────────

func TestTelemetryURL_ClientIPAnonymisedByDefault(t *testing.T) {
	resetTelemetry(t)
	withServerlessEnv(t, nil)
	clearTelemetryURLEnv(t)
	exporter := installSpyTracer(t)
	h := ObservabilityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.77:5555"
	h.ServeHTTP(httptest.NewRecorder(), req)
	s := exporter.GetSpans()[0]
	for _, k := range []string{"net.peer.ip", "client.address"} {
		if got := spanAttr(s, k); got != "203.0.113.0" {
			t.Errorf("%s = %q, want 203.0.113.0", k, got)
		}
	}

	exporter.Reset()
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "2001:db8:1234:5678:9abc::1, 10.0.0.1")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got := spanAttr(exporter.GetSpans()[0], "client.address"); got != "2001:db8:1234::" {
		t.Errorf("IPv6 client.address = %q, want 2001:db8:1234::", got)
	}

	exporter.Reset()
	t.Setenv("SKY_TELEMETRY_CLIENT_IP", "full")
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.77:5555"
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got := spanAttr(exporter.GetSpans()[0], "client.address"); got != "203.0.113.77" {
		t.Errorf("opt-in full client.address = %q", got)
	}
}

// ─── Logs and reports ─────────────────────────────────────────────

func TestTelemetryURL_WithLoggingRedactsPath(t *testing.T) {
	resetTelemetry(t)
	withServerlessEnv(t, nil)
	clearTelemetryURLEnv(t)
	handler := func(req any) any {
		return func() any { return Ok[any, any](SkyResponse{Status: 200}) }
	}
	req := SkyRequest{Method: "GET", Path: "/reset/3f9a77c2", Headers: map[string]any{}}
	Middleware_withLogging(handler).(func(any) any)(req).(func() any)()
	logs := logsText(telemetry.Default().RecentLogs(0))
	if !strings.Contains(logs, "path=/reset/:redacted") {
		t.Errorf("withLogging should record the redacted path: %s", logs)
	}
	if strings.Contains(logs, "3f9a77c2") {
		t.Errorf("withLogging recorded the raw path: %s", logs)
	}
}

func TestTelemetryURL_PanicContextRedacted(t *testing.T) {
	clearTelemetryURLEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/reset/3f9a77c2?token=xyz", nil)
	out := captureStderr(t, func() {
		dispatchSkyHandler(httptest.NewRecorder(), req, func(any) any { panic("boom") }, nil)
	})
	if !strings.Contains(out, "boom") {
		t.Fatalf("panic report missing: %q", out)
	}
	if strings.Contains(out, "3f9a77c2") || strings.Contains(out, "xyz") {
		t.Errorf("panic report carries the raw path: %q", out)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { panic("live boom") })
	h := liveListenerHandler(mux, "")
	out = captureStderr(t, func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/reset/3f9a77c2", nil))
	})
	if !strings.Contains(out, "live boom") {
		t.Fatalf("Sky.Live panic report missing: %q", out)
	}
	if strings.Contains(out, "3f9a77c2") {
		t.Errorf("Sky.Live panic report carries the raw path: %q", out)
	}
}

func TestTelemetryURL_AnalyticsPageViewRedacted(t *testing.T) {
	clearTelemetryURLEnv(t)
	sess := &liveSession{sid: "pv-redact"}
	setGoroutineLiveSession(sess)
	defer clearGoroutineLiveSession()
	var buf bytes.Buffer
	old := analyticsSink
	analyticsSink = &buf
	defer func() { analyticsSink = old }()

	req := httptest.NewRequest(http.MethodGet, "/reset/3f9a77c2?token=xyz", nil)
	req.Header.Set("Referer", "https://u:p@ref.example/p/9f8e?q=me#frag")
	analyticsTrackPageViewFor(req)
	out := buf.String()
	if !strings.Contains(out, `"path":"/reset/:redacted"`) {
		t.Errorf("page_view path not redacted: %s", out)
	}
	if !strings.Contains(out, `"referrer":"https://ref.example/p/:redacted"`) {
		t.Errorf("page_view referrer not redacted: %s", out)
	}
	for _, bad := range []string{"3f9a77c2", "xyz", "u:p", "9f8e", "q=me", "frag"} {
		if strings.Contains(out, bad) {
			t.Errorf("page_view carries %q: %s", bad, out)
		}
	}
}

// ─── Sky.Config builder ───────────────────────────────────────────

func TestTelemetryURL_ConfigWithTelemetryQueryKeys(t *testing.T) {
	resetEnvFor(t, "TELEMETRY_QUERY_KEYS")
	ApplyConfig(Config_withTelemetryQueryKeys([]any{"page", "sort"}, Config_default()))
	if got := procenv.Getenv(skyEnvName("TELEMETRY_QUERY_KEYS")); got != "page,sort" {
		t.Fatalf("SKY_TELEMETRY_QUERY_KEYS = %q, want page,sort", got)
	}
	_ = os.Setenv(skyEnvName("TELEMETRY_QUERY_KEYS"), "only")
	ApplyConfig(Config_withTelemetryQueryKeys([]any{"page"}, Config_default()))
	if got := procenv.Getenv(skyEnvName("TELEMETRY_QUERY_KEYS")); got != "only" {
		t.Fatalf("operator env must win over the builder, got %q", got)
	}
}

// ─── Class falsifier: a source scan ───────────────────────────────

// TestTelemetryURL_NoRawURLInTelemetrySinks fails when a raw request path,
// query, request URI, client address or forwarded-for header reaches a span
// attribute, a log, a panic report or a page view outside telemetry_url.go.
// It is the falsifier for the whole class: reverting any one site to the raw
// value turns it red.
func TestTelemetryURL_NoRawURLInTelemetrySinks(t *testing.T) {
	raw := regexp.MustCompile(`URL\.Path|RawQuery|RequestURI|URL\.String\(\)|RemoteAddr|X-Forwarded-For|X-Real-IP|\br\.Path\b|\.Referer\(\)|"Referer"`)

	// Files that exist only to record telemetry: no raw source at all.
	for _, f := range []string{"tracing.go", "observability_middleware.go", "trace_ring_processor.go", "panic_log.go"} {
		src := readSource(t, f)
		for i, line := range strings.Split(src, "\n") {
			if isGoComment(line) {
				continue
			}
			if raw.MatchString(line) {
				t.Errorf("%s:%d records a raw request value; use telemetry_url.go: %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}

	// Mixed files: the argument list of every telemetry sink call.
	sinks := []string{"LogRecoveredPanic(", "logPanicFrame(", "analyticsTrackPageView(", "attribute.String(", "logStructured(", "RecordLog("}
	for _, f := range []string{"rt.go", "rt_server.go", "live.go", "live_sse_session_lost.go", "analytics_kernel.go", "http_notjs.go", "stdlib_http_server.go", "http_stream.go"} {
		src := readSource(t, f)
		for _, sink := range sinks {
			for off := 0; ; {
				i := strings.Index(src[off:], sink)
				if i < 0 {
					break
				}
				start := off + i
				args := callArgs(src[start+len(sink)-1:])
				if raw.MatchString(args) && !strings.HasPrefix(strings.TrimSpace(lineAt(src, start)), "//") {
					t.Errorf("%s: %s%s records a raw request value; use telemetry_url.go", f, sink, args)
				}
				off = start + len(sink)
			}
		}
	}

	// Middleware.withLogging builds its context map before logEmit.
	body := funcBody(t, readSource(t, "rt.go"), "func Middleware_withLogging(")
	if regexp.MustCompile(`"path":\s*r\.Path|RemoteAddr|URL\.Path|RawQuery`).MatchString(body) {
		t.Errorf("Middleware.withLogging records a raw request value")
	}

	// Outbound HTTP error text: a transport error goes through redactURLError.
	for _, f := range []string{"http_notjs.go", "stdlib_http_server.go", "http_stream.go"} {
		src := readSource(t, f)
		if strings.Contains(src, "+ err.Error())") {
			t.Errorf("%s concatenates a raw transport error (its text carries the URL); use redactURLError", f)
		}
	}
}

func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func isGoComment(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "//")
}

func lineAt(src string, at int) string {
	s := strings.LastIndexByte(src[:at], '\n') + 1
	e := strings.IndexByte(src[at:], '\n')
	if e < 0 {
		return src[s:]
	}
	return src[s : at+e]
}

// callArgs returns the balanced "( … )" that starts at s[0].
func callArgs(s string) string {
	depth := 0
	for i, c := range s {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[:i+1]
			}
		}
	}
	return s
}

func funcBody(t *testing.T, src, sig string) string {
	t.Helper()
	i := strings.Index(src, sig)
	if i < 0 {
		t.Fatalf("%s not found", sig)
	}
	j := strings.Index(src[i:], "\n}\n")
	if j < 0 {
		t.Fatalf("end of %s not found", sig)
	}
	return src[i : i+j]
}

var _ = attribute.String
