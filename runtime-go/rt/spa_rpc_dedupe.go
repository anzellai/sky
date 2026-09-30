//go:build !js

package rt

import (
	"bytes"
	"net/http"
	"strings"
	"sync"
	"time"
)

// spa_rpc_dedupe.go — at-most-once execution for the auto-split's RPCs.
//
// The Sky.Spa client tags every server-branch RPC with a request id
// (`POST /_rpc/<Msg>?rid=<id>`, spa_rpcqueue.go) and re-sends the SAME id when
// the user presses Retry after a network failure. A network failure does not
// prove the server did not run the request: the response can be lost after the
// effect ran. Without this layer a Retry ran a non-idempotent effect twice (an
// append, a payment, a counter). With it, the backend answers a repeated id
// from the response it already produced, byte for byte, and never re-runs the
// handler. A duplicate that arrives while the first is still running waits for
// it and gets the same answer.
//
// The cache is bounded (spaRpcDedupeCap entries, oldest evicted first) and each
// entry expires after spaRpcDedupeTTL, so memory stays flat under any load. It
// is per process: a multi-replica deploy already needs sticky sessions for the
// split (auto-split.md §16), which keeps a client's retry on the same replica.
// The key includes the session cookies (`sky_spa`, and the legacy `sky_sid`),
// so one client cannot read another's cached answer by replaying its id.

const (
	spaRpcDedupeCap = 4096
	spaRpcDedupeTTL = 10 * time.Minute
	// spaRpcDedupeMaxBytes bounds the answer bytes the cache keeps in total. An
	// answer is the whole reply of an RPC (a Spa reply can carry the model, tens
	// or hundreds of kB), so the entry cap alone did not bound memory (H-7).
	spaRpcDedupeMaxBytes = 16 << 20
	// spaRpcDedupeWait bounds how long a duplicate waits for the first run.
	spaRpcDedupeWait = 2 * time.Minute
)

type spaRpcDedupeEntry struct {
	key    string
	done   chan struct{}
	at     time.Time
	status int
	header http.Header
	body   []byte
}

type spaRpcDedupeCache struct {
	mu    sync.Mutex
	m     map[string]*spaRpcDedupeEntry
	order []spaRpcDedupeSlot
	cap   int
	// bytes is the size of the completed answers held in m; maxBytes its bound.
	bytes    int
	maxBytes int
	ttl      time.Duration
	now      func() time.Time
}

func newSpaRpcDedupeCache(capacity int, ttl time.Duration) *spaRpcDedupeCache {
	return &spaRpcDedupeCache{m: map[string]*spaRpcDedupeEntry{}, cap: capacity, maxBytes: spaRpcDedupeMaxBytes, ttl: ttl, now: time.Now}
}

// claim returns the entry for key and whether the caller is the FIRST request
// with it (and so must run the handler and fill the entry).
func (c *spaRpcDedupeCache) claim(key string) (*spaRpcDedupeEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if e, ok := c.m[key]; ok && now.Sub(e.at) < c.ttl {
		return e, false
	}
	e := &spaRpcDedupeEntry{key: key, done: make(chan struct{}), at: now}
	c.m[key] = e
	c.order = append(c.order, spaRpcDedupeSlot{key: key, e: e})
	c.evict()
	return e, true
}

type spaRpcDedupeSlot struct {
	key string
	e   *spaRpcDedupeEntry
}

// evict drops, oldest first, every COMPLETED entry that has expired, then
// completed entries until the cache is within its entry cap and its byte
// budget. An entry still being filled is never evicted (a duplicate may be
// waiting on it); a slot whose key was re-claimed after expiry is dropped
// without touching the newer entry. PRECONDITION: c.mu is held.
//
// Before v0.27.0 the TTL was checked only on a lookup and nothing was dropped
// below the entry cap, so every answer stayed in memory until 4096 newer ones
// pushed it out (H-7).
func (c *spaRpcDedupeCache) evict() {
	now := c.now()
	kept := c.order[:0]
	over := len(c.order) - c.cap
	for _, s := range c.order {
		if c.m[s.key] != s.e {
			over--
			continue // superseded slot
		}
		select {
		case <-s.e.done:
			if over > 0 || c.bytes > c.maxBytes || now.Sub(s.e.at) >= c.ttl {
				delete(c.m, s.key)
				c.bytes -= len(s.e.body)
				over--
				continue
			}
		default:
		}
		kept = append(kept, s)
	}
	// Clear the tail so the dropped slots' entries can be collected.
	for i := len(kept); i < len(c.order); i++ {
		c.order[i] = spaRpcDedupeSlot{}
	}
	c.order = kept
}

// fill completes e with the first run's answer and wakes any duplicate waiting
// on it. The body is copied to its exact size (the recording buffer's growth
// slack is not kept), counted against the byte budget while e is the current
// entry for its key, and the cache is trimmed.
func (c *spaRpcDedupeCache) fill(e *spaRpcDedupeEntry, status int, header http.Header, body []byte) {
	c.mu.Lock()
	e.status = status
	e.header = header
	e.body = make([]byte, len(body))
	copy(e.body, body)
	close(e.done)
	if c.m[e.key] == e {
		c.bytes += len(e.body)
		c.evict()
	}
	c.mu.Unlock()
}

// spaRpcDedupeKey returns the cache key for a request, or "" when the request
// is not a tagged auto-split RPC (it then bypasses the cache entirely).
func spaRpcDedupeKey(r *http.Request) string {
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/_rpc/") {
		return ""
	}
	rid := r.URL.Query().Get("rid")
	if rid == "" || len(rid) > 128 {
		return ""
	}
	// Both session cookies (spa_session_legacy.go): `sky_spa`, and the legacy
	// `sky_sid` a pre-v0.27 browser still sends.
	sid := ""
	for _, name := range []string{spaSessionCookieName, spaLegacySessionCookieName} {
		if ck, err := r.Cookie(name); err == nil {
			sid += ck.Value + "\x00"
		}
	}
	return sid + "\x00" + r.URL.Path + "\x00" + rid
}

// spaRpcRecorder writes through to the client while keeping a copy.
type spaRpcRecorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (w *spaRpcRecorder) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *spaRpcRecorder) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.buf.Write(p)
	return w.ResponseWriter.Write(p)
}

// Unwrap exposes the wrapped writer to http.ResponseController (see
// stream_deadline.go).
func (w *spaRpcRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

var spaRpcDedupe = newSpaRpcDedupeCache(spaRpcDedupeCap, spaRpcDedupeTTL)

// spaRpcDedupeMiddleware wraps the server mux (rt_server.go).
func spaRpcDedupeMiddleware(next http.Handler) http.Handler {
	return spaRpcDedupeWith(spaRpcDedupe, next)
}

func spaRpcDedupeWith(cache *spaRpcDedupeCache, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := spaRpcDedupeKey(r)
		if key == "" {
			next.ServeHTTP(w, r)
			return
		}
		e, first := cache.claim(key)
		if !first {
			select {
			case <-e.done:
			case <-time.After(spaRpcDedupeWait):
				http.Error(w, "duplicate request still running", http.StatusServiceUnavailable)
				return
			}
			for k, vs := range e.header {
				for _, v := range vs {
					w.Header().Add(k, v)
				}
			}
			w.Header().Set("X-Sky-Rpc-Replayed", "1")
			w.WriteHeader(e.status)
			_, _ = w.Write(e.body)
			return
		}
		rec := &spaRpcRecorder{ResponseWriter: w}
		returned := false
		defer func() {
			if rec.status == 0 {
				// Nothing written: net/http answers 200 for a handler that
				// returned, and the handler panicked otherwise.
				rec.status = http.StatusOK
				if !returned {
					rec.status = http.StatusInternalServerError
				}
			}
			cache.fill(e, rec.status, w.Header().Clone(), rec.buf.Bytes())
		}()
		next.ServeHTTP(rec, r)
		returned = true
	})
}
