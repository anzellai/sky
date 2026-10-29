package hamtproto

// compact layout: like inline, one allocation per level, but the child array
// is held by a bare pointer and its length is popcount(bm) (or n for a
// collision bucket), so a slot is 40 bytes for an int value instead of 56.

import (
	"math/bits"
	"unsafe"
)

type cslot[V any] struct {
	key  string
	val  V
	bm   uint32 // branch: child bitmap; collision bucket: entry count
	coll bool
	kids *cslot[V] // nil for a leaf
}

func (s *cslot[V]) children() []cslot[V] {
	if s.kids == nil {
		return nil
	}
	n := bits.OnesCount32(s.bm)
	if s.coll {
		n = int(s.bm)
	}
	return unsafe.Slice(s.kids, n)
}

type CDict[V any] struct {
	root cslot[V]
	n    int
	w    uint
}

func NewC[V any](w uint) CDict[V] { return CDict[V]{w: w} }

func (d CDict[V]) Len() int { return d.n }

func (d CDict[V]) Get(k string) (V, bool) {
	var zero V
	nd := &d.root
	if nd.kids == nil {
		return zero, false
	}
	h := hashKey(k)
	mask := uint64(1)<<d.w - 1
	for shift := uint(0); ; shift += d.w {
		if nd.coll {
			for _, e := range nd.children() {
				if e.key == k {
					return e.val, true
				}
			}
			return zero, false
		}
		bit := uint32(1) << ((h >> shift) & mask)
		if nd.bm&bit == 0 {
			return zero, false
		}
		s := (*cslot[V])(unsafe.Add(unsafe.Pointer(nd.kids), uintptr(bits.OnesCount32(nd.bm&(bit-1)))*unsafe.Sizeof(*nd.kids)))
		if s.kids == nil {
			if s.key == k {
				return s.val, true
			}
			return zero, false
		}
		nd = s
	}
}

func (d CDict[V]) Insert(k string, v V) CDict[V] {
	h := hashKey(k)
	nr, added := cinsert(&d.root, 0, d.w, h, k, v)
	if added {
		return CDict[V]{root: nr, n: d.n + 1, w: d.w}
	}
	return CDict[V]{root: nr, n: d.n, w: d.w}
}

func mkSlots[V any](n int) []cslot[V] { return make([]cslot[V], n) }

func cinsert[V any](nd *cslot[V], shift, w uint, h uint64, k string, v V) (cslot[V], bool) {
	kids := nd.children()
	if nd.coll {
		for i := range kids {
			if kids[i].key == k {
				ns := mkSlots[V](len(kids))
				copy(ns, kids)
				ns[i].val = v
				return cslot[V]{coll: true, bm: uint32(len(ns)), kids: &ns[0]}, false
			}
		}
		ns := mkSlots[V](len(kids) + 1)
		copy(ns, kids)
		ns[len(kids)] = cslot[V]{key: k, val: v}
		return cslot[V]{coll: true, bm: uint32(len(ns)), kids: &ns[0]}, true
	}
	mask := uint64(1)<<w - 1
	bit := uint32(1) << ((h >> shift) & mask)
	pos := bits.OnesCount32(nd.bm & (bit - 1))
	if nd.bm&bit == 0 {
		ns := mkSlots[V](len(kids) + 1)
		copy(ns, kids[:pos])
		ns[pos] = cslot[V]{key: k, val: v}
		copy(ns[pos+1:], kids[pos:])
		return cslot[V]{bm: nd.bm | bit, kids: &ns[0]}, true
	}
	ns := mkSlots[V](len(kids))
	copy(ns, kids)
	s := &kids[pos]
	added := false
	if s.kids == nil {
		if s.key == k {
			ns[pos].val = v
		} else {
			ns[pos] = cmerge(shift+w, w, s.key, s.val, hashKey(s.key), k, v, h)
			added = true
		}
	} else {
		ns[pos], added = cinsert(s, shift+w, w, h, k, v)
	}
	return cslot[V]{bm: nd.bm, kids: &ns[0]}, added
}

func cmerge[V any](shift, w uint, k1 string, v1 V, h1 uint64, k2 string, v2 V, h2 uint64) cslot[V] {
	if shift >= 64 {
		ns := []cslot[V]{{key: k1, val: v1}, {key: k2, val: v2}}
		return cslot[V]{coll: true, bm: 2, kids: &ns[0]}
	}
	mask := uint64(1)<<w - 1
	i1 := (h1 >> shift) & mask
	i2 := (h2 >> shift) & mask
	if i1 == i2 {
		ns := []cslot[V]{cmerge(shift+w, w, k1, v1, h1, k2, v2, h2)}
		return cslot[V]{bm: 1 << i1, kids: &ns[0]}
	}
	a, b := cslot[V]{key: k1, val: v1}, cslot[V]{key: k2, val: v2}
	if i1 > i2 {
		a, b = b, a
	}
	ns := []cslot[V]{a, b}
	return cslot[V]{bm: 1<<i1 | 1<<i2, kids: &ns[0]}
}
