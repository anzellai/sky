package bluedb

import (
	"fmt"
	"sync/atomic"
	"testing"
)

// Phase-1 throughput benchmark (§8.1 #2-#5). Port of the old bench_test.go shape onto
// the Pebble-backed engine. Run WITHOUT -race:
//
//	go test ./bluedb/ -bench . -benchmem -run '^$'
//
// Honest ceiling (grill finding 4): the single-committer forgoes Pebble's commit-pipeline
// concurrency (one outstanding Apply at a time) in exchange for a strictly-monotonic
// commitTs total order (the load-bearing L2/L4 contract). Throughput therefore comes from
// GROUP COMMIT — many concurrent writers coalesce into one Apply(Sync)/one fsync — not
// from concurrent Applies (which would break the total order). The target is ~51k durable
// writes/s at concurrency; the reported durable-writes/s is the measured number.

// BenchmarkGroupCommitDurableWrites measures durable (Apply(Sync)) writes/s under
// concurrent writers coalescing through the single group-committer.
func BenchmarkGroupCommitDurableWrites(b *testing.B) {
	e, err := openWith(config{dir: b.TempDir()})
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	defer e.Close()

	var ctr atomic.Int64
	// High writer concurrency so the group-committer can form large batches (one fsync
	// amortized over many commits) — the group-commit ceiling the ~51k target lives at.
	b.SetParallelism(64)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		val := []byte("value-payload-32-bytes-xxxxxxxxx")
		for pb.Next() {
			i := ctr.Add(1)
			r := e.Commit(CommitReq{Writes: []VersionedWrite{{
				UserKey: []byte(fmt.Sprintf("k%09d", i)),
				Op:      OpPut,
				Value:   val,
			}}})
			if r.Err != nil {
				b.Fatalf("commit: %v", r.Err)
			}
		}
	})
	b.StopTimer()
	if secs := b.Elapsed().Seconds(); secs > 0 {
		b.ReportMetric(float64(b.N)/secs, "durable-writes/s")
	}
}

// BenchmarkPointRead measures cached point-read latency off a pinned snapshot (block
// cache; p99 fast, §8.1 #2).
func BenchmarkPointRead(b *testing.B) {
	e, err := openWith(config{dir: b.TempDir()})
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	defer e.Close()

	const n = 50000
	preload(b, e, "k%09d", n)
	rd := e.snapshotAt(e.NowTs())
	defer rd.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := []byte(fmt.Sprintf("k%09d", i%n))
		if _, _, ok := rd.Get(key); !ok {
			b.Fatalf("missing key %s", key)
		}
	}
}

// preload writes n keys (formatted via keyFmt) in batched commits (fast setup — not one
// fsync per key).
func preload(b *testing.B, e *pebbleEngine, keyFmt string, n int) {
	b.Helper()
	const batch = 1000
	for start := 0; start < n; start += batch {
		writes := make([]VersionedWrite, 0, batch)
		for i := start; i < start+batch && i < n; i++ {
			writes = append(writes, VersionedWrite{UserKey: []byte(fmt.Sprintf(keyFmt, i)), Op: OpPut, Value: []byte("v")})
		}
		if r := e.Commit(CommitReq{Writes: writes}); r.Err != nil {
			b.Fatalf("preload @%d: %v", start, r.Err)
		}
	}
}

// BenchmarkRangeScan measures an ordered range scan (O(log n + k), native Pebble
// iteration — no scan-then-sort, §8.1 #4).
func BenchmarkRangeScan(b *testing.B) {
	e, err := openWith(config{dir: b.TempDir()})
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	defer e.Close()

	const n = 50000
	preload(b, e, "user:%09d", n)
	rd := e.snapshotAt(e.NowTs())
	defer rd.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c := rd.Iterate([]byte("user:"))
		count := 0
		for c.Next() {
			count++
		}
		c.Close()
		if count != n {
			b.Fatalf("scan visited %d, want %d", count, n)
		}
	}
}

// TestSpillToDiskNoRAMCeiling proves §8.1 #5: a dataset larger than the memtable spills
// to SSTables and still reads correctly — no MaxKeys/ErrFull cliff. A tiny memtable forces
// many flushes; a large, sampled key set is written and read back exactly.
func TestSpillToDiskNoRAMCeiling(t *testing.T) {
	if testing.Short() {
		t.Skip("spill test writes a large dataset; skipped in -short")
	}
	e, err := openWith(config{dir: t.TempDir(), memTableSize: 256 << 10}) // 256 KiB memtable → many spills
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer e.Close()

	const n = 60000
	const batch = 500            // batch writes so the test isn't 60k sequential fsyncs (still spills)
	payload := make([]byte, 256) // ~15 MB of live data >> the 256 KiB memtable
	for i := range payload {
		payload[i] = byte(i)
	}
	for start := 0; start < n; start += batch {
		writes := make([]VersionedWrite, 0, batch)
		for i := start; i < start+batch && i < n; i++ {
			writes = append(writes, VersionedWrite{
				UserKey: []byte(fmt.Sprintf("k%09d", i)),
				Op:      OpPut,
				Value:   append([]byte(fmt.Sprintf("%d:", i)), payload...),
			})
		}
		if r := e.Commit(CommitReq{Writes: writes}); r.Err != nil {
			t.Fatalf("commit batch @%d: %v", start, r.Err)
		}
	}

	// Read a spread of keys back from a snapshot — data that has spilled to SSTables
	// resolves identically to data still in the memtable.
	rd := e.snapshotAt(e.NowTs())
	defer rd.Close()
	for _, i := range []int{0, 1, 123, 4567, 30000, 59999} {
		key := fmt.Sprintf("k%09d", i)
		v, _, ok := rd.Get([]byte(key))
		if !ok {
			t.Fatalf("spilled key %s not found (RAM ceiling / lost on flush)", key)
		}
		want := fmt.Sprintf("%d:", i)
		if string(v[:len(want)]) != want {
			t.Fatalf("spilled key %s value prefix=%q want %q", key, v[:len(want)], want)
		}
	}
}
