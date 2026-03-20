package skylive_rt

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// Session holds the state for a single client connection.
type Session struct {
	Model    any       // The current Model value
	PrevView *VNode    // The last rendered VNode tree (for diffing)
	MsgLog   []any     // Event log for replay (future use)
	Created  time.Time // When the session was created
	LastSeen time.Time // Last activity timestamp
}

// SessionStore is the interface for session persistence.
// V1 implements in-memory only. Other backends (sqlite, redis, etc.)
// implement this same interface.
type SessionStore interface {
	Get(sid string) (*Session, bool)
	Set(sid string, sess *Session)
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

func (s *MemoryStore) Set(sid string, sess *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess.LastSeen = time.Now()
	s.sessions[sid] = sess
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
