package rt

// array_trie.go — Sky.Core.Array: a persistent vector.
//
// REPRESENTATION. A 32-way persistent trie with a separate tail (the Clojure
// PersistentVector): the last up-to-32 elements sit in `tail`, every full
// block of 32 lives in the trie under `root`, and `shift` is 5 x (trie depth).
// Elements are stored erased (`any`). A Sky `Array a` is declared
// `type Array a = Array_OPAQUE`, the phantom opaque-handle shape (`Route`,
// `Cookie`), so emitted Go holds it as `any` (goty.rs `opaque`) and
// passes it to the erased kernels below; the value inside is a `SkyArray`,
// one pointer wide. Each kernel's result is narrowed at the call site by one
// O(1) step (doc 14 R5), never an O(n) rebuild.
//
// COSTS (n = length):
//
//	get, set:                       O(log32 n), 4 hops at 1e6
//	push:                           amortised O(1)
//	length, isEmpty:                O(1)
//	fromList, toList, toIndexedList,
//	map, indexedMap, foldl, foldr,
//	filter, initialize, repeat:     O(n)
//	append a b:                     O(length b)
//	slice:                          O(n) (no RRB tree)
//
// `get` and `set` outside 0 .. length-1 give Nothing / the array unchanged,
// as in Elm. Every value is immutable: set and push copy one path of at most
// 4 nodes of 32 slots.

import (
	"bytes"
	"encoding/gob"
)

const (
	arrBits  = 5
	arrWidth = 1 << arrBits
	arrMask  = arrWidth - 1
)

type arrNode struct {
	kids [arrWidth]any // a leaf holds elements; an inner node holds *arrNode
}

type arrayTrie struct {
	cnt   int
	shift uint
	root  *arrNode
	tail  []any
}

// SkyArray is the runtime value of a Sky `Array a`. The zero value is the
// empty array.
type SkyArray struct{ t *arrayTrie }

func arrayOf(v any) *arrayTrie {
	switch a := unwrapAny(v).(type) {
	case SkyArray:
		return a.t
	case *SkyArray:
		if a != nil {
			return a.t
		}
	}
	return nil
}

func (t *arrayTrie) length() int {
	if t == nil {
		return 0
	}
	return t.cnt
}

func (t *arrayTrie) tailOff() int {
	if t.cnt < arrWidth {
		return 0
	}
	return ((t.cnt - 1) >> arrBits) << arrBits
}

// leafFor returns the 32-slot block that holds index i (0 <= i < cnt).
func (t *arrayTrie) leafFor(i int) []any {
	if i >= t.tailOff() {
		return t.tail
	}
	n := t.root
	for level := t.shift; level > 0; level -= arrBits {
		n = n.kids[(i>>level)&arrMask].(*arrNode)
	}
	return n.kids[:]
}

func (t *arrayTrie) get(i int) (any, bool) {
	if t == nil || i < 0 || i >= t.cnt {
		return nil, false
	}
	return t.leafFor(i)[i&arrMask], true
}

func newPath(level uint, n *arrNode) *arrNode {
	if level == 0 {
		return n
	}
	ret := &arrNode{}
	ret.kids[0] = newPath(level-arrBits, n)
	return ret
}

func (t *arrayTrie) pushTail(level uint, parent *arrNode, tailNode *arrNode) *arrNode {
	ret := &arrNode{}
	if parent != nil {
		ret.kids = parent.kids
	}
	sub := ((t.cnt - 1) >> level) & arrMask
	if level == arrBits {
		ret.kids[sub] = tailNode
		return ret
	}
	var child *arrNode
	if parent != nil {
		child, _ = parent.kids[sub].(*arrNode)
	}
	if child != nil {
		ret.kids[sub] = t.pushTail(level-arrBits, child, tailNode)
	} else {
		ret.kids[sub] = newPath(level-arrBits, tailNode)
	}
	return ret
}

func (t *arrayTrie) push(x any) *arrayTrie {
	if t == nil || t.cnt == 0 {
		return &arrayTrie{cnt: 1, shift: arrBits, tail: []any{x}}
	}
	if t.cnt-t.tailOff() < arrWidth {
		tail := make([]any, len(t.tail)+1, arrWidth)
		copy(tail, t.tail)
		tail[len(t.tail)] = x
		return &arrayTrie{cnt: t.cnt + 1, shift: t.shift, root: t.root, tail: tail}
	}
	tailNode := &arrNode{}
	copy(tailNode.kids[:], t.tail)
	shift := t.shift
	var root *arrNode
	if (t.cnt >> arrBits) > (1 << t.shift) {
		root = &arrNode{}
		root.kids[0] = t.root
		root.kids[1] = newPath(t.shift, tailNode)
		shift += arrBits
	} else {
		root = t.pushTail(t.shift, t.root, tailNode)
	}
	return &arrayTrie{cnt: t.cnt + 1, shift: shift, root: root, tail: []any{x}}
}

func doAssoc(level uint, n *arrNode, i int, x any) *arrNode {
	ret := &arrNode{kids: n.kids}
	if level == 0 {
		ret.kids[i&arrMask] = x
		return ret
	}
	sub := (i >> level) & arrMask
	ret.kids[sub] = doAssoc(level-arrBits, n.kids[sub].(*arrNode), i, x)
	return ret
}

func (t *arrayTrie) set(i int, x any) *arrayTrie {
	if t == nil || i < 0 || i >= t.cnt {
		return t
	}
	if i >= t.tailOff() {
		tail := make([]any, len(t.tail), arrWidth)
		copy(tail, t.tail)
		tail[i&arrMask] = x
		return &arrayTrie{cnt: t.cnt, shift: t.shift, root: t.root, tail: tail}
	}
	return &arrayTrie{cnt: t.cnt, shift: t.shift, root: doAssoc(t.shift, t.root, i, x), tail: t.tail}
}

// each calls f on every element in order; it stops early when f says false.
func (t *arrayTrie) each(f func(i int, x any) bool) {
	if t == nil {
		return
	}
	for i := 0; i < t.cnt; i += arrWidth {
		leaf := t.leafFor(i)
		for j := 0; j < arrWidth && i+j < t.cnt; j++ {
			if !f(i+j, leaf[j]) {
				return
			}
		}
	}
}

func arrayFromSlice[T any](xs []T) *arrayTrie {
	var t *arrayTrie
	for _, x := range xs {
		t = t.push(x)
	}
	return t
}

func (t *arrayTrie) toSlice() []any {
	out := make([]any, 0, t.length())
	t.each(func(_ int, x any) bool { out = append(out, x); return true })
	return out
}

// ── erased kernels (`Ffi.kernel "Array_<name>"`) ──

func Array_empty() any { return SkyArray{} }

func Array_initialize(n any, f any) any {
	var t *arrayTrie
	for i := 0; i < AsInt(n); i++ {
		t = t.push(SkyCall(f, i))
	}
	return SkyArray{t: t}
}

func Array_repeat(n any, x any) any {
	var t *arrayTrie
	for i := 0; i < AsInt(n); i++ {
		t = t.push(x)
	}
	return SkyArray{t: t}
}

func Array_fromList(list any) any { return SkyArray{t: arrayFromSlice(AsList(list))} }

func Array_isEmpty(a any) any { return arrayOf(a).length() == 0 }

func Array_length(a any) any { return arrayOf(a).length() }

func Array_get(i any, a any) any {
	x, ok := arrayOf(a).get(AsInt(i))
	if !ok {
		return Nothing[any]()
	}
	return Just[any](x)
}

func Array_set(i any, x any, a any) any { return SkyArray{t: arrayOf(a).set(AsInt(i), x)} }

func Array_push(x any, a any) any { return SkyArray{t: arrayOf(a).push(x)} }

func Array_toList(a any) any { return arrayOf(a).toSlice() }

func Array_toIndexedList(a any) any {
	t := arrayOf(a)
	out := make([]any, 0, t.length())
	t.each(func(i int, x any) bool { out = append(out, SkyTuple2{V0: i, V1: x}); return true })
	return out
}

func Array_map(f any, a any) any {
	var out *arrayTrie
	arrayOf(a).each(func(_ int, x any) bool { out = out.push(SkyCall(f, x)); return true })
	return SkyArray{t: out}
}

func Array_indexedMap(f any, a any) any {
	var out *arrayTrie
	arrayOf(a).each(func(i int, x any) bool { out = out.push(SkyCall(f, i, x)); return true })
	return SkyArray{t: out}
}

func Array_foldl(f any, acc any, a any) any {
	arrayOf(a).each(func(_ int, x any) bool { acc = SkyCall(f, x, acc); return true })
	return acc
}

func Array_foldr(f any, acc any, a any) any {
	xs := arrayOf(a).toSlice()
	for i := len(xs) - 1; i >= 0; i-- {
		acc = SkyCall(f, xs[i], acc)
	}
	return acc
}

func Array_filter(f any, a any) any {
	var out *arrayTrie
	arrayOf(a).each(func(_ int, x any) bool {
		if AsBool(SkyCall(f, x)) {
			out = out.push(x)
		}
		return true
	})
	return SkyArray{t: out}
}

func Array_append(a any, b any) any {
	out := arrayOf(a)
	arrayOf(b).each(func(_ int, x any) bool { out = out.push(x); return true })
	return SkyArray{t: out}
}

// Array_slice is Elm's Array.slice: a negative index counts from the end,
// the range is clamped, and start >= end is empty.
func Array_slice(start any, end any, a any) any {
	t := arrayOf(a)
	n := t.length()
	st, en := AsInt(start), AsInt(end)
	if st < 0 {
		st += n
	}
	if en < 0 {
		en += n
	}
	if st < 0 {
		st = 0
	}
	if en > n {
		en = n
	}
	var out *arrayTrie
	for i := st; i < en; i++ {
		x, _ := t.get(i)
		out = out.push(x)
	}
	return SkyArray{t: out}
}

// ── equality, printing and the session wire ──

// arraysEqual compares two arrays element by element with deepEq.
func arraysEqual(a, b *arrayTrie) bool {
	if a.length() != b.length() {
		return false
	}
	eq := true
	a.each(func(i int, x any) bool {
		y, _ := b.get(i)
		eq = deepEq(x, y)
		return eq
	})
	return eq
}

// GobEncode / GobDecode keep an Array inside a Sky.Live model or a Tui
// snapshot: the elements as a gob []any.
func (a SkyArray) GobEncode() ([]byte, error) {
	var buf bytes.Buffer
	xs := a.t.toSlice()
	if err := gob.NewEncoder(&buf).Encode(&xs); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (a *SkyArray) GobDecode(b []byte) error {
	var xs []any
	if err := gob.NewDecoder(bytes.NewReader(b)).Decode(&xs); err != nil {
		return err
	}
	a.t = arrayFromSlice(xs)
	return nil
}

// A model held as `any` (a Sky.Live session, a Tui snapshot) carries an
// Array as an interface value, which gob encodes by registered name.
func init() { gob.Register(SkyArray{}) }
