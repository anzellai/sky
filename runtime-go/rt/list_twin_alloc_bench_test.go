package rt

import "testing"

// Allocation cost of each erased list helper against its typed twin, in
// allocs/op, exactly.
//
// WHY A BENCHMARK AND NOT ONLY THE A/B. The `++` change (`List_appendT`) has an
// 18% whole-app effect and the A/B resolves it with room to spare. The unary
// change (`List_isEmptyT` / `List_lengthT`) does not: three `List.isEmpty` calls
// per rendered element cost ~3 objects each against ~120 objects per element, so
// its ceiling is ~2.5% — the same size as this harness's between-run spread on
// objects/interaction. A null from the A/B alone could not distinguish "no
// effect" from "effect below resolution". These numbers are exact and have no
// spread, so the A/B becomes a CONSISTENCY check on a derived figure rather than
// the sole evidence.
//
// The inputs are TYPED slices, because that is what a well-typed Sky program
// hands these helpers. `asList`/`AsList` fast-path a `[]any` and reflect-walk
// everything else, so benchmarking with `[]any` would measure the path the
// compiler never takes and report the twins as worthless.
//
// `any(xs)` is written at the call site rather than hoisted, because boxing the
// slice header is part of what the erased form costs and what the twin removes:
// the emitted Go really is `rt.List_isEmpty(any(nearbyChildren_10))`.

type benchAttr struct {
	K string
	V int
}

func benchSlice(n int) []benchAttr {
	xs := make([]benchAttr, n)
	for i := range xs {
		xs[i] = benchAttr{K: "k", V: i}
	}
	return xs
}

// n = 0 is the case that dominates `Std_Ui.renderNodeAs`: `pseudoEntries`,
// `animationEntries` and `nearbyChildren` are empty on almost every element, and
// the erased form pays the header box regardless.
func BenchmarkIsEmpty_erased_empty(b *testing.B) {
	xs := benchSlice(0)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		twinSinkAny = List_isEmpty(any(xs))
	}
}

func BenchmarkIsEmpty_typed_empty(b *testing.B) {
	xs := benchSlice(0)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		twinSinkBool = List_isEmptyT(xs)
	}
}

func BenchmarkIsEmpty_erased_n8(b *testing.B) {
	xs := benchSlice(8)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		twinSinkAny = List_isEmpty(any(xs))
	}
}

func BenchmarkIsEmpty_typed_n8(b *testing.B) {
	xs := benchSlice(8)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		twinSinkBool = List_isEmptyT(xs)
	}
}

func BenchmarkLength_erased_n8(b *testing.B) {
	xs := benchSlice(8)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		twinSinkAny = List_length(any(xs))
	}
}

func BenchmarkLength_typed_n8(b *testing.B) {
	xs := benchSlice(8)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		twinSinkInt = List_lengthT(xs)
	}
}

// The `++` pair, for the same reason: it converts the A/B's 18% into a
// per-evaluation cost that can be reconciled against the call frequency.
func BenchmarkConcat_erased_n8(b *testing.B) {
	xs, ys := benchSlice(8), benchSlice(8)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		twinSinkSlice = AsListT[benchAttr](Concat(any(xs), any(ys)))
	}
}

func BenchmarkConcat_typed_n8(b *testing.B) {
	xs, ys := benchSlice(8), benchSlice(8)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		twinSinkSlice = List_appendT(xs, ys)
	}
}

// The shape `renderNodeAs` actually runs: three of its five `++` concatenate a
// list that is EMPTY on almost every element onto a short one.
func BenchmarkConcat_erased_emptyLhs(b *testing.B) {
	xs, ys := benchSlice(0), benchSlice(4)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		twinSinkSlice = AsListT[benchAttr](Concat(any(xs), any(ys)))
	}
}

func BenchmarkConcat_typed_emptyLhs(b *testing.B) {
	xs, ys := benchSlice(0), benchSlice(4)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		twinSinkSlice = List_appendT(xs, ys)
	}
}

// Package-level sinks so the compiler cannot elide the calls under test.
var (
	twinSinkAny   any
	twinSinkBool  bool
	twinSinkInt   int
	twinSinkSlice []benchAttr
)
