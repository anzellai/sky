package telemetry

import (
	"sync"
	"time"
)

// TraceEntry mirrors the OpenTelemetry Span Data Model in a compact
// in-memory form. The Cold-tier exporter (OTLP) maps these to OTel
// protobuf spans 1:1; the dashboard renders them in a waterfall view.
//
// Field naming follows OTel conventions so a future migration to a
// real OTel SDK is a rename, not a refactor:
//
//	TraceID, SpanID, ParentID   — W3C trace-context shape
//	Name                         — span name (e.g. "GET /api/notes")
//	Kind                         — "server" / "client" / "internal"
//	StartTime, EndTime           — wall-clock
//	Attributes                   — key-value pairs
//	StatusCode                   — "ok" / "error"
//	StatusMessage                — error description (when StatusCode = error)
type TraceEntry struct {
	TraceID       string
	SpanID        string
	ParentID      string
	Name          string
	Kind          string
	StartTime     time.Time
	EndTime       time.Time
	Attributes    map[string]string
	StatusCode    string
	StatusMessage string
	// Subapp namespace this span was emitted from. Populated by the
	// observability ingest endpoint when accepting cross-process
	// pushes; empty for parent-process spans. Console waterfall can
	// group / filter by this.
	Subapp string
}

// Duration returns the elapsed wall-clock time for a span. Returns
// zero when EndTime is unset (still in-flight).
func (e TraceEntry) Duration() time.Duration {
	if e.EndTime.IsZero() {
		return 0
	}
	return e.EndTime.Sub(e.StartTime)
}

// traceRing — same shape as logRing but typed differently. Could be
// generic, but Go generics on struct-field maps still don't compile
// down as efficiently as a hand-specialised version. Keep two
// near-duplicates rather than add a layer.
type traceRing struct {
	mu   sync.Mutex
	buf  []TraceEntry
	cap  int
	head int
	full bool
}

func newTraceRing(capacity int) *traceRing {
	return &traceRing{
		buf: make([]TraceEntry, capacity),
		cap: capacity,
	}
}

func (r *traceRing) append(e TraceEntry) {
	if e.StartTime.IsZero() {
		e.StartTime = time.Now()
	}
	r.mu.Lock()
	r.buf[r.head] = e
	r.head++
	if r.head == r.cap {
		r.head = 0
		r.full = true
	}
	r.mu.Unlock()
}

func (r *traceRing) recent(limit int) []TraceEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.cap
	if !r.full {
		n = r.head
	}
	if limit > 0 && limit < n {
		n = limit
	}
	out := make([]TraceEntry, n)
	pos := r.head - 1
	for i := 0; i < n; i++ {
		if pos < 0 {
			pos = r.cap - 1
		}
		out[i] = r.buf[pos]
		pos--
	}
	return out
}

func (r *traceRing) snapshotCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.full {
		return r.cap
	}
	return r.head
}
