package rt

// list_block.go — O(1) amortised `x :: xs` on Go slices.
//
// A Sky `List a` is a Go slice (`[]T` typed, `[]any` erased). A slice has no
// room in front, so `x :: xs` used to copy the whole tail: O(n) per cons and
// O(n²) for the accumulator loop `go (x :: acc) rest`.
//
// THE BLOCK. When a cons has to copy a tail of 31 elements or more, it copies
// into a BLOCK: a backing array with as much free room in FRONT as the list
// holds, so the list sits at the end of the array. Every list built on the
// block by cons is a suffix of it, so for each one `len == cap` and all of
// them share the block's last slot. Each block has one shared word, its
// FRONTIER: the lowest slot any list has claimed.
//
// A cons onto a list xs that is a registered block suffix starting at slot
// `off` claims slot `off-1` with ONE compare-and-swap of the frontier from
// `off` to `off-1`, writes x there, and returns the suffix that starts at
// `off-1`. No list that exists can see that slot (each starts at or after
// `off`), so the write is invisible to every other holder: lists stay
// immutable values. A second cons onto the same xs fails the CAS (the
// frontier has moved) and copies, so two lists never share a slot.
//
// THE TABLE. A fixed table of blockSets sets, 2 ways each, of
// atomic.Pointer[blockState], indexed by a hash of the block's last-slot
// address. A state holds a WEAK pointer to slot 0, so the table never keeps a
// block alive. A hit must pass two checks: the weak pointer is still live, and
// slot 0 plus (cap-1) elements is the same last-slot address. The Go heap
// does not move objects and clears a weak pointer before it reuses the
// memory, so a live weak pointer whose block ends at xs's last slot IS xs's
// block. A stale entry (weak pointer nil) is simply overwritten. There is no
// cleanup goroutine and no unbounded map: the memory is the table, about
// 32 kB, and a lookup is one atomic load, a compare and one weak.Value; it
// allocates nothing.
//
// Only blocks of capacity 32 or more are registered. A shorter list (the
// usual Html children, Std.Ui rows, `x :: [ literal ]`) gets a plain exact
// copy with no weak pointer, so short lists keep the cost of a typed copy.
// The accumulator loop pays the copies up to 32 once, then doubles: amortised
// O(1) per cons. An eviction (a third live block hashing to one set) costs
// one O(n) copy, which registers a fresh block, so the doubling continues.
//
// ZERO-SIZE ELEMENTS. `List ()` is `[]struct{}`: every zero-size allocation
// in Go has the same address, so blocks of them would share a table key.
// They never get a block: `make([]T, n+1)` of a zero-size T allocates
// nothing and copies nothing, so the cons is O(1) anyway.
//
// COSTS THAT STAY O(n), by design (documented in Sky.Core.List):
//   - replacing a head: `case xs of y :: rest -> z :: rest` copies, because
//     y still owns the slot in front of rest;
//   - two lists consed onto one shared tail: the second cons copies;
//   - the first cons onto a list that is not a block (a literal, List.map,
//     JSON, gob, FFI, `List.take`) copies once; later conses are O(1).
//
// SPACE. An old list keeps its whole block alive, and so every element of
// every newer list on that block: a long-lived short suffix of a large
// accumulator holds the large block. That is the price of O(1) cons.
//
// ALIASING. A list is now a suffix of every newer list built on top of it.
// Go code must never change a Sky list it was given (sort it, write to it);
// the runtime copies before it sorts (List_sort), and the FFI rule says the
// same for a slice argument (docs/ffi/boundary-philosophy.md).
//
// Go's `append` onto a block list always reallocates (len == cap), so Go
// code that appends cannot write into a block either.

import (
	"sync/atomic"
	"unsafe"
	"weak"
)

const (
	// blockMinCap is the smallest block capacity that is registered.
	blockMinCap = 32
	blockSets   = 1024 // power of two
)

type blockState struct {
	slot0    weak.Pointer[byte] // slot 0 of the block (weak: never keeps it alive)
	end      uintptr            // address of the last slot: the table key
	cap      int
	elemSize uintptr
	frontier atomic.Int64 // lowest claimed slot
}

var blockTable [blockSets][2]atomic.Pointer[blockState]

func blockSet(end uintptr) *[2]atomic.Pointer[blockState] {
	h := uint64(end) * 0x9E3779B97F4A7C15
	return &blockTable[h>>(64-10)]
}

// lookupBlock returns the live registered block whose last slot is `end`.
func lookupBlock(end uintptr, size uintptr) *blockState {
	set := blockSet(end)
	for w := 0; w < 2; w++ {
		st := set[w].Load()
		if st == nil || st.end != end || st.elemSize != size {
			continue
		}
		p := st.slot0.Value()
		if p == nil {
			continue
		}
		if uintptr(unsafe.Pointer(p))+uintptr(st.cap-1)*size != end {
			continue
		}
		return st
	}
	return nil
}

// registerBlock puts a new block in its set: an empty or stale way first,
// else it takes way 0 and moves the old way 0 to way 1.
func registerBlock(st *blockState) {
	set := blockSet(st.end)
	for w := 0; w < 2; w++ {
		old := set[w].Load()
		if old == nil || old.slot0.Value() == nil {
			if set[w].CompareAndSwap(old, st) {
				return
			}
		}
	}
	if old0 := set[0].Load(); old0 != nil {
		set[1].Store(old0)
	}
	set[0].Store(st)
}

// List_consT is `x :: xs` on a typed slice: amortised O(1) (see the file
// comment), never visible to any other holder of xs.
func List_consT[T any](x T, xs []T) []T {
	var zero T
	size := unsafe.Sizeof(zero)
	n := len(xs)
	if size == 0 {
		return make([]T, n+1)
	}
	if n < blockMinCap-1 {
		// A short list: the plain exact copy, inline.
		out := make([]T, n+1)
		out[0] = x
		copy(out[1:], xs)
		return out
	}
	if n == cap(xs) {
		base := unsafe.Pointer(unsafe.SliceData(xs))
		end := uintptr(base) + uintptr(n-1)*size
		if st := lookupBlock(end, size); st != nil {
			off := st.cap - n
			if off > 0 && st.frontier.CompareAndSwap(int64(off), int64(off-1)) {
				// slot off-1 is inside the same allocation (off > 0), so
				// this is pointer arithmetic within one object.
				s := unsafe.Slice((*T)(unsafe.Add(base, -int(size))), n+1)
				s[0] = x
				return s
			}
		}
	}
	return consCopy(x, xs, size)
}

// consCopy builds x :: xs in fresh memory: an exact copy below blockMinCap,
// else a registered block with as much free room in front as the list holds.
func consCopy[T any](x T, xs []T, size uintptr) []T {
	n := len(xs) + 1
	if n < blockMinCap {
		out := make([]T, n)
		out[0] = x
		copy(out[1:], xs)
		return out
	}
	c := 2 * n
	block := make([]T, c)
	h := c - n
	block[h] = x
	copy(block[h+1:], xs)
	st := &blockState{
		slot0:    weak.Make((*byte)(unsafe.Pointer(&block[0]))),
		end:      uintptr(unsafe.Pointer(&block[c-1])),
		cap:      c,
		elemSize: size,
	}
	st.frontier.Store(int64(h))
	registerBlock(st)
	return block[h:]
}

// List_appendBlockT is `xs ++ ys` on typed slices. When ys is a registered
// block suffix at its block's frontier with room for xs, it claims len(xs)
// slots with one CAS and copies xs in front: O(|xs|). Otherwise it copies
// both into a fresh slice: O(|xs| + |ys|). The result never aliases a slot
// another list can see.
func List_appendBlockT[T any](xs, ys []T) []T {
	if len(xs) == 0 {
		if ys == nil {
			return []T{}
		}
		// [] ++ ys is ys: the same immutable list, O(1).
		return ys
	}
	var zero T
	size := unsafe.Sizeof(zero)
	m, n := len(xs), len(ys)
	if size != 0 && n >= blockMinCap-1 && n == cap(ys) {
		base := unsafe.Pointer(unsafe.SliceData(ys))
		end := uintptr(base) + uintptr(n-1)*size
		if st := lookupBlock(end, size); st != nil {
			off := st.cap - n
			if off >= m && st.frontier.CompareAndSwap(int64(off), int64(off-m)) {
				s := unsafe.Slice((*T)(unsafe.Add(base, -m*int(size))), n+m)
				copy(s, xs)
				return s
			}
		}
	}
	if size == 0 {
		return make([]T, m+n)
	}
	total := m + n
	if total < blockMinCap {
		out := make([]T, total)
		copy(out, xs)
		copy(out[m:], ys)
		return out
	}
	// A fresh block with front room, so later conses onto the result are O(1).
	c := 2 * total
	block := make([]T, c)
	h := c - total
	copy(block[h:], xs)
	copy(block[h+m:], ys)
	st := &blockState{
		slot0:    weak.Make((*byte)(unsafe.Pointer(&block[0]))),
		end:      uintptr(unsafe.Pointer(&block[c-1])),
		cap:      c,
		elemSize: size,
	}
	st.frontier.Store(int64(h))
	registerBlock(st)
	return block[h:]
}
