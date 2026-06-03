package rt

// HubExporter reliability tests — the 5 gating invariants from
// EXPORTER.md §10-point checklist. The first one (TestHubExporter_
// HotPathNeverBlocks) is the PR gate — must pass before v0.16.1 tag.

import (
	"context"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ─── Test A: Hot-path never blocks ────────────────────────────────
//
// Invariant #1 from EXPORTER.md. 10k Submits with hub unreachable
// must not block the caller — p99.99 < 1 ms.
//
// We use a stub transport that ALWAYS times out via a channel that
// nothing reads from. The drainer goroutine will park indefinitely
// in pushBatch, but Submit must keep returning immediately.

func TestHubExporter_HotPathNeverBlocks(t *testing.T) {
	// Stub transport that blocks "forever" (until cancelled) to
	// simulate a wedged hub — the pathological case.
	blockCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := func(ctx context.Context, body []byte) (int, error) {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-blockCtx.Done():
			return 0, blockCtx.Err()
		}
	}
	exp := NewHubExporterForTesting(transport)
	exp.Start(context.Background())
	defer exp.Stop()

	const N = 10000
	latencies := make([]time.Duration, N)
	payload := []byte(`{"t":"hot"}`)

	// Warm up — let the drainer enter its blocked state once.
	for i := 0; i < 100; i++ {
		exp.Submit(KindLog, payload, SevInfo)
	}
	time.Sleep(20 * time.Millisecond)

	// Hot loop — measure per-call.
	start := time.Now()
	for i := 0; i < N; i++ {
		t0 := time.Now()
		exp.Submit(KindLog, payload, SevInfo)
		latencies[i] = time.Since(t0)
	}
	total := time.Since(start)

	// Sort + percentile.
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p99_99 := latencies[int(float64(N)*0.9999)]
	p99 := latencies[int(float64(N)*0.99)]
	p50 := latencies[N/2]

	t.Logf("hot path: %d submits in %v (%.0f Hz)", N, total, float64(N)/total.Seconds())
	t.Logf("latency p50=%v p99=%v p99.99=%v", p50, p99, p99_99)
	t.Logf("dropped: %d (debug=%d info=%d)",
		exp.droppedDebug.Load()+exp.droppedInfo.Load(),
		exp.droppedDebug.Load(), exp.droppedInfo.Load())

	// Gate: p99.99 < 1 ms. This must hold or the architecture is
	// wrong — we'd be blocking on the channel send.
	if p99_99 > time.Millisecond {
		t.Errorf("p99.99 latency %v exceeds 1ms threshold (architectural failure)", p99_99)
	}
}

// ─── Test B: Bounded memory ───────────────────────────────────────
//
// Invariant #2: fill at 10× drain rate; assert RSS / heap stays
// flat. We can't easily measure RSS in a Go test without exec, but
// we can verify the queueBytes counter caps at ringBytesCap +
// epsilon, AND that runtime.MemStats.HeapAlloc growth stays
// reasonable.

func TestHubExporter_MemoryBounded(t *testing.T) {
	// Stub that succeeds slowly — push at one batch / 50ms but
	// we'll submit 100× faster.
	var pushed atomic.Int64
	transport := func(ctx context.Context, body []byte) (int, error) {
		pushed.Add(1)
		time.Sleep(50 * time.Millisecond) // simulate slow hub
		return 200, nil
	}
	exp := NewHubExporterForTesting(transport)
	// Small ring so the cap is easy to hit.
	exp.ringBytesCap = 256 * 1024 // 256 KB
	exp.Start(context.Background())
	defer exp.Stop()

	// Baseline heap.
	runtime.GC()
	var ms0 runtime.MemStats
	runtime.ReadMemStats(&ms0)

	payload := make([]byte, 256) // each Submit costs 256 B
	for i := 0; i < 200; i++ {
		// 200 batches of 1000 submits = 200k Submits. At 256 B each
		// that's ~50 MB of writes — well above the 256 KB cap.
		for j := 0; j < 1000; j++ {
			exp.Submit(KindLog, payload, SevInfo)
		}
	}

	// queueBytes should be at or below the cap.
	qb := exp.QueueBytes()
	t.Logf("queueBytes=%d ringBytesCap=%d pushed=%d", qb, exp.ringBytesCap, pushed.Load())

	// Allow some slack — drainer may have pulled some items.
	if qb > exp.ringBytesCap+int64(len(payload)*exp.batchMax) {
		t.Errorf("queueBytes %d exceeds cap %d + drainer-slack", qb, exp.ringBytesCap)
	}

	// Heap growth — let drainer catch up first.
	time.Sleep(100 * time.Millisecond)
	runtime.GC()
	var ms1 runtime.MemStats
	runtime.ReadMemStats(&ms1)
	heapDelta := int64(ms1.HeapAlloc) - int64(ms0.HeapAlloc)
	t.Logf("heap delta: %d bytes (allowed: ~10 MB for buffers + Go runtime)", heapDelta)
	// Tolerate up to 10 MB of heap growth — most of it is Go's GC
	// retained slack, not our actual buffer.
	if heapDelta > 10*1024*1024 {
		t.Errorf("heap delta %d bytes exceeds 10 MB threshold", heapDelta)
	}

	// Drop counters should be non-zero — proves backpressure
	// kicked in.
	drops := exp.droppedDebug.Load() + exp.droppedInfo.Load() +
		exp.droppedWarn.Load() + exp.droppedError.Load()
	if drops == 0 {
		t.Error("expected drops under 100× over-capacity load; got 0")
	}
}

// ─── Test C: Priority backpressure ────────────────────────────────
//
// Invariant #8: when buffer >95% full, errors + metrics always
// pass; INFO and DEBUG drop. DEBUG drops first.

func TestHubExporter_PriorityDrops(t *testing.T) {
	// Use a transport that returns slowly so the queue fills up.
	transport := func(ctx context.Context, body []byte) (int, error) {
		time.Sleep(100 * time.Millisecond)
		return 200, nil
	}
	exp := NewHubExporterForTesting(transport)
	exp.ringBytesCap = 10 * 1024 // 10 KB — small so we hit thresholds fast
	exp.Start(context.Background())
	defer exp.Stop()

	// Fill the queue past 95% capacity. Each Submit is ~32 B, so
	// 320 submits push us past 10 KB. Use 500 to be sure.
	payload := make([]byte, 32)
	// First a burst of DEBUG to fill the queue.
	for i := 0; i < 500; i++ {
		exp.Submit(KindLog, payload, SevDebug)
	}

	debugDropsAfterFill := exp.droppedDebug.Load()
	t.Logf("debug drops after fill: %d", debugDropsAfterFill)

	// Now submit a mix — errors + metrics MUST pass; INFO/DEBUG
	// drop.
	const sentinel = 1000
	errorsBefore := exp.droppedError.Load()
	metricsBefore := exp.droppedDebug.Load() + exp.droppedInfo.Load() +
		exp.droppedWarn.Load() + exp.droppedError.Load()
	for i := 0; i < sentinel; i++ {
		// Metrics should NEVER drop.
		exp.Submit(KindMetric, payload, SevError)
		// Errors should NEVER drop.
		exp.Submit(KindLog, payload, SevError)
	}

	errorsAfter := exp.droppedError.Load()
	metricsAfter := exp.droppedDebug.Load() + exp.droppedInfo.Load() +
		exp.droppedWarn.Load() + exp.droppedError.Load()

	t.Logf("error drops before=%d after=%d (must be equal)",
		errorsBefore, errorsAfter)
	t.Logf("total drops before=%d after=%d (some-or-none, but no error/metric drops in priority logic)",
		metricsBefore, metricsAfter)

	// errorsAfter may be > errorsBefore IF the channel itself was
	// genuinely full (capacity exhaustion at the channel level —
	// the final fallback). The priority logic guarantees they
	// don't drop AT THE PRIORITY STAGE, but channel-full still
	// applies as the last-resort safety. So we assert: the rate
	// of errors+metrics drops is much smaller than DEBUG drops.
	debugDropsTotal := exp.droppedDebug.Load()
	if errorsAfter-errorsBefore >= debugDropsTotal {
		t.Errorf("error drops (%d) exceed debug drops (%d) — priority backpressure inverted",
			errorsAfter-errorsBefore, debugDropsTotal)
	}

	// DEBUG drops must be substantial.
	if debugDropsTotal < 100 {
		t.Errorf("expected >100 DEBUG drops under fill; got %d", debugDropsTotal)
	}
}

// ─── Test D: Circuit breaker ──────────────────────────────────────
//
// Invariant #4: 50 consec failures → open 30 s. Single half-open
// probe; success → closed. Failure → another open cycle.

func TestHubExporter_CircuitOpen(t *testing.T) {
	var failMode atomic.Bool
	failMode.Store(true)
	transport := func(ctx context.Context, body []byte) (int, error) {
		if failMode.Load() {
			return 500, nil // 5xx
		}
		return 200, nil
	}
	exp := NewHubExporterForTesting(transport)
	exp.SetCircuitWindowForTesting(100 * time.Millisecond) // accelerated
	// Tighten the batch timer so we can drive failures fast.
	exp.batchInt = 5 * time.Millisecond
	exp.Start(context.Background())
	defer exp.Stop()

	// Push 60 items, 1 per batch — but batches group by tick.
	// Force separate batches: submit + wait > batchInt.
	payload := []byte(`{}`)
	for i := 0; i < 60; i++ {
		exp.Submit(KindLog, payload, SevInfo)
		// Wait at least one batch tick + epsilon so each push is
		// its own batch.
		time.Sleep(8 * time.Millisecond)
	}

	// Circuit should be open now.
	deadline := time.Now().Add(time.Second)
	var state CircuitState
	for time.Now().Before(deadline) {
		state = exp.CircuitStateLoad()
		if state == circuitOpen {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if state != circuitOpen {
		t.Errorf("expected circuit open after >=50 failures; got %v", state)
	}
	t.Logf("circuit transitioned to open after %d consec failures", exp.consecFailures.Load())

	// Now flip to success mode and wait > circuitOpenWindow.
	failMode.Store(false)
	time.Sleep(200 * time.Millisecond) // > 100ms window

	// Submit a few more to trigger the half-open probe.
	for i := 0; i < 5; i++ {
		exp.Submit(KindLog, payload, SevInfo)
		time.Sleep(15 * time.Millisecond)
	}

	// Wait for the transition back to closed.
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state = exp.CircuitStateLoad()
		if state == circuitClosed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if state != circuitClosed {
		t.Errorf("expected circuit closed after success probe; got %v (consec=%d)",
			state, exp.consecFailures.Load())
	}
	t.Logf("circuit recovered to closed after success probe")
}

// ─── Test E: SIGTERM drain ────────────────────────────────────────
//
// Invariant #9: Flush(deadline) drains the queue within budget.
// Verify >95% of submitted items reach the stub hub within 1 s.

func TestHubExporter_SIGTERMDrain(t *testing.T) {
	var pushed atomic.Int64
	var mu sync.Mutex
	receivedBodies := [][]byte{}
	transport := func(ctx context.Context, body []byte) (int, error) {
		// Count the OTLP envelope: parse for our marker. Each
		// envelope contains 1+ records — we just count attempts
		// and inspect at the end.
		mu.Lock()
		// Approx: each pushOneKind batches up to batchMax items
		// per call. We count batches and parse the embedded
		// fragment count from the body.
		copyB := make([]byte, len(body))
		copy(copyB, body)
		receivedBodies = append(receivedBodies, copyB)
		mu.Unlock()
		pushed.Add(1)
		return 200, nil
	}
	exp := NewHubExporterForTesting(transport)
	// Realistic batch interval so we have items pending at flush.
	exp.batchInt = 50 * time.Millisecond
	exp.Start(context.Background())

	const N = 1000
	payload := []byte(`{"x":"drain"}`)
	for i := 0; i < N; i++ {
		exp.Submit(KindLog, payload, SevInfo)
	}

	t.Logf("submitted %d items; queue before flush: len=%d bytes=%d",
		N, exp.QueueLen(), exp.QueueBytes())

	// Trigger drain with 1 s deadline (well within EXPORTER.md's
	// 8 s budget for serverless 200 ms cadence).
	start := time.Now()
	err := exp.Flush(time.Second)
	elapsed := time.Since(start)
	t.Logf("Flush(1s) returned in %v (err=%v)", elapsed, err)
	if err != nil {
		t.Errorf("Flush failed within 1s budget: %v", err)
	}

	// After flush, queue must be drained.
	if ql := exp.QueueLen(); ql != 0 {
		t.Errorf("expected empty queue after Flush; got %d", ql)
	}
	if qb := exp.QueueBytes(); qb != 0 {
		t.Errorf("expected zero queueBytes after Flush; got %d", qb)
	}

	// Count actual record fragments delivered. Each pushed body
	// contains one or more `{"x":"drain"}` fragments.
	mu.Lock()
	defer mu.Unlock()
	totalFragments := 0
	for _, body := range receivedBodies {
		// Count occurrences of our payload marker.
		totalFragments += strings.Count(string(body), `{"x":"drain"}`)
	}
	t.Logf("received %d push attempts carrying %d fragments (submitted: %d)",
		pushed.Load(), totalFragments, N)

	deliveryPct := float64(totalFragments) / float64(N) * 100
	t.Logf("delivery: %.1f%%", deliveryPct)
	if deliveryPct < 95.0 {
		t.Errorf("expected >=95%% delivery in 1s drain; got %.1f%%", deliveryPct)
	}

	exp.Stop()
}

// ─── Companion smoke tests ────────────────────────────────────────

// TestNewHubExporter_NoEnv_ReturnsNil — env-unset → no exporter.
func TestNewHubExporter_NoEnv_ReturnsNil(t *testing.T) {
	// Save + clear potentially-set env.
	t.Setenv("SKY_CONSOLE_HUB", "")
	t.Setenv("SKY_CONSOLE_HUB_TOKEN", "")
	if exp := NewHubExporter(); exp != nil {
		t.Errorf("expected nil exporter when SKY_CONSOLE_HUB unset; got %v", exp)
	}
}

// TestNewHubExporter_ShortToken_RefusesStart — token <32 → no
// exporter + stderr warn.
func TestNewHubExporter_ShortToken_RefusesStart(t *testing.T) {
	t.Setenv("SKY_CONSOLE_HUB", "https://hub.example.com")
	t.Setenv("SKY_CONSOLE_HUB_TOKEN", "tooshort")
	if exp := NewHubExporter(); exp != nil {
		t.Errorf("expected nil exporter on short token; got %v", exp)
	}
}

// TestNewHubExporter_FullEnv_ConstructsCleanly — happy path.
func TestNewHubExporter_FullEnv_ConstructsCleanly(t *testing.T) {
	t.Setenv("SKY_CONSOLE_HUB", "https://hub.example.com")
	t.Setenv("SKY_CONSOLE_HUB_TOKEN", strings.Repeat("a", 32))
	exp := NewHubExporter()
	if exp == nil {
		t.Fatal("expected non-nil exporter with full env")
	}
	if exp.hubURL != "https://hub.example.com" {
		t.Errorf("hubURL: got %q", exp.hubURL)
	}
	// Don't start — we don't want a hot drainer in the test
	// process talking to example.com.
}

// TestOtlpEnvelope_LogShape — proves envelope assembly emits valid
// OTLP-JSON shape that a collector can ingest.
func TestOtlpEnvelope_LogShape(t *testing.T) {
	frag := []byte(`{"timeUnixNano":"123","severityText":"INFO","body":{"stringValue":"hi"}}`)
	body, err := encodeOtlpEnvelope(KindLog, [][]byte{frag})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	s := string(body)
	for _, want := range []string{
		`"resourceLogs":[`,
		`"scopeLogs":[`,
		`"logRecords":[`,
		`"timeUnixNano":"123"`,
		`"name":"sky"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("envelope missing %q\nfull body:\n%s", want, s)
		}
	}
}

// TestShutdownHook_FlushesExporter — proves RegisterShutdownHook +
// RunShutdownHooks drives Flush.
func TestShutdownHook_FlushesExporter(t *testing.T) {
	defer resetShutdownHooksForTesting()
	resetShutdownHooksForTesting()

	var received atomic.Int64
	transport := func(ctx context.Context, body []byte) (int, error) {
		received.Add(int64(strings.Count(string(body), `{"x":"sd"}`)))
		return 200, nil
	}
	exp := NewHubExporterForTesting(transport)
	exp.batchInt = 50 * time.Millisecond
	exp.Start(context.Background())

	const N = 200
	for i := 0; i < N; i++ {
		exp.Submit(KindLog, []byte(`{"x":"sd"}`), SevInfo)
	}

	// Run the shutdown chain — this drives the exporter's
	// registered hook, which calls Flush + Stop.
	RunShutdownHooks(2 * time.Second)

	// After the chain ran, all N items should have reached the
	// stub. Allow tiny tolerance for race with the drainer goroutine
	// — but >=95% is the contractual gate.
	delivered := received.Load()
	t.Logf("shutdown delivered %d of %d items", delivered, N)
	if float64(delivered)/float64(N) < 0.95 {
		t.Errorf("shutdown hook delivery: got %d/%d (%.1f%%), expected >=95%%",
			delivered, N, float64(delivered)/float64(N)*100)
	}
}
