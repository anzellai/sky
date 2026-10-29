package hamtproto

// ptr layout: a node is {bitmap, []slot}; a slot is a leaf (key, val) or a
// pointer to a child node. Path copy: two allocations per level (the node and
// its slot slice).

import (
	"hash/maphash"
	"math/bits"
)

var seed = maphash.MakeSeed()

func hashKey(k string) uint64 { return maphash.String(seed, k) }

type pslot[V any] struct {
	key string
	val V
	kid *pnode[V]
}

type pnode[V any] struct {
	bm    uint32
	slots []pslot[V]
}

type PDict[V any] struct {
	root *pnode[V]
	n    int
}

const pbits = 5
const pmask = 1<<pbits - 1

func (d PDict[V]) Len() int { return d.n }

func (d PDict[V]) Get(k string) (V, bool) {
	var zero V
	nd := d.root
	if nd == nil {
		return zero, false
	}
	h := hashKey(k)
	for shift := uint(0); ; shift += pbits {
		if shift >= 64 {
			for i := range nd.slots {
				if nd.slots[i].key == k {
					return nd.slots[i].val, true
				}
			}
			return zero, false
		}
		bit := uint32(1) << ((h >> shift) & pmask)
		if nd.bm&bit == 0 {
			return zero, false
		}
		s := &nd.slots[bits.OnesCount32(nd.bm&(bit-1))]
		if s.kid == nil {
			if s.key == k {
				return s.val, true
			}
			return zero, false
		}
		nd = s.kid
	}
}

func (d PDict[V]) Insert(k string, v V) PDict[V] {
	h := hashKey(k)
	root := d.root
	if root == nil {
		root = &pnode[V]{}
	}
	nr, added := pinsert(root, 0, h, k, v)
	if added {
		return PDict[V]{root: nr, n: d.n + 1}
	}
	return PDict[V]{root: nr, n: d.n}
}

func pinsert[V any](nd *pnode[V], shift uint, h uint64, k string, v V) (*pnode[V], bool) {
	if shift >= 64 {
		for i := range nd.slots {
			if nd.slots[i].key == k {
				ns := make([]pslot[V], len(nd.slots))
				copy(ns, nd.slots)
				ns[i].val = v
				return &pnode[V]{slots: ns}, false
			}
		}
		ns := make([]pslot[V], len(nd.slots)+1)
		copy(ns, nd.slots)
		ns[len(nd.slots)] = pslot[V]{key: k, val: v}
		return &pnode[V]{slots: ns}, true
	}
	bit := uint32(1) << ((h >> shift) & pmask)
	pos := bits.OnesCount32(nd.bm & (bit - 1))
	if nd.bm&bit == 0 {
		ns := make([]pslot[V], len(nd.slots)+1)
		copy(ns, nd.slots[:pos])
		ns[pos] = pslot[V]{key: k, val: v}
		copy(ns[pos+1:], nd.slots[pos:])
		return &pnode[V]{bm: nd.bm | bit, slots: ns}, true
	}
	s := nd.slots[pos]
	ns := make([]pslot[V], len(nd.slots))
	copy(ns, nd.slots)
	added := false
	if s.kid == nil {
		if s.key == k {
			ns[pos].val = v
		} else {
			ns[pos] = pslot[V]{kid: pmerge(shift+pbits, s.key, s.val, hashKey(s.key), k, v, h)}
			added = true
		}
	} else {
		ns[pos].kid, added = pinsert(s.kid, shift+pbits, h, k, v)
	}
	return &pnode[V]{bm: nd.bm, slots: ns}, added
}

func pmerge[V any](shift uint, k1 string, v1 V, h1 uint64, k2 string, v2 V, h2 uint64) *pnode[V] {
	if shift >= 64 {
		return &pnode[V]{slots: []pslot[V]{{key: k1, val: v1}, {key: k2, val: v2}}}
	}
	i1 := (h1 >> shift) & pmask
	i2 := (h2 >> shift) & pmask
	if i1 == i2 {
		return &pnode[V]{bm: 1 << i1, slots: []pslot[V]{{kid: pmerge(shift+pbits, k1, v1, h1, k2, v2, h2)}}}
	}
	a, b := pslot[V]{key: k1, val: v1}, pslot[V]{key: k2, val: v2}
	if i1 > i2 {
		a, b = b, a
	}
	return &pnode[V]{bm: 1<<i1 | 1<<i2, slots: []pslot[V]{a, b}}
}
