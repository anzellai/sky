package rt

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sky-app/rt/telemetry"
)

// fakeIngest captures POST bodies so tests can assert exporter
// behaviour without spinning up a full parent runtime.
type fakeIngest struct {
	mu       sync.Mutex
	received []IngestPayload
	status   int // 0 = default 202
	delay    time.Duration
	calls    atomic.Int32
}

func (f *fakeIngest) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if f.delay > 0 {
			time.Sleep(f.delay)
		}
		body, _ := io.ReadAll(r.Body)
		var p IngestPayload
		_ = json.Unmarshal(body, &p)
		f.mu.Lock()
		f.received = append(f.received, p)
		f.mu.Unlock()
		st := f.status
		if st == 0 {
			st = http.StatusAccepted
		}
		w.WriteHeader(st)
	}
}

func (f *fakeIngest) latest() (IngestPayload, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.received) == 0 {
		return IngestPayload{}, false
	}
	return f.received[len(f.received)-1], true
}

// resetPushExporter — clear the singleton between test cases.
//
// Waits for the exporter's goroutine to finish rather than merely signalling
// it: the stop now triggers a final flush on that goroutine, and a test that
// moved on without waiting would leave a POST in flight against an httptest
// server the next test is about to close.
func resetPushExporter() {
	if exp := activeExporter.Load(); exp != nil {
		exp.stopOnce.Do(func() { close(exp.stopCh) })
		exp.wg.Wait()
	}
	activeExporter.Store(nil)
}

// TestPushExporter_IsRegisteredWithTheShutdownChain proves the shutdown flush
// is WIRED, not merely implemented.
//
// TestPushExporter_FlushOnStop below calls StopPushExporter directly, and it
// passed for the exporter's entire existence while `StopPushExporter` had
// exactly ONE caller in the tree — that test. Its doc comment claimed the
// runtime's signal handler called it. Nothing did. Every sub-app therefore
// dropped up to a full push interval of logs, metrics and spans on every
// deploy, and the suite reported that as healthy. This asserts the
// registration itself, which is the half a direct-call test cannot see.
func TestPushExporter_IsRegisteredWithTheShutdownChain(t *testing.T) {
	resetPushExporter()
	resetShutdownHooksForTesting()
	t.Cleanup(func() {
		resetPushExporter()
		resetShutdownHooksForTesting()
	})

	before := shutdownHookNames()
	if containsName(before, "observability-push") {
		t.Fatal("the hook registry was not reset — this gate cannot see its own effect")
	}

	srv := httptest.NewServer((&fakeIngest{}).handler())
	defer srv.Close()
	t.Setenv("SKY_PARENT_URL", srv.URL)
	t.Setenv("SKY_LIVE_NAMESPACE", "test")
	t.Setenv("SKY_OBSERVABILITY_PUSH_INTERVAL_MS", "10000")
	if StartPushExporter() == nil {
		t.Fatal("no exporter")
	}

	after := shutdownHookNames()
	if !containsName(after, "observability-push") {
		t.Fatalf("starting the push exporter registered no shutdown hook (registry: %v) — "+
			"the buffered logs, metrics and spans would be dropped on SIGTERM", after)
	}
}

// The deploy-safety gate, driven through the REAL shutdown chain.
//
// The push interval is pinned to ten seconds so the ticker provably cannot
// fire during the test: if the buffer reaches the parent, it is because the
// shutdown hook drained it. This is the assertion that would have caught the
// missing wiring, and it exercises the same entry point production uses
// (RunShutdownHooks) rather than reaching for StopPushExporter directly.
func TestPushExporter_ShutdownChainDrainsTheBuffer(t *testing.T) {
	resetPushExporter()
	resetShutdownHooksForTesting()
	t.Cleanup(func() {
		resetPushExporter()
		resetShutdownHooksForTesting()
	})

	fake := &fakeIngest{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	t.Setenv("SKY_PARENT_URL", srv.URL)
	t.Setenv("SKY_LIVE_NAMESPACE", "test")
	t.Setenv("SKY_INGEST_TOKEN", "tok")
	t.Setenv("SKY_OBSERVABILITY_PUSH_INTERVAL_MS", "10000")
	exp := StartPushExporter()
	if exp == nil {
		t.Fatal("no exporter")
	}
	exp.PushLog(telemetry.LogEntry{Level: "info", Message: "the tail of the deploy"})

	RunShutdownHooks(5 * time.Second)

	if got := fake.calls.Load(); got != 1 {
		t.Fatalf("the shutdown chain delivered %d pushes, want 1 — the buffered "+
			"entry was dropped on shutdown", got)
	}
	payload, ok := fake.latest()
	if !ok || len(payload.Logs) != 1 || payload.Logs[0].Message != "the tail of the deploy" {
		t.Fatalf("shutdown push did not carry the buffered log: %+v", payload)
	}
}

func TestPushExporter_NoopWithoutEnv(t *testing.T) {
	resetPushExporter()
	t.Setenv("SKY_PARENT_URL", "")
	t.Setenv("SKY_LIVE_NAMESPACE", "")
	got := StartPushExporter()
	if got != nil {
		t.Errorf("StartPushExporter should be nil without env; got %+v", got)
	}
	if ActivePushExporter() != nil {
		t.Error("ActivePushExporter should be nil")
	}
}

func TestPushExporter_StartsWithEnv(t *testing.T) {
	resetPushExporter()
	t.Setenv("SKY_PARENT_URL", "http://127.0.0.1:1")
	t.Setenv("SKY_LIVE_NAMESPACE", "test")
	t.Setenv("SKY_INGEST_TOKEN", "tok")
	t.Setenv("SKY_OBSERVABILITY_PUSH_INTERVAL_MS", "100")
	defer resetPushExporter()
	exp := StartPushExporter()
	if exp == nil {
		t.Fatal("expected exporter")
	}
	if exp.namespace != "test" {
		t.Errorf("namespace=%q want test", exp.namespace)
	}
	if exp.token != "tok" {
		t.Errorf("token=%q want tok", exp.token)
	}
	if ActivePushExporter() != exp {
		t.Error("ActivePushExporter should return the singleton")
	}
}

func TestPushExporter_Idempotent(t *testing.T) {
	resetPushExporter()
	t.Setenv("SKY_PARENT_URL", "http://127.0.0.1:1")
	t.Setenv("SKY_LIVE_NAMESPACE", "x")
	defer resetPushExporter()
	a := StartPushExporter()
	b := StartPushExporter()
	if a != b {
		t.Error("second StartPushExporter should return the same singleton")
	}
}

func TestPushExporter_BatchedDelivery(t *testing.T) {
	resetPushExporter()
	fake := &fakeIngest{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	t.Setenv("SKY_PARENT_URL", srv.URL)
	t.Setenv("SKY_LIVE_NAMESPACE", "test")
	t.Setenv("SKY_INGEST_TOKEN", "tok")
	t.Setenv("SKY_OBSERVABILITY_PUSH_INTERVAL_MS", "50") // tight loop for fast test
	defer resetPushExporter()
	exp := StartPushExporter()
	if exp == nil {
		t.Fatal("no exporter")
	}
	// Push three log entries.
	exp.PushLog(telemetry.LogEntry{Level: "info", Message: "one"})
	exp.PushLog(telemetry.LogEntry{Level: "warn", Message: "two"})
	exp.PushLog(telemetry.LogEntry{Level: "error", Message: "three"})
	// Wait for at least one tick to fire.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fake.calls.Load() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if fake.calls.Load() == 0 {
		t.Fatal("ingest never called")
	}
	p, ok := fake.latest()
	if !ok || p.Namespace != "test" || len(p.Logs) != 3 {
		t.Errorf("expected 3-log batch under namespace=test; got %+v", p)
	}
	// Token round-tripped on the wire — fake's handler doesn't check
	// it but exporter must have sent it.
}

func TestPushExporter_DropsOnOverflow(t *testing.T) {
	resetPushExporter()
	fake := &fakeIngest{delay: 100 * time.Millisecond} // slow parent
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	t.Setenv("SKY_PARENT_URL", srv.URL)
	t.Setenv("SKY_LIVE_NAMESPACE", "test")
	t.Setenv("SKY_INGEST_TOKEN", "tok")
	t.Setenv("SKY_OBSERVABILITY_BUFFER", "5")                // tiny cap
	t.Setenv("SKY_OBSERVABILITY_PUSH_INTERVAL_MS", "10_000") // don't auto-flush during test
	defer resetPushExporter()
	exp := StartPushExporter()
	if exp == nil {
		t.Fatal("no exporter")
	}
	// Push more than cap — excess should be dropped.
	for i := 0; i < 20; i++ {
		exp.PushLog(telemetry.LogEntry{Level: "info", Message: "x"})
	}
	exp.mu.Lock()
	lenLogs := len(exp.logs)
	dropped := exp.dropped
	exp.mu.Unlock()
	if lenLogs != 5 {
		t.Errorf("expected buffer to cap at 5; got %d", lenLogs)
	}
	if dropped != 15 {
		t.Errorf("expected 15 drops; got %d", dropped)
	}
}

func TestPushExporter_ParentDownDoesNotBlock(t *testing.T) {
	resetPushExporter()
	// Point at a port nothing's listening on.
	t.Setenv("SKY_PARENT_URL", "http://127.0.0.1:1")
	t.Setenv("SKY_LIVE_NAMESPACE", "test")
	t.Setenv("SKY_INGEST_TOKEN", "tok")
	t.Setenv("SKY_OBSERVABILITY_PUSH_INTERVAL_MS", "50")
	defer resetPushExporter()
	exp := StartPushExporter()
	if exp == nil {
		t.Fatal("no exporter")
	}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			exp.PushLog(telemetry.LogEntry{Level: "info", Message: "x"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("PushLog blocked on unreachable parent (must not block caller)")
	}
}

func TestPushExporter_FlushOnStop(t *testing.T) {
	resetPushExporter()
	fake := &fakeIngest{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	t.Setenv("SKY_PARENT_URL", srv.URL)
	t.Setenv("SKY_LIVE_NAMESPACE", "test")
	t.Setenv("SKY_INGEST_TOKEN", "tok")
	t.Setenv("SKY_OBSERVABILITY_PUSH_INTERVAL_MS", "10000")
	exp := StartPushExporter()
	if exp == nil {
		t.Fatal("no exporter")
	}
	exp.PushLog(telemetry.LogEntry{Level: "info", Message: "drain me"})
	StopPushExporter()
	// Stop should have flushed.
	if fake.calls.Load() != 1 {
		t.Errorf("expected 1 flush on stop; got %d", fake.calls.Load())
	}
	resetPushExporter()
}

// End-to-end: a sub-app uses logEmit (which is the runtime path
// every Sky-side Log.* call takes); verify the entry both lands
// in the local ring AND gets pushed to the parent's ring via the
// real HandleObservabilityIngest handler.
func TestPushExporter_RoundTrip_LogEmit_ToParentStore(t *testing.T) {
	resetPushExporter()
	resetIngestState(t, "tok")
	// Real parent endpoint — uses HandleObservabilityIngest +
	// telemetry.Default() (which we just reset). The sub-app uses
	// its own private "store" via a fakeIngest forwarder so we
	// don't pollute one telemetry singleton from both sides.
	parentMux := http.NewServeMux()
	parentMux.HandleFunc("/_sky/observability/ingest", HandleObservabilityIngest)
	parentSrv := httptest.NewServer(parentMux)
	defer parentSrv.Close()

	t.Setenv("SKY_PARENT_URL", parentSrv.URL)
	t.Setenv("SKY_LIVE_NAMESPACE", "myapp")
	t.Setenv("SKY_INGEST_TOKEN", "tok")
	t.Setenv("SKY_OBSERVABILITY_PUSH_INTERVAL_MS", "50")
	defer resetPushExporter()
	if StartPushExporter() == nil {
		t.Fatal("no exporter")
	}
	// Simulate a Sky-side Log.info call.
	logEmit(logLevelInfo, "info", "from sub-app", nil)
	// Wait for delivery.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		logs := telemetry.Default().RecentLogs(0)
		// Parent's store should now contain TWO entries: the local
		// logEmit's append (which goes to the SAME default store
		// in this test process — both parent and child use the
		// same singleton) and the ingested copy.
		// For the assertion, we just need to see the subapp=myapp
		// entry — proves the push round-tripped.
		for _, l := range logs {
			if l.Subapp == "myapp" && l.Message == "from sub-app" {
				return // pass
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected subapp=myapp log entry to round-trip through parent ingest")
}
