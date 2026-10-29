package hamtproto

// inline layout: a child node is stored INSIDE its parent's slot as
// (bitmap, []slot), so a path copy is one allocation per level (the new slot
// slice). A slot with kids == nil is a leaf. Width is a parameter (bits per
// level) so the same code measures 16- and 32-way nodes.

import "math/bits"

type islot[V any] struct {
	key  string
	val  V
	bm   uint32
	coll bool // a collision bucket (all 64 hash bits used)
	kids []islot[V]
}

type IDict[V any] struct {
	root islot[V]
	n    int
	w    uint // bits per level
}

func NewI[V any](w uint) IDict[V] { return IDict[V]{w: w} }

func (d IDict[V]) Len() int { return d.n }

func (d IDict[V]) Get(k string) (V, bool) {
	var zero V
	nd := &d.root
	if nd.kids == nil {
		return zero, false
	}
	h := hashKey(k)
	mask := uint64(1)<<d.w - 1
	for shift := uint(0); ; shift += d.w {
		if nd.coll {
			for i := range nd.kids {
				if nd.kids[i].key == k {
					return nd.kids[i].val, true
				}
			}
			return zero, false
		}
		bit := uint32(1) << ((h >> shift) & mask)
		if nd.bm&bit == 0 {
			return zero, false
		}
		s := &nd.kids[bits.OnesCount32(nd.bm&(bit-1))]
		if s.kids == nil {
			if s.key == k {
				return s.val, true
			}
			return zero, false
		}
		nd = s
	}
}

func (d IDict[V]) Insert(k string, v V) IDict[V] {
	h := hashKey(k)
	nr, added := iinsert(d.root, 0, d.w, h, k, v)
	if added {
		return IDict[V]{root: nr, n: d.n + 1, w: d.w}
	}
	return IDict[V]{root: nr, n: d.n, w: d.w}
}

func iinsert[V any](nd islot[V], shift, w uint, h uint64, k string, v V) (islot[V], bool) {
	if nd.coll {
		for i := range nd.kids {
			if nd.kids[i].key == k {
				ns := make([]islot[V], len(nd.kids))
				copy(ns, nd.kids)
				ns[i].val = v
				return islot[V]{coll: true, kids: ns}, false
			}
		}
		ns := make([]islot[V], len(nd.kids)+1)
		copy(ns, nd.kids)
		ns[len(nd.kids)] = islot[V]{key: k, val: v}
		return islot[V]{coll: true, kids: ns}, true
	}
	mask := uint64(1)<<w - 1
	bit := uint32(1) << ((h >> shift) & mask)
	pos := bits.OnesCount32(nd.bm & (bit - 1))
	if nd.bm&bit == 0 {
		ns := make([]islot[V], len(nd.kids)+1)
		copy(ns, nd.kids[:pos])
		ns[pos] = islot[V]{key: k, val: v}
		copy(ns[pos+1:], nd.kids[pos:])
		return islot[V]{bm: nd.bm | bit, kids: ns}, true
	}
	s := nd.kids[pos]
	ns := make([]islot[V], len(nd.kids))
	copy(ns, nd.kids)
	added := false
	if s.kids == nil {
		if s.key == k {
			ns[pos].val = v
		} else {
			ns[pos] = imerge(shift+w, w, s.key, s.val, hashKey(s.key), k, v, h)
			added = true
		}
	} else {
		ns[pos], added = iinsert(s, shift+w, w, h, k, v)
	}
	return islot[V]{bm: nd.bm, kids: ns}, added
}

func imerge[V any](shift, w uint, k1 string, v1 V, h1 uint64, k2 string, v2 V, h2 uint64) islot[V] {
	if shift >= 64 {
		return islot[V]{coll: true, kids: []islot[V]{{key: k1, val: v1}, {key: k2, val: v2}}}
	}
	mask := uint64(1)<<w - 1
	i1 := (h1 >> shift) & mask
	i2 := (h2 >> shift) & mask
	if i1 == i2 {
		return islot[V]{bm: 1 << i1, kids: []islot[V]{imerge(shift+w, w, k1, v1, h1, k2, v2, h2)}}
	}
	a, b := islot[V]{key: k1, val: v1}, islot[V]{key: k2, val: v2}
	if i1 > i2 {
		a, b = b, a
	}
	return islot[V]{bm: 1<<i1 | 1<<i2, kids: []islot[V]{a, b}}
}
