package btproto

import "cmp"

// v3: keys typed (K), values erased; exact-length slices; leaves and inner
// nodes as distinct types behind a small interface.

var W3 = 16

type node3[K cmp.Ordered] struct {
	keys []K
	vals []any       // leaf
	kids []*node3[K] // inner
}

type Tree3[K cmp.Ordered] struct {
	root *node3[K]
	size int
}

func search3[K cmp.Ordered](keys []K, k K) (int, bool) {
	lo, hi := 0, len(keys)
	for lo < hi {
		m := int(uint(lo+hi) >> 1)
		if keys[m] < k {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo, lo < len(keys) && keys[lo] == k
}

func (t *Tree3[K]) Get(k K) (any, bool) {
	n := t.root
	for n != nil {
		i, found := search3(n.keys, k)
		if n.kids == nil {
			if !found {
				return nil, false
			}
			return n.vals[i], true
		}
		if !found {
			i--
		}
		if i < 0 {
			return nil, false
		}
		n = n.kids[i]
	}
	return nil, false
}

func insertAt[T any](s []T, i int, x T) []T {
	out := make([]T, len(s)+1)
	copy(out, s[:i])
	out[i] = x
	copy(out[i+1:], s[i:])
	return out
}

func ins3[K cmp.Ordered](n *node3[K], k K, v any) (*node3[K], *node3[K], bool) {
	i, found := search3(n.keys, k)
	if n.kids == nil {
		if found {
			vals := make([]any, len(n.vals))
			copy(vals, n.vals)
			vals[i] = v
			return &node3[K]{keys: n.keys, vals: vals}, nil, false
		}
		keys := insertAt(n.keys, i, k)
		vals := insertAt(n.vals, i, v)
		if len(keys) <= W3 {
			return &node3[K]{keys: keys, vals: vals}, nil, true
		}
		h := len(keys) / 2
		return &node3[K]{keys: keys[:h:h], vals: vals[:h:h]}, &node3[K]{keys: keys[h:], vals: vals[h:]}, true
	}
	if !found {
		i--
	}
	if i < 0 {
		i = 0
	}
	c, split, isNew := ins3(n.kids[i], k, v)
	if split == nil {
		kids := make([]*node3[K], len(n.kids))
		copy(kids, n.kids)
		kids[i] = c
		keys := n.keys
		if c.keys[0] != keys[i] {
			keys = make([]K, len(n.keys))
			copy(keys, n.keys)
			keys[i] = c.keys[0]
		}
		return &node3[K]{keys: keys, kids: kids}, nil, isNew
	}
	keys := make([]K, len(n.keys)+1)
	kids := make([]*node3[K], len(n.kids)+1)
	copy(keys, n.keys[:i])
	copy(kids, n.kids[:i])
	keys[i], kids[i] = c.keys[0], c
	keys[i+1], kids[i+1] = split.keys[0], split
	copy(keys[i+2:], n.keys[i+1:])
	copy(kids[i+2:], n.kids[i+1:])
	if len(keys) <= W3 {
		return &node3[K]{keys: keys, kids: kids}, nil, isNew
	}
	h := len(keys) / 2
	return &node3[K]{keys: keys[:h:h], kids: kids[:h:h]}, &node3[K]{keys: keys[h:], kids: kids[h:]}, isNew
}

func (t *Tree3[K]) Insert(k K, v any) *Tree3[K] {
	if t.root == nil {
		return &Tree3[K]{root: &node3[K]{keys: []K{k}, vals: []any{v}}, size: 1}
	}
	c, split, isNew := ins3(t.root, k, v)
	nt := &Tree3[K]{size: t.size}
	if isNew {
		nt.size++
	}
	if split == nil {
		nt.root = c
	} else {
		nt.root = &node3[K]{keys: []K{c.keys[0], split.keys[0]}, kids: []*node3[K]{c, split}}
	}
	return nt
}
