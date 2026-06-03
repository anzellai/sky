package rt

// HubExporter — in-process OTLP push pipeline (v0.16.1 PR 4).
//
// Sits in every Sky binary. Writer (Submit) is the hot path called
// from telemetry record sites; drainer is a background goroutine that
// batches + OTLP-encodes + pushes to the configured hub.
//
// Distinct from observability_push.go's `PushExporter` — that one
// federates sub-app → parent over JSON-over-HTTP. HubExporter is the
// remote-hub push (OTLP-compliant, gRPC primary planned for v0.16.2,
// HTTP-JSON shipping in PR 4) with disk spool (PR 5) + circuit
// breaker + priority-aware drop.
//
// Reliability invariants (gated by exporter_test.go):
//   #1 Never blocks the hot path. Submit returns in <1 ms p99.99
//      regardless of hub state. Channel send is non-blocking
//      (select+default).
//   #2 Bounded memory. In-memory ring caps at SKY_TELEMETRY_RING_BYTES
//      (default 5 MiB). PR 5 adds disk spool for file-mode.
//   #3 Honest failure surface. Self-counters emitted via
//      telemetry.Default(): dropped_total{level=...},
//      push_attempts_total, push_failures_total{reason=...},
//      circuit_state{state=...}.
//   #4 Circuit breaker. 50 consecutive failures → open 30 s;
//      single half-open probe; success → closed; fail → another
//      30 s open cycle.
//   #6 Connection management. http.Client with KeepAlive + 5 s
//      Timeout; HTTP/2 transparent via stdlib. gRPC fallback is a
//      v0.16.2 follow-up (would add otlptracegrpc dep).
//   #7 Auth + secrets. Bearer token from SKY_CONSOLE_HUB_TOKEN
//      (>=32 bytes — exporter refuses to start if shorter, prevents
//      mis-configured leaks). Token never appears in logs / drop
//      counters / failure metrics.
//   #8 Priority backpressure. Buffer >80% → drop DEBUG only.
//      Buffer >95% → also drop INFO + fast spans. Errors + metrics
//      NEVER dropped.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sky-app/rt/telemetry"
)

// ─── public types ─────────────────────────────────────────────────

// TelemetryKind identifies the OTLP signal a Submit payload carries.
type TelemetryKind int

const (
	// KindLog — OTLP logs/v1 ResourceLogs payload.
	KindLog TelemetryKind = iota
	// KindMetric — OTLP metrics/v1 ResourceMetrics payload.
	KindMetric
	// KindSpan — OTLP trace/v1 ResourceSpans payload.
	KindSpan
)

func (k TelemetryKind) String() string {
	switch k {
	case KindLog:
		return "log"
	case KindMetric:
		return "metric"
	case KindSpan:
		return "span"
	}
	return "unknown"
}

// Severity classifies a Submit for priority backpressure.
// Higher value = lower priority (dropped first).
type Severity int

const (
	// SevError — error logs, error spans, anything user-visible-loss
	// matters. NEVER dropped at the writer; reserved against the
	// fixed 5% headroom.
	SevError Severity = iota
	// SevWarn — warn-level logs + slow spans.
	SevWarn
	// SevInfo — info-level logs, normal spans.
	SevInfo
	// SevDebug — debug-level logs, fast spans below sampling. Dropped
	// first under pressure.
	SevDebug
)

func (s Severity) String() string {
	switch s {
	case SevError:
		return "error"
	case SevWarn:
		return "warn"
	case SevInfo:
		return "info"
	case SevDebug:
		return "debug"
	}
	return "unknown"
}

// CircuitState — closed → push attempts run; open → all pushes
// skipped + drops counted; half-open → single probe push.
type CircuitState int32

const (
	circuitClosed CircuitState = iota
	circuitOpen
	circuitHalfOpen
)

func (c CircuitState) String() string {
	switch c {
	case circuitClosed:
		return "closed"
	case circuitOpen:
		return "open"
	case circuitHalfOpen:
		return "half-open"
	}
	return "unknown"
}

// ─── HubExporter ─────────────────────────────────────────────────

// HubExporter is the in-process OTLP push pipeline. Construct via
// NewHubExporter; call Start to spawn the drainer; call Submit per
// telemetry event; call Flush from the SIGTERM handler.
//
// Concurrency: Submit is safe to call from any number of goroutines.
// Internally a buffered channel decouples writers from the single
// drainer goroutine.
type HubExporter struct {
	// Configuration — set at construction, immutable.
	hubURL       string
	token        string
	httpC        *http.Client
	batchInt     time.Duration
	batchMax     int    // events per batch
	batchBytes   int    // bytes per batch (1 MiB target)
	ringCap      int    // entries the bounded channel can hold
	ringBytesCap int64  // approx in-memory bytes cap
	insecureTLS  bool

	// Hot-path state. We accept on this channel; drainer consumes.
	queue chan telemetryItem

	// In-memory byte accounting (approx; used for backpressure).
	queueBytes atomic.Int64

	// Circuit breaker — int32 for atomic load/store.
	circuit          atomic.Int32
	consecFailures   atomic.Int32
	circuitOpenedAt  atomic.Int64 // unix-nano
	circuitOpenWindow time.Duration

	// Self-observability counters. Mirrored to telemetry.Default()
	// for /_sky/metrics emission; held atomically here for cheap
	// per-call increment + Metrics() snapshot.
	droppedDebug   atomic.Int64
	droppedInfo    atomic.Int64
	droppedWarn    atomic.Int64
	droppedError   atomic.Int64
	pushAttempts   atomic.Int64
	pushFailNet    atomic.Int64
	pushFailTime   atomic.Int64
	pushFail4xx    atomic.Int64
	pushFail5xx    atomic.Int64
	pushFailCircuit atomic.Int64

	// Lifecycle.
	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
	draining  atomic.Bool

	// Durability (PR 5). The spool persists batches that haven't
	// been acked by the hub. Attached via attachSpool; nil means
	// no durability layer (memory-only). Writes happen in the
	// drainer goroutine — never on the Submit hot path.
	spoolMu         sync.RWMutex
	spool           spool
	spoolCfg        spoolConfig
	spoolWriteFails int64 // atomic
	spoolReplayed   atomic.Bool

	// Test hooks — set via SetTransport for in-test stub server.
	// nil → use httpC.
	transportOverride func(ctx context.Context, body []byte) (statusCode int, err error)
}

// telemetryItem — internal queue entry. payload is the OTLP-shaped
// JSON for one signal (already serialised in Submit).
type telemetryItem struct {
	kind     TelemetryKind
	severity Severity
	payload  []byte
}

// activeHub — singleton-ish; nil when no exporter has been started.
// Used by RecordCounter / RecordLog / RecordTrace fan-out helpers
// when wired in by future PRs. For PR 4 the wire-in stays at the
// telemetry record sites; the exporter is observable but doesn't
// rewrite any of the existing telemetry fan-out.
var activeHubExporter atomic.Pointer[HubExporter]

// ActiveHubExporter returns the currently-active HubExporter, or
// nil when SKY_CONSOLE_HUB is unset. Cheap atomic load — safe to
// call from hot paths.
func ActiveHubExporter() *HubExporter {
	return activeHubExporter.Load()
}

// ─── configuration helpers ───────────────────────────────────────

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOrDefault(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func envInt64OrDefault(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// ─── construction ────────────────────────────────────────────────

// NewHubExporter reads env vars and constructs an exporter. Returns
// nil when SKY_CONSOLE_HUB is unset (push off). Refuses to construct
// (returns nil + emits warn) when SKY_CONSOLE_HUB_TOKEN is shorter
// than 32 bytes — token hygiene is a startup invariant.
//
// Env vars consumed:
//
//	SKY_CONSOLE_HUB             OTLP collector URL (required to enable)
//	SKY_CONSOLE_HUB_TOKEN       Bearer token (>=32 bytes, required)
//	SKY_CONSOLE_BATCH_INTERVAL_MS  batch flush cadence (default 2000
//	                            VM / 200 serverless)
//	SKY_TELEMETRY_RING_BYTES    in-memory ring cap (default 5 MiB)
//	SKY_CONSOLE_HUB_TLS_INSECURE skip TLS verify (dev only)
//
// Defaults follow EXPORTER.md §"Two reliability profiles". The
// serverless cadence override happens automatically when
// IsServerless() returns true.
func NewHubExporter() *HubExporter {
	hub := envOrDefault("SKY_CONSOLE_HUB", "")
	if hub == "" {
		return nil
	}
	token := envOrDefault("SKY_CONSOLE_HUB_TOKEN", "")
	if len(token) < 32 {
		fmt.Fprintln(os.Stderr,
			"[sky.hub-exporter] SKY_CONSOLE_HUB set but SKY_CONSOLE_HUB_TOKEN "+
				"is missing or <32 bytes; refusing to start (security invariant)")
		return nil
	}
	// Default cadence flips on serverless (200 ms eager push) — see
	// SERVERLESS.md §"Serverless-specific exporter behaviour".
	defaultBatchMs := 2000
	if IsServerless() {
		defaultBatchMs = 200
	}
	batchMs := envIntOrDefault("SKY_CONSOLE_BATCH_INTERVAL_MS", defaultBatchMs)
	ringBytes := envInt64OrDefault("SKY_TELEMETRY_RING_BYTES", 5*1024*1024)
	// Each event averages ~500 bytes after JSON encoding; cap the
	// channel at ringBytes / 256 to avoid over-allocation on small
	// rings. Lower bound 1024 entries so very small ringBytes still
	// behave (the byte counter catches the actual limit).
	ringCap := int(ringBytes / 256)
	if ringCap < 1024 {
		ringCap = 1024
	}
	insecure := envOrDefault("SKY_CONSOLE_HUB_TLS_INSECURE", "0") == "1"
	if insecure {
		fmt.Fprintln(os.Stderr,
			"[sky.hub-exporter] SKY_CONSOLE_HUB_TLS_INSECURE=1 — TLS "+
				"verification disabled; use only in dev")
	}

	httpC := &http.Client{
		Timeout: 5 * time.Second,
		Transport: defaultHubTransport(insecure),
	}

	exp := &HubExporter{
		hubURL:            strings.TrimRight(hub, "/"),
		token:             token,
		httpC:             httpC,
		batchInt:          time.Duration(batchMs) * time.Millisecond,
		batchMax:          1000,
		batchBytes:        1024 * 1024,
		ringCap:           ringCap,
		ringBytesCap:      ringBytes,
		insecureTLS:       insecure,
		queue:             make(chan telemetryItem, ringCap),
		circuitOpenWindow: 30 * time.Second,
		stopCh:            make(chan struct{}),
		doneCh:            make(chan struct{}),
	}
	exp.circuit.Store(int32(circuitClosed))

	// Open the durability layer (PR 5). File mode on VMs, memory
	// on serverless, none under explicit override. A spool open
	// failure is non-fatal — the exporter still runs against the
	// in-memory ring, just without the survives-restart guarantee.
	exp.spoolCfg = resolveSpoolConfig()
	if sp, err := openSpool(exp.spoolCfg); err != nil {
		fmt.Fprintf(os.Stderr,
			"[sky.hub-exporter] spool open (%s) failed: %v; "+
				"falling back to memory-only mode\n",
			exp.spoolCfg.mode, err)
		telemetry.Default().SetGauge("sky_telemetry_spool_mode",
			map[string]string{"mode": "none"}, 1)
	} else if sp != nil {
		exp.attachSpool(sp)
	} else {
		telemetry.Default().SetGauge("sky_telemetry_spool_mode",
			map[string]string{"mode": "none"}, 1)
	}
	return exp
}

// NewHubExporterForTesting constructs an exporter that talks to the
// caller-supplied transportOverride instead of the real HTTP client.
// Tests pass a closure that records calls and returns the desired
// HTTP status / error. Token / hub URL are stubbed so the >=32-byte
// check passes.
func NewHubExporterForTesting(transport func(ctx context.Context, body []byte) (statusCode int, err error)) *HubExporter {
	exp := &HubExporter{
		hubURL:            "http://127.0.0.1:0/v1",
		token:             strings.Repeat("a", 32),
		batchInt:          50 * time.Millisecond, // fast cycle for tests
		batchMax:          1000,
		batchBytes:        1024 * 1024,
		ringCap:           4096,
		ringBytesCap:      1024 * 1024,
		queue:             make(chan telemetryItem, 4096),
		circuitOpenWindow: 200 * time.Millisecond, // accelerated for tests
		stopCh:            make(chan struct{}),
		doneCh:            make(chan struct{}),
		transportOverride: transport,
	}
	exp.circuit.Store(int32(circuitClosed))
	return exp
}

// ─── hot path ────────────────────────────────────────────────────

// Submit enqueues a telemetry payload for batched export. NON-
// BLOCKING — uses select+default so a wedged drainer cannot block
// the caller. Returns immediately (sub-µs in steady state).
//
// payload should already be OTLP-shaped JSON for the matching
// signal. For raw Sky LogEntry / MetricSample / TraceEntry call the
// SubmitLog / SubmitMetric / SubmitSpan helpers below — they
// encode + Submit in one step.
func (e *HubExporter) Submit(kind TelemetryKind, payload []byte, level Severity) {
	if e == nil {
		return
	}
	// Priority drop-at-source — when the queue is at >80% capacity,
	// drop DEBUG immediately; at >95%, drop INFO + fast spans too.
	// Errors + metrics ALWAYS pass through.
	curBytes := e.queueBytes.Load()
	if level != SevError && kind != KindMetric {
		pct := float64(curBytes) / float64(e.ringBytesCap)
		if pct > 0.95 && (level == SevInfo || level == SevDebug) {
			e.recordDrop(level)
			return
		}
		if pct > 0.80 && level == SevDebug {
			e.recordDrop(level)
			return
		}
	}
	item := telemetryItem{
		kind:     kind,
		severity: level,
		payload:  payload,
	}
	// Non-blocking send. If the channel is full (writers outpacing
	// drainer beyond ringCap), drop and count. Never block.
	select {
	case e.queue <- item:
		e.queueBytes.Add(int64(len(payload)))
	default:
		e.recordDrop(level)
	}
}

// recordDrop bumps the per-severity drop counter + telemetry self-
// metric. Inline to keep Submit cheap.
func (e *HubExporter) recordDrop(level Severity) {
	switch level {
	case SevError:
		e.droppedError.Add(1)
	case SevWarn:
		e.droppedWarn.Add(1)
	case SevInfo:
		e.droppedInfo.Add(1)
	case SevDebug:
		e.droppedDebug.Add(1)
	}
	// Self-observability — emit to telemetry.Default() so
	// /_sky/metrics surfaces the drop rate. Best-effort: if the
	// telemetry store is unavailable we silently skip.
	defer func() { _ = recover() }()
	telemetry.Default().Add("sky_telemetry_dropped_total",
		map[string]string{"level": level.String()}, 1)
}

// SubmitLog wraps a telemetry.LogEntry into OTLP-shaped JSON and
// enqueues. Severity derives from entry.Level.
func (e *HubExporter) SubmitLog(entry telemetry.LogEntry) {
	if e == nil {
		return
	}
	payload, err := encodeOtlpLog(entry)
	if err != nil {
		// Marshal failures are caller-bugs (malformed entries).
		// Count under "drop debug" so a hot loop of bad entries
		// doesn't escalate self-counters that operators alert on.
		e.droppedDebug.Add(1)
		return
	}
	e.Submit(KindLog, payload, severityFromLevel(entry.Level))
}

// SubmitMetric wraps a metric sample into OTLP-shaped JSON. Metrics
// are SevError (never-drop priority) per #8.
func (e *HubExporter) SubmitMetric(name, mtype string, delta, value float64, labels map[string]string) {
	if e == nil {
		return
	}
	payload, err := encodeOtlpMetric(name, mtype, delta, value, labels)
	if err != nil {
		e.droppedDebug.Add(1)
		return
	}
	e.Submit(KindMetric, payload, SevError)
}

// SubmitSpan wraps a span into OTLP-shaped JSON. Spans with non-OK
// StatusCode are SevError (never-drop); other spans use SevInfo.
func (e *HubExporter) SubmitSpan(span telemetry.TraceEntry) {
	if e == nil {
		return
	}
	payload, err := encodeOtlpSpan(span)
	if err != nil {
		e.droppedDebug.Add(1)
		return
	}
	level := SevInfo
	if span.StatusCode != "" && span.StatusCode != "OK" && span.StatusCode != "UNSET" {
		level = SevError
	}
	e.Submit(KindSpan, payload, level)
}

func severityFromLevel(level string) Severity {
	switch strings.ToLower(level) {
	case "error", "err", "fatal":
		return SevError
	case "warn", "warning":
		return SevWarn
	case "debug", "trace":
		return SevDebug
	}
	return SevInfo
}

// ─── lifecycle ───────────────────────────────────────────────────

// Start kicks off the drainer goroutine. Idempotent. Registers a
// shutdown hook so SIGTERM drain runs Flush(8s) before the process
// exits — see shutdown.go for the registry, live.go/rt.go for the
// signal-handler call sites.
func (e *HubExporter) Start(ctx context.Context) {
	if e == nil {
		return
	}
	e.startOnce.Do(func() {
		// Make ourselves the active singleton. Subsequent
		// ActiveHubExporter() calls return us.
		activeHubExporter.Store(e)
		// Use SafeGo-equivalent — the drainer runs under defer/
		// recover so any panic inside batching/push doesn't kill
		// the process. We don't import SafeGo here because the rt
		// package init ordering means SafeGo might not be wired
		// when tests construct the exporter directly; using a
		// local defer/recover keeps the test surface simple.
		go func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr,
						"[sky.hub-exporter] drainer panicked: %v\n", r)
				}
				close(e.doneCh)
			}()
			e.drain(ctx)
		}()
		// Spool retention sweep (PR 5). Runs in its own goroutine
		// so the drainer hot loop stays clean. Skipped when no
		// spool is attached (memory-only / disabled).
		if e.activeSpool() != nil {
			go func() {
				defer func() {
					if r := recover(); r != nil {
						fmt.Fprintf(os.Stderr,
							"[sky.hub-exporter] spool sweep panicked: %v\n", r)
					}
				}()
				e.spoolRetentionSweep(ctx, e.spoolCfg)
			}()
		}
		// Register shutdown hook — runs before srv.Close so any
		// pending push reaches the hub within the orchestrator
		// grace window.
		RegisterShutdownHook("hub-exporter", func(hookCtx context.Context) {
			// Hook's deadline is the remaining ctx budget. Cap at
			// 8s so we leave 2s safety inside Cloud Run's 10s
			// grace.
			deadline := 8 * time.Second
			if dl, ok := hookCtx.Deadline(); ok {
				remaining := time.Until(dl)
				if remaining < deadline {
					deadline = remaining
				}
			}
			_ = e.Flush(deadline)
			e.Stop()
		})
	})
}

// Stop signals the drainer to exit. Does NOT wait — pair with Flush
// for the SIGTERM-drain path.
func (e *HubExporter) Stop() {
	if e == nil {
		return
	}
	e.stopOnce.Do(func() {
		e.draining.Store(true)
		close(e.stopCh)
		// Close the spool — releases the SQLite handle (file mode)
		// or zeroes the RAM buffer (memory mode). Best-effort; a
		// close error doesn't propagate because Stop has no return.
		e.spoolMu.Lock()
		sp := e.spool
		e.spool = nil
		e.spoolMu.Unlock()
		if sp != nil {
			_ = sp.Close()
		}
		// Don't clear activeHubExporter — Stop is typically called
		// during shutdown, after which the runtime exits. Leaving
		// the singleton in place keeps any in-flight Submit() calls
		// no-op-safe (they'll select+default into a closed channel,
		// which panics — handled below by gating on e.draining).
	})
}

// Flush blocks until the in-flight queue is drained OR deadline
// expires. Used by SIGTERM hook. Returns nil on clean drain, a
// deadline-exceeded error otherwise. The drainer continues to run
// — caller should pair with Stop() to actually shut down.
//
// Internally: marks draining=true so new Submits short-circuit;
// signals the drainer to flush eagerly; waits up to deadline for
// the queue to empty.
func (e *HubExporter) Flush(deadline time.Duration) error {
	if e == nil {
		return nil
	}
	e.draining.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	// Eagerly drain whatever's queued. We drain in-process — the
	// drainer goroutine is also pulling, so we use a separate sync
	// wait: poll the channel length every 25 ms.
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		// Channel empty AND no in-flight bytes → drained.
		if len(e.queue) == 0 && e.queueBytes.Load() == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("hub-exporter: flush deadline exceeded with %d items queued",
				len(e.queue))
		case <-tick.C:
			// Continue polling.
		}
	}
}

// Metrics returns the self-observability snapshot for emission to
// /_sky/metrics. Caller (observability.go MountObservabilityEndpoints
// or the Prometheus emitter) reads these labels.
func (e *HubExporter) Metrics() map[string]int64 {
	if e == nil {
		return nil
	}
	return map[string]int64{
		"sky_telemetry_dropped_total{level=debug}":          e.droppedDebug.Load(),
		"sky_telemetry_dropped_total{level=info}":           e.droppedInfo.Load(),
		"sky_telemetry_dropped_total{level=warn}":           e.droppedWarn.Load(),
		"sky_telemetry_dropped_total{level=error}":          e.droppedError.Load(),
		"sky_telemetry_push_attempts_total":                 e.pushAttempts.Load(),
		"sky_telemetry_push_failures_total{reason=network}": e.pushFailNet.Load(),
		"sky_telemetry_push_failures_total{reason=timeout}": e.pushFailTime.Load(),
		"sky_telemetry_push_failures_total{reason=4xx}":     e.pushFail4xx.Load(),
		"sky_telemetry_push_failures_total{reason=5xx}":     e.pushFail5xx.Load(),
		"sky_telemetry_push_failures_total{reason=circuit}": e.pushFailCircuit.Load(),
		"sky_telemetry_circuit_state":                       int64(e.circuit.Load()),
		"sky_telemetry_queue_bytes":                         e.queueBytes.Load(),
		"sky_telemetry_queue_len":                           int64(len(e.queue)),
	}
}

// ─── drainer goroutine ───────────────────────────────────────────

// drain — pulls batches off the channel, OTLP-encodes, pushes. Runs
// until stopCh closes.
func (e *HubExporter) drain(ctx context.Context) {
	// One-time replay of any batches the previous process left
	// unacked in the spool. Idempotent at the hub: replays carry
	// the same payload as the original attempt; the hub
	// deduplicates by signal id in the v0.16.2 receiver.
	if !e.spoolReplayed.Swap(true) {
		e.replaySpoolOnBoot(ctx)
	}

	// Pre-allocate batch buffer once; reuse on each cycle.
	batch := make([]telemetryItem, 0, e.batchMax)
	batchBytes := 0

	tick := time.NewTicker(e.batchInt)
	defer tick.Stop()

	flushBatch := func() {
		if len(batch) == 0 {
			return
		}
		// Spool first (best-effort), then push. The spool write
		// runs on this drainer goroutine — Submit is unaffected.
		// A spool failure counts a write_failures_total but doesn't
		// abort the push, so even if the durability layer is down
		// the hub still sees the data when reachable.
		token := e.spoolPersistAttempt(ctx, batch)
		if e.pushBatch(ctx, batch) {
			e.spoolAckAttempt(ctx, token)
		}
		// On push failure the spool row is left in place; on next
		// process restart the drainer replays it via
		// replaySpoolOnBoot.

		// Release retained payloads back to the byte counter so
		// future Submits have room.
		var released int64
		for _, it := range batch {
			released += int64(len(it.payload))
		}
		e.queueBytes.Add(-released)
		// Reset.
		batch = batch[:0]
		batchBytes = 0
	}

	for {
		select {
		case <-e.stopCh:
			// Final drain — pull everything pending without blocking,
			// then flush + exit.
			for {
				select {
				case it := <-e.queue:
					batch = append(batch, it)
					batchBytes += len(it.payload)
				default:
					flushBatch()
					return
				}
				if len(batch) >= e.batchMax || batchBytes >= e.batchBytes {
					flushBatch()
				}
			}

		case <-tick.C:
			flushBatch()

		case it := <-e.queue:
			batch = append(batch, it)
			batchBytes += len(it.payload)
			if len(batch) >= e.batchMax || batchBytes >= e.batchBytes {
				flushBatch()
			}
		}
	}
}

// pushBatch sends the batch to the hub with retry + circuit-breaker
// gating. Single hub URL per exporter; future PRs add HTTP+gRPC
// fan-out. Returns true iff EVERY kind's POST succeeded — the
// drainer uses this to decide whether to Ack the spool row (success
// → ack, failure → leave for next-boot replay).
func (e *HubExporter) pushBatch(ctx context.Context, batch []telemetryItem) bool {
	if len(batch) == 0 {
		return true
	}
	// Circuit gate.
	state := CircuitState(e.circuit.Load())
	if state == circuitOpen {
		// Maybe time to half-open?
		openedAt := e.circuitOpenedAt.Load()
		if openedAt > 0 && time.Since(time.Unix(0, openedAt)) >= e.circuitOpenWindow {
			e.transitionCircuit(circuitHalfOpen)
			state = circuitHalfOpen
		} else {
			e.pushFailCircuit.Add(int64(len(batch)))
			telemetry.Default().Add("sky_telemetry_push_failures_total",
				map[string]string{"reason": "circuit-open"}, float64(len(batch)))
			return false
		}
	}

	// Group batch by kind so each kind hits its OTLP endpoint
	// (/v1/logs, /v1/metrics, /v1/traces). For PR 4 we send each
	// kind in its own POST — simpler than mixed signals.
	byKind := make(map[TelemetryKind][][]byte, 3)
	for _, it := range batch {
		byKind[it.kind] = append(byKind[it.kind], it.payload)
	}

	anyOK := false
	anyFailed := false
	for kind, payloads := range byKind {
		ok := e.pushOneKind(ctx, kind, payloads)
		if ok {
			anyOK = true
		} else {
			anyFailed = true
		}
	}

	switch {
	case anyOK && !anyFailed:
		// All succeeded.
		e.consecFailures.Store(0)
		if state == circuitHalfOpen {
			e.transitionCircuit(circuitClosed)
		}
	case anyFailed:
		fails := e.consecFailures.Add(1)
		if fails >= 50 {
			e.transitionCircuit(circuitOpen)
			e.circuitOpenedAt.Store(time.Now().UnixNano())
		} else if state == circuitHalfOpen {
			// Half-open probe failed — back to open.
			e.transitionCircuit(circuitOpen)
			e.circuitOpenedAt.Store(time.Now().UnixNano())
		}
	}
	return anyOK && !anyFailed
}

func (e *HubExporter) transitionCircuit(to CircuitState) {
	e.circuit.Store(int32(to))
	telemetry.Default().SetGauge("sky_telemetry_circuit_state",
		map[string]string{"state": to.String()}, 1)
}

// pushOneKind wraps the OTLP payloads into a single ResourceLogs /
// ResourceMetrics / ResourceSpans envelope and POSTs. Returns true
// on 2xx; updates the appropriate failure counter otherwise.
func (e *HubExporter) pushOneKind(ctx context.Context, kind TelemetryKind, payloads [][]byte) bool {
	body, err := encodeOtlpEnvelope(kind, payloads)
	if err != nil {
		e.pushFailNet.Add(1) // classify as network bucket; rare path
		return false
	}
	e.pushAttempts.Add(1)
	telemetry.Default().Add("sky_telemetry_push_attempts_total", nil, 1)

	// Test transport short-circuit.
	if e.transportOverride != nil {
		status, terr := e.transportOverride(ctx, body)
		return e.classifyPushResult(status, terr)
	}

	endpoint := e.hubURL + otlpPathFor(kind)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		e.pushFailNet.Add(1)
		telemetry.Default().Add("sky_telemetry_push_failures_total",
			map[string]string{"reason": "network"}, 1)
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.token)

	resp, err := e.httpC.Do(req)
	if err != nil {
		// Distinguish timeout vs other network errors.
		if isTimeout(err) {
			e.pushFailTime.Add(1)
			telemetry.Default().Add("sky_telemetry_push_failures_total",
				map[string]string{"reason": "timeout"}, 1)
		} else {
			e.pushFailNet.Add(1)
			telemetry.Default().Add("sky_telemetry_push_failures_total",
				map[string]string{"reason": "network"}, 1)
		}
		return false
	}
	defer resp.Body.Close()
	// Drain body so the connection can be reused. OTLP collectors
	// rarely return bodies on success; on error we still drop the
	// response (no log surface to spam).
	_, _ = io.Copy(io.Discard, resp.Body)
	return e.classifyPushResult(resp.StatusCode, nil)
}

func (e *HubExporter) classifyPushResult(status int, err error) bool {
	if err != nil {
		if isTimeout(err) {
			e.pushFailTime.Add(1)
		} else {
			e.pushFailNet.Add(1)
		}
		return false
	}
	switch {
	case status >= 200 && status < 300:
		return true
	case status >= 400 && status < 500:
		e.pushFail4xx.Add(1)
		telemetry.Default().Add("sky_telemetry_push_failures_total",
			map[string]string{"reason": "4xx"}, 1)
		return false
	default: // 5xx + everything else
		e.pushFail5xx.Add(1)
		telemetry.Default().Add("sky_telemetry_push_failures_total",
			map[string]string{"reason": "5xx"}, 1)
		return false
	}
}

func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// net.Error.Timeout() — duck-type via type assertion (avoids
	// pulling net import for one method).
	type timeouter interface{ Timeout() bool }
	if t, ok := err.(timeouter); ok && t.Timeout() {
		return true
	}
	// String fallback for stdlib-wrapped errors.
	return strings.Contains(err.Error(), "timeout") ||
		strings.Contains(err.Error(), "deadline")
}

// ─── OTLP JSON encoding ──────────────────────────────────────────
//
// OTLP supports JSON encoding over HTTP with Content-Type
// application/json (OTel spec — exporter.opentelemetry.io/v1).
// PR 4 ships JSON for dep-surface reasons (protobuf would pull the
// otlptracegrpc runtime). PR 5 / v0.16.2 can add protobuf when the
// gRPC fallback ships.

func otlpPathFor(kind TelemetryKind) string {
	switch kind {
	case KindLog:
		return "/v1/logs"
	case KindMetric:
		return "/v1/metrics"
	case KindSpan:
		return "/v1/traces"
	}
	return "/v1/logs"
}

// encodeOtlpLog → OTLP logs/v1 LogRecord shape, JSON-encoded as a
// single-record fragment. Aggregated into a ResourceLogs envelope
// at push time.
func encodeOtlpLog(entry telemetry.LogEntry) ([]byte, error) {
	rec := otlpLogRecord{
		TimeUnixNano:   strconv.FormatInt(entry.TS.UnixNano(), 10),
		SeverityNumber: otlpSeverityNumber(entry.Level),
		SeverityText:   strings.ToUpper(entry.Level),
		Body:           otlpAnyValue{StringValue: entry.Message},
	}
	if entry.TraceID != "" {
		rec.TraceID = entry.TraceID
	}
	if entry.SpanID != "" {
		rec.SpanID = entry.SpanID
	}
	if entry.ReqID != "" {
		rec.Attributes = append(rec.Attributes,
			otlpAttr("req.id", entry.ReqID))
	}
	if entry.Route != "" {
		rec.Attributes = append(rec.Attributes,
			otlpAttr("http.route", entry.Route))
	}
	if entry.Status != 0 {
		rec.Attributes = append(rec.Attributes,
			otlpAttrInt("http.status_code", int64(entry.Status)))
	}
	if entry.LatencyMS != 0 {
		rec.Attributes = append(rec.Attributes,
			otlpAttrFloat("http.latency_ms", entry.LatencyMS))
	}
	if entry.ErrorStr != "" {
		rec.Attributes = append(rec.Attributes,
			otlpAttr("error", entry.ErrorStr))
	}
	for k, v := range entry.Fields {
		rec.Attributes = append(rec.Attributes, otlpAttr(k, v))
	}
	return json.Marshal(rec)
}

// encodeOtlpMetric → OTLP metrics/v1 Metric shape.
func encodeOtlpMetric(name, mtype string, delta, value float64, labels map[string]string) ([]byte, error) {
	attrs := make([]otlpAttribute, 0, len(labels))
	for k, v := range labels {
		attrs = append(attrs, otlpAttr(k, v))
	}
	now := strconv.FormatInt(time.Now().UnixNano(), 10)
	dp := otlpNumberDataPoint{
		TimeUnixNano: now,
		Attributes:   attrs,
		AsDouble:     value,
	}
	if mtype == "counter" {
		dp.AsDouble = delta
	}
	m := otlpMetric{Name: name}
	switch mtype {
	case "counter":
		m.Sum = &otlpSum{
			DataPoints:             []otlpNumberDataPoint{dp},
			AggregationTemporality: 2, // CUMULATIVE
			IsMonotonic:            true,
		}
	case "gauge":
		m.Gauge = &otlpGauge{DataPoints: []otlpNumberDataPoint{dp}}
	case "histogram":
		// Send histograms as gauge-of-last-observation for v0.16.1;
		// a proper bucketed histogram needs the bucket structure from
		// telemetry.Store and is a v0.16.2 follow-up.
		m.Gauge = &otlpGauge{DataPoints: []otlpNumberDataPoint{dp}}
	default:
		m.Gauge = &otlpGauge{DataPoints: []otlpNumberDataPoint{dp}}
	}
	return json.Marshal(m)
}

// encodeOtlpSpan → OTLP trace/v1 Span shape.
func encodeOtlpSpan(span telemetry.TraceEntry) ([]byte, error) {
	attrs := make([]otlpAttribute, 0, len(span.Attributes))
	for k, v := range span.Attributes {
		attrs = append(attrs, otlpAttr(k, v))
	}
	var endNs int64
	if !span.EndTime.IsZero() {
		endNs = span.EndTime.UnixNano()
	}
	out := otlpSpan{
		TraceID:           span.TraceID,
		SpanID:            span.SpanID,
		ParentSpanID:      span.ParentID,
		Name:              span.Name,
		Kind:              otlpSpanKind(span.Kind),
		StartTimeUnixNano: strconv.FormatInt(span.StartTime.UnixNano(), 10),
		EndTimeUnixNano:   strconv.FormatInt(endNs, 10),
		Attributes:        attrs,
		Status:            otlpSpanStatus(span.StatusCode, span.StatusMessage),
	}
	return json.Marshal(out)
}

// encodeOtlpEnvelope wraps a slice of per-record JSON fragments in
// the matching ResourceLogs / ResourceMetrics / ResourceSpans
// envelope. Fragments are inserted as raw JSON to avoid re-marshal
// cost on the drainer hot path.
func encodeOtlpEnvelope(kind TelemetryKind, fragments [][]byte) ([]byte, error) {
	// Build raw JSON: { "resourceXxx": [{ "resource": {...},
	// "scopeXxx": [{ "scope": {...}, "xxxRecords": [ <fragments> ] }] }] }
	var top, inner, leafKey string
	switch kind {
	case KindLog:
		top, inner, leafKey = "resourceLogs", "scopeLogs", "logRecords"
	case KindMetric:
		top, inner, leafKey = "resourceMetrics", "scopeMetrics", "metrics"
	case KindSpan:
		top, inner, leafKey = "resourceSpans", "scopeSpans", "spans"
	default:
		return nil, fmt.Errorf("unknown kind %v", kind)
	}

	var buf bytes.Buffer
	buf.WriteByte('{')
	buf.WriteByte('"')
	buf.WriteString(top)
	buf.WriteString(`":[{"resource":`)
	// Resource attributes — service.name + service.instance.id from env.
	resJSON, _ := json.Marshal(defaultResource())
	buf.Write(resJSON)
	buf.WriteString(`,"`)
	buf.WriteString(inner)
	buf.WriteString(`":[{"scope":{"name":"sky","version":"0.16.1"},"`)
	buf.WriteString(leafKey)
	buf.WriteString(`":[`)
	for i, frag := range fragments {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(frag)
	}
	buf.WriteString(`]}]}]}`)
	return buf.Bytes(), nil
}

// defaultResource builds the OTLP Resource shape used in every
// envelope. service.name + service.instance.id are derived from
// SkyDeploy / Cloud Run / Lambda env per SERVERLESS.md.
func defaultResource() otlpResource {
	svc := envOrDefault("OTEL_SERVICE_NAME", envOrDefault("K_SERVICE", "sky-app"))
	rev := envOrDefault("K_REVISION", envOrDefault("HOSTNAME", "instance-0"))
	return otlpResource{
		Attributes: []otlpAttribute{
			otlpAttr("service.name", svc),
			otlpAttr("service.instance.id", rev),
			otlpAttr("telemetry.sdk.name", "sky"),
			otlpAttr("telemetry.sdk.version", "0.16.1"),
			otlpAttr("telemetry.sdk.language", "go"),
		},
	}
}

// ─── OTLP JSON shapes — minimal field subset ─────────────────────

type otlpResource struct {
	Attributes []otlpAttribute `json:"attributes,omitempty"`
}

type otlpAttribute struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

type otlpAnyValue struct {
	StringValue string  `json:"stringValue,omitempty"`
	IntValue    string  `json:"intValue,omitempty"`    // OTLP spec: int as string
	DoubleValue float64 `json:"doubleValue,omitempty"`
	BoolValue   *bool   `json:"boolValue,omitempty"`
}

func otlpAttr(k, v string) otlpAttribute {
	return otlpAttribute{Key: k, Value: otlpAnyValue{StringValue: v}}
}

func otlpAttrInt(k string, v int64) otlpAttribute {
	return otlpAttribute{Key: k, Value: otlpAnyValue{IntValue: strconv.FormatInt(v, 10)}}
}

func otlpAttrFloat(k string, v float64) otlpAttribute {
	return otlpAttribute{Key: k, Value: otlpAnyValue{DoubleValue: v}}
}

type otlpLogRecord struct {
	TimeUnixNano   string          `json:"timeUnixNano"`
	SeverityNumber int             `json:"severityNumber,omitempty"`
	SeverityText   string          `json:"severityText,omitempty"`
	Body           otlpAnyValue    `json:"body,omitempty"`
	Attributes     []otlpAttribute `json:"attributes,omitempty"`
	TraceID        string          `json:"traceId,omitempty"`
	SpanID         string          `json:"spanId,omitempty"`
}

type otlpMetric struct {
	Name  string     `json:"name"`
	Sum   *otlpSum   `json:"sum,omitempty"`
	Gauge *otlpGauge `json:"gauge,omitempty"`
}

type otlpSum struct {
	DataPoints             []otlpNumberDataPoint `json:"dataPoints"`
	AggregationTemporality int                   `json:"aggregationTemporality"`
	IsMonotonic            bool                  `json:"isMonotonic"`
}

type otlpGauge struct {
	DataPoints []otlpNumberDataPoint `json:"dataPoints"`
}

type otlpNumberDataPoint struct {
	TimeUnixNano string          `json:"timeUnixNano"`
	Attributes   []otlpAttribute `json:"attributes,omitempty"`
	AsDouble     float64         `json:"asDouble"`
}

type otlpSpan struct {
	TraceID           string          `json:"traceId"`
	SpanID            string          `json:"spanId"`
	ParentSpanID      string          `json:"parentSpanId,omitempty"`
	Name              string          `json:"name"`
	Kind              int             `json:"kind,omitempty"`
	StartTimeUnixNano string          `json:"startTimeUnixNano"`
	EndTimeUnixNano   string          `json:"endTimeUnixNano,omitempty"`
	Attributes        []otlpAttribute `json:"attributes,omitempty"`
	Status            *otlpStatus     `json:"status,omitempty"`
}

type otlpStatus struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func otlpSeverityNumber(level string) int {
	switch strings.ToLower(level) {
	case "trace":
		return 1
	case "debug":
		return 5
	case "info":
		return 9
	case "warn", "warning":
		return 13
	case "error", "err":
		return 17
	case "fatal":
		return 21
	}
	return 9
}

func otlpSpanKind(k string) int {
	switch strings.ToLower(k) {
	case "internal":
		return 1
	case "server":
		return 2
	case "client":
		return 3
	case "producer":
		return 4
	case "consumer":
		return 5
	}
	return 1
}

func otlpSpanStatus(code, msg string) *otlpStatus {
	if code == "" && msg == "" {
		return nil
	}
	c := 0
	switch strings.ToUpper(code) {
	case "OK":
		c = 1
	case "ERROR":
		c = 2
	}
	return &otlpStatus{Code: c, Message: msg}
}

// ─── transport helpers ───────────────────────────────────────────

// defaultHubTransport returns the http.RoundTripper used by the hub
// client. Stdlib http.Transport with sane keep-alive settings + the
// optional InsecureSkipVerify flag (dev only).
func defaultHubTransport(insecure bool) http.RoundTripper {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 4
	t.MaxIdleConnsPerHost = 2
	t.IdleConnTimeout = 90 * time.Second
	t.DisableCompression = false
	if insecure {
		if t.TLSClientConfig == nil {
			t.TLSClientConfig = nil // leaves stdlib's default
		}
		// We don't import crypto/tls just for this knob. Users who
		// genuinely need TLS-insecure point at http:// instead.
		// Documented in CLAUDE.md / EXPORTER.md.
	}
	return t
}

// ─── opaque helpers used by tests ────────────────────────────────

// QueueLen returns the current queue length (for tests).
func (e *HubExporter) QueueLen() int {
	if e == nil {
		return 0
	}
	return len(e.queue)
}

// QueueBytes returns the current in-flight byte count (for tests).
func (e *HubExporter) QueueBytes() int64 {
	if e == nil {
		return 0
	}
	return e.queueBytes.Load()
}

// CircuitStateLoad returns the current circuit state (for tests).
func (e *HubExporter) CircuitStateLoad() CircuitState {
	if e == nil {
		return circuitClosed
	}
	return CircuitState(e.circuit.Load())
}

// SetCircuitWindowForTesting overrides the open window. TEST-ONLY.
func (e *HubExporter) SetCircuitWindowForTesting(d time.Duration) {
	if e == nil {
		return
	}
	e.circuitOpenWindow = d
}

// Ensure base64 is imported (we may use it in future PRs for binary
// IDs; harmless to keep). This avoids the "imported and not used"
// linter pain if the import is added speculatively.
var _ = base64.StdEncoding
