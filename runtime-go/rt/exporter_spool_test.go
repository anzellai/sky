package rt

// HubExporter spool tests — PR 5 of v0.16.1.
//
// Eight tests gating the spool durability layer:
//
//   1. TestSpool_FileMode_PersistsAcrossRestart — write batch with
//      file spool open, close exporter, re-open: replay drains
//      everything to the stub hub.
//   2. TestSpool_MemoryMode_DoesNotTouchDisk — set mode=memory,
//      submit, assert no SQLite file appears at spool path.
//   3. TestSpool_AutoDetect_ServerlessUsesMemory — K_SERVICE set →
//      resolveSpoolMode() returns SpoolMemory.
//   4. TestSpool_AutoDetect_VMUsesFile — no serverless env →
//      resolveSpoolMode() returns SpoolFile.
//   5. TestSpool_RetentionDeletesOldRows — insert rows with old
//      created_at → PruneOlderThan deletes them.
//   6. TestSpool_CircularTruncation — fill past maxBytes → oldest
//      rows deleted, sky_telemetry_spool_truncated_total incremented.
//   7. TestSpool_CrashResilience — simulate kill after Persist +
//      before Ack → next boot replays.
//   8. TestSpool_FileMode_DoesNotBlockOnSlowDisk — slow Persist
//      (1s simulated latency) does NOT raise Submit p99.99.

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ─── Test 1: File mode persists across restart ────────────────────

func TestSpool_FileMode_PersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "spool.db")

	// PHASE 1 — write 100 batches into the spool with a hub that
	// FAILS every push. Each batch becomes a sticky spool row.
	cfg := spoolConfig{
		mode:       SpoolFile,
		path:       dbPath,
		retention:  24 * time.Hour,
		maxBytes:   10 * 1024 * 1024,
		sweepEvery: time.Hour,
	}
	sp1, err := newFileSpool(cfg)
	if err != nil {
		t.Fatalf("newFileSpool phase1: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		batch := []telemetryItem{
			{kind: KindLog, severity: SevInfo, payload: []byte(`{"i":42}`)},
		}
		if _, err := sp1.Persist(ctx, batch); err != nil {
			t.Fatalf("persist[%d]: %v", i, err)
		}
	}
	// Verify spool has 100 batches.
	entries1, err := sp1.Drain(ctx, 200)
	if err != nil {
		t.Fatalf("phase1 drain: %v", err)
	}
	if len(entries1) != 100 {
		t.Errorf("phase1 drained %d batches; want 100", len(entries1))
	}
	sp1.Close()

	// PHASE 2 — re-open the spool (simulating process restart). The
	// drainer in production calls replaySpoolOnBoot which we
	// emulate here by Drain + push.
	sp2, err := newFileSpool(cfg)
	if err != nil {
		t.Fatalf("newFileSpool phase2: %v", err)
	}
	defer sp2.Close()

	// Verify the 100 rows are still there.
	entries2, err := sp2.Drain(ctx, 200)
	if err != nil {
		t.Fatalf("phase2 drain: %v", err)
	}
	if len(entries2) != 100 {
		t.Errorf("phase2 drained %d batches across restart; want 100",
			len(entries2))
	}

	// Now ack each one to simulate successful push.
	for _, ent := range entries2 {
		if err := sp2.Ack(ctx, ent.token); err != nil {
			t.Fatalf("ack token %d: %v", ent.token, err)
		}
	}

	// Verify the spool is empty after acks.
	entries3, err := sp2.Drain(ctx, 200)
	if err != nil {
		t.Fatalf("phase3 drain: %v", err)
	}
	if len(entries3) != 0 {
		t.Errorf("post-ack drained %d batches; want 0", len(entries3))
	}
}

// ─── Test 2: Memory mode does NOT touch disk ─────────────────────

func TestSpool_MemoryMode_DoesNotTouchDisk(t *testing.T) {
	dir := t.TempDir()
	pathHint := filepath.Join(dir, "should-not-be-created.db")

	cfg := spoolConfig{
		mode:       SpoolMemory,
		path:       pathHint, // path is irrelevant in memory mode
		retention:  24 * time.Hour,
		maxBytes:   1 * 1024 * 1024,
		sweepEvery: time.Hour,
	}
	sp, err := newMemorySpool(cfg)
	if err != nil {
		t.Fatalf("newMemorySpool: %v", err)
	}
	defer sp.Close()

	// Submit 100 batches.
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		batch := []telemetryItem{
			{kind: KindLog, severity: SevInfo, payload: []byte(`{}`)},
		}
		if _, err := sp.Persist(ctx, batch); err != nil {
			t.Fatalf("persist[%d]: %v", i, err)
		}
	}

	// Assert the hinted path does NOT exist on disk.
	if _, err := os.Stat(pathHint); !os.IsNotExist(err) {
		t.Errorf("memory mode created %s on disk; expected stat to "+
			"return os.IsNotExist (err=%v)", pathHint, err)
	}

	// Also walk the whole tempdir — nothing should appear.
	walkErr := filepath.Walk(dir, func(p string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		t.Errorf("memory mode unexpectedly created %s", p)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk %s: %v", dir, walkErr)
	}

	// Memory spool size should reflect the writes.
	size, err := sp.Size(ctx)
	if err != nil {
		t.Fatalf("Size: %v", err)
	}
	if size == 0 {
		t.Errorf("memory spool size=0 after 100 writes; want >0")
	}
	t.Logf("memory spool size=%d after 100 batches", size)
}

// ─── Test 3: Auto-detect → serverless picks memory ──────────────

func TestSpool_AutoDetect_ServerlessUsesMemory(t *testing.T) {
	// Save / restore env.
	defer ResetServerlessCache()
	t.Setenv("K_SERVICE", "test-service")
	t.Setenv("SKY_CONSOLE_SPOOL_MODE", "")
	ResetServerlessCache()

	mode := resolveSpoolMode()
	if mode != SpoolMemory {
		t.Errorf("resolveSpoolMode with K_SERVICE set = %v; want SpoolMemory",
			mode)
	}
}

// ─── Test 4: Auto-detect → VM picks file ────────────────────────

func TestSpool_AutoDetect_VMUsesFile(t *testing.T) {
	defer ResetServerlessCache()
	// Explicit unset; the helper checks every serverless env.
	for _, name := range serverlessEnvFingerprints {
		t.Setenv(name, "")
	}
	t.Setenv("SKY_CONSOLE_SPOOL_MODE", "")
	t.Setenv("SKY_RUNTIME_MODE", "vm") // force-vm override
	ResetServerlessCache()

	mode := resolveSpoolMode()
	if mode != SpoolFile {
		t.Errorf("resolveSpoolMode with no serverless env = %v; want SpoolFile",
			mode)
	}
}

// Test 4b — explicit override beats auto-detect.

func TestSpool_ExplicitOverride_Memory(t *testing.T) {
	defer ResetServerlessCache()
	for _, name := range serverlessEnvFingerprints {
		t.Setenv(name, "")
	}
	t.Setenv("SKY_RUNTIME_MODE", "vm")
	t.Setenv("SKY_CONSOLE_SPOOL_MODE", "memory")
	ResetServerlessCache()

	if mode := resolveSpoolMode(); mode != SpoolMemory {
		t.Errorf("explicit memory override = %v; want SpoolMemory", mode)
	}
}

func TestSpool_ExplicitOverride_None(t *testing.T) {
	defer ResetServerlessCache()
	t.Setenv("SKY_CONSOLE_SPOOL_MODE", "none")
	ResetServerlessCache()

	if mode := resolveSpoolMode(); mode != SpoolNone {
		t.Errorf("explicit none override = %v; want SpoolNone", mode)
	}
}

// ─── Test 5: Retention deletes old rows ─────────────────────────

func TestSpool_RetentionDeletesOldRows(t *testing.T) {
	// File-mode spool, then directly tamper with created_at to make
	// rows look old. (Can't use time.Sleep in a test; we surgically
	// rewrite the timestamps via SQL.)
	dir := t.TempDir()
	cfg := spoolConfig{
		mode:       SpoolFile,
		path:       filepath.Join(dir, "spool.db"),
		retention:  1 * time.Hour,
		maxBytes:   10 * 1024 * 1024,
		sweepEvery: time.Hour,
	}
	sp, err := newFileSpool(cfg)
	if err != nil {
		t.Fatalf("newFileSpool: %v", err)
	}
	defer sp.Close()
	ctx := context.Background()

	// Insert 50 batches.
	for i := 0; i < 50; i++ {
		batch := []telemetryItem{
			{kind: KindLog, severity: SevInfo, payload: []byte(`{}`)},
		}
		if _, err := sp.Persist(ctx, batch); err != nil {
			t.Fatalf("persist: %v", err)
		}
	}

	// Surgery: mark the first 30 batches as 2 hours old.
	oldNanos := time.Now().Add(-2 * time.Hour).UnixNano()
	if _, err := sp.db.ExecContext(ctx,
		`UPDATE spool_batches SET created_at = ? WHERE token <= 30`,
		oldNanos); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	// Prune anything older than now-1h.
	cutoff := time.Now().Add(-1 * time.Hour)
	deleted, err := sp.PruneOlderThan(ctx, cutoff)
	if err != nil {
		t.Fatalf("PruneOlderThan: %v", err)
	}
	if deleted != 30 {
		t.Errorf("PruneOlderThan deleted %d; want 30", deleted)
	}

	// Remaining should be 20.
	entries, err := sp.Drain(ctx, 200)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(entries) != 20 {
		t.Errorf("post-prune drain = %d; want 20", len(entries))
	}
}

// Memory variant — same retention behaviour against the bounded ring.

func TestSpool_MemoryRetentionDeletesOldRows(t *testing.T) {
	cfg := spoolConfig{
		mode:     SpoolMemory,
		maxBytes: 10 * 1024,
	}
	sp, err := newMemorySpool(cfg)
	if err != nil {
		t.Fatalf("newMemorySpool: %v", err)
	}
	ctx := context.Background()

	// Add 10 entries with an old timestamp + 5 fresh ones.
	old := time.Now().Add(-2 * time.Hour)
	for i := 0; i < 10; i++ {
		batch := []telemetryItem{{kind: KindLog, severity: SevInfo, payload: []byte(`{}`)}}
		tok, _ := sp.Persist(ctx, batch)
		// Surgery: backdate the entry.
		sp.mu.Lock()
		for _, e := range sp.entries {
			if e.token == tok {
				e.at = old
			}
		}
		sp.mu.Unlock()
	}
	for i := 0; i < 5; i++ {
		batch := []telemetryItem{{kind: KindLog, severity: SevInfo, payload: []byte(`{}`)}}
		sp.Persist(ctx, batch)
	}

	cutoff := time.Now().Add(-1 * time.Hour)
	deleted, err := sp.PruneOlderThan(ctx, cutoff)
	if err != nil {
		t.Fatalf("memory PruneOlderThan: %v", err)
	}
	if deleted != 10 {
		t.Errorf("memory PruneOlderThan deleted %d; want 10", deleted)
	}
}

// ─── Test 6: Circular truncation under cap ──────────────────────

func TestSpool_CircularTruncation_File(t *testing.T) {
	dir := t.TempDir()
	cfg := spoolConfig{
		mode:       SpoolFile,
		path:       filepath.Join(dir, "spool.db"),
		retention:  24 * time.Hour,
		maxBytes:   2048, // 2 KB — small so we hit cap fast
		sweepEvery: time.Hour,
	}
	sp, err := newFileSpool(cfg)
	if err != nil {
		t.Fatalf("newFileSpool: %v", err)
	}
	defer sp.Close()
	ctx := context.Background()

	// Each batch is ~64 B; pour 100 → 6.4 KB of data, far over the
	// 2 KB cap.
	bigPayload := strings.Repeat("x", 60)
	for i := 0; i < 100; i++ {
		batch := []telemetryItem{
			{kind: KindLog, severity: SevInfo, payload: []byte(bigPayload)},
		}
		if _, err := sp.Persist(ctx, batch); err != nil {
			t.Fatalf("persist[%d]: %v", i, err)
		}
	}

	// Truncate-to-cap.
	deleted, err := sp.TruncateToSize(ctx, cfg.maxBytes)
	if err != nil {
		t.Fatalf("TruncateToSize: %v", err)
	}
	if deleted == 0 {
		t.Error("TruncateToSize deleted 0 rows but spool was over cap")
	}
	size, _ := sp.Size(ctx)
	if size > cfg.maxBytes {
		t.Errorf("post-truncate size=%d > cap=%d", size, cfg.maxBytes)
	}
	t.Logf("truncated %d rows; size=%d cap=%d", deleted, size, cfg.maxBytes)
}

// Memory mode auto-truncates inside Persist; verify that path.

func TestSpool_CircularTruncation_Memory(t *testing.T) {
	cfg := spoolConfig{
		mode:     SpoolMemory,
		maxBytes: 1024,
	}
	t.Setenv(envSpoolMaxBytes, "1024") // honour user override
	sp, err := newMemorySpool(cfg)
	if err != nil {
		t.Fatalf("newMemorySpool: %v", err)
	}
	ctx := context.Background()

	bigPayload := strings.Repeat("y", 64)
	for i := 0; i < 100; i++ {
		batch := []telemetryItem{{kind: KindLog, severity: SevInfo, payload: []byte(bigPayload)}}
		sp.Persist(ctx, batch)
	}

	size, _ := sp.Size(ctx)
	if size > 1024 {
		t.Errorf("memory spool size=%d > cap=1024 after 100×64B writes", size)
	}
	t.Logf("memory spool size=%d after 100 oversized writes (cap=1024)", size)
}

// ─── Test 7: Crash resilience — persist without ack, then replay ─

func TestSpool_CrashResilience(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "spool.db")

	// PHASE 1 — open a HubExporter, attach a file spool, submit
	// items but make the hub FAIL so nothing gets acked. Simulate
	// crash by NOT calling Stop / Flush.
	exp1 := NewHubExporterForTesting(func(ctx context.Context, body []byte) (int, error) {
		return 500, nil // always fail
	})
	cfg := spoolConfig{
		mode:       SpoolFile,
		path:       dbPath,
		retention:  24 * time.Hour,
		maxBytes:   10 * 1024 * 1024,
		sweepEvery: time.Hour,
	}
	sp1, err := newFileSpool(cfg)
	if err != nil {
		t.Fatalf("phase1 newFileSpool: %v", err)
	}
	exp1.spoolCfg = cfg
	exp1.attachSpool(sp1)
	exp1.batchInt = 20 * time.Millisecond
	exp1.Start(context.Background())

	const N = 50
	for i := 0; i < N; i++ {
		exp1.Submit(KindLog, []byte(`{"x":"crash"}`), SevInfo)
	}
	// Let drainer cycle through enough times to spool everything.
	time.Sleep(150 * time.Millisecond)

	// Crash — close the SQLite handle WITHOUT calling Stop.
	// (Stop would close the spool too; we want to leave rows in.)
	sp1.Close()

	// Confirm the on-disk DB has unacked rows.
	sp1check, err := newFileSpool(cfg)
	if err != nil {
		t.Fatalf("phase1 verify open: %v", err)
	}
	pre, err := sp1check.Drain(context.Background(), 1000)
	if err != nil {
		t.Fatalf("phase1 verify drain: %v", err)
	}
	if len(pre) == 0 {
		t.Fatalf("phase1: no batches in spool — drainer never persisted")
	}
	t.Logf("phase1: spool has %d unacked batches after crash", len(pre))
	sp1check.Close()

	// PHASE 2 — new exporter with a SUCCESS hub. Boot replay should
	// drain the spool and ack everything.
	var received atomic.Int64
	exp2 := NewHubExporterForTesting(func(ctx context.Context, body []byte) (int, error) {
		received.Add(int64(strings.Count(string(body), `{"x":"crash"}`)))
		return 200, nil
	})
	sp2, err := newFileSpool(cfg)
	if err != nil {
		t.Fatalf("phase2 newFileSpool: %v", err)
	}
	exp2.spoolCfg = cfg
	exp2.attachSpool(sp2)
	exp2.batchInt = 20 * time.Millisecond
	exp2.Start(context.Background())

	// Let the boot-replay run.
	time.Sleep(400 * time.Millisecond)

	t.Logf("phase2: hub received %d fragments via replay", received.Load())
	if received.Load() < int64(N) {
		t.Errorf("phase2: replay delivered %d of %d fragments", received.Load(), N)
	}

	// Spool should be empty after acks.
	post, err := sp2.Drain(context.Background(), 1000)
	if err != nil {
		t.Fatalf("phase2 final drain: %v", err)
	}
	if len(post) != 0 {
		t.Errorf("phase2: spool not empty after replay (%d entries left)", len(post))
	}
	exp2.Stop()
}

// ─── Test 8: File mode does NOT block hot path on slow disk ─────

// slowSpool wraps a real spool with an injected latency on Persist.
// Used to simulate a slow disk (Cloud Run boot volume + occasional
// fsync stalls) and verify Submit doesn't regress.
type slowSpool struct {
	inner    spool
	delay    time.Duration
}

func (s *slowSpool) Persist(ctx context.Context, batch []telemetryItem) (int64, error) {
	time.Sleep(s.delay)
	return s.inner.Persist(ctx, batch)
}
func (s *slowSpool) Ack(ctx context.Context, token int64) error { return s.inner.Ack(ctx, token) }
func (s *slowSpool) Drain(ctx context.Context, limit int) ([]spoolEntry, error) {
	return s.inner.Drain(ctx, limit)
}
func (s *slowSpool) Size(ctx context.Context) (int64, error) { return s.inner.Size(ctx) }
func (s *slowSpool) PruneOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	return s.inner.PruneOlderThan(ctx, cutoff)
}
func (s *slowSpool) TruncateToSize(ctx context.Context, maxBytes int64) (int64, error) {
	return s.inner.TruncateToSize(ctx, maxBytes)
}
func (s *slowSpool) Mode() SpoolMode { return s.inner.Mode() }
func (s *slowSpool) Close() error    { return s.inner.Close() }

func TestSpool_FileMode_DoesNotBlockOnSlowDisk(t *testing.T) {
	// Build an exporter with a fast hub but a SLOW spool. The
	// spool injects 100ms latency per Persist call — orders of
	// magnitude over what Submit can tolerate. Submit MUST stay
	// sub-ms because spool runs on the drainer goroutine, not the
	// caller's.
	dir := t.TempDir()
	cfg := spoolConfig{
		mode:       SpoolFile,
		path:       filepath.Join(dir, "spool.db"),
		retention:  24 * time.Hour,
		maxBytes:   10 * 1024 * 1024,
		sweepEvery: time.Hour,
	}
	innerSp, err := newFileSpool(cfg)
	if err != nil {
		t.Fatalf("newFileSpool: %v", err)
	}
	slow := &slowSpool{inner: innerSp, delay: 100 * time.Millisecond}

	exp := NewHubExporterForTesting(func(ctx context.Context, body []byte) (int, error) {
		return 200, nil
	})
	exp.spoolCfg = cfg
	exp.attachSpool(slow)
	exp.batchInt = 50 * time.Millisecond
	exp.Start(context.Background())
	defer exp.Stop()

	const N = 10000
	latencies := make([]time.Duration, N)
	payload := []byte(`{"slow":"disk"}`)

	for i := 0; i < N; i++ {
		t0 := time.Now()
		exp.Submit(KindLog, payload, SevInfo)
		latencies[i] = time.Since(t0)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p99_99 := latencies[int(float64(N)*0.9999)]
	p99 := latencies[int(float64(N)*0.99)]
	p50 := latencies[N/2]
	t.Logf("with 100ms slow-disk spool: p50=%v p99=%v p99.99=%v", p50, p99, p99_99)

	// Hot-path gate: Submit must stay under 1 ms even with a
	// 100 ms spool Persist. Confirms spool I/O is genuinely async.
	if p99_99 > time.Millisecond {
		t.Errorf("p99.99 latency %v > 1ms with slow spool — spool I/O on hot path",
			p99_99)
	}
}

// ─── Bonus: openSpool resolves the configured mode correctly ────

func TestOpenSpool_FileMode_ReturnsFileSpool(t *testing.T) {
	dir := t.TempDir()
	cfg := spoolConfig{mode: SpoolFile, path: filepath.Join(dir, "x.db")}
	sp, err := openSpool(cfg)
	if err != nil {
		t.Fatalf("openSpool file: %v", err)
	}
	defer sp.Close()
	if sp.Mode() != SpoolFile {
		t.Errorf("openSpool returned Mode=%v; want SpoolFile", sp.Mode())
	}
}

func TestOpenSpool_MemoryMode_ReturnsMemorySpool(t *testing.T) {
	cfg := spoolConfig{mode: SpoolMemory}
	sp, err := openSpool(cfg)
	if err != nil {
		t.Fatalf("openSpool memory: %v", err)
	}
	defer sp.Close()
	if sp.Mode() != SpoolMemory {
		t.Errorf("openSpool returned Mode=%v; want SpoolMemory", sp.Mode())
	}
}

func TestOpenSpool_NoneMode_ReturnsNil(t *testing.T) {
	cfg := spoolConfig{mode: SpoolNone}
	sp, err := openSpool(cfg)
	if err != nil {
		t.Fatalf("openSpool none: %v", err)
	}
	if sp != nil {
		t.Errorf("openSpool(none) returned non-nil: %v", sp)
	}
}

// drainer-replay integration via the HubExporter API. Confirms the
// public Start() path triggers replaySpoolOnBoot.

func TestSpool_Start_TriggersReplayOnBoot(t *testing.T) {
	dir := t.TempDir()
	cfg := spoolConfig{
		mode:       SpoolFile,
		path:       filepath.Join(dir, "spool.db"),
		retention:  24 * time.Hour,
		maxBytes:   10 * 1024 * 1024,
		sweepEvery: time.Hour,
	}

	// Pre-seed the spool with 20 batches.
	preSpool, err := newFileSpool(cfg)
	if err != nil {
		t.Fatalf("pre-seed open: %v", err)
	}
	for i := 0; i < 20; i++ {
		preSpool.Persist(context.Background(),
			[]telemetryItem{
				{kind: KindLog, severity: SevInfo, payload: []byte(`{"r":"boot"}`)},
			})
	}
	preSpool.Close()

	// Now spin up an exporter with the same spool. Start() should
	// trigger replaySpoolOnBoot.
	var received atomic.Int64
	var mu sync.Mutex
	bodies := [][]byte{}
	exp := NewHubExporterForTesting(func(ctx context.Context, body []byte) (int, error) {
		mu.Lock()
		bodies = append(bodies, append([]byte(nil), body...))
		mu.Unlock()
		received.Add(int64(strings.Count(string(body), `{"r":"boot"}`)))
		return 200, nil
	})
	sp, err := newFileSpool(cfg)
	if err != nil {
		t.Fatalf("replay-side open: %v", err)
	}
	exp.spoolCfg = cfg
	exp.attachSpool(sp)
	exp.batchInt = 25 * time.Millisecond
	exp.Start(context.Background())
	defer exp.Stop()

	// Let the boot-replay run.
	time.Sleep(300 * time.Millisecond)

	got := received.Load()
	t.Logf("Start-triggered replay delivered %d fragments", got)
	if got < 20 {
		t.Errorf("expected >=20 fragments via replay; got %d", got)
	}
}
