package btproto

import "cmp"

// v4: entries as (key, value) pairs: one slice per node, plus the node header.
// Inner node entries are (smallest key, child).

var W4 = 16

type ent4[K cmp.Ordered] struct {
	k K
	v any // leaf: the value; inner: *node4[K]
}

type node4[K cmp.Ordered] struct {
	leaf bool
	e    []ent4[K]
}

type Tree4[K cmp.Ordered] struct {
	root *node4[K]
	size int
}

func search4[K cmp.Ordered](e []ent4[K], k K) (int, bool) {
	lo, hi := 0, len(e)
	for lo < hi {
		m := int(uint(lo+hi) >> 1)
		if e[m].k < k {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo, lo < len(e) && e[lo].k == k
}

func (t *Tree4[K]) Get(k K) (any, bool) {
	n := t.root
	for n != nil {
		i, found := search4(n.e, k)
		if n.leaf {
			if !found {
				return nil, false
			}
			return n.e[i].v, true
		}
		if !found {
			i--
		}
		if i < 0 {
			return nil, false
		}
		n = n.e[i].v.(*node4[K])
	}
	return nil, false
}

func ins4[K cmp.Ordered](n *node4[K], k K, v any) (*node4[K], *node4[K], bool) {
	i, found := search4(n.e, k)
	if n.leaf {
		if found {
			e := make([]ent4[K], len(n.e))
			copy(e, n.e)
			e[i].v = v
			return &node4[K]{leaf: true, e: e}, nil, false
		}
		e := make([]ent4[K], len(n.e)+1)
		copy(e, n.e[:i])
		e[i] = ent4[K]{k, v}
		copy(e[i+1:], n.e[i:])
		if len(e) <= W4 {
			return &node4[K]{leaf: true, e: e}, nil, true
		}
		h := len(e) / 2
		return &node4[K]{leaf: true, e: e[:h:h]}, &node4[K]{leaf: true, e: e[h:]}, true
	}
	if !found {
		i--
	}
	if i < 0 {
		i = 0
	}
	c, split, isNew := ins4(n.e[i].v.(*node4[K]), k, v)
	if split == nil {
		e := make([]ent4[K], len(n.e))
		copy(e, n.e)
		e[i] = ent4[K]{c.e[0].k, c}
		return &node4[K]{e: e}, nil, isNew
	}
	e := make([]ent4[K], len(n.e)+1)
	copy(e, n.e[:i])
	e[i] = ent4[K]{c.e[0].k, c}
	e[i+1] = ent4[K]{split.e[0].k, split}
	copy(e[i+2:], n.e[i+1:])
	if len(e) <= W4 {
		return &node4[K]{e: e}, nil, isNew
	}
	h := len(e) / 2
	return &node4[K]{e: e[:h:h]}, &node4[K]{e: e[h:]}, isNew
}

func (t *Tree4[K]) Insert(k K, v any) *Tree4[K] {
	if t.root == nil {
		return &Tree4[K]{root: &node4[K]{leaf: true, e: []ent4[K]{{k, v}}}, size: 1}
	}
	c, split, isNew := ins4(t.root, k, v)
	nt := &Tree4[K]{size: t.size}
	if isNew {
		nt.size++
	}
	if split == nil {
		nt.root = c
	} else {
		nt.root = &node4[K]{e: []ent4[K]{{c.e[0].k, c}, {split.e[0].k, split}}}
	}
	return nt
}
