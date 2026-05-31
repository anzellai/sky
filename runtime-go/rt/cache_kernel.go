// Package rt — Std.Cache runtime kernels.
//
// v0.15.47 stdlib batch (#380): LRU + TTL in-memory cache.
//
// Uses `hashicorp/golang-lru/v2` for the LRU core (already in
// go.mod indirect via the OTLP stack). TTL is enforced lazily on
// `get`: an expired entry is treated as a miss + removed.
//
// Keys are stringified via fmt.%v — the Sky-side surface is
// parametric `Cache k v`, but runtime needs comparable keys, so
// k must be displayable (typical: String / Int).
package rt

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

type cacheEntry struct {
	value  any
	expiry time.Time // zero = never
}

type cacheHandle struct {
	id        int64
	lru       *lru.Cache[string, cacheEntry]
	ttl       time.Duration
	hits      atomic.Int64
	misses    atomic.Int64
	evictions atomic.Int64
}

var (
	cacheRegistryMu sync.Mutex
	cacheRegistry   = map[int64]*cacheHandle{}
	cacheNextID     atomic.Int64
)

// Cache_new implements:
//
//	Std.Cache.newRaw : CacheCfg -> Task Error Int
func Cache_new(cfgArg any) any {
	maxEntries := AsInt(recordField(cfgArg, "MaxEntries", "maxEntries"))
	ttlMs := AsInt(recordField(cfgArg, "TtlMs", "ttlMs"))
	// Floor: max 1, no -1 hacks.
	if maxEntries <= 0 {
		maxEntries = 1024
	}
	return func() any {
		h := &cacheHandle{
			id:  cacheNextID.Add(1),
			ttl: time.Duration(ttlMs) * time.Millisecond,
		}
		// onEvict counts every eviction (capacity AND manual removal).
		// We want capacity-driven evictions only, so increment from
		// the LRU's own eviction callback for that subset.
		l, err := lru.NewWithEvict[string, cacheEntry](maxEntries, func(k string, v cacheEntry) {
			h.evictions.Add(1)
		})
		if err != nil {
			return Err[any, any](ErrFfi("cache.new: " + err.Error()))
		}
		h.lru = l
		cacheRegistryMu.Lock()
		cacheRegistry[h.id] = h
		cacheRegistryMu.Unlock()
		return Ok[any, any](int(h.id))
	}
}

func cacheLookup(idArg any) *cacheHandle {
	id := int64(AsInt(idArg))
	cacheRegistryMu.Lock()
	h := cacheRegistry[id]
	cacheRegistryMu.Unlock()
	return h
}

// Cache_get implements:
//
//	Cache.getRaw : Int -> k -> Task Error (Maybe v)
func Cache_get(idArg, keyArg any) any {
	return func() any {
		h := cacheLookup(idArg)
		if h == nil {
			return Err[any, any](ErrInvalidInput("cache.get: cache not found"))
		}
		k := fmt.Sprintf("%v", keyArg)
		entry, ok := h.lru.Get(k)
		if !ok {
			h.misses.Add(1)
			return Ok[any, any](makeMaybeNothing())
		}
		// Lazy TTL expiration
		if !entry.expiry.IsZero() && time.Now().After(entry.expiry) {
			h.lru.Remove(k)
			h.misses.Add(1)
			return Ok[any, any](makeMaybeNothing())
		}
		h.hits.Add(1)
		return Ok[any, any](makeMaybeJust(entry.value))
	}
}

// Cache_put implements:
//
//	Cache.putRaw : Int -> k -> v -> Task Error ()
func Cache_put(idArg, keyArg, valueArg any) any {
	return func() any {
		h := cacheLookup(idArg)
		if h == nil {
			return Err[any, any](ErrInvalidInput("cache.put: cache not found"))
		}
		k := fmt.Sprintf("%v", keyArg)
		var exp time.Time
		if h.ttl > 0 {
			exp = time.Now().Add(h.ttl)
		}
		h.lru.Add(k, cacheEntry{value: valueArg, expiry: exp})
		return Ok[any, any](nil)
	}
}

// Cache_remove implements:
//
//	Cache.removeRaw : Int -> k -> Task Error ()
func Cache_remove(idArg, keyArg any) any {
	return func() any {
		h := cacheLookup(idArg)
		if h == nil {
			return Err[any, any](ErrInvalidInput("cache.remove: cache not found"))
		}
		k := fmt.Sprintf("%v", keyArg)
		// Decrement eviction count if remove fires the callback — the
		// LRU OnEvict counter increments on every remove; we want
		// capacity evictions only. Reverse the increment here.
		if h.lru.Remove(k) {
			h.evictions.Add(-1)
		}
		return Ok[any, any](nil)
	}
}

// Cache_clear implements:
//
//	Cache.clearRaw : Int -> Task Error ()
func Cache_clear(idArg any) any {
	return func() any {
		h := cacheLookup(idArg)
		if h == nil {
			return Err[any, any](ErrInvalidInput("cache.clear: cache not found"))
		}
		n := h.lru.Len()
		h.lru.Purge()
		// Purge fires OnEvict for every entry; subtract those from
		// the eviction counter so stats reflect capacity evictions only.
		h.evictions.Add(int64(-n))
		return Ok[any, any](nil)
	}
}

// Cache_size implements:
//
//	Cache.sizeRaw : Int -> Task Error Int
func Cache_size(idArg any) any {
	return func() any {
		h := cacheLookup(idArg)
		if h == nil {
			return Err[any, any](ErrInvalidInput("cache.size: cache not found"))
		}
		return Ok[any, any](h.lru.Len())
	}
}

// Cache_stats implements:
//
//	Cache.statsRaw : Int -> Task Error { hits, misses, evictions }
func Cache_stats(idArg any) any {
	return func() any {
		h := cacheLookup(idArg)
		if h == nil {
			return Err[any, any](ErrInvalidInput("cache.stats: cache not found"))
		}
		return Ok[any, any](map[string]any{
			"hits":       int(h.hits.Load()),
			"misses":     int(h.misses.Load()),
			"evictions":  int(h.evictions.Load()),
		})
	}
}
