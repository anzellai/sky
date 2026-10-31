package hamtproto

import (
	"fmt"
	"math/rand"
	"strconv"
	"testing"
)

func key(i int) string { return "\x01i" + strconv.Itoa(i) }

type dict interface {
	get(string) (int, bool)
	ins(string, int) dict
	size() int
}

type pW struct{ PDict[int] }
type iW struct{ IDict[int] }
type mW struct{ MapDict[int] }
type fW struct{ FDict[int] }
type cW struct{ CDict[int] }

func (d cW) get(k string) (int, bool) { return d.Get(k) }
func (d cW) ins(k string, v int) dict { return cW{d.Insert(k, v)} }
func (d cW) size() int                { return d.Len() }

func (d pW) get(k string) (int, bool) { return d.Get(k) }
func (d pW) ins(k string, v int) dict { return pW{d.Insert(k, v)} }
func (d pW) size() int                { return d.Len() }
func (d iW) get(k string) (int, bool) { return d.Get(k) }
func (d iW) ins(k string, v int) dict { return iW{d.Insert(k, v)} }
func (d iW) size() int                { return d.Len() }
func (d mW) get(k string) (int, bool) { return d.Get(k) }
func (d mW) ins(k string, v int) dict { return mW{d.Insert(k, v)} }
func (d mW) size() int                { return len(d.MapDict) }
func (d fW) get(k string) (int, bool) { return d.Get(k) }
func (d fW) ins(k string, v int) dict { return fW{d.Insert(k, v)} }
func (d fW) size() int                { return len(d.FDict) }

var variants = []struct {
	name string
	mk   func() dict
}{
	{"map", func() dict { return mW{MapDict[int]{}} }},
	{"flat", func() dict { return fW{} }},
	{"ptr32", func() dict { return pW{} }},
	{"inline32", func() dict { return iW{NewI[int](5)} }},
	{"inline16", func() dict { return iW{NewI[int](4)} }},
	{"compact32", func() dict { return cW{NewC[int](5)} }},
	{"compact16", func() dict { return cW{NewC[int](4)} }},
	{"compact8", func() dict { return cW{NewC[int](3)} }},
}

// TestModel: every variant agrees with a Go map, and an old version is
// unchanged after later inserts (persistence).
func TestModel(t *testing.T) {
	for _, vr := range variants {
		if vr.name == "flat" {
			continue // linear; the small range only
		}
		d := vr.mk()
		ref := map[string]int{}
		r := rand.New(rand.NewSource(1))
		var old dict
		var oldRef map[string]int
		for i := 0; i < 30000; i++ {
			k := key(r.Intn(8000))
			d = d.ins(k, i)
			ref[k] = i
			if i == 15000 {
				old = d
				oldRef = map[string]int{}
				for k, v := range ref {
					oldRef[k] = v
				}
			}
		}
		if d.size() != len(ref) {
			t.Fatalf("%s size %d want %d", vr.name, d.size(), len(ref))
		}
		for k, v := range ref {
			if got, ok := d.get(k); !ok || got != v {
				t.Fatalf("%s get %q", vr.name, k)
			}
		}
		for k, v := range oldRef {
			if got, ok := old.get(k); !ok || got != v {
				t.Fatalf("%s old version changed at %q", vr.name, k)
			}
		}
		if _, ok := d.get(key(99999)); ok {
			t.Fatalf("%s found an absent key", vr.name)
		}
	}
}

var sink int
var sinkD dict

func build(mk func() dict, n int) dict {
	d := mk()
	for i := 0; i < n; i++ {
		d = d.ins(key(i), i)
	}
	return d
}

func BenchmarkSmallInsertNew(b *testing.B) {
	for _, vr := range variants {
		for _, n := range []int{1, 4, 8, 16, 64} {
			b.Run(fmt.Sprintf("%s/n=%d", vr.name, n), func(b *testing.B) {
				d := build(vr.mk, n)
				k := key(n + 1)
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					sinkD = d.ins(k, i)
				}
			})
		}
	}
}

func BenchmarkSmallInsertUpd(b *testing.B) {
	for _, vr := range variants {
		for _, n := range []int{1, 4, 8, 16, 64} {
			b.Run(fmt.Sprintf("%s/n=%d", vr.name, n), func(b *testing.B) {
				d := build(vr.mk, n)
				k := key(n / 2)
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					sinkD = d.ins(k, i)
				}
			})
		}
	}
}

func BenchmarkSmallGet(b *testing.B) {
	for _, vr := range variants {
		for _, n := range []int{1, 4, 8, 16, 64} {
			b.Run(fmt.Sprintf("%s/n=%d", vr.name, n), func(b *testing.B) {
				d := build(vr.mk, n)
				ks := make([]string, n)
				for i := range ks {
					ks[i] = key(i)
				}
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					v, _ := d.get(ks[i%n])
					sink += v
				}
			})
		}
	}
}

func BenchmarkRandInsert1e5(b *testing.B) {
	for _, vr := range variants[2:] {
		b.Run(vr.name, func(b *testing.B) {
			r := rand.New(rand.NewSource(2))
			ks := make([]string, 100000)
			for i := range ks {
				ks[i] = key(r.Int())
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				d := vr.mk()
				for j, k := range ks {
					d = d.ins(k, j)
				}
				sinkD = d
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(ks)), "ns/insert")
		})
	}
}

// The dict-counts shape: 1e6 get+insert over 1e5 keys.
func BenchmarkCounts(b *testing.B) {
	for _, vr := range variants[2:] {
		b.Run(vr.name, func(b *testing.B) {
			ks := make([]string, 100000)
			for i := range ks {
				ks[i] = key(i)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				d := vr.mk()
				for j := 1; j <= 1000000; j++ {
					k := ks[j%100000]
					v, _ := d.get(k)
					d = d.ins(k, v+1)
				}
				sinkD = d
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*1000000), "ns/update")
		})
	}
}

func BenchmarkGet1e6(b *testing.B) {
	for _, vr := range variants[2:] {
		b.Run(vr.name, func(b *testing.B) {
			d := build(vr.mk, 1000000)
			r := rand.New(rand.NewSource(3))
			ks := make([]string, 4096)
			for i := range ks {
				ks[i] = key(r.Intn(1000000))
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				v, _ := d.get(ks[i&4095])
				sink += v
			}
		})
	}
}

func BenchmarkSmallGetFlatPF(b *testing.B) {
	for _, n := range []int{1, 4, 8, 16, 32} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			var d FDict[int]
			ks := make([]string, n)
			for i := range ks {
				ks[i] = key(1000 + i)
				d = d.Insert(ks[i], i)
			}
			for i := 0; i < b.N; i++ {
				v, _ := d.GetPF(ks[i%n])
				sink += v
			}
		})
	}
}
