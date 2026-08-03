package bluedb

import (
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func benchDB(b *testing.B, opts Options) *DB {
	b.Helper()
	db, err := Open(filepath.Join(b.TempDir(), "app.blue"), opts)
	if err != nil {
		b.Fatal(err)
	}
	return db
}

// Cached point read — the hot read path (working set in RAM). Concurrent readers.
func BenchmarkGetCachedParallel(b *testing.B) {
	db := benchDB(b, Options{Sync: false})
	defer db.Close()
	const n = 10000
	keys := make([][]byte, n)
	for i := 0; i < n; i++ {
		keys[i] = []byte(fmt.Sprintf("k%06d", i))
		_ = db.Put(keys[i], []byte("a-typical-record-value-payload-of-modest-size"))
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			db.Get(keys[i%n])
			i++
		}
	})
}

// Durable write, Sync mode, concurrent writers — the group-commit throughput path
// (many concurrent writes ride one fsync). Reports the mean batch size.
func BenchmarkPutSyncParallel(b *testing.B) {
	db := benchDB(b, Options{Sync: true, CheckpointEvery: 200000})
	defer db.Close()
	val := []byte("a-typical-small-value-payload-around-sixty-four-bytes-long!!!")
	var ctr int64
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := atomic.AddInt64(&ctr, 1)
			if err := db.Put([]byte(fmt.Sprintf("k%08d", n)), val); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.StopTimer()
	batches, writes, _ := db.Stats()
	if batches > 0 {
		b.ReportMetric(float64(writes)/float64(batches), "writes/fsync")
	}
}

// Durable write with MANY in-flight writers — group commit amortizes one fsync
// across a bigger batch, so durable throughput scales with concurrency (the key
// property for a reactive app with many concurrent sessions). Compare its
// writes/fsync to BenchmarkPutSyncParallel's.
func BenchmarkPutSyncHighConcurrency(b *testing.B) {
	db := benchDB(b, Options{Sync: true, CheckpointEvery: 500000})
	defer db.Close()
	val := []byte("a-typical-small-value-payload-around-sixty-four-bytes-long!!!")
	var ctr int64
	b.SetParallelism(64) // 64x GOMAXPROCS in-flight writers → large group-commit batches
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := atomic.AddInt64(&ctr, 1)
			if err := db.Put([]byte(fmt.Sprintf("k%08d", n)), val); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.StopTimer()
	batches, writes, _ := db.Stats()
	if batches > 0 {
		b.ReportMetric(float64(writes)/float64(batches), "writes/fsync")
	}
}

// Relaxed (NoSync) concurrent write throughput — the ceiling without fsync.
func BenchmarkPutNoSyncParallel(b *testing.B) {
	db := benchDB(b, Options{Sync: false, CheckpointEvery: 200000})
	defer db.Close()
	val := []byte("a-typical-small-value-payload-around-sixty-four-bytes-long!!!")
	var ctr int64
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := atomic.AddInt64(&ctr, 1)
			if err := db.Put([]byte(fmt.Sprintf("k%08d", n)), val); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// A read-heavy mix (90% get / 10% durable put) — the reactive-app shape.
func BenchmarkMixed90Read(b *testing.B) {
	db := benchDB(b, Options{Sync: true, CheckpointEvery: 200000})
	defer db.Close()
	const n = 10000
	keys := make([][]byte, n)
	for i := 0; i < n; i++ {
		keys[i] = []byte(fmt.Sprintf("k%06d", i))
		_ = db.Put(keys[i], []byte("payload"))
	}
	val := []byte("updated-payload")
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if i%10 == 0 {
				_ = db.Put(keys[i%n], val)
			} else {
				db.Get(keys[i%n])
			}
			i++
		}
	})
}
