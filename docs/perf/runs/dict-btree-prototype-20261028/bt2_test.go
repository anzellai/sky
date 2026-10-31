package btproto

import (
	"math/rand"
	"testing"
)

func TestModel2(t *testing.T) {
	tr := &Tree2{}
	ref := map[int]int{}
	r := rand.New(rand.NewSource(1))
	var snaps []*Tree2
	var refs []map[int]int
	for i := 0; i < 50000; i++ {
		k := r.Intn(8000)
		tr = tr.Insert(k, i)
		ref[k] = i
		if i%10000 == 0 {
			snaps = append(snaps, tr)
			c := map[int]int{}
			for a, b := range ref {
				c[a] = b
			}
			refs = append(refs, c)
		}
	}
	if tr.size != len(ref) {
		t.Fatalf("size %d want %d", tr.size, len(ref))
	}
	for k, v := range ref {
		got, ok := tr.Get(k)
		if !ok || got.(int) != v {
			t.Fatalf("get %d", k)
		}
	}
	for si, s := range snaps {
		for k, v := range refs[si] {
			got, ok := s.Get(k)
			if !ok || got.(int) != v {
				t.Fatalf("snapshot %d get %d", si, k)
			}
		}
		if s.size != len(refs[si]) {
			t.Fatal("snapshot size")
		}
	}
}

func BenchmarkV2Insert1e5(b *testing.B) {
	keys := make([]any, 100000)
	r := rand.New(rand.NewSource(2))
	for i := range keys {
		keys[i] = r.Intn(1 << 30)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr := &Tree2{}
		for _, k := range keys {
			tr = tr.Insert(k, 1)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*100000), "ns/insert")
}

func BenchmarkV2Counts1e6(b *testing.B) {
	for i := 0; i < b.N; i++ {
		tr := &Tree2{}
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
}
