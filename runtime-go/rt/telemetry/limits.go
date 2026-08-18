package telemetry

// Byte bounds for retained telemetry.
//
// The rings are COUNT-bounded (10k logs / 1k spans) but a slot used
// to hold attacker-sized strings verbatim: a request path is
// accepted up to 1 MiB (serverMaxHeaderBytes), lands raw in the
// access-log message and Fields["path"], and the trace-ring
// processor copies every span attribute uncapped — http.target,
// user_agent.original, the full db.statement, StatusMessage =
// err.Error(). 10k requests with 1 MiB paths pinned ~20 GiB
// forever; metric label values are worse, because a series key
// lives for the whole process lifetime.
//
// The bound is applied where entries are BUILT INTO the store
// (AppendLog / AppendTrace / the metric series getters) and where
// they are BUFFERED for the parent push (PushExporter.Push*), so
// the rings, the persistence queue, and the wire all inherit it.
// Truncation is marked, never silent.
//
// Cap rationale:
//   - 4 KiB generic: comfortably above any legitimate path, UA,
//     message, or error string; 10k log slots × ~8 KiB ⇒ the ring's
//     worst case is tens of MB, not tens of GB.
//   - 8 KiB for db.statement: real application SQL (reports, big
//     IN-lists get placeholders) fits; a statement past 8 KiB is
//     truncated with the marker and remains identifiable.
//   - 64 map entries / 256 B keys: telemetry fields are structural
//     metadata, not payload storage.

import (
	"sort"
	"strconv"
)

const (
	// maxFieldBytes bounds every generic retained string: log
	// messages, error strings, routes, span names, status messages,
	// map values, metric label values.
	maxFieldBytes = 4 << 10
	// maxSQLBytes bounds the db.statement span attribute, which is
	// legitimately longer than other fields.
	maxSQLBytes = 8 << 10
	// maxKeyBytes bounds map keys (field names, attr names, label
	// names) and metric names.
	maxKeyBytes = 256
	// maxMapEntries bounds Fields / Attributes / Labels entry count.
	maxMapEntries = 64
	// truncationMarker is appended to every truncated value so an
	// operator can tell "short value" from "value that was cut".
	truncationMarker = "…[truncated]"
	// droppedFieldsKey carries the count of map entries dropped by
	// the entry-count cap.
	droppedFieldsKey = "sky.dropped_fields"
)

// truncateBytes cuts s to at most max bytes (on a rune boundary so
// the retained prefix stays valid UTF-8) and appends the marker.
// Values within the cap are returned unchanged, zero-alloc.
func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && cut > max-4 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + truncationMarker
}

// attrValueCap: db.statement gets the SQL cap when the map is
// attribute-shaped; everything else the generic cap.
func attrValueCap(sqlAware bool, key string) int {
	if sqlAware && key == "db.statement" {
		return maxSQLBytes
	}
	return maxFieldBytes
}

// boundMap returns m unchanged when every key/value is within
// bounds (the overwhelmingly common case — one length check per
// entry, no allocation). Otherwise it returns a bounded copy:
// lexicographically-first maxMapEntries keys kept (deterministic —
// map iteration order is not), keys and values truncated, and a
// marker entry counting dropped fields.
func boundMap(m map[string]string, sqlAware bool) map[string]string {
	if len(m) == 0 {
		return m
	}
	ok := len(m) <= maxMapEntries
	if ok {
		for k, v := range m {
			if len(k) > maxKeyBytes || len(v) > attrValueCap(sqlAware, k) {
				ok = false
				break
			}
		}
	}
	if ok {
		return m
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	dropped := 0
	if len(keys) > maxMapEntries {
		dropped = len(keys) - maxMapEntries
		keys = keys[:maxMapEntries]
	}
	out := make(map[string]string, len(keys)+1)
	for _, k := range keys {
		out[truncateBytes(k, maxKeyBytes)] = truncateBytes(m[k], attrValueCap(sqlAware, k))
	}
	if dropped > 0 {
		out[droppedFieldsKey] = strconv.Itoa(dropped)
	}
	return out
}

// BoundLogEntry clamps every attacker-sizable field of a log entry.
// Exported so the sub-app PushExporter (package rt) applies the
// same bound to its wire buffers.
func BoundLogEntry(e LogEntry) LogEntry {
	e.Message = truncateBytes(e.Message, maxFieldBytes)
	e.ErrorStr = truncateBytes(e.ErrorStr, maxFieldBytes)
	e.Route = truncateBytes(e.Route, maxFieldBytes)
	e.Fields = boundMap(e.Fields, false)
	return e
}

// BoundTraceEntry clamps every attacker-sizable field of a span.
func BoundTraceEntry(e TraceEntry) TraceEntry {
	e.Name = truncateBytes(e.Name, maxFieldBytes)
	e.StatusMessage = truncateBytes(e.StatusMessage, maxFieldBytes)
	e.Attributes = boundMap(e.Attributes, true)
	return e
}

// BoundLabels clamps a metric label map. Label values become part
// of the series key and live as long as the process, so this is the
// bound that matters most.
func BoundLabels(m map[string]string) map[string]string {
	return boundMap(m, false)
}

// boundMetricName clamps a metric family name (arbitrary on the
// ingest wire).
func boundMetricName(n string) string {
	return truncateBytes(n, maxKeyBytes)
}
