// lazy.go — Std.Ui.Lazy memoisation kernel.
//
// `Std.Ui.Lazy.{lazy, lazy2..lazy5} : (a -> [...] -> Element msg)
// -> a -> [...] -> Element msg` wrappers used to be no-op
// passthroughs. v0.12 wires them to runtime helpers that
// memoise on (function-pointer, args fingerprint).
//
// Cache scope is process-wide with an LRU bound (lazyCacheCap,
// default 1024 entries). The cache is shared across renders;
// stable subtrees (a long list whose items don't change) hit the
// cache on every subsequent render. Pathological cases — every
// call has a fresh fingerprint — degrade to no-op behaviour
// (cache misses, no benefit) without unbounded memory growth.
//
// Fingerprint: `fmt.Sprintf("%p|%v|...|%v", fn, a, b, ...)`. The
// `%v` formatter gives stable strings for primitives, ADTs (via
// SkyADT.SkyName), records (via Go map ordering — deterministic
// under sort), and lists. Function pointers and channel handles
// fingerprint to their runtime address — reasonable for the cache
// key but means two structurally-identical funcs miss the cache.
// Acceptable trade-off: the expected use case is `lazy renderItem
// item` where the function is a stable top-level binding.
//
// Concurrency: sync.Mutex around a map + linked list. Sky.Live's
// per-session lock + Sky.Tui's main-goroutine model means the
// cache is rarely contended in practice; a heavier sharded
// design would add complexity without measurable gain at our
// scale. Profile first.

package rt

import (
	"container/list"
	"fmt"
	"sync"
)

// lazyCacheCap caps the LRU. Tuneable via the SKY_UI_LAZY_CAP env
// var for projects with very-wide trees (default 1024). Read once
// at first access; restart Sky.Live to pick up a new value.
var (
	lazyCacheCap   = 1024
	lazyCacheOnce  sync.Once
	lazyCache      *lazyLRU
	lazyCacheMutex sync.Mutex
)

type lazyEntry struct {
	key   string
	value any
}

type lazyLRU struct {
	mu    sync.Mutex
	cap   int
	items map[string]*list.Element
	order *list.List
}

func newLazyLRU(cap int) *lazyLRU {
	if cap <= 0 {
		cap = 1024
	}
	return &lazyLRU{
		cap:   cap,
		items: make(map[string]*list.Element, cap),
		order: list.New(),
	}
}

func lazyCacheInit() {
	lazyCacheOnce.Do(func() {
		// Honour SKY_UI_LAZY_CAP; fall back to default on unset /
		// malformed.
		cap := lazyCacheCap
		if v := getenvInt("SKY_UI_LAZY_CAP", lazyCacheCap); v > 0 {
			cap = v
		}
		lazyCache = newLazyLRU(cap)
	})
}

// getenvInt reads an integer env var with a default. Defined here
// instead of importing os each time — keeps the rt package self-
// contained for embedding.
func getenvInt(key string, def int) int {
	v, ok := osLookupEnv(key)
	if !ok || v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return def
	}
	return n
}

func (c *lazyLRU) lookup(k string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[k]; ok {
		c.order.MoveToFront(el)
		return el.Value.(*lazyEntry).value, true
	}
	return nil, false
}

func (c *lazyLRU) store(k string, v any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[k]; ok {
		el.Value.(*lazyEntry).value = v
		c.order.MoveToFront(el)
		return
	}
	el := c.order.PushFront(&lazyEntry{key: k, value: v})
	c.items[k] = el
	if c.order.Len() > c.cap {
		// Evict the back of the list.
		back := c.order.Back()
		if back != nil {
			c.order.Remove(back)
			delete(c.items, back.Value.(*lazyEntry).key)
		}
	}
}

// lazyKey computes the cache key for a (function, args) pair.
// Uses %p for the function pointer (stable per top-level binding),
// %v for each arg. Pipe-delimited so primitive args don't blur
// across boundaries (`1` + `2` and `12` would otherwise collide).
func lazyKey(fn any, args ...any) string {
	parts := make([]string, 0, 1+len(args))
	parts = append(parts, fmt.Sprintf("%p", fn))
	for _, a := range args {
		parts = append(parts, fmt.Sprintf("%v", a))
	}
	// Strings.Join would be ~3% faster but adds an import for one
	// call site. Manual loop is fine.
	out := parts[0]
	for i := 1; i < len(parts); i++ {
		out += "|" + parts[i]
	}
	return out
}

// callWithArgs invokes a Sky-shaped curried function with the
// given args. Sky lambdas lower to func(any) any (one arg at a
// time), but multi-arg functions emitted from typed-codegen are
// func(a, b) any. SkyCall handles both shapes uniformly.
func callWithArgs(fn any, args []any) any {
	r := fn
	for _, a := range args {
		r = SkyCall(r, a)
	}
	return r
}

// Std_Ui_Lazy_lazy : (a -> Element msg) -> a -> Element msg
//
// Memoises calls to `fn(a)` where (fn, a) is the cache key. Cache
// hit returns the cached Element; miss invokes fn and stores.
func Std_Ui_Lazy_lazy(fn any, a any) any {
	lazyCacheInit()
	key := lazyKey(fn, a)
	if v, ok := lazyCache.lookup(key); ok {
		return v
	}
	v := callWithArgs(fn, []any{a})
	lazyCache.store(key, v)
	return v
}

func Std_Ui_Lazy_lazy2(fn any, a any, b any) any {
	lazyCacheInit()
	key := lazyKey(fn, a, b)
	if v, ok := lazyCache.lookup(key); ok {
		return v
	}
	v := callWithArgs(fn, []any{a, b})
	lazyCache.store(key, v)
	return v
}

func Std_Ui_Lazy_lazy3(fn any, a any, b any, c any) any {
	lazyCacheInit()
	key := lazyKey(fn, a, b, c)
	if v, ok := lazyCache.lookup(key); ok {
		return v
	}
	v := callWithArgs(fn, []any{a, b, c})
	lazyCache.store(key, v)
	return v
}

func Std_Ui_Lazy_lazy4(fn any, a any, b any, c any, d any) any {
	lazyCacheInit()
	key := lazyKey(fn, a, b, c, d)
	if v, ok := lazyCache.lookup(key); ok {
		return v
	}
	v := callWithArgs(fn, []any{a, b, c, d})
	lazyCache.store(key, v)
	return v
}

func Std_Ui_Lazy_lazy5(fn any, a any, b any, c any, d any, e any) any {
	lazyCacheInit()
	key := lazyKey(fn, a, b, c, d, e)
	if v, ok := lazyCache.lookup(key); ok {
		return v
	}
	v := callWithArgs(fn, []any{a, b, c, d, e})
	lazyCache.store(key, v)
	return v
}

// LazyCacheClear clears the entire cache. Wired to runtime exit
// paths so tests aren't polluted by previous runs. NOT exported to
// Sky-side (no kernel binding) — cache management is internal.
func LazyCacheClear() {
	lazyCacheMutex.Lock()
	defer lazyCacheMutex.Unlock()
	lazyCache = newLazyLRU(lazyCacheCap)
}
