// live_store_bluedb.go — Sky.Live SessionStore backed by the embedded BlueDB
// engine (runtime-go/bluedb): a group-committed, single-file, durable KV. This
// is the zero-ops embedded default that survives a restart, selected with
// SKY_LIVE_STORE=bluedb (or [live] store = "bluedb").
//
// TTL: BlueDB is a pure KV with no native expiry, so each value is prefixed with
// an 8-byte last-seen timestamp; a read slides it (write-coalesced) and a cleanup
// loop reaps expired keys via bluedb.ForEach. Broker: in-process *topicRegistry,
// matching the memory/sqlite stores (single-instance; no cross-process pub/sub).
package rt

import (
	"encoding/binary"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"sky-app/bluedb"
	"sky-app/rt/telemetry"
)

type bluedbStore struct {
	db   *bluedb.DB
	ttl  time.Duration
	stop chan struct{}
	wg   sync.WaitGroup

	// idleEvict — tiered-session-cache idle-evict window (0 disables). When
	// 0 < idleEvict < ttl, cleanupLoop drops a session's live memCache pointer
	// (the ~37KB liveSession object + its subscription goroutines) after it has
	// been idle this long — persisting a FRESH blob first, then resurrecting it
	// from disk on the next access. Mirrors sqliteStore.idleEvict. NOTE: BlueDB
	// keeps the persisted blob in db.mem regardless (it's an in-RAM KV), so the
	// win here is freeing the liveSession OBJECT + goroutines, not the blob.
	// See docs/skylive/tiered-session-cache.md.
	idleEvict time.Duration

	// memCache holds the LIVE session pointer (the one owning Time.every /
	// subscription goroutines) and is the fallback for sessions whose Model
	// isn't gob-encodable — same trade-off as the sqlite store.
	memMu    sync.RWMutex
	memCache map[string]*liveSession

	broker Broker
}

func newBlueDBStore(path string, ttl, idleEvict time.Duration) (*bluedbStore, error) {
	// Create the parent dir so a configured storePath like "data/app.blue" works
	// out of the box — otherwise Open fails ("no such file or directory") and the
	// store silently falls back to memory (sessions lost on restart). Mirrors the
	// mkdir-p the Std.BlueDB app-data kernel does.
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	// MaxKeys bounds the RAM-resident session set (it's a soft DoS/OOM ceiling —
	// a flood of new sessions gets ErrFull, not an OOM kill; active sessions are
	// also TTL-reaped). MaxValueBytes guards a single pathological session blob.
	db, err := bluedb.Open(path, bluedb.Options{
		Sync:            true,
		CheckpointEvery: 5000,
		MaxKeys:         5_000_000,
		MaxValueBytes:   16 << 20,
	})
	if err != nil {
		return nil, err
	}
	s := &bluedbStore{
		db:        db,
		ttl:       ttl,
		idleEvict: idleEvict,
		stop:      make(chan struct{}),
		memCache:  map[string]*liveSession{},
		broker:    newTopicRegistry(0),
	}
	s.wg.Add(1)
	go s.cleanupLoop()
	return s, nil
}

func (s *bluedbStore) Broker() Broker { return s.broker }

// Ping — report the engine's health for /_sky/readyz. A sealed (unrecoverable
// write error) or closed engine returns an error so the orchestrator stops
// routing to this replica instead of it silently persisting nothing.
func (s *bluedbStore) Ping() error { return s.db.Err() }

func (s *bluedbStore) NewID() string { return generateSkySessionID() }

func (s *bluedbStore) Close() error {
	close(s.stop)
	s.wg.Wait() // join cleanupLoop before closing the engine
	return s.db.Close()
}

// value layout: [lastSeen unix-seconds int64 LE (8 bytes)][gob session blob]
func encodeBlueValue(lastSeen int64, blob []byte) []byte {
	out := make([]byte, 8+len(blob))
	binary.LittleEndian.PutUint64(out[:8], uint64(lastSeen))
	copy(out[8:], blob)
	return out
}

func decodeBlueValue(v []byte) (lastSeen int64, blob []byte, ok bool) {
	if len(v) < 8 {
		return 0, nil, false
	}
	return int64(binary.LittleEndian.Uint64(v[:8])), v[8:], true
}

func (s *bluedbStore) Set(sid string, sess *liveSession) {
	// fix #2: never re-insert an evicted (corpse) pointer. An abandoned
	// session's late async result (runPerformBody / Time.every tick / subscriber
	// dispatch) arriving after the idle-evict pass markDone'd this pointer must
	// be DROPPED — resurrecting the corpse into memCache would split-brain
	// against the fresh pointer a later Get decodes from disk. Mirrors
	// sqliteStore.Set.
	if sess.evicted.Load() {
		return
	}
	sess.touchLastSeen()
	s.memMu.Lock()
	if sess.evicted.Load() {
		// Re-check under memMu (fix #2 TOCTOU): the idle-evict pass sets
		// `evicted` UNDER memMu.Lock, so a Set that passed the pre-lock atomic
		// check can still race the evict and re-insert the markDone'd corpse.
		s.memMu.Unlock()
		return
	}
	s.memCache[sid] = sess
	s.memMu.Unlock()

	blob, err := encodeSession(sess)
	if err != nil {
		telemetry.Default().Inc("sky_live_session_encode_fail_total", map[string]string{"store": "bluedb"})
		logOnce("bluedb-encode-"+sid, func() {
			log.Printf("[sky.live] bluedb: session %s not persistable (%v); using in-memory fallback", sid, err)
		})
		return
	}
	if err := s.db.Put([]byte(sid), encodeBlueValue(sess.lastSeenTime().Unix(), blob)); err != nil {
		log.Printf("[sky.live] bluedb: failed to save session %s: %v", sid, err)
	}
}

func (s *bluedbStore) Get(sid string) (*liveSession, bool) {
	s.memMu.RLock()
	if sess, ok := s.memCache[sid]; ok {
		// Touch WHILE holding the RLock (matching memoryStore) so the cleanup
		// loop — which takes the write lock and reads lastSeen to decide
		// eviction — can't interleave between this read and the touch and hand
		// back a session it's about to tear down.
		sess.touchLastSeen()
		s.memMu.RUnlock()
		return sess, true
	}
	s.memMu.RUnlock()

	v, ok := s.db.Get([]byte(sid))
	if !ok {
		return nil, false
	}
	lastSeen, blob, ok := decodeBlueValue(v)
	if !ok {
		return nil, false
	}
	if time.Since(time.Unix(lastSeen, 0)) > s.ttl {
		_ = s.db.Delete([]byte(sid)) // lazily drop an expired session
		return nil, false
	}
	sess, err := decodeSession(blob)
	if err != nil {
		log.Printf("[sky.live] bluedb: failed to decode session %s: %v", sid, err)
		return nil, false
	}
	// Slide the TTL on read, but coalesce: only re-persist when last-seen has
	// drifted by more than a fraction of the TTL, so a read-heavy (SSE-only)
	// session doesn't turn every read into a write.
	if time.Since(time.Unix(lastSeen, 0)) > s.slideInterval() {
		_ = s.db.Put([]byte(sid), encodeBlueValue(time.Now().Unix(), blob))
	}
	return sess, true
}

func (s *bluedbStore) slideInterval() time.Duration {
	d := s.ttl / 20
	if d < 10*time.Second {
		d = 10 * time.Second
	}
	return d
}

func (s *bluedbStore) Delete(sid string) {
	s.memMu.Lock()
	sess := s.memCache[sid]
	delete(s.memCache, sid)
	s.memMu.Unlock()
	if sess != nil {
		sess.markDone() // terminal teardown for the live pointer's goroutines
	}
	_ = s.db.Delete([]byte(sid))
}

func (s *bluedbStore) cleanupLoop() {
	defer s.wg.Done()
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case now := <-t.C:
			s.reap(now)
			// Tiered-session-cache idle-evict pass (docs/skylive/
			// tiered-session-cache.md). Runs AFTER the TTL reap so an
			// already-expired session is reaped (blob deleted), not evicted
			// (blob kept).
			s.runIdleEvictOnce(now)
		}
	}
}

// runIdleEvictOnce performs one tiered-session-cache idle-evict pass. Called
// each 60s tick by cleanupLoop AND directly by tests. No-op unless
// 0 < idleEvict < ttl. The persist closure writes a FRESH blob + last_seen so
// the TTL reap doesn't delete a resurrectable blob (fix #8). Reuses the same
// store-agnostic idleEvictPass the sqlite/postgres/redis stores use — same
// grill fixes (re-check under memMu.Lock, evicted flag, markDone outside memMu,
// skip encode-fail, persist fresh blob first).
func (s *bluedbStore) runIdleEvictOnce(now time.Time) {
	if s.idleEvict <= 0 || s.idleEvict >= s.ttl {
		return
	}
	idleEvictPass(now, s.idleEvict, &s.memMu, s.memCache,
		func(sid string, blob []byte, lastSeenUnix int64) {
			_ = s.db.Put([]byte(sid), encodeBlueValue(lastSeenUnix, blob))
		})
}

// reap expires idle sessions. Reads slide only the in-memory clock, so a
// persisted blob can look expired while its session is still active via reads;
// this pass consults memCache before destroying a durable blob and REFRESHES a
// still-active one (the coalesced persisted-slide) instead of deleting it — so a
// read-heavy session isn't silently lost on the next restart. It also closes the
// collect→delete TOCTOU: a concurrent Set that refreshed the session shows up in
// memCache, so its just-written blob isn't destroyed.
func (s *bluedbStore) reap(now time.Time) {
	cutoff := now.Add(-s.ttl).Unix()
	cut := now.Add(-s.ttl)

	// Snapshot the persisted keyspace (collect under ForEach's read lock; act
	// after — Delete/Put go through the committer).
	type cand struct {
		key string
		ls  int64
	}
	var cands []cand
	s.db.ForEach(func(k, v []byte) bool {
		if ls, _, ok := decodeBlueValue(v); ok {
			cands = append(cands, cand{string(k), ls})
		}
		return true
	})

	for _, c := range cands {
		if c.ls >= cutoff {
			continue // persisted blob still fresh
		}
		s.memMu.RLock()
		sess, inCache := s.memCache[c.key]
		fresh := inCache && !sess.lastSeenTime().Before(cut)
		s.memMu.RUnlock()
		if fresh {
			// Active via reads (or just re-Set): refresh the durable blob. R2:
			// encode under sess.mu — a concurrent dispatch reassigns sess.model,
			// so an off-lock encodeSession here would race → concurrent-map crash.
			sess.mu.Lock()
			blob, err := encodeSession(sess)
			ls := sess.lastSeenTime().Unix()
			sess.mu.Unlock()
			if err == nil {
				_ = s.db.Put([]byte(c.key), encodeBlueValue(ls, blob))
			}
			continue
		}
		// Truly idle: drop the blob, evict the live pointer, teardown (once).
		_ = s.db.Delete([]byte(c.key))
		s.memMu.Lock()
		dead := s.memCache[c.key]
		delete(s.memCache, c.key)
		s.memMu.Unlock()
		if dead != nil {
			dead.markDone()
		}
	}

	// Sweep memCache-only sessions (never persisted, e.g. non-gob-encodable):
	// evict + teardown the ones idle past the TTL. Persisted sessions were
	// already handled above (fresh ones kept, idle ones evicted).
	s.memMu.Lock()
	var dead []*liveSession
	for sid, sess := range s.memCache {
		if sess.lastSeenTime().Before(cut) {
			dead = append(dead, sess)
			delete(s.memCache, sid)
		}
	}
	s.memMu.Unlock()
	for _, sess := range dead {
		sess.markDone()
	}
}
