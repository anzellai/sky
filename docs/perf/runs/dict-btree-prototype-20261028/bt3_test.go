package btproto

import (
	"fmt"
	"math/rand"
	"testing"
)

func TestModel3(t *testing.T) {
	for _, w := range []int{4, 8, 16, 32} {
		W3 = w
		tr := &Tree3[int]{}
		ref := map[int]int{}
		r := rand.New(rand.NewSource(1))
		for i := 0; i < 30000; i++ {
			k := r.Intn(6000)
			tr = tr.Insert(k, i)
			ref[k] = i
		}
		for k, v := range ref {
			got, ok := tr.Get(k)
			if !ok || got.(int) != v {
				t.Fatalf("w%d get %d", w, k)
			}
		}
		if tr.size != len(ref) {
			t.Fatal("size")
		}
	}
}

func BenchmarkV3(b *testing.B) {
	for _, w := range []int{8, 16, 32} {
		b.Run(fmt.Sprintf("insert1e5/w%d", w), func(b *testing.B) {
			W3 = w
			keys := make([]int, 100000)
			r := rand.New(rand.NewSource(2))
			for i := range keys {
				keys[i] = r.Intn(1 << 30)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tr := &Tree3[int]{}
				for _, k := range keys {
					tr = tr.Insert(k, 1)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*100000), "ns/insert")
		})
		b.Run(fmt.Sprintf("counts1e6/w%d", w), func(b *testing.B) {
			W3 = w
			for i := 0; i < b.N; i++ {
				tr := &Tree3[int]{}
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
