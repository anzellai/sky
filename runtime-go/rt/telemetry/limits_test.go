package telemetry

// UNBOUNDED-MEMORY regression tests: the rings are COUNT-bounded
// (10k logs / 1k spans), but each slot used to hold attacker-sized
// strings verbatim — a request path is accepted up to 1 MiB
// (serverMaxHeaderBytes), lands RAW in the access-log message AND
// Fields["path"], and every span attribute (http.target,
// user_agent.original, the full db.statement) was copied uncapped
// into the trace ring. 10k requests with 1 MiB paths pinned ~20 GiB
// forever. These tests assert BYTES, not counts, at the point where
// entries are built into the store — so every ring, the persistence
// queue, and the parent-push wire inherit the bound.
//
// Caps under test (limits.go): 4 KiB per generic string field /
// attr value, 8 KiB for db.statement, 256 B keys, 64 map entries.
// The slack constant below only allows for the truncation marker.

import (
	"strings"
	"testing"
)

const (
	tGenericCap = 4096
	tSQLCap     = 8192
	tMarkSlack  = 64 // room for the truncation marker
)

func TestAppendLog_AdversarialEntryIsByteBounded(t *testing.T) {
	s := NewStore()
	mega := strings.Repeat("p", 1<<20)
	s.AppendLog(LogEntry{
		Level:    "info",
		Message:  "GET " + mega + " 200 (1ms)",
		ErrorStr: mega,
		Route:    mega,
		Fields:   map[string]string{"path": mega, "method": "GET"},
	})
	got := s.RecentLogs(1)[0]
	if len(got.Message) > tGenericCap+tMarkSlack {
		t.Errorf("Message retained %d bytes; want <= %d (+marker)", len(got.Message), tGenericCap)
	}
	if len(got.ErrorStr) > tGenericCap+tMarkSlack {
		t.Errorf("ErrorStr retained %d bytes; want <= %d (+marker)", len(got.ErrorStr), tGenericCap)
	}
	if len(got.Route) > tGenericCap+tMarkSlack {
		t.Errorf("Route retained %d bytes; want <= %d (+marker)", len(got.Route), tGenericCap)
	}
	if v := got.Fields["path"]; len(v) > tGenericCap+tMarkSlack {
		t.Errorf("Fields[path] retained %d bytes; want <= %d (+marker)", len(v), tGenericCap)
	}
	if got.Fields["method"] != "GET" {
		t.Errorf("in-bounds field disturbed: %q", got.Fields["method"])
	}
}

func TestAppendTrace_AdversarialAttributesAreByteBounded(t *testing.T) {
	s := NewStore()
	mega := strings.Repeat("q", 1 << 20)
	s.AppendTrace(TraceEntry{
		TraceID: "t1", SpanID: "s1", Name: "db.query " + mega,
		StatusMessage: mega,
		Attributes: map[string]string{
			"db.statement":        mega,
			"user_agent.original": mega,
			"http.target":         mega,
		},
	})
	got := s.RecentTraces(1)[0]
	if len(got.Name) > tGenericCap+tMarkSlack {
		t.Errorf("Name retained %d bytes; want <= %d (+marker)", len(got.Name), tGenericCap)
	}
	if len(got.StatusMessage) > tGenericCap+tMarkSlack {
		t.Errorf("StatusMessage retained %d bytes; want <= %d (+marker)", len(got.StatusMessage), tGenericCap)
	}
	if v := got.Attributes["db.statement"]; len(v) > tSQLCap+tMarkSlack {
		t.Errorf("db.statement retained %d bytes; want <= %d (+marker)", len(v), tSQLCap)
	}
	for _, k := range []string{"user_agent.original", "http.target"} {
		if v := got.Attributes[k]; len(v) > tGenericCap+tMarkSlack {
			t.Errorf("%s retained %d bytes; want <= %d (+marker)", k, len(v), tGenericCap)
		}
	}
}

// A map with an absurd number of entries is capped in entry count,
// with a marker naming how many were dropped.
func TestAppendLog_AdversarialFieldCountIsBounded(t *testing.T) {
	s := NewStore()
	fields := make(map[string]string, 1000)
	for i := 0; i < 1000; i++ {
		fields["k"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+itoa(i)] = "v"
	}
	s.AppendLog(LogEntry{Level: "info", Message: "m", Fields: fields})
	got := s.RecentLogs(1)[0]
	if len(got.Fields) > 64+1 { // +1 for the dropped-fields marker
		t.Errorf("retained %d fields; want <= 64 (+marker)", len(got.Fields))
	}
}

// The end-to-end bound the attacker actually fights: total bytes
// pinned by the log ring after a burst of mega-entries.
func TestLogRing_TotalRetainedBytesBounded(t *testing.T) {
	s := NewStore()
	mega := strings.Repeat("z", 1<<20)
	const n = 50
	for i := 0; i < n; i++ {
		s.AppendLog(LogEntry{
			Level:   "info",
			Message: mega,
			Fields:  map[string]string{"path": mega},
		})
	}
	total := 0
	for _, e := range s.RecentLogs(0) {
		total += len(e.Message) + len(e.ErrorStr)
		for k, v := range e.Fields {
			total += len(k) + len(v)
		}
	}
	// 50 entries × (4 KiB message + 4 KiB path + slack). Unbounded
	// retention would be 50 × 2 MiB = 100 MiB here.
	const budget = n * (2*tGenericCap + 2*tMarkSlack + 256)
	if total > budget {
		t.Errorf("log ring pins %d bytes after %d adversarial entries; want <= %d", total, n, budget)
	}
}

// Metric label values are part of the series key and live for the
// process lifetime once a series exists — they get the same bound.
func TestMetricLabels_AdversarialValueIsByteBounded(t *testing.T) {
	s := NewStore()
	mega := strings.Repeat("l", 1<<20)
	s.Inc("hits", map[string]string{"route": mega})
	for _, m := range s.Snapshot() {
		if m.Name != "hits" {
			continue
		}
		if v := m.Labels["route"]; len(v) > tGenericCap+tMarkSlack {
			t.Errorf("label value retained %d bytes; want <= %d (+marker)", len(v), tGenericCap)
		}
		return
	}
	t.Fatal("hits series not found")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for n > 0 {
		p--
		b[p] = byte('0' + n%10)
		n /= 10
	}
	return string(b[p:])
}
