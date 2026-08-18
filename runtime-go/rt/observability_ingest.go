package rt

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"sky-app/rt/telemetry"
)

// ─────────────────────────────────────────────────────────────────
// Universal observability ingestion endpoint
// ─────────────────────────────────────────────────────────────────
//
// Each Sky app that mounts sub-apps (via `rt.MountSubApp`) is the
// root of an observability namespace. Child processes push their
// log entries, metrics, and trace spans to the parent's
// `/_sky/observability/ingest` endpoint over plain JSON-over-HTTP;
// the parent stores them in its shared `telemetry.Default()` ring
// buffer with a `subapp=<namespace>` label so the existing console
// + Prometheus exposition surface everything in one place.
//
// Wire shape (single endpoint accepts a mixed envelope so children
// can batch heterogeneous events into a single POST, cutting
// per-message overhead):
//
//   POST /_sky/observability/ingest
//   X-Sky-Ingest-Token: <shared secret>
//   {
//     "namespace": "billing",
//     "logs":    [ {"ts": "...", "level": "info", "msg": "...", ...}, ...],
//     "metrics": [ {"name": "...", "type": "counter|gauge", ...}, ...],
//     "spans":   [ {"name": "...", "start_ms": ..., "duration_ms": ..., ...}, ...]
//   }
//
// Auth: HMAC-style shared secret in `X-Sky-Ingest-Token`. The token
// is auto-generated per parent boot (`generateIngestToken`); the
// parent passes it to every child via the `SKY_INGEST_TOKEN` env
// var (`subapp.go`). Constant-time compare to defeat timing
// attacks. Bad / missing token → 401.
//
// Threat model: this endpoint is reachable from anything that can
// hit the parent's listen address. In dev mode (localhost-only),
// the per-boot random token is enough. In production, operators
// can override via `SKY_INGEST_TOKEN` env to a fixed value managed
// by their secret store (so a sub-app deployed from a different
// host can authenticate).

// IngestPayload is the JSON envelope POSTed to
// /_sky/observability/ingest. All sections optional — children
// may push a logs-only batch, a metrics-only batch, etc., to
// minimise overhead.
type IngestPayload struct {
	Namespace string              `json:"namespace"`
	Logs      []IngestLog         `json:"logs,omitempty"`
	Metrics   []IngestMetric      `json:"metrics,omitempty"`
	Spans     []IngestSpan        `json:"spans,omitempty"`
}

// IngestLog mirrors telemetry.LogEntry on the wire. Time arrives
// as an RFC3339 string (parsed lazily; missing TS gets a server-
// side timestamp so children can omit it for "now").
type IngestLog struct {
	TS       string            `json:"ts,omitempty"`
	Level    string            `json:"level"`
	Message  string            `json:"msg"`
	ReqID    string            `json:"req_id,omitempty"`
	TraceID  string            `json:"trace_id,omitempty"`
	SpanID   string            `json:"span_id,omitempty"`
	Route    string            `json:"route,omitempty"`
	Status   int               `json:"status,omitempty"`
	LatencyMS float64          `json:"latency_ms,omitempty"`
	ErrorStr string            `json:"error,omitempty"`
	Fields   map[string]string `json:"fields,omitempty"`
}

// IngestMetric — one counter / gauge sample. For counters, `Delta`
// is the increment since the previous push (parent's store calls
// `Add` with the delta — robust to child restarts because no double-
// counting on lost / replayed pushes; the running total lives in
// the parent's store, not the child).
//
// For gauges, `Value` is the absolute level (parent calls
// `SetGauge`). `Type` defaults to "counter" when absent for
// backwards-compatible parsing.
//
// Histogram pushes (`type: "histogram"`) are accepted as well —
// `Value` then carries the observation that the child made; the
// parent's `Observe` finds the right bucket. Sub-apps with high-
// volume histogram traffic should batch aggressively to keep the
// wire chatter manageable.
type IngestMetric struct {
	Name   string            `json:"name"`
	Type   string            `json:"type,omitempty"` // counter | gauge | histogram (default counter)
	Delta  float64           `json:"delta,omitempty"`
	Value  float64           `json:"value,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

// IngestSpan — OTel-shape span over the wire. `StartMS` is unix
// millis; the duration is on the wire (not end-time) so children
// with skewed clocks still produce sensible parent-side durations.
type IngestSpan struct {
	TraceID       string            `json:"trace_id"`
	SpanID        string            `json:"span_id"`
	ParentID      string            `json:"parent_id,omitempty"`
	Name          string            `json:"name"`
	Kind          string            `json:"kind,omitempty"` // server | client | internal
	StartMS       int64             `json:"start_ms"`
	DurationMS    int64             `json:"duration_ms"`
	Attributes    map[string]string `json:"attrs,omitempty"`
	StatusCode    string            `json:"status,omitempty"` // ok | error
	StatusMessage string            `json:"status_message,omitempty"`
}

// ingestToken — per-process shared secret. Set once at startup
// (init) — never mutated thereafter, so atomic load suffices.
// Overridable via SKY_INGEST_TOKEN env (for prod / multi-host
// deployments where the token is managed externally).
var ingestToken atomic.Value // string

// IngestTokenInit picks (or generates) the ingestion token for
// this process. Reads SKY_INGEST_TOKEN env first (operator
// override); otherwise generates 32 random bytes hex-encoded.
// Safe to call multiple times — first call wins.
//
// Public so tests can pin a known token. Production callers
// invoke it once at startup (see Sky.Live + Sky.Http.Server
// runtime init).
func IngestTokenInit() string {
	if existing, ok := ingestToken.Load().(string); ok && existing != "" {
		return existing
	}
	if v := strings.TrimSpace(os.Getenv("SKY_INGEST_TOKEN")); v != "" {
		ingestToken.Store(v)
		return v
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure is catastrophic but we'd rather
		// still boot the app than die. Fall back to a process-
		// PID + time hash. Worst case the token is predictable;
		// auth then degrades but the endpoint still functions.
		tok := fmt.Sprintf("fallback-%d-%d", os.Getpid(), time.Now().UnixNano())
		ingestToken.Store(tok)
		return tok
	}
	tok := hex.EncodeToString(buf)
	ingestToken.Store(tok)
	return tok
}

// CurrentIngestToken returns the active token for sub-app spawn
// (so SpawnBinary can pass it to children). Returns empty if
// IngestTokenInit hasn't been called yet.
func CurrentIngestToken() string {
	v, _ := ingestToken.Load().(string)
	return v
}

// ─────────────────────────────────────────────────────────────────
// Endpoint mount + handler
// ─────────────────────────────────────────────────────────────────

// ingestMaxBytes caps the per-request body to defend against an
// accidentally-misconfigured child spamming the parent. 1 MiB is
// well above the typical batch (a few hundred entries) without
// being a useful DoS vector.
const ingestMaxBytes = 1 << 20

// ingestRingCapPerSubapp caps the cumulative number of pushed
// entries the parent will accumulate per sub-app namespace before
// the global telemetry ring's natural overflow (10K logs / 1K
// spans, see telemetry.NewStore defaults) takes over. Sub-app
// pushes share the same ring with parent-emitted entries — the
// in-memory store's eviction discipline applies uniformly.

// MountObservabilityIngestEndpoint registers
// /_sky/observability/ingest on `mux`. Called from
// `MountObservabilityEndpoints` (observability.go) so every Sky
// app that mounts observability gets ingestion for free.
//
// Idempotent: re-mounts panic at the http.ServeMux level (per
// safeMount discipline elsewhere), so callers should run only
// once per mux.
func MountObservabilityIngestEndpoint(mux *http.ServeMux) {
	// Materialise the token now so first-write children can pick
	// it up via CurrentIngestToken at spawn time.
	IngestTokenInit()
	safeMount(mux, "/_sky/observability/ingest", HandleObservabilityIngest)
}

// HandleObservabilityIngest accepts pushed observability data from
// sub-apps and writes it into the local telemetry store with a
// `subapp=` label (logs/traces use the dedicated `Subapp` struct
// field; metrics add it to their labels map).
//
// Status codes:
//   202 Accepted  — payload valid, written; no body
//   400 Bad Request — JSON parse failure or missing namespace
//   401 Unauthorized — missing / wrong X-Sky-Ingest-Token
//   413 Payload Too Large — body exceeds ingestMaxBytes
//   405 Method Not Allowed — anything other than POST
//
// Returns 202 instead of 200 because the write happens but a
// future async-to-disk pipeline could be a different latency
// class; sticking with the standard "accepted, processed later"
// semantic gives us room.
func HandleObservabilityIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Token check — constant-time compare to defeat timing
	// side-channels.
	expected := CurrentIngestToken()
	got := r.Header.Get("X-Sky-Ingest-Token")
	if expected == "" || got == "" ||
		subtle.ConstantTimeCompare([]byte(expected), []byte(got)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Bound the body so a misconfigured child can't OOM the
	// parent with a runaway batch.
	r.Body = http.MaxBytesReader(w, r.Body, ingestMaxBytes)
	defer r.Body.Close()

	var payload IngestPayload
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // surface schema-drift between child + parent versions
	if err := dec.Decode(&payload); err != nil {
		// MaxBytesReader's "request body too large" surfaces as
		// a wrapped error — distinguish so the caller can react.
		if err.Error() == "http: request body too large" {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	ns := strings.TrimSpace(payload.Namespace)
	if ns == "" {
		http.Error(w, "bad request: missing namespace", http.StatusBadRequest)
		return
	}
	// Ingest into the local telemetry store. We don't fail the
	// request on partial-ingest errors — observability is best-
	// effort by design (losing one log line should not break the
	// caller's tick). Counts surface in the response body for
	// the child to log + decide retry policy.
	store := telemetry.Default()
	stats := ingestInto(store, ns, payload)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "accepted",
		"logs":    stats.logs,
		"metrics": stats.metrics,
		"spans":   stats.spans,
	})
}

type ingestStats struct {
	logs, metrics, spans int
}

// ingestInto writes the payload into `store` with `namespace` as
// the subapp tag on every entry. Extracted from the handler so
// tests can drive it directly without going through HTTP.
func ingestInto(store *telemetry.Store, namespace string, p IngestPayload) ingestStats {
	var st ingestStats
	for _, l := range p.Logs {
		ts := time.Time{}
		if l.TS != "" {
			if t, err := time.Parse(time.RFC3339Nano, l.TS); err == nil {
				ts = t
			}
		}
		store.AppendLog(telemetry.LogEntry{
			TS:        ts,
			Level:     l.Level,
			Message:   l.Message,
			ReqID:     l.ReqID,
			TraceID:   l.TraceID,
			SpanID:    l.SpanID,
			Route:     l.Route,
			Status:    l.Status,
			LatencyMS: l.LatencyMS,
			ErrorStr:  l.ErrorStr,
			Subapp:    namespace,
			Fields:    l.Fields,
		})
		st.logs++
	}
	for _, m := range p.Metrics {
		// Boundary shape gate — see ingestMetricShapeOK. The 1 MiB
		// body limit bounds one REQUEST, not what accumulates: a
		// metric name seeds the store's cardinality-warn dedupe and a
		// label set becomes a process-lifetime series key, so absurd
		// shapes are refused here rather than remembered there.
		if !ingestMetricShapeOK(m.Name, m.Labels) {
			continue
		}
		labels := withSubappLabel(m.Labels, namespace)
		switch strings.ToLower(m.Type) {
		case "", "counter":
			store.Add(m.Name, labels, m.Delta)
		case "gauge":
			store.SetGauge(m.Name, labels, m.Value)
		case "histogram":
			store.Observe(m.Name, labels, m.Value)
		}
		st.metrics++
	}
	for _, s := range p.Spans {
		start := time.Unix(0, s.StartMS*int64(time.Millisecond))
		end := start.Add(time.Duration(s.DurationMS) * time.Millisecond)
		store.AppendTrace(telemetry.TraceEntry{
			TraceID:       s.TraceID,
			SpanID:        s.SpanID,
			ParentID:      s.ParentID,
			Name:          s.Name,
			Kind:          s.Kind,
			StartTime:     start,
			EndTime:       end,
			Attributes:    s.Attributes,
			StatusCode:    s.StatusCode,
			StatusMessage: s.StatusMessage,
			Subapp:        namespace,
		})
		st.spans++
	}
	return st
}

// Metric-shape bounds enforced at the ingest boundary. Metrics are
// enum-shaped by design (method names, status codes, msg names);
// anything past these bounds is a misbehaving or malicious child,
// and accepting it would grow process-lifetime state (series keys,
// the cardinality-warn dedupe) from wire input. Logs and spans take
// the generic byte bounds in telemetry/limits.go on their store
// path; metrics additionally REJECT here because a truncated metric
// name or label silently collides distinct series.
const (
	ingestMaxMetricNameBytes = 256
	ingestMaxLabels          = 32
	ingestMaxLabelKeyBytes   = 128
	ingestMaxLabelValueBytes = 512
)

// ingestMetricShapeOK reports whether a pushed metric sample is
// within the boundary bounds above. Rejection is per-sample: the
// rest of the batch still lands.
func ingestMetricShapeOK(name string, labels map[string]string) bool {
	if name == "" || len(name) > ingestMaxMetricNameBytes {
		return false
	}
	if len(labels) > ingestMaxLabels {
		return false
	}
	for k, v := range labels {
		if len(k) > ingestMaxLabelKeyBytes || len(v) > ingestMaxLabelValueBytes {
			return false
		}
	}
	return true
}

// withSubappLabel copies `in` and sets `subapp=<ns>`. We always
// override the caller's value (sub-apps shouldn't be able to
// masquerade as another namespace via the wire). Returns a fresh
// map even when `in` is nil so the caller can mutate safely.
func withSubappLabel(in map[string]string, ns string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for k, v := range in {
		if k == "subapp" {
			continue
		}
		out[k] = v
	}
	out["subapp"] = ns
	return out
}
