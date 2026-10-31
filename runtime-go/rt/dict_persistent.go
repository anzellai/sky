// dict_persistent.go — `Sky.Core.Dict`'s runtime value: a persistent map.
//
// A Sky `Dict k v` used to be a Go `map[string]V`, and every `insert` /
// `remove` copied the whole map: O(n) per update, so a fold that builds a
// Dict was O(n²) (perf-gate `dict-counts`: 1e4 updates in 1.2 s, 4e4 in 20 s).
// It is now `SkyDict[V]`, an immutable value with O(log n) updates that share
// every untouched part with the version they came from.
//
// The design was picked by measurement (docs/perf/runs/dict-hamt-20261028/):
//
//   - Up to dictFlatMax entries the Dict is FLAT: one array of entries,
//     scanned linearly with a last-byte prefilter. At these sizes that is
//     faster than a Go map lookup, and an insert copies at most 16 entries
//     (one allocation), where the map copy cost two to four.
//   - Above that it is a hash array mapped trie (HAMT) over the same string
//     keys the map used, 16-way (4 hash bits per level), with each child
//     array held by a bare pointer whose length is the popcount of the
//     bitmap. An update copies one root-to-leaf path: one allocation per
//     level. A 64-bit seeded hash (hash/maphash) keeps an adversary from
//     building deep collision chains; the rare full collision is a bucket.
//
// Keys are the runtime's encoded key strings (encodeDictKey), exactly as in
// the map, so key equality, D1's probe of the bare form a foreign map holds,
// and the tag-driven decode all keep working unchanged. Order is NOT a
// property of the structure: Sky's Dict is ordered (Elm semantics), and the
// ordered operations (toList / keys / values / foldl / map) decode and sort
// the entries exactly as they did over the map (dictEntries), which keeps
// their O(n log n) cost and their output byte-for-byte.
//
// `foreign` marks a Dict that was converted from a Go map this runtime did
// not build (an FFI or Std.Db `map[string]any`, a JSON object, a kernel
// result). Only such a Dict can hold a bare "10" key for a `Dict Int v`, so
// only such a Dict needs the second, bare-form probe; a Dict the kernels
// built answers a lookup with one probe.
package rt

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"hash/maphash"
	"math/bits"
	"reflect"
	"strconv"
	"strings"
	"unsafe"
)

// dictFlatMax is the largest Dict kept as a flat entry array (measured: a
// linear scan of 16 short keys beats a map lookup; at 32 it does not).
const dictFlatMax = 16

// dictBits is the number of hash bits consumed per trie level (16-way).
const dictBits = 4
const dictMask = 1<<dictBits - 1

var dictSeed = maphash.MakeSeed()

// dictSeedA / dictSeedB key the short-key hash. They are drawn from the
// process's random maphash seed, so an adversary who chooses Dict keys (a
// JSON object, form fields) cannot precompute colliding keys.
var dictSeedA, dictSeedB = maphash.String(dictSeed, "sky.dict.a") | 1, maphash.String(dictSeed, "sky.dict.b")

// dictHash hashes an encoded key. Keys of up to 16 bytes (nearly every Int,
// Char and short String key) take an inline keyed multiply-mix in the style of
// wyhash: two loads and two 64x64->128 multiplies, about half the cost of a
// maphash call, which is most of a small Dict lookup. Longer keys use maphash.
func dictHash(k string) uint64 {
	n := len(k)
	if n > 16 {
		return maphash.String(dictSeed, k)
	}
	var a, b uint64
	if n > 0 {
		// A byte view of the string (no copy); the little-endian loads
		// compile to single loads where the target allows unaligned ones.
		p := unsafe.Slice(unsafe.StringData(k), n)
		switch {
		case n >= 8:
			a = binary.LittleEndian.Uint64(p)
			b = binary.LittleEndian.Uint64(p[n-8:])
		case n >= 4:
			a = uint64(binary.LittleEndian.Uint32(p))
			b = uint64(binary.LittleEndian.Uint32(p[n-4:]))
		default:
			a = uint64(p[0])<<16 | uint64(p[n>>1])<<8 | uint64(p[n-1])
		}
	}
	hi, lo := bits.Mul64(a^dictSeedA, b^dictSeedB^uint64(n))
	hi, lo = bits.Mul64(hi^lo^dictSeedA, uint64(n)^0x9E3779B97F4A7C15^dictSeedB)
	return hi ^ lo
}

// dslot is one trie slot: a leaf (key, val; kids == nil), a branch (bm is the
// child bitmap, kids its popcount(bm) children), or a collision bucket (coll;
// bm is the entry count, kids the entries).
type dslot[V any] struct {
	key  string
	val  V
	bm   uint32
	coll bool
	kids *dslot[V]
}

func (s *dslot[V]) children() []dslot[V] {
	if s.kids == nil {
		return nil
	}
	if s.coll {
		return unsafe.Slice(s.kids, int(s.bm))
	}
	return unsafe.Slice(s.kids, bits.OnesCount32(s.bm))
}

func dslotsPtr[V any](xs []dslot[V]) *dslot[V] {
	if len(xs) == 0 {
		return nil
	}
	return &xs[0]
}

// SkyDict is the runtime value of a Sky `Dict k v` (Go type of the typed
// lowering: `rt.SkyDict[V]`). The zero value is the empty Dict.
type SkyDict[V any] struct {
	kids    *dslot[V] // flat: n entries; trie: the root's children
	n       int
	bm      uint32 // trie root bitmap
	trie    bool
	foreign bool
}

// Len is the number of entries, O(1).
func (d SkyDict[V]) Len() int { return d.n }

func (d SkyDict[V]) flat() []dslot[V] {
	if d.kids == nil {
		return nil
	}
	return unsafe.Slice(d.kids, d.n)
}

func (d SkyDict[V]) root() dslot[V] { return dslot[V]{bm: d.bm, kids: d.kids} }

// flatFind is the linear scan with a last-byte prefilter: keys of one length
// that differ usually differ at the end ("key-3" / "key-4", "\x01i1003" /
// "\x01i1004"), so most entries are rejected without a memequal call.
func flatFind[V any](es []dslot[V], k string) int {
	n := len(k)
	if n == 0 {
		for i := range es {
			if len(es[i].key) == 0 {
				return i
			}
		}
		return -1
	}
	last := k[n-1]
	for i := range es {
		e := &es[i]
		if len(e.key) == n && e.key[n-1] == last && e.key == k {
			return i
		}
	}
	return -1
}

// Lookup returns the value stored under the encoded key k.
func (d SkyDict[V]) Lookup(k string) (V, bool) {
	if !d.trie {
		es := d.flat()
		if i := flatFind(es, k); i >= 0 {
			return es[i].val, true
		}
		var zero V
		return zero, false
	}
	return d.lookupH(k, dictHash(k))
}

// lookupH is the trie walk for a key whose hash is h (a parameter so a test
// can force full 64-bit collisions).
func (d SkyDict[V]) lookupH(k string, h uint64) (V, bool) {
	var zero V
	if !d.trie {
		es := d.flat()
		if i := flatFind(es, k); i >= 0 {
			return es[i].val, true
		}
		return zero, false
	}
	bm, kids := d.bm, d.kids
	for shift := uint(0); shift < 64; shift += dictBits {
		bit := uint32(1) << ((h >> shift) & dictMask)
		if bm&bit == 0 {
			return zero, false
		}
		s := (*dslot[V])(unsafe.Add(unsafe.Pointer(kids),
			uintptr(bits.OnesCount32(bm&(bit-1)))*unsafe.Sizeof(*kids)))
		if s.kids == nil {
			if s.key == k {
				return s.val, true
			}
			return zero, false
		}
		if s.coll {
			for _, e := range unsafe.Slice(s.kids, int(s.bm)) {
				if e.key == k {
					return e.val, true
				}
			}
			return zero, false
		}
		bm, kids = s.bm, s.kids
	}
	return zero, false
}

// Has reports whether the encoded key k is present.
func (d SkyDict[V]) Has(k string) bool {
	_, ok := d.Lookup(k)
	return ok
}

// Insert returns a Dict with k bound to v; d is unchanged.
func (d SkyDict[V]) Insert(k string, v V) SkyDict[V] {
	if !d.trie {
		es := d.flat()
		if i := flatFind(es, k); i >= 0 {
			ns := make([]dslot[V], len(es))
			copy(ns, es)
			ns[i].val = v
			return SkyDict[V]{kids: &ns[0], n: d.n, foreign: d.foreign}
		}
		if len(es) < dictFlatMax {
			ns := make([]dslot[V], len(es)+1)
			copy(ns, es)
			ns[len(es)] = dslot[V]{key: k, val: v}
			return SkyDict[V]{kids: &ns[0], n: d.n + 1, foreign: d.foreign}
		}
		// Grow into a trie: build it in one pass from the flat entries and k.
		keys := make([]string, len(es)+1)
		vals := make([]V, len(es)+1)
		for i := range es {
			keys[i], vals[i] = es[i].key, es[i].val
		}
		keys[len(es)], vals[len(es)] = k, v
		return dictBuild(keys, vals, d.foreign)
	}
	return d.insertH(k, v, dictHash(k))
}

func (d SkyDict[V]) insertH(k string, v V, h uint64) SkyDict[V] {
	r := d.root()
	nr, added := dictInsertSlot(&r, 0, h, k, v)
	n := d.n
	if added {
		n++
	}
	return SkyDict[V]{kids: nr.kids, bm: nr.bm, n: n, trie: true, foreign: d.foreign}
}

func dictInsertSlot[V any](nd *dslot[V], shift uint, h uint64, k string, v V) (dslot[V], bool) {
	kids := nd.children()
	if nd.coll {
		for i := range kids {
			if kids[i].key == k {
				ns := make([]dslot[V], len(kids))
				copy(ns, kids)
				ns[i].val = v
				return dslot[V]{coll: true, bm: uint32(len(ns)), kids: &ns[0]}, false
			}
		}
		ns := make([]dslot[V], len(kids)+1)
		copy(ns, kids)
		ns[len(kids)] = dslot[V]{key: k, val: v}
		return dslot[V]{coll: true, bm: uint32(len(ns)), kids: &ns[0]}, true
	}
	bit := uint32(1) << ((h >> shift) & dictMask)
	pos := bits.OnesCount32(nd.bm & (bit - 1))
	if nd.bm&bit == 0 {
		ns := make([]dslot[V], len(kids)+1)
		copy(ns, kids[:pos])
		ns[pos] = dslot[V]{key: k, val: v}
		copy(ns[pos+1:], kids[pos:])
		return dslot[V]{bm: nd.bm | bit, kids: &ns[0]}, true
	}
	ns := make([]dslot[V], len(kids))
	copy(ns, kids)
	s := &kids[pos]
	added := false
	if s.kids == nil {
		if s.key == k {
			ns[pos].val = v
		} else {
			ns[pos] = dictMerge(shift+dictBits, s.key, s.val, dictHash(s.key), k, v, h)
			added = true
		}
	} else {
		ns[pos], added = dictInsertSlot(s, shift+dictBits, h, k, v)
	}
	return dslot[V]{bm: nd.bm, kids: &ns[0]}, added
}

func dictMerge[V any](shift uint, k1 string, v1 V, h1 uint64, k2 string, v2 V, h2 uint64) dslot[V] {
	if shift >= 64 {
		ns := []dslot[V]{{key: k1, val: v1}, {key: k2, val: v2}}
		return dslot[V]{coll: true, bm: 2, kids: &ns[0]}
	}
	i1 := (h1 >> shift) & dictMask
	i2 := (h2 >> shift) & dictMask
	if i1 == i2 {
		ns := []dslot[V]{dictMerge(shift+dictBits, k1, v1, h1, k2, v2, h2)}
		return dslot[V]{bm: 1 << i1, kids: &ns[0]}
	}
	a, b := dslot[V]{key: k1, val: v1}, dslot[V]{key: k2, val: v2}
	if i1 > i2 {
		a, b = b, a
	}
	ns := []dslot[V]{a, b}
	return dslot[V]{bm: 1<<i1 | 1<<i2, kids: &ns[0]}
}

// dictRemoved is what removing a key did to a slot.
type dictRemoved uint8

const (
	dictUnchanged dictRemoved = iota // the key was absent
	dictReplaced                     // the slot is replaced by the returned one
	dictDeleted                      // the slot is gone (its last entry left)
)

// Remove returns a Dict without k; d is unchanged.
func (d SkyDict[V]) Remove(k string) SkyDict[V] {
	if !d.trie {
		es := d.flat()
		i := flatFind(es, k)
		if i < 0 {
			return d
		}
		if len(es) == 1 {
			return SkyDict[V]{foreign: d.foreign}
		}
		ns := make([]dslot[V], 0, len(es)-1)
		ns = append(ns, es[:i]...)
		ns = append(ns, es[i+1:]...)
		return SkyDict[V]{kids: &ns[0], n: d.n - 1, foreign: d.foreign}
	}
	return d.removeH(k, dictHash(k))
}

func (d SkyDict[V]) removeH(k string, h uint64) SkyDict[V] {
	r := d.root()
	nr, how := dictRemoveSlot(&r, 0, h, k, true)
	switch how {
	case dictUnchanged:
		return d
	case dictDeleted:
		return SkyDict[V]{foreign: d.foreign}
	}
	return SkyDict[V]{kids: nr.kids, bm: nr.bm, n: d.n - 1, trie: true, foreign: d.foreign}
}

// dictRemoveSlot removes k below the branch (or bucket) nd. A non-root branch
// left with one leaf collapses into that leaf, so a trie never keeps a chain
// of single-child branches above a lone entry.
func dictRemoveSlot[V any](nd *dslot[V], shift uint, h uint64, k string, isRoot bool) (dslot[V], dictRemoved) {
	kids := nd.children()
	if nd.coll {
		for i := range kids {
			if kids[i].key != k {
				continue
			}
			if len(kids) == 2 {
				return kids[1-i], dictReplaced
			}
			ns := make([]dslot[V], 0, len(kids)-1)
			ns = append(ns, kids[:i]...)
			ns = append(ns, kids[i+1:]...)
			return dslot[V]{coll: true, bm: uint32(len(ns)), kids: &ns[0]}, dictReplaced
		}
		return dslot[V]{}, dictUnchanged
	}
	bit := uint32(1) << ((h >> shift) & dictMask)
	if nd.bm&bit == 0 {
		return dslot[V]{}, dictUnchanged
	}
	pos := bits.OnesCount32(nd.bm & (bit - 1))
	s := &kids[pos]
	var repl dslot[V]
	how := dictDeleted
	if s.kids == nil {
		if s.key != k {
			return dslot[V]{}, dictUnchanged
		}
	} else {
		repl, how = dictRemoveSlot(s, shift+dictBits, h, k, false)
		if how == dictUnchanged {
			return dslot[V]{}, dictUnchanged
		}
	}
	if how == dictDeleted {
		if len(kids) == 1 {
			return dslot[V]{}, dictDeleted
		}
		if len(kids) == 2 && !isRoot {
			if other := kids[1-pos]; other.kids == nil {
				return other, dictReplaced
			}
		}
		ns := make([]dslot[V], 0, len(kids)-1)
		ns = append(ns, kids[:pos]...)
		ns = append(ns, kids[pos+1:]...)
		return dslot[V]{bm: nd.bm &^ bit, kids: &ns[0]}, dictReplaced
	}
	// The child was replaced. A lone child that became a leaf moves up.
	if len(kids) == 1 && !isRoot && repl.kids == nil {
		return repl, dictReplaced
	}
	ns := make([]dslot[V], len(kids))
	copy(ns, kids)
	ns[pos] = repl
	return dslot[V]{bm: nd.bm, kids: &ns[0]}, dictReplaced
}

// Range calls fn for every entry, in no particular order, until fn returns
// false. The ordered operations sort what this yields (dictEntries).
func (d SkyDict[V]) Range(fn func(k string, v V) bool) {
	if !d.trie {
		for _, e := range d.flat() {
			if !fn(e.key, e.val) {
				return
			}
		}
		return
	}
	r := d.root()
	dictRangeSlot(&r, fn)
}

func dictRangeSlot[V any](s *dslot[V], fn func(string, V) bool) bool {
	kids := s.children()
	for i := range kids {
		c := &kids[i]
		if c.kids == nil {
			if !fn(c.key, c.val) {
				return false
			}
			continue
		}
		if !dictRangeSlot(c, fn) {
			return false
		}
	}
	return true
}

// dictBuild builds a Dict from distinct keys in one pass: a flat array, or a
// trie built bottom-up (one allocation per node, no path copies). The caller
// guarantees the keys are distinct.
func dictBuild[V any](keys []string, vals []V, foreign bool) SkyDict[V] {
	n := len(keys)
	if n == 0 {
		return SkyDict[V]{foreign: foreign}
	}
	if n <= dictFlatMax {
		ns := make([]dslot[V], n)
		for i := range keys {
			ns[i] = dslot[V]{key: keys[i], val: vals[i]}
		}
		return SkyDict[V]{kids: &ns[0], n: n, foreign: foreign}
	}
	hs := make([]uint64, n)
	idx := make([]int32, n)
	for i := range keys {
		hs[i] = dictHash(keys[i])
		idx[i] = int32(i)
	}
	r := dictBuildNode(keys, vals, hs, idx, 0)
	return SkyDict[V]{kids: r.kids, bm: r.bm, n: n, trie: true, foreign: foreign}
}

func dictBuildNode[V any](keys []string, vals []V, hs []uint64, idx []int32, shift uint) dslot[V] {
	if shift >= 64 {
		ns := make([]dslot[V], len(idx))
		for i, j := range idx {
			ns[i] = dslot[V]{key: keys[j], val: vals[j]}
		}
		return dslot[V]{coll: true, bm: uint32(len(ns)), kids: &ns[0]}
	}
	var counts [1 << dictBits]int
	for _, j := range idx {
		counts[(hs[j]>>shift)&dictMask]++
	}
	var bm uint32
	nk := 0
	for b, c := range counts {
		if c > 0 {
			bm |= 1 << b
			nk++
		}
	}
	// Bucket the indices by nibble (a counting sort into one scratch slice).
	var start [1 << dictBits]int
	off := 0
	for b := range counts {
		start[b] = off
		off += counts[b]
	}
	sorted := make([]int32, len(idx))
	fill := start
	for _, j := range idx {
		b := (hs[j] >> shift) & dictMask
		sorted[fill[b]] = j
		fill[b]++
	}
	ns := make([]dslot[V], nk)
	p := 0
	for b := range counts {
		c := counts[b]
		if c == 0 {
			continue
		}
		group := sorted[start[b] : start[b]+c]
		if c == 1 {
			j := group[0]
			ns[p] = dslot[V]{key: keys[j], val: vals[j]}
		} else {
			ns[p] = dictBuildNode(keys, vals, hs, group, shift+dictBits)
		}
		p++
	}
	return dslot[V]{bm: bm, kids: &ns[0]}
}

// ── The erased view ────────────────────────────────────────────────
//
// The erased kernels (`Dict_insert(key, val, dict any) any`, …) receive a
// Dict of ANY value type as `any`. skyDictAny lets them operate on it
// without knowing V and, crucially, without rebuilding it: an insert into a
// `SkyDict[int]` stays a `SkyDict[int]`, so the typed slot that receives it
// (`rt.AsMapT[int]`) takes it in O(1).

type skyDictAny interface {
	dictLen() int
	dictLookupAny(k string) (any, bool)
	dictInsertAny(k string, v any) skyDictAny
	dictRemoveAny(k string) skyDictAny
	dictRangeAny(fn func(k string, v any) bool)
	dictForeign() bool
	dictMarkForeign() skyDictAny
	dictValueType() reflect.Type
	// dictFromEntries builds a Dict of THIS value type from erased entries,
	// narrowing each value (used by AsMapT and reflect narrowing).
	dictFromEntries(keys []string, vals []any, foreign bool) (skyDictAny, error)
}

var skyDictAnyType = reflect.TypeOf((*skyDictAny)(nil)).Elem()

// isSkyDictType reports whether t is some SkyDict[V].
func isSkyDictType(t reflect.Type) bool {
	return t != nil && t.Kind() == reflect.Struct && t.Implements(skyDictAnyType)
}

func (d SkyDict[V]) dictLen() int { return d.n }

func (d SkyDict[V]) dictLookupAny(k string) (any, bool) {
	v, ok := d.Lookup(k)
	if !ok {
		return nil, false
	}
	return any(v), true
}

func (d SkyDict[V]) dictInsertAny(k string, v any) skyDictAny {
	if tv, ok := dictValueAs[V](v); ok {
		return d.Insert(k, tv)
	}
	// A value that cannot become V (only reachable through erasure): widen
	// this Dict to SkyDict[any] rather than panic; the typed slot that
	// receives the result narrows it, exactly as it narrowed a widened map.
	return dictWiden[V](d).Insert(k, v)
}

func (d SkyDict[V]) dictRemoveAny(k string) skyDictAny { return d.Remove(k) }

func (d SkyDict[V]) dictRangeAny(fn func(k string, v any) bool) {
	d.Range(func(k string, v V) bool { return fn(k, any(v)) })
}

func (d SkyDict[V]) dictForeign() bool { return d.foreign }

func (d SkyDict[V]) dictMarkForeign() skyDictAny {
	d.foreign = true
	return d
}

func (d SkyDict[V]) dictValueType() reflect.Type { return reflect.TypeOf((*V)(nil)).Elem() }

func (d SkyDict[V]) dictFromEntries(keys []string, vals []any, foreign bool) (skyDictAny, error) {
	out := make([]V, len(vals))
	for i, x := range vals {
		tv, ok := dictValueAs[V](x)
		if !ok {
			var zero V
			return nil, fmt.Errorf("rt.Coerce: expected a string-keyed Dict of %T (value of key %s), got %T",
				zero, strconv.Quote(keys[i]), x)
		}
		out[i] = tv
	}
	return dictBuild(keys, out, foreign), nil
}

// dictValueAs narrows an erased value to V: a direct assertion first, then
// the same reflect narrowing AsMapT applied to a map's values.
func dictValueAs[V any](v any) (V, bool) {
	if tv, ok := v.(V); ok {
		return tv, true
	}
	var zero V
	if v == nil {
		return zero, true
	}
	vt := reflect.TypeOf((*V)(nil)).Elem()
	if vt.Kind() == reflect.String {
		// Mixed-type SQL / Firestore rows into a `Dict String String`
		// (AsMapT's historical widening).
		return reflect.ValueOf(fmt.Sprintf("%v", v)).Convert(vt).Interface().(V), true
	}
	narrowed := narrowReflectValue(reflect.ValueOf(v), vt)
	if narrowed.IsValid() {
		if tv, ok := narrowed.Interface().(V); ok {
			return tv, true
		}
	}
	return zero, false
}

func dictWiden[V any](d SkyDict[V]) SkyDict[any] {
	keys := make([]string, 0, d.n)
	vals := make([]any, 0, d.n)
	d.Range(func(k string, v V) bool {
		keys = append(keys, k)
		vals = append(vals, any(v))
		return true
	})
	return dictBuild(keys, vals, d.foreign)
}

// asSkyDict views any Dict-shaped runtime value as a skyDictAny: a SkyDict
// of any value type (O(1)), a Go string-keyed map from FFI / Std.Db / JSON /
// an older kernel (converted once, marked foreign), a Result / Maybe
// wrapping one (the historical AsDict unwrap). Anything else is the empty
// Dict, as AsDict made it.
func asSkyDict(v any) skyDictAny {
	if d, ok := v.(skyDictAny); ok {
		return d
	}
	if v == nil {
		return SkyDict[any]{}
	}
	if m, ok := v.(map[string]any); ok {
		return dictFromAnyMap(m)
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Map && rv.Type().Key().Kind() == reflect.String {
		return dictFromReflectMap(rv)
	}
	if rv.Kind() == reflect.Pointer && !rv.IsNil() {
		return asSkyDict(rv.Elem().Interface())
	}
	if isSkyContainer(v) {
		if u := unwrapAny(v); u != nil {
			if d, ok := u.(skyDictAny); ok {
				return d
			}
			return asSkyDict(u)
		}
	}
	return SkyDict[any]{}
}

// DictFromMap is the boundary conversion a kernel or an FFI wrapper uses to
// hand a Go map to Sky as a Dict.
func DictFromMap(v any) any { return asSkyDict(v) }

func dictFromAnyMap(m map[string]any) SkyDict[any] {
	keys := make([]string, 0, len(m))
	vals := make([]any, 0, len(m))
	for k, x := range m {
		keys = append(keys, k)
		vals = append(vals, x)
	}
	return dictBuild(keys, vals, true)
}

// dictFromReflectMap keeps the map's own value type when it can: a
// `map[string]string` becomes a `SkyDict[string]`, so a typed slot of that
// type takes it without another copy.
func dictFromReflectMap(rv reflect.Value) skyDictAny {
	switch m := rv.Interface().(type) {
	case map[string]string:
		return dictFromTypedMap(m)
	case map[string]int:
		return dictFromTypedMap(m)
	case map[string]bool:
		return dictFromTypedMap(m)
	case map[string]float64:
		return dictFromTypedMap(m)
	}
	keys := make([]string, 0, rv.Len())
	vals := make([]any, 0, rv.Len())
	it := rv.MapRange()
	for it.Next() {
		keys = append(keys, it.Key().String())
		vals = append(vals, it.Value().Interface())
	}
	return dictBuild(keys, vals, true)
}

func dictFromTypedMap[V any](m map[string]V) SkyDict[V] {
	keys := make([]string, 0, len(m))
	vals := make([]V, 0, len(m))
	for k, x := range m {
		keys = append(keys, k)
		vals = append(vals, x)
	}
	return dictBuild(keys, vals, true)
}

// dictToMapAny materialises a Dict as a fresh `map[string]any` keyed by the
// encoded keys: the shape every kernel that hands a Dict to Go (Std.Db rows,
// HTTP headers, JSON objects, FFI parameters) already consumes.
func dictToMapAny(d skyDictAny) map[string]any {
	out := make(map[string]any, d.dictLen())
	d.dictRangeAny(func(k string, v any) bool {
		out[k] = v
		return true
	})
	return out
}

// DictToMap is the exported form of dictToMapAny for a value that may be a
// Dict or an already-plain map (nil for anything else).
func DictToMap(v any) map[string]any {
	if d, ok := v.(skyDictAny); ok {
		return dictToMapAny(d)
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

// dictProbe is the lookup counterpart of encodeDictKey over a Dict: the key
// under which `key` is stored. A Dict the kernels built holds only encoded
// keys, so it answers with one probe; a foreign one (converted from a Go map)
// may hold the bare `%v` form of a non-String key ("10" for a `Dict Int v`),
// which is tried second, exactly as dictProbeKey did over the map.
func dictProbe(d skyDictAny, key any) string {
	enc := encodeDictKey(key)
	if len(enc) < 2 || enc[0] != dictKeyTagByte || !d.dictForeign() {
		return enc
	}
	if _, ok := d.dictLookupAny(enc); ok {
		return enc
	}
	plain := fmt.Sprintf("%v", derefPointer(unwrapAny(key)))
	if _, ok := d.dictLookupAny(plain); ok {
		return plain
	}
	return enc
}

func dictProbeT[V any](d SkyDict[V], key any) string {
	enc := encodeDictKey(key)
	if len(enc) < 2 || enc[0] != dictKeyTagByte || !d.foreign {
		return enc
	}
	if d.Has(enc) {
		return enc
	}
	plain := fmt.Sprintf("%v", derefPointer(unwrapAny(key)))
	if d.Has(plain) {
		return plain
	}
	return enc
}

// narrowToSkyDict converts a Dict-shaped reflect value to the SkyDict type
// `target` (a reflect-driven slot: a record field, a list element, a Maybe
// payload). Invalid when src is not Dict-shaped or a value cannot narrow.
func narrowToSkyDict(src reflect.Value, target reflect.Type) reflect.Value {
	if !src.IsValid() || !src.CanInterface() {
		return reflect.Value{}
	}
	sv := src.Interface()
	if _, ok := sv.(skyDictAny); ok && reflect.TypeOf(sv) == target {
		return src
	}
	var d skyDictAny
	switch {
	case src.Kind() == reflect.Map && src.Type().Key().Kind() == reflect.String:
		d = asSkyDict(sv)
	default:
		sd, ok := sv.(skyDictAny)
		if !ok {
			return reflect.Value{}
		}
		d = sd
	}
	proto := reflect.Zero(target).Interface().(skyDictAny)
	keys := make([]string, 0, d.dictLen())
	vals := make([]any, 0, d.dictLen())
	d.dictRangeAny(func(k string, v any) bool {
		keys = append(keys, k)
		vals = append(vals, v)
		return true
	})
	out, err := proto.dictFromEntries(keys, vals, d.dictForeign())
	if err != nil {
		return reflect.Value{}
	}
	return reflect.ValueOf(out)
}

// ── gob and JSON ───────────────────────────────────────────────────

// dictWire is a Dict's serialised form: parallel key and value lists.
type dictWire[V any] struct {
	K []string
	V []V
	F bool
}

// GobEncode keeps a Dict inside a Sky.Live model, a Tui snapshot or a
// durable snapshot. The format is new in v0.29.0: a session written by an
// older binary held a Go map here and fails to decode, which the session
// store treats as an expired session (docs/migration/v0.29.md).
func (d SkyDict[V]) GobEncode() ([]byte, error) {
	w := dictWire[V]{K: make([]string, 0, d.n), V: make([]V, 0, d.n), F: d.foreign}
	d.Range(func(k string, v V) bool {
		w.K = append(w.K, k)
		w.V = append(w.V, v)
		return true
	})
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(&w); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (d *SkyDict[V]) GobDecode(b []byte) error {
	var w dictWire[V]
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&w); err != nil {
		return err
	}
	if len(w.K) != len(w.V) {
		return fmt.Errorf("rt.SkyDict: %d keys and %d values", len(w.K), len(w.V))
	}
	*d = dictBuild(dictDedupe(w.K, w.V))
	d.foreign = w.F
	return nil
}

// dictDedupe keeps the LAST value of each repeated key (Dict.fromList's
// rule), so dictBuild's distinct-keys precondition holds for any input.
func dictDedupe[V any](keys []string, vals []V) ([]string, []V, bool) {
	seen := make(map[string]int, len(keys))
	ok := true
	for i, k := range keys {
		if _, dup := seen[k]; dup {
			ok = false
		}
		seen[k] = i
	}
	if ok {
		return keys, vals, false
	}
	ks := make([]string, 0, len(seen))
	vs := make([]V, 0, len(seen))
	for i, k := range keys {
		if seen[k] == i {
			ks = append(ks, k)
			vs = append(vs, vals[i])
		}
	}
	return ks, vs, false
}

// MarshalJSON writes a Dict as the JSON object a Go map of the same keys
// wrote, so a Dict crossing a JSON wire keeps its exact prior bytes.
func (d SkyDict[V]) MarshalJSON() ([]byte, error) {
	m := make(map[string]V, d.n)
	d.Range(func(k string, v V) bool {
		m[k] = v
		return true
	})
	return json.Marshal(m)
}

func (d *SkyDict[V]) UnmarshalJSON(b []byte) error {
	var m map[string]V
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	*d = dictFromTypedMap(m)
	return nil
}

// A model held as `any` carries a Dict as an interface value, which gob
// encodes by registered name; the erased instantiation is the one the
// kernels produce.
func init() {
	gob.Register(SkyDict[any]{})
	gob.Register(SkyDict[string]{})
	gob.Register(SkyDict[int]{})
	gob.Register(SkyDict[bool]{})
	gob.Register(SkyDict[float64]{})
}

// dictsEqual compares two Dicts by content (Sky `==`).
func dictsEqual(a, b skyDictAny) bool {
	if a.dictLen() != b.dictLen() {
		return false
	}
	eq := true
	a.dictRangeAny(func(k string, v any) bool {
		w, ok := b.dictLookupAny(k)
		eq = ok && deepEq(v, w)
		return eq
	})
	return eq
}

// reflectDictOf reads the Dict behind a reflect value: a SkyDict of any value
// type, or a SkySet's store. A value read out of an unexported field (which
// cannot Interface()) is read through its address when it has one.
func reflectDictOf(v reflect.Value) (skyDictAny, bool) {
	if !v.IsValid() || v.Kind() != reflect.Struct {
		return nil, false
	}
	t := v.Type()
	if t == skySetType {
		s, _ := skySetValue(v)
		return s.items, true
	}
	if !isSkyDictType(t) {
		return nil, false
	}
	if v.CanInterface() {
		return v.Interface().(skyDictAny), true
	}
	if v.CanAddr() {
		return reflect.NewAt(t, unsafe.Pointer(v.UnsafeAddr())).Elem().Interface().(skyDictAny), true
	}
	return SkyDict[any]{}, true
}

// ── Typed twins with a typed key ───────────────────────────────────
//
// lower.rs routes `Dict.get` / `member` / `insert` / `remove` over a typed
// `SkyDict[V]` here when the key's Go type is `int` (a Sky `Int`) or `string`
// (a Sky `String`): no key or Dict boxing, and an Int key is encoded into a
// stack buffer, so a lookup allocates nothing and an update allocates only the
// path it copies (plus the key, when it is new).

// dictIntKeyBuf encodes an Int key ("\x01i" + decimal) into buf and returns
// a string that ALIASES buf: valid only while buf is, and never stored.
func dictIntKeyBuf(buf *[24]byte, n int) string {
	b := append(buf[:0], dictKeyTagByte, 'i')
	b = strconv.AppendInt(b, int64(n), 10)
	return unsafe.String(&b[0], len(b))
}

// dictIntProbe is dictProbeT for an Int key held in buf. A foreign Dict may
// hold the bare decimal form; that form is returned as a fresh string.
func dictIntProbe[V any](d SkyDict[V], buf *[24]byte, n int) string {
	enc := dictIntKeyBuf(buf, n)
	if !d.foreign || d.Has(enc) {
		return enc
	}
	if plain := strconv.Itoa(n); d.Has(plain) {
		return plain
	}
	return enc
}

func Dict_getIntT[V any](k int, d SkyDict[V]) SkyMaybe[V] {
	var buf [24]byte
	if v, ok := d.Lookup(dictIntProbe(d, &buf, k)); ok {
		return Just[V](v)
	}
	return Nothing[V]()
}

func Dict_memberIntT[V any](k int, d SkyDict[V]) bool {
	var buf [24]byte
	return d.Has(dictIntProbe(d, &buf, k))
}

func Dict_insertIntT[V any](k int, v V, d SkyDict[V]) SkyDict[V] {
	var buf [24]byte
	key := dictIntProbe(d, &buf, k)
	if d.Has(key) {
		// The stored key string is kept; the aliasing one is never stored.
		return d.Insert(key, v)
	}
	return d.Insert(strings.Clone(key), v)
}

func Dict_removeIntT[V any](k int, d SkyDict[V]) SkyDict[V] {
	var buf [24]byte
	return d.Remove(dictIntProbe(d, &buf, k))
}

// A String key is stored verbatim unless it starts with the tag byte
// (encodeDictKey's escape), so these encode only in that rare case.
func dictStrKey(k string) string {
	if len(k) > 0 && k[0] == dictKeyTagByte {
		return encodeDictKey(k)
	}
	return k
}

func Dict_getStrT[V any](k string, d SkyDict[V]) SkyMaybe[V] {
	if v, ok := d.Lookup(dictStrKey(k)); ok {
		return Just[V](v)
	}
	return Nothing[V]()
}

func Dict_memberStrT[V any](k string, d SkyDict[V]) bool {
	return d.Has(dictStrKey(k))
}

func Dict_insertStrT[V any](k string, v V, d SkyDict[V]) SkyDict[V] {
	return d.Insert(dictStrKey(k), v)
}

func Dict_removeStrT[V any](k string, d SkyDict[V]) SkyDict[V] {
	return d.Remove(dictStrKey(k))
}

// Dict_insertProbeT / Dict_removeProbeT: an update over a typed Dict whose key
// the compiler could not type (it stays erased for the probe).
func Dict_insertProbeT[V any](key any, v V, d SkyDict[V]) SkyDict[V] {
	return d.Insert(dictProbeT(d, key), v)
}

func Dict_removeProbeT[V any](key any, d SkyDict[V]) SkyDict[V] {
	return d.Remove(dictProbeT(d, key))
}

// dictFieldAsMap reads a record field that may hold a Dict as the Go map it
// stands for, so the runtime's field-by-field converters (a Sky `Response`
// into rt.SkyResponse, a `Request` into rt.SkyRequest) keep their map logic.
// Any other field is returned unchanged.
func dictFieldAsMap(f reflect.Value) reflect.Value {
	if f.IsValid() && f.Type() != skySetType {
		if d, ok := reflectDictOf(f); ok {
			return reflect.ValueOf(dictToMapAny(d))
		}
	}
	return f
}
