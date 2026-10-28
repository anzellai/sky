package btproto

// v2: one allocation per node (fixed arrays), int fast-path comparator.

const W2 = 8

type leaf2 struct {
	n    int
	keys [W2]any
	vals [W2]any
}

type inner2 struct {
	n    int
	keys [W2]any // keys[i] = smallest key under kids[i]
	kids [W2]any // *leaf2 or *inner2
}

type Tree2 struct {
	root any
	size int
}

func cmp2(a, b any) int {
	if x, ok := a.(int); ok {
		y := b.(int)
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
		return 0
	}
	return IntCmp(a, b)
}

func search2(keys []any, k any) (int, bool) {
	lo, hi := 0, len(keys)
	for lo < hi {
		m := int(uint(lo+hi) >> 1)
		if cmp2(keys[m], k) < 0 {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo, lo < len(keys) && cmp2(keys[lo], k) == 0
}

func (t *Tree2) Get(k any) (any, bool) {
	n := t.root
	for n != nil {
		switch x := n.(type) {
		case *inner2:
			i, found := search2(x.keys[:x.n], k)
			if !found {
				i--
			}
			if i < 0 {
				return nil, false
			}
			n = x.kids[i]
		case *leaf2:
			i, found := search2(x.keys[:x.n], k)
			if !found {
				return nil, false
			}
			return x.vals[i], true
		}
	}
	return nil, false
}

func ins2(n any, k, v any) (any, any, bool) {
	switch x := n.(type) {
	case *leaf2:
		i, found := search2(x.keys[:x.n], k)
		if found {
			c := *x
			c.vals[i] = v
			return &c, nil, false
		}
		if x.n < W2 {
			c := &leaf2{n: x.n + 1}
			copy(c.keys[:], x.keys[:i])
			copy(c.vals[:], x.vals[:i])
			c.keys[i], c.vals[i] = k, v
			copy(c.keys[i+1:], x.keys[i:x.n])
			copy(c.vals[i+1:], x.vals[i:x.n])
			return c, nil, true
		}
		// split: W2+1 entries into two leaves
		var ks [W2 + 1]any
		var vs [W2 + 1]any
		copy(ks[:], x.keys[:i])
		copy(vs[:], x.vals[:i])
		ks[i], vs[i] = k, v
		copy(ks[i+1:], x.keys[i:])
		copy(vs[i+1:], x.vals[i:])
		h := (W2 + 1) / 2
		l := &leaf2{n: h}
		r := &leaf2{n: W2 + 1 - h}
		copy(l.keys[:], ks[:h])
		copy(l.vals[:], vs[:h])
		copy(r.keys[:], ks[h:])
		copy(r.vals[:], vs[h:])
		return l, r, true
	case *inner2:
		i, found := search2(x.keys[:x.n], k)
		if !found {
			i--
		}
		if i < 0 {
			i = 0
		}
		c, split, isNew := ins2(x.kids[i], k, v)
		if split == nil {
			nc := *x
			nc.kids[i] = c
			if i == 0 && cmp2(k, x.keys[0]) < 0 {
				nc.keys[0] = k
			}
			return &nc, nil, isNew
		}
		sk := firstKey(split)
		if x.n < W2 {
			nc := &inner2{n: x.n + 1}
			copy(nc.keys[:], x.keys[:i+1])
			copy(nc.kids[:], x.kids[:i+1])
			nc.kids[i] = c
			nc.keys[i] = firstKey(c)
			nc.keys[i+1], nc.kids[i+1] = sk, split
			copy(nc.keys[i+2:], x.keys[i+1:x.n])
			copy(nc.kids[i+2:], x.kids[i+1:x.n])
			return nc, nil, isNew
		}
		var ks [W2 + 1]any
		var cs [W2 + 1]any
		copy(ks[:], x.keys[:i+1])
		copy(cs[:], x.kids[:i+1])
		cs[i] = c
		ks[i] = firstKey(c)
		ks[i+1], cs[i+1] = sk, split
		copy(ks[i+2:], x.keys[i+1:])
		copy(cs[i+2:], x.kids[i+1:])
		h := (W2 + 1) / 2
		l := &inner2{n: h}
		r := &inner2{n: W2 + 1 - h}
		copy(l.keys[:], ks[:h])
		copy(l.kids[:], cs[:h])
		copy(r.keys[:], ks[h:])
		copy(r.kids[:], cs[h:])
		return l, r, isNew
	}
	panic("bad node")
}

func firstKey(n any) any {
	switch x := n.(type) {
	case *leaf2:
		return x.keys[0]
	case *inner2:
		return x.keys[0]
	}
	return nil
}

func (t *Tree2) Insert(k, v any) *Tree2 {
	if t.root == nil {
		l := &leaf2{n: 1}
		l.keys[0], l.vals[0] = k, v
		return &Tree2{root: l, size: 1}
	}
	c, split, isNew := ins2(t.root, k, v)
	nt := &Tree2{size: t.size}
	if isNew {
		nt.size++
	}
	if split == nil {
		nt.root = c
	} else {
		r := &inner2{n: 2}
		r.keys[0], r.kids[0] = firstKey(c), c
		r.keys[1], r.kids[1] = firstKey(split), split
		nt.root = r
	}
	return nt
}
