//go:build !js

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
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
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
		if isSkyInternalRequest(r) {
			next.ServeHTTP(w, r)
			return
		}
		if skyGetenv("OBSERVABILITY_DISABLED") == "1" {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()

		// The route slot the app writes its route template into during
		// dispatch (telemetry_url.go). It goes on the context BEFORE the
		// span, so the span context, the goroutine trace context and every
		// copy of the request carry the same slot.
		r = withTelemetryRouteSlot(r)

		// Step 7 — OTel server span. Created FIRST so the request-id
		// can be unified with the trace-id (below). Honours inbound
		// traceparent so distributed traces chain correctly
		// (browser → CDN → API gateway → Sky → DB shows as one trace).
		// Before dispatch only the fallback route is known; the span is
		// renamed with the template after dispatch (FinishHTTPServerSpanRoute).
		spanCtx, span := StartHTTPServerSpan(r, telemetryRoute(r))

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
		route := telemetryRoute(r)
		target := telemetryTarget(r)
		FinishHTTPServerSpanRoute(span, r.Method, route, target, telemetryPath(r))
		EndHTTPServerSpan(span, sw.status, sw.bytesWritten)

		elapsed := time.Since(start).Seconds()
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
			Path:         target,
			Status:       sw.status,
			LatencyMS:    elapsed * 1000,
			BytesWritten: sw.bytesWritten,
			UserAgent:    r.Header.Get("User-Agent"),
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

// Unwrap exposes the wrapped writer to http.ResponseController, so a
// long-lived handler behind this middleware can lift the connection's
// deadlines (see stream_deadline.go). Without it the controller stops here
// with http.ErrNotSupported.
func (s *statusCapture) Unwrap() http.ResponseWriter { return s.ResponseWriter }

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
}

// accessLogLine is the serverless access-log line. It is built with
// json.Marshal so a `"` or `\` in a path (in `path` or `full` mode) cannot
// break the line. Field order is the order of the struct.
type accessLogLine struct {
	TS        string  `json:"ts"`
	Level     string  `json:"level"`
	Msg       string  `json:"msg"`
	ReqID     string  `json:"req_id"`
	Method    string  `json:"method"`
	Route     string  `json:"route"`
	Path      string  `json:"path"`
	Status    int     `json:"status"`
	LatencyMS float64 `json:"latency_ms"`
	Bytes     int64   `json:"bytes"`
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
		// stderr capture. json.Marshal escapes every value: the old
		// Fprintf line broke on a `"` in the path.
		line, err := json.Marshal(accessLogLine{
			TS:        time.Now().UTC().Format(time.RFC3339Nano),
			Level:     "info",
			Msg:       "http_request",
			ReqID:     e.ReqID,
			Method:    e.Method,
			Route:     e.Route,
			Path:      e.Path,
			Status:    e.Status,
			LatencyMS: math.Round(e.LatencyMS*100) / 100,
			Bytes:     e.BytesWritten,
		})
		if err == nil {
			os.Stderr.Write(append(line, '\n'))
		}
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
