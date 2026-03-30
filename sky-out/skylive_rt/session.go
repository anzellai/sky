package skylive_rt

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// StoreFactory creates a SessionStore from a connection path and TTL.
type StoreFactory func(path string, ttl time.Duration) (SessionStore, error)

// storeRegistry allows store backends to register themselves via init().
var storeRegistry = make(map[string]StoreFactory)

// RegisterStore registers a session store backend by name.
func RegisterStore(name string, factory StoreFactory) {
	storeRegistry[name] = factory
}

// Session holds the state for a single client connection.
type Session struct {
	Model    any       // The current Model value
	PrevView *VNode    // The last rendered VNode tree (for diffing)
	MsgLog   []any     // Event log for replay (future use)
	Created  time.Time // When the session was created
	LastSeen time.Time // Last activity timestamp
	Version  int64     // Monotonic version for optimistic concurrency control
}

// SessionStore is the interface for session persistence.
// Set uses optimistic concurrency: it only writes if the session's Version
// matches what's in the store. Returns true on success, false on conflict.
// On conflict the caller should re-Get, re-apply the update, and retry.
type SessionStore interface {
	Get(sid string) (*Session, bool)
	Set(sid string, sess *Session) bool // returns false on version conflict
	Delete(sid string)
	NewID() string
}

// MemoryStore is an in-memory session store with TTL-based expiration.
type MemoryStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	ttl      time.Duration
}

// NewMemoryStore creates a new in-memory session store.
func NewMemoryStore(ttl time.Duration) *MemoryStore {
	store := &MemoryStore{
		sessions: make(map[string]*Session),
		ttl:      ttl,
	}
	// Start background cleanup goroutine
	go store.cleanup()
	return store
}

func (s *MemoryStore) Get(sid string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[sid]
	if ok {
		sess.LastSeen = time.Now()
	}
	return sess, ok
}

func (s *MemoryStore) Set(sid string, sess *Session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.sessions[sid]
	if ok && existing.Version != sess.Version {
		return false // version conflict
	}
	sess.Version++
	sess.LastSeen = time.Now()
	s.sessions[sid] = sess
	return true
}

func (s *MemoryStore) Delete(sid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sid)
}

func (s *MemoryStore) NewID() string {
	return generateSessionID()
}

// generateSessionID creates a cryptographically random 256-bit session ID
// using URL-safe base64 encoding (43 chars, no padding).
func generateSessionID() string {
	b := make([]byte, 32) // 256 bits
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// SessionLocker provides per-session mutexes so that concurrent operations
// (event handling and SSE subscriptions) on the same session are serialized.
// This prevents race conditions where an SSE tick overwrites an in-flight
// event handler's model update.
//
// Scales to millions of sessions: each session gets its own lightweight mutex
// (only contention is between a single user's concurrent event + SSE tick).
// Ref-counted entries are cleaned up automatically when no goroutine holds
// the lock, so memory stays proportional to active (in-flight) sessions,
// not total sessions.
type SessionLocker struct {
	mu    sync.Mutex
	locks map[string]*lockEntry
}

type lockEntry struct {
	mu      sync.Mutex
	waiters int // number of goroutines waiting or holding this lock
}

func NewSessionLocker() *SessionLocker {
	return &SessionLocker{locks: make(map[string]*lockEntry)}
}

func (sl *SessionLocker) Lock(sid string) {
	sl.mu.Lock()
	e, ok := sl.locks[sid]
	if !ok {
		e = &lockEntry{}
		sl.locks[sid] = e
	}
	e.waiters++
	sl.mu.Unlock()
	e.mu.Lock()
}

func (sl *SessionLocker) Unlock(sid string) {
	sl.mu.Lock()
	e, ok := sl.locks[sid]
	if ok {
		e.waiters--
		if e.waiters == 0 {
			delete(sl.locks, sid) // No one waiting — free the entry
		}
	}
	sl.mu.Unlock()
	if ok {
		e.mu.Unlock()
	}
}

func (s *MemoryStore) cleanup() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.Lock()
		now := time.Now()
		for sid, sess := range s.sessions {
			if now.Sub(sess.LastSeen) > s.ttl {
				delete(s.sessions, sid)
			}
		}
		s.mu.Unlock()
	}
}
