//go:build !js

package hub

// The hub console's controls, at the reader they drive (v0.27.8): the level
// toggles (any subset of levels), the range, the global and tab searches and
// the session pivot all run in SQL before the row limit; a trace search keeps
// the whole trace. Before, a selection of two or three levels sent no level
// filter at all (only exactly one level reached SQL), and the range and the
// searches were applied in the browser to the newest 200 rows.

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"
)

func newQueryTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := newStore(t.TempDir(), storeOptions{retentionHours: 24, pruneInterval: time.Hour})
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Now().UTC()
	ages := map[string]time.Duration{"2m": 2 * time.Minute, "30m": 30 * time.Minute, "5h": 5 * time.Hour}
	for name, age := range ages {
		for _, level := range []string{"debug", "info", "warn", "error"} {
			s.Insert([]pendingItem{{
				kind: signalLog, ts: now.Add(-age), serviceName: "svc", level: level,
				message: "seed-" + name + "-" + level, traceID: "trace" + name,
				attrs: map[string]string{"session_id": "sess-" + name, "route": "/r/" + name},
			}})
		}
		start := now.Add(-age)
		s.Insert([]pendingItem{
			{kind: signalSpan, ts: start, serviceName: "svc", spanName: "root-" + name, traceID: "trace" + name, spanID: "a" + name, startTime: start, endTime: start.Add(time.Millisecond)},
			{kind: signalSpan, ts: start, serviceName: "svc", spanName: "child-" + name, traceID: "trace" + name, spanID: "b" + name, parentID: "a" + name, startTime: start, endTime: start.Add(time.Millisecond)},
		})
	}
	// Volume newer than every seed: 250 info lines.
	for i := 0; i < 250; i++ {
		s.Insert([]pendingItem{{kind: signalLog, ts: now, serviceName: "svc", level: "info", message: "volume"}})
	}
	s.FlushSync(5 * time.Second)
	return s
}

func seedMessages(t *testing.T, raw string) string {
	t.Helper()
	var rows []hubLogRow
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	out := []string{}
	for _, r := range rows {
		if strings.HasPrefix(r.Message, "seed-") {
			out = append(out, r.Message)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func TestHubConsoleLogs_LevelsRangeSearchAndSessionRunBeforeTheLimit(t *testing.T) {
	r := newQueryTestStore(t).AsReader()
	q := func(filter string) string {
		out, err := r.QueryFilteredLogsJSON("", filter)
		if err != nil {
			t.Fatalf("%s: %v", filter, err)
		}
		return seedMessages(t, out)
	}
	cases := []struct{ filter, want string }{
		// Two levels on (warn off, debug off): the store used to drop the
		// filter whenever the toggles left more than one level on.
		{`{"showInfo":true,"showError":true,"range":"all","search":"seed"}`,
			"seed-2m-error,seed-2m-info,seed-30m-error,seed-30m-info,seed-5h-error,seed-5h-info"},
		{`{"showWarn":true,"range":"1h","search":"seed"}`, "seed-2m-warn,seed-30m-warn"},
		{`{"showError":true,"range":"15m","search":"seed"}`, "seed-2m-error"},
		// The global search and the Logs tab's search both apply.
		{`{"showDebug":true,"showInfo":true,"showWarn":true,"showError":true,"range":"all","search":"seed","query":"5h-w"}`, "seed-5h-warn"},
		// The search reaches the route and the session, not only the message.
		{`{"showError":true,"range":"all","search":"/r/30m"}`, "seed-30m-error"},
		{`{"showInfo":true,"range":"all","session":"sess-5h"}`, "seed-5h-info"},
	}
	for _, c := range cases {
		if got := q(c.filter); got != c.want {
			t.Errorf("%s:\n got [%s]\nwant [%s]", c.filter, got, c.want)
		}
	}
}

func TestHubConsoleTraces_SearchKeepsTheWholeTraceAndRangeApplies(t *testing.T) {
	r := newQueryTestStore(t).AsReader()
	names := func(query string) string {
		out, err := r.QueryFilteredSpansJSON("", query)
		if err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		var rows []hubTraceRow
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		got := []string{}
		for _, row := range rows {
			got = append(got, row.Name)
		}
		sort.Strings(got)
		return strings.Join(got, ",")
	}
	if got := names(`{"range":"all","traceQuery":"child-5h"}`); got != "child-5h,root-5h" {
		t.Errorf("traceQuery child-5h: got [%s], want the whole trace", got)
	}
	if got := names(`{"range":"1h","search":"root"}`); got != "child-2m,child-30m,root-2m,root-30m" {
		t.Errorf("range 1h: got [%s]", got)
	}
}

func TestHubConsoleErrors_RangeAndSearch(t *testing.T) {
	r := newQueryTestStore(t).AsReader()
	msgs := func(query string) string {
		out, err := r.QueryFilteredErrorsJSON("", query)
		if err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		var rows []hubErrorRow
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		got := []string{}
		for _, row := range rows {
			got = append(got, row.Message)
		}
		return strings.Join(got, ",")
	}
	if got := msgs(`{"range":"1h"}`); got != "seed-2m-error,seed-30m-error" {
		t.Errorf("range 1h: got [%s] (ordered by count, then message)", got)
	}
	if got := msgs(`{"range":"all","search":"5h"}`); got != "seed-5h-error" {
		t.Errorf("search 5h: got [%s]", got)
	}
}
