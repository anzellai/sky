package rt

// Kernel-level Dict cost at small and large sizes, in the shapes the compiler
// emits (see tests/perf/dict-counts' main.go):
//
//	insert: rt.AsMapT[V](rt.Dict_insert(any(k), any(v), any(d)))
//	get:    rt.Dict_getProbeT[V](any(k), d)
//	erased: rt.Dict_get(any(k), any(d)) on a dict built by the erased kernels
//
// The keys are boxed once, outside the timed loop, so the numbers are the
// kernels' own cost. docs/perf/runs/dict-hamt-20261028/ records these before
// and after the persistent Dict (gate b: no size from 1 to 64 slower than
// 1.1x the map-copy figure).

import (
	"fmt"
	"strconv"
	"testing"
)

var dictBenchSink any
var dictBenchSinkInt int

func dictBenchKeys(kind string, n int) []any {
	ks := make([]any, n)
	for i := range ks {
		if kind == "int" {
			ks[i] = any(1000 + i)
		} else {
			ks[i] = any("key-" + strconv.Itoa(i))
		}
	}
	return ks
}

var dictBenchSizes = []int{1, 4, 8, 16, 64}

func BenchmarkDictKernelInsert(b *testing.B) {
	for _, kind := range []string{"int", "string"} {
		for _, n := range dictBenchSizes {
			b.Run(fmt.Sprintf("%s/n=%d", kind, n), func(b *testing.B) {
				ks := dictBenchKeys(kind, n+1)
				d := AsMapT[int](Dict_empty())
				for i := 0; i < n; i++ {
					d = AsMapT[int](Dict_insert(ks[i], any(i), any(d)))
				}
				nk, one := ks[n], any(1)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					dictBenchSink = AsMapT[int](Dict_insert(nk, one, any(d)))
				}
			})
		}
	}
}

func BenchmarkDictKernelGet(b *testing.B) {
	for _, kind := range []string{"int", "string"} {
		for _, n := range dictBenchSizes {
			b.Run(fmt.Sprintf("%s/n=%d", kind, n), func(b *testing.B) {
				ks := dictBenchKeys(kind, n)
				d := AsMapT[int](Dict_empty())
				for i := 0; i < n; i++ {
					d = AsMapT[int](Dict_insert(ks[i], any(i), any(d)))
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					m := Dict_getProbeT[int](ks[i%n], d)
					if m.Tag == 0 {
						dictBenchSinkInt += m.JustValue
					}
				}
			})
		}
	}
}

func BenchmarkDictKernelGetErased(b *testing.B) {
	for _, kind := range []string{"int", "string"} {
		for _, n := range dictBenchSizes {
			b.Run(fmt.Sprintf("%s/n=%d", kind, n), func(b *testing.B) {
				ks := dictBenchKeys(kind, n)
				d := Dict_empty()
				for i := 0; i < n; i++ {
					d = Dict_insert(ks[i], any(i), d)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					dictBenchSink = Dict_get(ks[i%n], d)
				}
			})
		}
	}
}

// Gate (c): a lookup stays O(log n) and fast at 1e6 entries. Keys are drawn
// at random so the walk does not stay in one cache-warm corner of the Dict.
func BenchmarkDictKernelGetLarge(b *testing.B) {
	for _, n := range []int{10_000, 100_000, 1_000_000} {
		b.Run(fmt.Sprintf("int/n=%d", n), func(b *testing.B) {
			keys := make([]string, n)
			vals := make([]int, n)
			for i := range keys {
				keys[i] = encodeDictKey(i)
				vals[i] = i
			}
			d := dictBuild(keys, vals, false)
			probe := make([]int, 4096)
			for i := range probe {
				probe[i] = (i * 2654435761) % n
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m := Dict_getIntT(probe[i&4095], d)
				dictBenchSinkInt += m.JustValue
			}
		})
	}
}
