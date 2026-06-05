// hub-bridge unit tests.
//
// v0.16.4 Option B B4 verification. Asserts the Sky-callable Hub_*
// kernels route through the registered HubStoreReader and return
// payloads matching the State.* record shape the console UI's
// rt.Coerce[State_*_R] expects.

package rt

import (
	"encoding/json"
	"errors"
	"testing"
)

// fakeHubStoreReader is a deterministic stub the tests drive. Each
// method returns a fixed payload; Counts returns the requested
// numbers; the QueryXJSON methods accept a builder so per-test
// scenarios can vary the output without hand-rolling JSON.
type fakeHubStoreReader struct {
	logs    int
	metrics int
	spans   int
	rowsLog string
	rowsMet string
	rowsSpn string
	rowsErr string
	svcs    []string
	err     error
}

func (f *fakeHubStoreReader) Counts() (int, int, int, error) {
	return f.logs, f.metrics, f.spans, f.err
}

func (f *fakeHubStoreReader) QueryLogsJSON(_ string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.rowsLog, nil
}

func (f *fakeHubStoreReader) QueryMetricsJSON() (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.rowsMet, nil
}

func (f *fakeHubStoreReader) QuerySpansJSON() (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.rowsSpn, nil
}

func (f *fakeHubStoreReader) QueryErrorsJSON() (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.rowsErr, nil
}

func (f *fakeHubStoreReader) Services() ([]string, error) {
	return f.svcs, f.err
}

// resetHubStore clears the registry; called from each test that
// touches the global so parallel test runs don't leak state.
func resetHubStore(t *testing.T) {
	t.Cleanup(func() { SetHubStore(nil) })
}

func TestHubReadOverview_NoStoreRegistered(t *testing.T) {
	resetHubStore(t)
	SetHubStore(nil)
	task := Hub_readOverview("/tmp/x").(func() any)
	res := task()
	res0, ok := res.(SkyResult[any, any])
	if !ok {
		t.Fatalf("expected SkyResult[any, any], got %T", res)
	}
	if res0.Tag != 0 {
		t.Fatalf("expected Ok (Tag 0), got Tag %d (err=%v)", res0.Tag, res0.ErrValue)
	}
	m, ok := res0.OkValue.(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", res0.OkValue)
	}
	// Defaults from emptyHubOverview when no reader is registered.
	if m["skyVersion"] != "hub" {
		t.Errorf("skyVersion: want hub, got %v", m["skyVersion"])
	}
	if m["productionMode"] != false {
		t.Errorf("productionMode: want false, got %v", m["productionMode"])
	}
}

func TestHubReadOverview_WithReader(t *testing.T) {
	resetHubStore(t)
	SetHubStore(&fakeHubStoreReader{
		logs: 100, metrics: 50, spans: 25,
	})
	task := Hub_readOverview("/tmp/x").(func() any)
	res := task().(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("expected Ok, got Err %v", res.ErrValue)
	}
	m := res.OkValue.(map[string]any)
	if m["bufferLogUsed"] != 100 {
		t.Errorf("bufferLogUsed: want 100, got %v", m["bufferLogUsed"])
	}
	if m["bufferTraceUsed"] != 25 {
		t.Errorf("bufferTraceUsed: want 25, got %v", m["bufferTraceUsed"])
	}
	if m["requestsTotal"] != 175 {
		t.Errorf("requestsTotal: want 175, got %v", m["requestsTotal"])
	}
}

func TestHubReadOverview_ReaderError(t *testing.T) {
	resetHubStore(t)
	SetHubStore(&fakeHubStoreReader{err: errors.New("disk full")})
	task := Hub_readOverview("/tmp/x").(func() any)
	res := task().(SkyResult[any, any])
	if res.Tag != 1 {
		t.Fatalf("expected Err, got Ok %v", res.OkValue)
	}
}

func TestHubReadLogs_DecodesJSON(t *testing.T) {
	resetHubStore(t)
	payload := mustJSON([]map[string]any{
		{"time": "2026-06-05T12:00:00Z", "level": "info", "message": "hi"},
		{"time": "2026-06-05T12:00:01Z", "level": "error", "message": "boom"},
	})
	SetHubStore(&fakeHubStoreReader{rowsLog: payload})
	task := Hub_readLogs("/tmp/x", nil).(func() any)
	res := task().(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("expected Ok, got Err %v", res.ErrValue)
	}
	rows, ok := res.OkValue.([]any)
	if !ok {
		t.Fatalf("expected []any, got %T", res.OkValue)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	r0 := rows[0].(map[string]any)
	if r0["message"] != "hi" {
		t.Errorf("row[0].message: want hi, got %v", r0["message"])
	}
}

func TestHubReadLogs_NoStoreReturnsEmpty(t *testing.T) {
	resetHubStore(t)
	SetHubStore(nil)
	task := Hub_readLogs("/tmp/x", nil).(func() any)
	res := task().(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("expected Ok, got Err %v", res.ErrValue)
	}
	rows := res.OkValue.([]any)
	if len(rows) != 0 {
		t.Errorf("want empty rows, got %d", len(rows))
	}
}

func TestHubReadMetrics_RoundTrip(t *testing.T) {
	resetHubStore(t)
	payload := mustJSON([]map[string]any{
		{"name": "requests_total", "typ": "counter", "labels": "route=/, status=200", "value": 42.0},
	})
	SetHubStore(&fakeHubStoreReader{rowsMet: payload})
	task := Hub_readMetrics("/tmp/x").(func() any)
	res := task().(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("expected Ok, got Err %v", res.ErrValue)
	}
	rows := res.OkValue.([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["name"] != "requests_total" {
		t.Errorf("metric row mismatch: %v", rows)
	}
}

func TestHubReadTraces_AndErrors(t *testing.T) {
	resetHubStore(t)
	traces := mustJSON([]map[string]any{{"traceId": "abc", "spanId": "def", "name": "GET /"}})
	errs := mustJSON([]map[string]any{{"count": 7, "message": "boom"}})
	SetHubStore(&fakeHubStoreReader{rowsSpn: traces, rowsErr: errs})
	t1 := Hub_readTraces("/tmp/x").(func() any)
	if t1().(SkyResult[any, any]).Tag != 0 {
		t.Error("traces: expected Ok")
	}
	t2 := Hub_readErrors("/tmp/x").(func() any)
	res := t2().(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatal("errors: expected Ok")
	}
	rows := res.OkValue.([]any)
	if rows[0].(map[string]any)["message"] != "boom" {
		t.Errorf("error message: %v", rows[0])
	}
}

func TestHubListServices(t *testing.T) {
	resetHubStore(t)
	SetHubStore(&fakeHubStoreReader{svcs: []string{"app-a", "app-b"}})
	task := Hub_listServices("/tmp/x").(func() any)
	res := task().(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("expected Ok, got Err %v", res.ErrValue)
	}
	svcs := res.OkValue.([]any)
	if len(svcs) != 2 || svcs[0] != "app-a" || svcs[1] != "app-b" {
		t.Errorf("services: %v", svcs)
	}
}

func TestHubReadLogs_BadJSON_ReturnsErr(t *testing.T) {
	resetHubStore(t)
	SetHubStore(&fakeHubStoreReader{rowsLog: "not-valid-json"})
	task := Hub_readLogs("/tmp/x", nil).(func() any)
	res := task().(SkyResult[any, any])
	if res.Tag != 1 {
		t.Fatalf("expected Err from bad JSON, got Ok %v", res.OkValue)
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
