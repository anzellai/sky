package rt

// HTTP observability middleware for Sky.Live + Sky.Http.Server.
// Generates / honours X-Request-Id, observes request latency,
// counts requests by method+route+status, emits one structured
// access-log line per request.
//
// Wires Phase 1.1a Step 3 onto the foundations from Step 1
// (telemetry store) + Step 4 (the /_sky/metrics endpoint that
// surfaces these counters).
//
// Design notes (per docs/v1-rfc/1-observability.md):
//
//   - The middleware MUST skip metering the observability endpoints
//     themselves. A /_sky/metrics scrape that bumps
//     sky_live_requests_total{route="/_sky/metrics"} would inflate
//     counters by the scrape rate — useless noise.
//
//   - Route labels are bounded. Go's 1.22+ ServeMux exposes
//     r.Pattern; we use that when present. For dynamic
//     wildcard-matched URLs the pattern naturally contains "{}"
//     placeholders, giving sensible low-cardinality labels.
//     Untemplated routes (everything under "/") fall through to a
//     coarse first-segment heuristic.
//
//   - Serverless mode: access logs go to stderr (Cloud Run / Lambda
//     auto-capture stderr for their logging pipeline), NOT the
//     in-memory ring buffer (container evicts before any reader
//     gets value out of it).
//
//   - Request context carries the req-id for downstream readers
//     (Cmd.perform from Step 2 will use this).

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"sky-app/rt/telemetry"
)

// errHijackNotSupported — returned when middleware-wrapped writer is
// asked to Hijack but the underlying writer doesn't implement
// http.Hijacker. Pre-built so the error string is stable across
// stack traces (avoids per-call allocation too).
var errHijackNotSupported = errors.New("hijack not supported by wrapped ResponseWriter")

// requestIDContextKey — unique key type so context.WithValue
// collisions are impossible. Per Go convention, unexported struct
// type with no fields.
type requestIDContextKey struct{}

var reqIDKey = requestIDContextKey{}

// RequestIDFromContext extracts the Sky request-id stamped by the
// observability middleware. Returns "" when called outside a
// request-handling goroutine (typical for background workers, init
// code, tests). Step 2 (Cmd.perform propagation) reads this via
// the goroutine-local ambient context.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(reqIDKey).(string); ok {
		return v
	}
	return ""
}

// WithRequestID stamps a request-id on the given context. Used by
// the middleware on incoming requests, and by Cmd.perform when
// spawning Task goroutines (Step 2).
func WithRequestID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, reqIDKey, id)
}

// ObservabilityMiddleware wraps the given handler with the standard
// req-id + access-log + metric pipeline. Mounted by Sky.Live and
// Sky.Http.Server immediately after panic-recovery so every served
// request is observed.
//
// Skips observability endpoints (/_sky/...) to avoid recursive
// metering: a Prometheus scrape against /_sky/metrics would
// otherwise bump request counters at the scrape interval. Health-
// check endpoints are also skipped so the per-second readyz pings
// from orchestrators don't drown out real traffic in dashboards.
func ObservabilityMiddleware(next http.Handler) http.Handler {
	// PR10-D — stamp service.namespace on the request context so
	// telemetry call sites can label per-app emissions. Wrapping
	// next lets the namespace tag survive even the early-exit paths
	// below (/_sky/* short-circuit + OBSERVABILITY_DISABLED bail).
	// Cost is one map snapshot read per request; the inner loop
	// returns immediately when no in-process sub-apps are mounted
	// (the common case).
	next = WithSubAppNamespace(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/_sky/") {
			next.ServeHTTP(w, r)
			return
		}
		if skyGetenv("OBSERVABILITY_DISABLED") == "1" {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()

		// Step 7 — OTel server span. Created FIRST so the request-id
		// can be unified with the trace-id (below). Honours inbound
		// traceparent so distributed traces chain correctly
		// (browser → CDN → API gateway → Sky → DB shows as one trace).
		spanCtx, span := StartHTTPServerSpan(r, routeLabelFor(r))

		// Unify the request-id with the OTEL trace-id. Before this,
		// logs carried a standalone reqID and spans carried a
		// separate traceID — two id spaces, so a log line could not
		// be pivoted to its trace. Now they are the SAME id: a log's
		// correlation badge equals the trace's id, and the console's
		// Logs / Traces search boxes cross-reference. Falls back to a
		// generated id only if the tracer is fully disabled (zero
		// trace-id) — which doesn't happen while the in-process ring
		// processor is registered.
		reqID := span.SpanContext().TraceID().String()
		if reqID == "" || reqID == "00000000000000000000000000000000" {
			reqID = r.Header.Get("X-Request-Id")
			if reqID == "" {
				reqID = generateRequestID()
			}
		}
		w.Header().Set("X-Request-Id", reqID)

		// Stamp the unified id on the context so Cmd.perform,
		// RequestIDFromContext, and every Log.* call correlate to
		// the trace.
		spanCtx = WithRequestID(spanCtx, reqID)
		r = r.WithContext(spanCtx)

		// Stamp the goroutine with the FULL span context (it carries
		// both the OTEL server span and the req-id). Auto-instrumented
		// kernels — Db.* / Auth.* / Http.* / session store — running
		// on this request goroutine read it via CurrentTraceContext()
		// in WithSpan and nest their spans under the request span.
		// Cleared on handler exit so the entry doesn't leak past the
		// net/http worker goroutine being reused for the next request.
		SetGoroutineTraceContext(spanCtx)
		defer ClearGoroutineTraceContext()

		// Wrap ResponseWriter to capture status + bytes-written.
		sw := newStatusCapture(w)
		next.ServeHTTP(sw, r)
		EndHTTPServerSpan(span, sw.status, sw.bytesWritten)

		elapsed := time.Since(start).Seconds()
		route := routeLabelFor(r)
		status := strconv.Itoa(sw.status)
		// RecordCounter / RecordHistogram dual-write to local store
		// + push to parent when running as a sub-app, so sub-app
		// request counts surface alongside parent metrics on the
		// parent's /_sky/metrics with `subapp=<ns>` label.
		RecordCounter("sky_live_requests_total", map[string]string{
			"method": r.Method,
			"route":  route,
			"status": status,
		}, 1)
		RecordHistogram("sky_live_request_seconds",
			map[string]string{"route": route}, elapsed)
		if sw.bytesWritten > 0 {
			RecordHistogram("sky_http_response_bytes",
				map[string]string{"route": route}, float64(sw.bytesWritten))
		}

		// Phase 3: record the request as a span in the local trace
		// ring (and push to parent if we're a sub-app). The OTel
		// SDK path (StartHTTPServerSpan above) exports to remote
		// collectors via OTLP; this in-process write feeds the
		// console's Traces tab and the parent's aggregated view.
		// Cheap — one struct alloc + ring append.
		statusCode := "ok"
		if sw.status >= 400 {
			statusCode = "error"
		}
		RecordTrace(telemetry.TraceEntry{
			TraceID:    reqID,
			SpanID:     reqID, // reqID doubles as root span id
			Name:       r.Method + " " + route,
			Kind:       "server",
			StartTime:  start,
			EndTime:    time.Now(),
			StatusCode: statusCode,
			Attributes: map[string]string{
				"http.method":      r.Method,
				"http.route":       route,
				"http.status_code": status,
			},
		})

		emitAccessLog(accessLogEntry{
			ReqID:        reqID,
			Method:       r.Method,
			Route:        route,
			Path:         r.URL.Path,
			Status:       sw.status,
			LatencyMS:    elapsed * 1000,
			BytesWritten: sw.bytesWritten,
			UserAgent:    r.Header.Get("User-Agent"),
			RemoteAddr:   clientIP(r),
		})
	})
}

// ─── Request ID generation ────────────────────────────────────

// generateRequestID returns a 16-byte hex-encoded random id. Not
// UUID v7 (would need a third-party lib for time-sorted UUIDs);
// 128 bits of randomness is collision-free at any practical request
// rate and matches the format orchestrators (Envoy, Istio, fly.io
// proxy) use for their own request-id headers.
//
// Falls back to a counter-based id on the (unlikely) event that
// crypto/rand fails — we never want to drop a request because the
// id generator hiccuped.
func generateRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fallback: nanosecond timestamp. Loses uniqueness across
		// simultaneous requests but is better than empty / crash.
		return fmt.Sprintf("ts-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// ─── Route label normalisation ────────────────────────────────

// routeLabelFor returns the cardinality-bounded route label for
// metrics. Priority:
//
//  1. Go 1.22+ r.Pattern (the registered ServeMux pattern,
//     including any {wildcard} placeholders) when non-empty AND
//     non-"/" (the bare "/" pattern means "catch-all" and would
//     collapse every dynamic URL into one label).
//  2. First two path segments joined with "/" — bounds label
//     cardinality at the number of distinct top-level resources.
//  3. "/" fallback.
//
// Trade-off: a URL like /users/123/orders/456 becomes /users/123
// under heuristic (2). Lossy but predictable. Users wanting true
// route templates register their handlers with explicit patterns
// (e.g. `mux.HandleFunc("/users/{id}", ...)`) which path 1 picks
// up cleanly.
func routeLabelFor(r *http.Request) string {
	if p := r.Pattern; p != "" && p != "/" {
		return p
	}
	path := r.URL.Path
	if path == "" || path == "/" {
		return "/"
	}
	segments := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 3)
	if len(segments) >= 2 && segments[1] != "" {
		return "/" + segments[0] + "/" + segments[1]
	}
	return "/" + segments[0]
}

// ─── Status capture ───────────────────────────────────────────

// statusCapture wraps http.ResponseWriter to remember the status
// code and bytes written for post-request metering. Without this,
// we'd have no way to know whether the handler returned 200 / 404
// / 500 — http.ResponseWriter exposes Write/WriteHeader but no
// read-back.
type statusCapture struct {
	http.ResponseWriter
	status       int
	bytesWritten int64
	wroteHeader  bool
}

func newStatusCapture(w http.ResponseWriter) *statusCapture {
	return &statusCapture{
		ResponseWriter: w,
		status:         http.StatusOK, // default if handler never calls WriteHeader
	}
}

func (s *statusCapture) WriteHeader(code int) {
	if s.wroteHeader {
		// http.ResponseWriter docs say second WriteHeader is a no-op
		// (with a stdlib log warning). Match that.
		return
	}
	s.status = code
	s.wroteHeader = true
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusCapture) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.wroteHeader = true
		// Status stays at the default 200.
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytesWritten += int64(n)
	return n, err
}

// Flush propagates to the underlying ResponseWriter when it
// supports the http.Flusher interface. SSE handlers depend on
// being able to flush per chunk; without this method Sky.Live's
// SSE goes silent through the middleware.
func (s *statusCapture) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack propagates to the underlying ResponseWriter when it
// supports http.Hijacker (used for WebSocket upgrades, raw TCP
// streams). Without this method, hijackable handlers panic when
// wrapped.
func (s *statusCapture) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		// Once hijacked we lose status capture — that's correct
		// (hijacked connections don't have a single response status).
		// Mark as written so subsequent WriteHeader is a no-op.
		s.wroteHeader = true
		return h.Hijack()
	}
	return nil, nil, errHijackNotSupported
}

// ─── Access log ───────────────────────────────────────────────

type accessLogEntry struct {
	ReqID        string
	Method       string
	Route        string
	Path         string
	Status       int
	LatencyMS    float64
	BytesWritten int64
	UserAgent    string
	RemoteAddr   string
}

// emitAccessLog routes the entry to the right sink:
//   - Serverless mode → stderr (Cloud Run / Lambda capture stderr
//     into their managed logging pipeline). Format: structured
//     JSON line because that's what the managed pipelines parse
//     best.
//   - VM mode → telemetry Hot-tier ring buffer (dashboard reads it).
//     Stderr in addition when [log] format = "json" is set (so the
//     classic `tail -f production.log` workflow keeps working).
func emitAccessLog(e accessLogEntry) {
	if IsServerless() {
		// Single structured line per request → Cloud Run / Lambda
		// stderr capture. Minimal allocation; no JSON encoder
		// roundtrip for the hot path.
		fmt.Fprintf(os.Stderr,
			`{"ts":"%s","level":"info","msg":"http_request","req_id":"%s","method":"%s","route":"%s","path":"%s","status":%d,"latency_ms":%.2f,"bytes":%d}`+"\n",
			time.Now().UTC().Format(time.RFC3339Nano),
			e.ReqID, e.Method, e.Route, e.Path,
			e.Status, e.LatencyMS, e.BytesWritten)
		return
	}
	// Bake the request shape into the visible Message so the
	// console Logs tab (which doesn't render Fields) shows
	// "GET / 200 (3ms)" instead of a wall of identical
	// "http_request" lines. Status escalates the log level so 4xx
	// surfaces as warn and 5xx as error — both stand out against
	// the steady-state INFO stream.
	level := "info"
	if e.Status >= 500 {
		level = "error"
	} else if e.Status >= 400 {
		level = "warn"
	}
	msg := fmt.Sprintf("%s %s %d (%dms)",
		e.Method, e.Path, e.Status, int(e.LatencyMS))
	RecordLog(telemetry.LogEntry{
		TS:        time.Now(),
		Level:     level,
		Message:   msg,
		ReqID:     e.ReqID,
		Route:     e.Route,
		Status:    e.Status,
		LatencyMS: e.LatencyMS,
		Fields: map[string]string{
			"method": e.Method,
			"path":   e.Path,
			"bytes":  strconv.FormatInt(e.BytesWritten, 10),
		},
	})
}

// clientIP best-effort extracts the client IP. Honours
// X-Forwarded-For (first IP — the original client; subsequent
// IPs are proxies) and X-Real-IP, falling back to r.RemoteAddr.
// Not trying to be cryptographically authoritative — for access
// logs only.
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
