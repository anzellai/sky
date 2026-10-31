package rt

import (
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

func buildCons(n int) []int {
	var acc []int
	for i := n; i > 0; i-- {
		acc = List_consT(i, acc)
	}
	return acc
}

func TestConsBuildsTheRightList(t *testing.T) {
	for _, n := range []int{0, 1, 2, 30, 31, 32, 33, 64, 65, 1000, 100000} {
		xs := buildCons(n)
		if len(xs) != n {
			t.Fatalf("n=%d: len %d", n, len(xs))
		}
		for i, v := range xs {
			if v != i+1 {
				t.Fatalf("n=%d: xs[%d] = %d", n, i, v)
			}
		}
	}
}

// y :: rest then z :: rest: the second cons must copy, and the first list
// must not change.
func TestConsOntoOneTailTwiceLeavesTheFirstListAlone(t *testing.T) {
	rest := buildCons(100)
	a := List_consT(-1, rest)
	b := List_consT(-2, rest)
	if a[0] != -1 || b[0] != -2 {
		t.Fatalf("heads: a=%d b=%d", a[0], b[0])
	}
	if !slices.Equal(a[1:], rest) || !slices.Equal(b[1:], rest) {
		t.Fatal("a tail changed")
	}
	// replacing a head: case xs of y :: rest -> z :: rest
	c := List_consT(-3, a[1:])
	if a[0] != -1 || c[0] != -3 {
		t.Fatalf("head replacement wrote into a: a[0]=%d", a[0])
	}
}

// The cons that claims a slot extends the block in place; the next cons on
// the result does too (O(1)), and every older list keeps its contents.
func TestConsExtendsTheBlockInPlace(t *testing.T) {
	xs := buildCons(40)
	ys := List_consT(0, xs)
	if &ys[1] != &xs[0] {
		t.Fatal("a cons onto a block suffix copied instead of claiming the slot")
	}
	zs := List_consT(-1, ys)
	if &zs[1] != &ys[0] {
		t.Fatal("the second cons copied")
	}
	if xs[0] != 1 || ys[0] != 0 || zs[0] != -1 {
		t.Fatal("an older list changed")
	}
}

// A list shorter than 31 elements gets an exact copy and no block.
func TestShortListsAreExactCopies(t *testing.T) {
	xs := []int{2, 3, 4}
	ys := List_consT(1, xs)
	if len(ys) != 4 || cap(ys) != 4 || !slices.Equal(ys, []int{1, 2, 3, 4}) {
		t.Fatalf("short cons: %v cap %d", ys, cap(ys))
	}
}

func TestConsAfterTakeAndDropIsCorrect(t *testing.T) {
	xs := buildCons(100)
	pre := xs[:10] // List.take: len < cap
	p2 := List_consT(0, pre)
	if !slices.Equal(p2, append([]int{0}, xs[:10]...)) {
		t.Fatalf("cons onto a take: %v", p2)
	}
	if xs[10] != 11 {
		t.Fatal("cons onto a take wrote into the source")
	}
	suf := xs[50:] // List.drop: a suffix of the block
	s2 := List_consT(0, suf)
	if s2[0] != 0 || xs[49] != 50 {
		t.Fatal("cons onto a drop wrote over the slot of an older list")
	}
	empty := xs[100:] // cap 0
	if e := List_consT(7, empty); !slices.Equal(e, []int{7}) {
		t.Fatalf("cons onto an empty suffix: %v", e)
	}
}

// List () is []struct{}: zero-size elements never get a block.
func TestConsOverZeroSizeElements(t *testing.T) {
	var acc []struct{}
	for i := 0; i < 100000; i++ {
		acc = List_consT(struct{}{}, acc)
	}
	if len(acc) != 100000 {
		t.Fatalf("len %d", len(acc))
	}
	a := List_consT(struct{}{}, acc)
	b := List_consT(struct{}{}, acc)
	if len(a) != 100001 || len(b) != 100001 || len(acc) != 100000 {
		t.Fatal("zero-size cons changed a length")
	}
}

// Two goroutines cons onto ONE shared list 1e5 times each: every result is
// right and no slot is shared (run under -race).
func TestConcurrentConsOntoOneSharedList(t *testing.T) {
	shared := buildCons(64)
	var wg sync.WaitGroup
	results := make([][]int, 4)
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			acc := shared
			for i := 0; i < 100000; i++ {
				acc = List_consT(-(g*1000000 + i), acc)
			}
			results[g] = acc
		}(g)
	}
	wg.Wait()
	for g, acc := range results {
		if len(acc) != 100064 {
			t.Fatalf("goroutine %d: len %d", g, len(acc))
		}
		for i := 0; i < 100000; i++ {
			if acc[i] != -(g*1000000 + 99999 - i) {
				t.Fatalf("goroutine %d: acc[%d] = %d", g, i, acc[i])
			}
		}
		if !slices.Equal(acc[100000:], shared) {
			t.Fatalf("goroutine %d: the shared tail changed", g)
		}
	}
	for i, v := range shared {
		if v != i+1 {
			t.Fatal("the shared list changed")
		}
	}
}

// Force the block to be collected, then build new blocks that may reuse its
// memory: a stale table entry must never be taken for a live block.
func TestStaleEntriesAfterGCAreNeverUsed(t *testing.T) {
	for round := 0; round < 20; round++ {
		_ = buildCons(5000)
		runtime.GC()
		xs := buildCons(3000)
		ys := List_consT(0, xs)
		zs := List_consT(-1, xs)
		if ys[0] != 0 || zs[0] != -1 || xs[0] != 1 || !slices.Equal(ys[1:], zs[1:]) {
			t.Fatalf("round %d: a stale block was claimed", round)
		}
	}
}

func TestAppendClaimAndCopy(t *testing.T) {
	ys := buildCons(100)
	xs := []int{-3, -2, -1}
	a := listAppendBlock(xs, ys)
	if !slices.Equal(a, append(append([]int{}, xs...), ys...)) {
		t.Fatalf("append: %v", a[:5])
	}
	if &a[3] != &ys[0] {
		t.Fatal("append at the frontier copied ys instead of claiming room")
	}
	b := listAppendBlock([]int{9}, ys) // ys is no longer at the frontier
	if b[0] != 9 || a[2] != -1 || &b[1] == &ys[0] {
		t.Fatal("a second append onto ys must copy")
	}
	// The left operand is never written: append a block list onto another.
	left := buildCons(50)
	c := listAppendBlock(left, buildCons(60))
	if len(c) != 110 || c[0] != 1 || c[50] != 1 || left[0] != 1 {
		t.Fatal("append of two block lists")
	}
	if got := listAppendBlock([]int{}, ys); &got[0] != &ys[0] {
		t.Fatal("[] ++ ys must be ys")
	}
}

// A JSON-style tokeniser: cons tokens onto an accumulator, reverse once.
func TestTokeniserConsThenReverse(t *testing.T) {
	src := []byte("[1, 22, 333, 4444]")
	var toks []string
	cur := []byte{}
	for _, c := range src {
		switch c {
		case '[', ']', ',', ' ':
			if len(cur) > 0 {
				toks = List_consT(string(cur), toks)
				cur = cur[:0]
			}
			if c != ' ' {
				toks = List_consT(string(c), toks)
			}
		default:
			cur = append(cur, c)
		}
	}
	slices.Reverse(toks)
	want := []string{"[", "1", ",", "22", ",", "333", ",", "4444", "]"}
	if !slices.Equal(toks, want) {
		t.Fatalf("tokens: %q", toks)
	}
}

// ── the prototype benchmarks (design 1a: <= 50 ns per cons in the
// accumulator loop, no regression for lists under 32) ──

func plainCons[T any](x T, xs []T) []T {
	out := make([]T, len(xs)+1)
	out[0] = x
	copy(out[1:], xs)
	return out
}

func BenchmarkConsAccumulator1e6(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var acc []int
		for k := 0; k < 1000000; k++ {
			acc = List_consT(k, acc)
		}
		if len(acc) != 1000000 {
			b.Fatal(len(acc))
		}
	}
}

func BenchmarkConsAccumulatorAny1e6(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var acc []any
		for k := 0; k < 1000000; k++ {
			acc = List_consT[any](k&127, acc)
		}
	}
}

var shortTail = []int{2, 3, 4, 5, 6}

func BenchmarkConsShortNew(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = List_consT(1, shortTail)
	}
}

func BenchmarkConsShortPlainCopy(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = plainCons(1, shortTail)
	}
}

var tail20 = buildCons(20)

func BenchmarkConsLen20New(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = List_consT(1, tail20)
	}
}

func BenchmarkConsLen20PlainCopy(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = plainCons(1, tail20)
	}
}

// The FFI rule (docs/ffi/boundary-philosophy.md): Go code must treat a Sky
// list argument as read-only, because a cons-block list shares its memory
// with older lists. The runtime's own sorts obey it, and the doc states it.
func TestRuntimeNeverChangesAListArgument(t *testing.T) {
	older := buildCons(100) // 1..100, a block suffix
	desc := List_consT(1000, older)
	snapshot := slices.Clone(older)
	_ = List_sort(desc)
	_ = List_sortBy(func(x any) any { return -AsInt(x) }, desc)
	_ = List_sortWith(func(a any) any {
		return func(b any) any { return AsInt(b) - AsInt(a) }
	}, desc)
	_ = List_reverseT(desc)
	if !slices.Equal(older, snapshot) || desc[0] != 1000 {
		t.Fatal("a runtime list function changed its argument")
	}
	doc, err := os.ReadFile("../../docs/ffi/boundary-philosophy.md")
	if err != nil {
		t.Fatalf("read the FFI doc: %v", err)
	}
	if !strings.Contains(string(doc), "must treat every slice argument as\nread-only") {
		t.Fatal("docs/ffi/boundary-philosophy.md no longer states the read-only slice rule")
	}
}
