//go:build !js

package rt

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func rpcReq(path, rid, sid string) *http.Request {
	url := path
	if rid != "" {
		url += "?rid=" + rid
	}
	r := httptest.NewRequest(http.MethodPost, url, strings.NewReader("{}"))
	if sid != "" {
		r.AddCookie(&http.Cookie{Name: "sky_sid", Value: sid})
	}
	return r
}

// SPA-6: a retried RPC (same id) is answered from the first run and never runs
// the non-idempotent handler twice.
func TestSpaRpcDedupe_RetryDoesNotRerun(t *testing.T) {
	var runs int32
	h := spaRpcDedupeWith(newSpaRpcDedupeCache(16, time.Minute), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&runs, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"hits":` + strconv.Itoa(int(n)) + `}`))
	}))
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, rpcReq("/_rpc/Hit", "n-1", "s1"))
		if got := rec.Body.String(); got != `{"hits":1}` {
			t.Fatalf("attempt %d body = %s, want the first answer", i, got)
		}
	}
	if runs != 1 {
		t.Fatalf("handler ran %d times, want 1", runs)
	}
	// A NEW id runs again.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, rpcReq("/_rpc/Hit", "n-2", "s1"))
	if runs != 2 || rec.Body.String() != `{"hits":2}` {
		t.Fatalf("new id: runs=%d body=%s", runs, rec.Body.String())
	}
}

// The key is scoped to the session cookie: another client replaying an id
// does not get the first client's cached answer.
func TestSpaRpcDedupe_ScopedToSession(t *testing.T) {
	var runs int32
	h := spaRpcDedupeWith(newSpaRpcDedupeCache(16, time.Minute), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&runs, 1)
		w.Write([]byte("ok"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), rpcReq("/_rpc/Hit", "x-1", "alice"))
	h.ServeHTTP(httptest.NewRecorder(), rpcReq("/_rpc/Hit", "x-1", "bob"))
	if runs != 2 {
		t.Fatalf("runs = %d, want 2 (different sessions)", runs)
	}
}

// Untagged requests and non-RPC paths bypass the cache.
func TestSpaRpcDedupe_UntaggedBypass(t *testing.T) {
	var runs int32
	h := spaRpcDedupeWith(newSpaRpcDedupeCache(16, time.Minute), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&runs, 1)
	}))
	h.ServeHTTP(httptest.NewRecorder(), rpcReq("/_rpc/Hit", "", ""))
	h.ServeHTTP(httptest.NewRecorder(), rpcReq("/_rpc/Hit", "", ""))
	h.ServeHTTP(httptest.NewRecorder(), rpcReq("/api/x", "r", ""))
	h.ServeHTTP(httptest.NewRecorder(), rpcReq("/api/x", "r", ""))
	if runs != 4 {
		t.Fatalf("runs = %d, want 4", runs)
	}
}

// A duplicate arriving while the first is still running waits for it and gets
// the same answer; the handler still runs once.
func TestSpaRpcDedupe_ConcurrentDuplicateWaits(t *testing.T) {
	var runs int32
	release := make(chan struct{})
	h := spaRpcDedupeWith(newSpaRpcDedupeCache(16, time.Minute), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&runs, 1)
		<-release
		w.Write([]byte("done"))
	}))
	var wg sync.WaitGroup
	bodies := make([]string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, rpcReq("/_rpc/Pay", "p-1", "s"))
			bodies[i] = rec.Body.String()
		}(i)
		time.Sleep(20 * time.Millisecond)
	}
	close(release)
	wg.Wait()
	if runs != 1 || bodies[0] != "done" || bodies[1] != "done" {
		t.Fatalf("runs=%d bodies=%v", runs, bodies)
	}
}

// The cache stays bounded: old completed entries are evicted.
func TestSpaRpcDedupe_Bounded(t *testing.T) {
	c := newSpaRpcDedupeCache(8, time.Minute)
	h := spaRpcDedupeWith(c, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("x")) }))
	for i := 0; i < 100; i++ {
		h.ServeHTTP(httptest.NewRecorder(), rpcReq("/_rpc/A", "r-"+strconv.Itoa(i), "s"))
	}
	if len(c.m) > 8 || len(c.order) > 8 {
		t.Fatalf("cache grew to m=%d order=%d, want <= 8", len(c.m), len(c.order))
	}
}

// H-7: an expired answer is released, not kept until 4096 newer ones push it
// out. sky-lang.org kept every RPC reply (the whole model, ~100 KB) for the
// cap's worth of requests, so the live heap grew under load and GOGC=400 made
// the process RSS about five times that.
func TestSpaRpcDedupe_ExpiredEntriesAreReleased(t *testing.T) {
	now := time.Unix(1_000, 0)
	c := newSpaRpcDedupeCache(4096, time.Minute)
	c.now = func() time.Time { return now }
	h := spaRpcDedupeWith(c, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("answer")) }))
	for i := 0; i < 50; i++ {
		h.ServeHTTP(httptest.NewRecorder(), rpcReq("/_rpc/A", "old-"+strconv.Itoa(i), "s"))
	}
	now = now.Add(2 * time.Minute)
	h.ServeHTTP(httptest.NewRecorder(), rpcReq("/_rpc/A", "new", "s"))
	if len(c.m) != 1 || len(c.order) != 1 {
		t.Fatalf("expired answers kept: m=%d order=%d, want 1", len(c.m), len(c.order))
	}
	if c.bytes != len("answer") {
		t.Fatalf("byte count %d, want %d", c.bytes, len("answer"))
	}
}

// The cache is bounded in BYTES as well as entries: large answers cannot hold
// cap x size of memory. The oldest completed answers go first, and a kept
// answer holds exactly its bytes (no buffer growth slack).
func TestSpaRpcDedupe_ByteBudget(t *testing.T) {
	c := newSpaRpcDedupeCache(4096, time.Hour)
	c.maxBytes = 10_000
	big := strings.Repeat("x", 3_000)
	h := spaRpcDedupeWith(c, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 3; i++ {
			w.Write([]byte(big[:1000]))
		}
	}))
	for i := 0; i < 20; i++ {
		h.ServeHTTP(httptest.NewRecorder(), rpcReq("/_rpc/A", "r-"+strconv.Itoa(i), "s"))
	}
	if c.bytes > c.maxBytes {
		t.Fatalf("kept %d bytes, budget %d", c.bytes, c.maxBytes)
	}
	total := 0
	for _, e := range c.m {
		if cap(e.body) != len(e.body) {
			t.Fatalf("a kept answer holds %d bytes of capacity for %d bytes", cap(e.body), len(e.body))
		}
		total += len(e.body)
	}
	if total != c.bytes || len(c.m) != 3 {
		t.Fatalf("entries=%d bytes=%d counted=%d, want 3 entries of 3000", len(c.m), total, c.bytes)
	}
	// The newest answer is still replayed.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, rpcReq("/_rpc/A", "r-19", "s"))
	if rec.Header().Get("X-Sky-Rpc-Replayed") != "1" {
		t.Fatal("the newest answer must still be replayed")
	}
}
