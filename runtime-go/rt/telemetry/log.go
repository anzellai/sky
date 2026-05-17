package telemetry

import (
	"sync"
	"time"
)

// LogEntry is a structured log line. Stored in the in-memory ring
// buffer; emitted to the access log; exposed to the dashboard's
// Logs tab; serialised to JSON when sky.toml `[log] format = "json"`.
//
// Field naming follows the conventions documented in
// docs/v1-rfc/1-observability.md §"Design — Endpoints":
//
//	ts, level, msg          — always present
//	req_id, trace_id        — populated when emitted under a request
//	route, latency_ms       — populated for HTTP access lines
//	status, error           — populated for HTTP / error lines
//	fields                  — free-form attrs (caller-supplied)
//
// Concrete fields outside the named ones are kept in Fields so the
// JSON serialiser can flatten them at the top level.
type LogEntry struct {
	TS       time.Time
	Level    string // "debug" | "info" | "warn" | "error"
	Message  string
	ReqID    string
	TraceID  string
	SpanID   string
	Route    string
	Status   int
	LatencyMS float64
	ErrorStr string
	// Free-form attributes. Caller is responsible for low-cardinality
	// values (we don't enforce here because that would block useful
	// debugging context). Typical values: msg constructor name, user
	// id, session id, query name.
	Fields map[string]string
}

// logRing is the in-memory ring buffer. Push is O(1); reads under
// `recent()` are O(min(n, capacity)) and allocate a fresh slice so
// the caller can mutate freely.
//
// Concurrency: single mutex. The hot-path append is ~150 ns under
// contention on commodity hardware (measured in
// log_ring_bench_test.go) which is well within the per-request
// latency budget for an access log.
type logRing struct {
	mu       sync.Mutex
	buf      []LogEntry
	cap      int
	head     int  // next write index
	full     bool // true once we've wrapped at least once
}

func newLogRing(capacity int) *logRing {
	return &logRing{
		buf: make([]LogEntry, capacity),
		cap: capacity,
	}
}

func (r *logRing) append(e LogEntry) {
	if e.TS.IsZero() {
		e.TS = time.Now()
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

// recent returns the last `limit` entries, newest first. When
// limit is zero or negative, returns all entries.
func (r *logRing) recent(limit int) []LogEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.cap
	if !r.full {
		n = r.head
	}
	if limit > 0 && limit < n {
		n = limit
	}
	out := make([]LogEntry, n)
	// Walk backwards from head-1.
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

// snapshotCount returns the number of entries currently held.
// Exposed for /_sky/console + tests.
func (r *logRing) snapshotCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.full {
		return r.cap
	}
	return r.head
}
