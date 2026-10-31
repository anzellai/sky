package btproto

// A persistent B+-tree prototype with erased keys and values and a comparator
// fixed per tree. W is the node width (max entries per node).

type cmpFn func(a, b any) int

type node struct {
	leaf     bool
	n        int
	keys     []any   // leaf: keys; internal: keys[i] = min key of children[i]
	vals     []any   // leaf only
	children []*node // internal only
}

type Tree struct {
	root *node
	size int
	cmp  cmpFn
	w    int
}

func New(w int, cmp cmpFn) *Tree { return &Tree{w: w, cmp: cmp} }

func (t *Tree) search(n *node, k any) (int, bool) {
	lo, hi := 0, n.n
	for lo < hi {
		m := int(uint(lo+hi) >> 1)
		if t.cmp(n.keys[m], k) < 0 {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo, lo < n.n && t.cmp(n.keys[lo], k) == 0
}

func (t *Tree) Get(k any) (any, bool) {
	n := t.root
	if n == nil {
		return nil, false
	}
	for !n.leaf {
		i, found := t.search(n, k)
		if !found {
			i--
		}
		if i < 0 {
			return nil, false
		}
		n = n.children[i]
	}
	i, found := t.search(n, k)
	if !found {
		return nil, false
	}
	return n.vals[i], true
}

// insert returns the new node, an optional split right sibling, and whether
// the key was new.
func (t *Tree) ins(n *node, k, v any) (*node, *node, bool) {
	if n.leaf {
		i, found := t.search(n, k)
		if found {
			c := &node{leaf: true, n: n.n, keys: n.keys, vals: make([]any, n.n, n.n)}
			copy(c.vals, n.vals[:n.n])
			c.vals[i] = v
			return c, nil, false
		}
		m := n.n + 1
		keys := make([]any, m)
		vals := make([]any, m)
		copy(keys, n.keys[:i])
		copy(vals, n.vals[:i])
		keys[i], vals[i] = k, v
		copy(keys[i+1:], n.keys[i:n.n])
		copy(vals[i+1:], n.vals[i:n.n])
		if m <= t.w {
			return &node{leaf: true, n: m, keys: keys, vals: vals}, nil, true
		}
		h := m / 2
		l := &node{leaf: true, n: h, keys: keys[:h:h], vals: vals[:h:h]}
		r := &node{leaf: true, n: m - h, keys: keys[h:], vals: vals[h:]}
		return l, r, true
	}
	i, found := t.search(n, k)
	if !found {
		i--
	}
	if i < 0 {
		i = 0
	}
	c, split, isNew := t.ins(n.children[i], k, v)
	if split == nil {
		nc := &node{n: n.n, keys: n.keys, children: make([]*node, n.n)}
		copy(nc.children, n.children[:n.n])
		nc.children[i] = c
		if i == 0 && t.cmp(k, n.keys[0]) < 0 {
			nk := make([]any, n.n)
			copy(nk, n.keys[:n.n])
			nk[0] = k
			nc.keys = nk
		}
		return nc, nil, isNew
	}
	m := n.n + 1
	keys := make([]any, m)
	ch := make([]*node, m)
	copy(keys, n.keys[:i])
	copy(ch, n.children[:i])
	keys[i] = c.keys[0]
	ch[i] = c
	keys[i+1] = split.keys[0]
	ch[i+1] = split
	copy(keys[i+2:], n.keys[i+1:n.n])
	copy(ch[i+2:], n.children[i+1:n.n])
	if m <= t.w {
		return &node{n: m, keys: keys, children: ch}, nil, isNew
	}
	h := m / 2
	return &node{n: h, keys: keys[:h:h], children: ch[:h:h]}, &node{n: m - h, keys: keys[h:], children: ch[h:]}, isNew
}

func (t *Tree) Insert(k, v any) *Tree {
	if t.root == nil {
		return &Tree{w: t.w, cmp: t.cmp, size: 1, root: &node{leaf: true, n: 1, keys: []any{k}, vals: []any{v}}}
	}
	c, split, isNew := t.ins(t.root, k, v)
	nt := &Tree{w: t.w, cmp: t.cmp, size: t.size}
	if isNew {
		nt.size++
	}
	if split == nil {
		nt.root = c
	} else {
		nt.root = &node{n: 2, keys: []any{c.keys[0], split.keys[0]}, children: []*node{c, split}}
	}
	return nt
}

func IntCmp(a, b any) int {
	x, y := a.(int), b.(int)
	if x < y {
		return -1
	}
	if x > y {
		return 1
	}
	return 0
}
