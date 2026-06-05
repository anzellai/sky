// Package hub — bridge from hub.Store → rt.HubStoreReader.
//
// v0.16.4 Option B B4. `rt/` declares the interface (see
// `runtime-go/rt/hub_bridge.go`); this file implements it as a
// thin wrapper around `*Store`. The boundary uses JSON strings
// for filter/row payloads so `rt/` doesn't need to import `hub/`
// (would form a cycle — `hub/` already imports `rt/` for
// logging + telemetry).
//
// JSON marshalling is the cost; per-row processing is dominated by
// the SQLite query, so the marshal overhead is invisible (a few
// hundred bytes per response).

package hub

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	rt "sky-app/rt"
)

// AsReader returns a `rt.HubStoreReader` view of this store. The
// hub daemon's Run() calls `rt.SetHubStore(store.AsReader())` once
// the store is open; subsequent Sky-side Hub_* kernel calls route
// here.
func (s *Store) AsReader() rt.HubStoreReader {
	return &storeReader{s: s}
}

// storeReader is the concrete `rt.HubStoreReader`. One per Store.
type storeReader struct {
	s *Store
}

// Counts delegates straight to Store.Counts.
func (r *storeReader) Counts() (logs, metrics, spans int, err error) {
	return r.s.Counts()
}

// hubLogFilter mirrors the Sky-side LogFilter shape (camelCase
// fields). Sent in via `rt.encodeFilterJSON` per call.
type hubLogFilter struct {
	Query     string `json:"query"`
	Session   string `json:"session"`
	ShowDebug bool   `json:"showDebug"`
	ShowInfo  bool   `json:"showInfo"`
	ShowWarn  bool   `json:"showWarn"`
	ShowError bool   `json:"showError"`
}

// hubLogRow is the wire row shape the console UI's LogEntry record
// decodes against (matches `sky-bundled/console/src/State.sky`'s
// `type alias LogEntry` field set). Field tags use lowerCamel so
// `narrowMapToStruct` resolves them off the runtime's lower-first
// probe path.
type hubLogRow struct {
	Time      string  `json:"time"`
	Level     string  `json:"level"`
	Message   string  `json:"message"`
	Subapp    string  `json:"subapp"`
	ReqID     string  `json:"reqId"`
	SessionID string  `json:"sessionId"`
	UserLabel string  `json:"userLabel"`
	Route     string  `json:"route"`
	Status    float64 `json:"status"`
	LatencyMS float64 `json:"latencyMs"`
}

// hubMetricRow mirrors State.MetricRow.
type hubMetricRow struct {
	Name   string  `json:"name"`
	Typ    string  `json:"typ"`
	Labels string  `json:"labels"`
	Value  float64 `json:"value"`
	Sum    float64 `json:"sum"`
	Count  float64 `json:"count"`
}

// hubTraceRow mirrors State.TraceRow.
type hubTraceRow struct {
	TraceID    string  `json:"traceId"`
	SpanID     string  `json:"spanId"`
	ParentID   string  `json:"parentId"`
	Name       string  `json:"name"`
	Kind       string  `json:"kind"`
	StartTime  string  `json:"startTime"`
	DurationMs float64 `json:"durationMs"`
	Status     string  `json:"status"`
}

// hubErrorRow mirrors State.ErrorRow (aggregated bad-status logs).
type hubErrorRow struct {
	Count   int    `json:"count"`
	Message string `json:"message"`
}

// QueryLogsJSON parses the Sky-side filter JSON, translates to a
// hub.LogFilter, runs QueryLogs, and emits a JSON array of
// hubLogRow values.
//
// `showDebug/Info/Warn/Error` map to the store's `Level` filter the
// same way the embedded console's HTTP endpoint does (server-side
// when exactly one level is selected; client-side / no-filter
// otherwise — the store-side filter only accepts ONE level at a
// time, so this matches behaviour).
func (r *storeReader) QueryLogsJSON(filterJSON string) (string, error) {
	var f hubLogFilter
	if filterJSON != "" {
		if err := json.Unmarshal([]byte(filterJSON), &f); err != nil {
			return "", fmt.Errorf("filter unmarshal: %w", err)
		}
	}
	storeFilter := LogFilter{
		Limit: 200,
		Level: pickSingleLevel(f),
	}
	rows, err := r.s.QueryLogs(storeFilter)
	if err != nil {
		return "", err
	}
	// Free-text + session filters are applied client-side because
	// the store's where-clause doesn't have a `LIKE` arm yet —
	// match the embedded console's UI behaviour.
	out := make([]hubLogRow, 0, len(rows))
	for _, row := range rows {
		if f.Query != "" && !logMatchesQuery(row, f.Query) {
			continue
		}
		if f.Session != "" && row.Attrs["session_id"] != f.Session {
			continue
		}
		out = append(out, toHubLogRow(row))
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// pickSingleLevel returns "" (no filter) when zero or two-plus
// levels are toggled — the store applies an `=` filter so we can
// only express "exactly one level" at a time. Mirror of the
// existing HTTP endpoint behaviour.
func pickSingleLevel(f hubLogFilter) string {
	count := 0
	chosen := ""
	if f.ShowDebug {
		count++
		chosen = "debug"
	}
	if f.ShowInfo {
		count++
		chosen = "info"
	}
	if f.ShowWarn {
		count++
		chosen = "warn"
	}
	if f.ShowError {
		count++
		chosen = "error"
	}
	if count == 1 {
		return chosen
	}
	return ""
}

func logMatchesQuery(row LogRow, q string) bool {
	ql := strings.ToLower(q)
	if strings.Contains(strings.ToLower(row.Message), ql) {
		return true
	}
	if strings.Contains(strings.ToLower(row.ServiceName), ql) {
		return true
	}
	return false
}

func toHubLogRow(row LogRow) hubLogRow {
	out := hubLogRow{
		Time:    row.Time.UTC().Format(time.RFC3339),
		Level:   row.Level,
		Message: row.Message,
		Subapp:  row.ServiceName,
	}
	if row.Attrs != nil {
		out.ReqID = row.Attrs["req_id"]
		out.SessionID = row.Attrs["session_id"]
		out.UserLabel = row.Attrs["user_label"]
		out.Route = row.Attrs["route"]
		// status / latencyMs are stored as attrs in the existing
		// telemetry encoder; ignore for now (display path tolerates 0).
	}
	return out
}

// QueryMetricsJSON returns the most recent metric rows as a JSON
// array matching State.MetricRow.
func (r *storeReader) QueryMetricsJSON() (string, error) {
	rows, err := r.s.QueryMetrics(MetricFilter{Limit: 200})
	if err != nil {
		return "", err
	}
	out := make([]hubMetricRow, 0, len(rows))
	for _, m := range rows {
		labels := ""
		if len(m.Attrs) > 0 {
			parts := make([]string, 0, len(m.Attrs))
			for k, v := range m.Attrs {
				parts = append(parts, k+"="+v)
			}
			labels = strings.Join(parts, ", ")
		}
		out = append(out, hubMetricRow{
			Name:   m.Name,
			Typ:    m.Type,
			Labels: labels,
			Value:  m.Value,
			Sum:    0,
			Count:  0,
		})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// QuerySpansJSON returns spans as TraceRow JSON.
func (r *storeReader) QuerySpansJSON() (string, error) {
	rows, err := r.s.QuerySpans(SpanFilter{Limit: 100})
	if err != nil {
		return "", err
	}
	out := make([]hubTraceRow, 0, len(rows))
	for _, sp := range rows {
		durMs := 0.0
		if !sp.StartTime.IsZero() && !sp.EndTime.IsZero() {
			durMs = float64(sp.EndTime.Sub(sp.StartTime)) / float64(time.Millisecond)
		}
		status := ""
		if sp.Attrs != nil {
			status = sp.Attrs["status"]
		}
		out = append(out, hubTraceRow{
			TraceID:    sp.TraceID,
			SpanID:     sp.SpanID,
			ParentID:   sp.ParentID,
			Name:       sp.Name,
			Kind:       sp.ServiceName,
			StartTime:  sp.StartTime.UTC().Format(time.RFC3339),
			DurationMs: durMs,
			Status:     status,
		})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// QueryErrorsJSON aggregates error-level logs into ErrorRow shape.
// v0.16.4 ships the simplest possible grouping (count by message).
// Future cycles can layer in span error-rates + http-status
// classification (B5/B6 territory).
func (r *storeReader) QueryErrorsJSON() (string, error) {
	rows, err := r.s.QueryLogs(LogFilter{Level: "error", Limit: 500})
	if err != nil {
		return "", err
	}
	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		counts[row.Message]++
	}
	out := make([]hubErrorRow, 0, len(counts))
	for msg, c := range counts {
		out = append(out, hubErrorRow{Count: c, Message: msg})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Services delegates to Store.Services.
func (r *storeReader) Services() ([]string, error) {
	return r.s.Services()
}
