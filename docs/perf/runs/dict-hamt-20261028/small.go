package hamtproto

// The two small-dict shapes: today's map copy (the baseline every ratio is
// against) and a flat entry slice with a linear scan.

type MapDict[V any] map[string]V

func (m MapDict[V]) Insert(k string, v V) MapDict[V] {
	out := make(map[string]V, len(m)+1)
	for kk, vv := range m {
		out[kk] = vv
	}
	out[k] = v
	return out
}

func (m MapDict[V]) Get(k string) (V, bool) { v, ok := m[k]; return v, ok }

type fent[V any] struct {
	key string
	val V
}

type FDict[V any] []fent[V]

func (f FDict[V]) Get(k string) (V, bool) {
	for i := range f {
		if f[i].key == k {
			return f[i].val, true
		}
	}
	var zero V
	return zero, false
}

func (f FDict[V]) Insert(k string, v V) FDict[V] {
	for i := range f {
		if f[i].key == k {
			out := make(FDict[V], len(f))
			copy(out, f)
			out[i].val = v
			return out
		}
	}
	out := make(FDict[V], len(f)+1)
	copy(out, f)
	out[len(f)] = fent[V]{k, v}
	return out
}

// GetPF is Get with a last-byte prefilter: two keys of one length that differ
// usually differ at the end ("key-3" / "key-4", "\x01i1003" / "\x01i1004"),
// so most non-matching entries are rejected without a memequal call.
func (f FDict[V]) GetPF(k string) (V, bool) {
	n := len(k)
	if n == 0 {
		for i := range f {
			if len(f[i].key) == 0 {
				return f[i].val, true
			}
		}
		var zero V
		return zero, false
	}
	last := k[n-1]
	for i := range f {
		e := &f[i]
		if len(e.key) == n && e.key[n-1] == last && e.key == k {
			return e.val, true
		}
	}
	var zero V
	return zero, false
}
