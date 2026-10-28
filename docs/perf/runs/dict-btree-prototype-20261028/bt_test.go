package btproto

import (
	"fmt"
	"math/rand"
	"testing"
)

func TestModel(t *testing.T) {
	for _, w := range []int{4, 8, 16, 32} {
		tr := New(w, IntCmp)
		ref := map[int]int{}
		r := rand.New(rand.NewSource(1))
		old := tr
		for i := 0; i < 20000; i++ {
			k := r.Intn(5000)
			tr = tr.Insert(k, i)
			ref[k] = i
			if i == 10000 {
				old = tr
			}
		}
		_ = old
		if tr.size != len(ref) {
			t.Fatalf("w=%d size %d want %d", w, tr.size, len(ref))
		}
		for k, v := range ref {
			got, ok := tr.Get(k)
			if !ok || got.(int) != v {
				t.Fatalf("w=%d get %d", w, k)
			}
		}
	}
}

func benchInsert(b *testing.B, w, nkeys int) {
	keys := make([]any, nkeys)
	r := rand.New(rand.NewSource(2))
	for i := range keys {
		keys[i] = r.Intn(1 << 30)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr := New(w, IntCmp)
		for _, k := range keys {
			tr = tr.Insert(k, 1)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*nkeys), "ns/insert")
}

func BenchmarkInsert1e5(b *testing.B) {
	for _, w := range []int{8, 16, 32} {
		b.Run(fmt.Sprintf("w%d", w), func(b *testing.B) { benchInsert(b, w, 100000) })
	}
}

// The perf-gate shape: 1e6 updates over 1e5 distinct keys (get then insert).
func BenchmarkCounts1e6(b *testing.B) {
	for _, w := range []int{8, 16, 32} {
		b.Run(fmt.Sprintf("w%d", w), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				tr := New(w, IntCmp)
				for j := 1; j <= 1000000; j++ {
					k := j % 100000
					c := 0
					if v, ok := tr.Get(k); ok {
						c = v.(int)
					}
					tr = tr.Insert(k, c+1)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*1000000), "ns/update")
		})
	}
}
