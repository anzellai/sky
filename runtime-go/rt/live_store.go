// live_store.go — SessionStore abstraction + memory / SQLite / Postgres
// implementations. The store persists the raw Go `any` model + rendered
// VNode tree between HTTP requests for the same session id.
//
// Wire protocol: every Session is encoded with encoding/gob. Gob handles
// arbitrary Go values without needing a schema, including the compiled
// ADT struct types. Concrete types seen in one binary will always round-
// trip back to the same concrete types because the gob stream embeds
// the type descriptors on first encode.
//
// Selected via sky.toml (or Live.app config):
//   store     = "memory" | "sqlite" | "postgres"
//   storePath = "sessions.db"         (sqlite)
//            = "postgres://user:pass@host/db"  (postgres)
//   ttl       = 1800                   (seconds; default 30m)

package rt

import (
	"bytes"
	crand "crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/gob"
	"fmt"
	"log"
	"os"
	"reflect"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// gob can't serialise interface values unless each concrete type at the
// interface boundary has been registered. The Sky compiler mints a fresh
// Go struct type for every record-alias (`Model_R`, `Shape_R`, …) and
// every ADT constructor (`Msg_Increment`, …), so we can't statically
// list them at runtime-link time. gobRegisterAll walks a value and
// registers every concrete struct / slice / map element type it sees.
var (
	gobRegMu      sync.Mutex
	gobRegistered = map[reflect.Type]bool{}
)

func gobRegisterAll(v any) {
	gobRegMu.Lock()
	defer gobRegMu.Unlock()
	walkGob(reflect.ValueOf(v))
}

func walkGob(v reflect.Value) {
	if !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Ptr:
		if !v.IsNil() {
			walkGob(v.Elem())
		}
	case reflect.Struct:
		t := v.Type()
		if t.PkgPath() != "" && !gobRegistered[t] {
			gobRegistered[t] = true
			// Register by the actual fully-qualified type name; safe to
			// call repeatedly for the same name-type pair.
			defer func() { recover() }()
			gob.Register(reflect.New(t).Elem().Interface())
		}
		for i := 0; i < v.NumField(); i++ {
			walkGob(v.Field(i))
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walkGob(v.Index(i))
		}
	case reflect.Map:
		it := v.MapRange()
		for it.Next() {
			walkGob(it.Value())
		}
	}
}

func cryptoRandRead(b []byte) (int, error) { return crand.Read(b) }
func urlBase64(b []byte) string            { return base64.RawURLEncoding.EncodeToString(b) }

// logOnce: emit a log message at most once per key across the process
// lifetime. Used to avoid log spam when a per-session operation fails
// repeatedly (one message on first keystroke is enough).
var (
	logOnceMu   sync.Mutex
	logOnceKeys = map[string]bool{}
)

func logOnce(key string, fn func()) {
	logOnceMu.Lock()
	seen := logOnceKeys[key]
	if !seen {
		logOnceKeys[key] = true
	}
	logOnceMu.Unlock()
	if !seen {
		fn()
	}
}

// stringField: read a named record field and return its string form, or
// "" when the field is absent / nil.
func stringField(cfg any, name string) string {
	v := Field(cfg, name)
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}


// SessionStore: common interface for the three backends. The runtime
// reads/writes via `Get`, `Set`, `Delete`, and generates IDs via
// `NewID`. Callers are responsible for per-session locking (the runtime
// uses a SessionLocker to serialise event handling + SSE writes).
type SessionStore interface {
	Get(sid string) (*liveSession, bool)
	Set(sid string, sess *liveSession)
	Delete(sid string)
	NewID() string
	Close() error
}


// ═════════════════════════════════════════════════════════════════════
// Memory store — default; in-process, lost on restart.
// ═════════════════════════════════════════════════════════════════════

type memoryStore struct {
	mu       sync.RWMutex
	sessions map[string]*liveSession
	ttl      time.Duration
	stop     chan struct{}
}

func newMemoryStore(ttl time.Duration) *memoryStore {
	s := &memoryStore{
		sessions: map[string]*liveSession{},
		ttl:      ttl,
		stop:     make(chan struct{}),
	}
	go s.cleanupLoop()
	return s
}

func (s *memoryStore) Get(sid string) (*liveSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[sid]
	if ok {
		sess.lastSeen = time.Now()
	}
	return sess, ok
}

func (s *memoryStore) Set(sid string, sess *liveSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess.lastSeen = time.Now()
	s.sessions[sid] = sess
}

func (s *memoryStore) Delete(sid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sid)
}

func (s *memoryStore) NewID() string { return generateSkySessionID() }

func (s *memoryStore) Close() error {
	close(s.stop)
	return nil
}

func (s *memoryStore) cleanupLoop() {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case now := <-t.C:
			s.mu.Lock()
			for id, sess := range s.sessions {
				if now.Sub(sess.lastSeen) > s.ttl {
					delete(s.sessions, id)
				}
			}
			s.mu.Unlock()
		}
	}
}


// ═════════════════════════════════════════════════════════════════════
// SQLite store — persistent sessions on disk, zero-op setup.
// Uses modernc.org/sqlite (pure Go, no CGO).
// ═════════════════════════════════════════════════════════════════════

type sqliteStore struct {
	db    *sql.DB
	ttl   time.Duration
	stop  chan struct{}
	// memCache is a pointer cache so sessions that fail to gob-encode
	// (anonymous struct types the Sky compiler emits for records) still
	// behave correctly within a single process. Restart forgets them,
	// which is the same trade-off the memoryStore makes.
	memMu    sync.RWMutex
	memCache map[string]*liveSession
}

func newSQLiteStore(path string, ttl time.Duration) (*sqliteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS sky_sessions (
			sid        TEXT PRIMARY KEY,
			blob       BLOB NOT NULL,
			last_seen  INTEGER NOT NULL
		)`); err != nil {
		db.Close()
		return nil, err
	}
	s := &sqliteStore{
		db:       db,
		ttl:      ttl,
		stop:     make(chan struct{}),
		memCache: map[string]*liveSession{},
	}
	go s.cleanupLoop()
	return s, nil
}

func (s *sqliteStore) Get(sid string) (*liveSession, bool) {
	// Memory cache hit: current-process sessions we couldn't encode.
	s.memMu.RLock()
	if sess, ok := s.memCache[sid]; ok {
		s.memMu.RUnlock()
		return sess, true
	}
	s.memMu.RUnlock()
	var blob []byte
	err := s.db.QueryRow(`SELECT blob FROM sky_sessions WHERE sid = ?`, sid).Scan(&blob)
	if err != nil {
		return nil, false
	}
	sess, err := decodeSession(blob)
	if err != nil {
		log.Printf("[sky.live] sqlite: failed to decode session %s: %v", sid, err)
		return nil, false
	}
	// Touch last_seen.
	_, _ = s.db.Exec(`UPDATE sky_sessions SET last_seen = ? WHERE sid = ?`,
		time.Now().Unix(), sid)
	return sess, true
}

func (s *sqliteStore) Set(sid string, sess *liveSession) {
	sess.lastSeen = time.Now()
	// Always keep the live pointer in memory so intra-process requests
	// find the session even when the value isn't gob-encodable.
	s.memMu.Lock()
	s.memCache[sid] = sess
	s.memMu.Unlock()
	blob, err := encodeSession(sess)
	if err != nil {
		// Log ONCE per session (not every event) — the alternative is
		// spamming logs for every onInput keystroke.
		logOnce("sqlite-encode-"+sid, func() {
			log.Printf("[sky.live] sqlite: session %s not persistable (%v); using in-memory fallback", sid, err)
		})
		return
	}
	_, err = s.db.Exec(`
		INSERT INTO sky_sessions (sid, blob, last_seen) VALUES (?, ?, ?)
		ON CONFLICT(sid) DO UPDATE SET blob=excluded.blob, last_seen=excluded.last_seen`,
		sid, blob, sess.lastSeen.Unix())
	if err != nil {
		log.Printf("[sky.live] sqlite: failed to save session %s: %v", sid, err)
	}
}

func (s *sqliteStore) Delete(sid string) {
	s.memMu.Lock()
	delete(s.memCache, sid)
	s.memMu.Unlock()
	_, _ = s.db.Exec(`DELETE FROM sky_sessions WHERE sid = ?`, sid)
}

func (s *sqliteStore) NewID() string { return generateSkySessionID() }

func (s *sqliteStore) Close() error {
	close(s.stop)
	return s.db.Close()
}

func (s *sqliteStore) cleanupLoop() {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case now := <-t.C:
			_, _ = s.db.Exec(`DELETE FROM sky_sessions WHERE last_seen < ?`,
				now.Add(-s.ttl).Unix())
		}
	}
}


// ═════════════════════════════════════════════════════════════════════
// Postgres store — same schema, same blob-gob protocol, prod-ready.
// ═════════════════════════════════════════════════════════════════════

type postgresStore struct {
	db       *sql.DB
	ttl      time.Duration
	stop     chan struct{}
	memMu    sync.RWMutex
	memCache map[string]*liveSession
}

func newPostgresStore(connStr string, ttl time.Duration) (*postgresStore, error) {
	db, err := sql.Open("pgx", connStr)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS sky_sessions (
			sid        TEXT PRIMARY KEY,
			blob       BYTEA NOT NULL,
			last_seen  BIGINT NOT NULL
		)`); err != nil {
		db.Close()
		return nil, err
	}
	s := &postgresStore{
		db:       db,
		ttl:      ttl,
		stop:     make(chan struct{}),
		memCache: map[string]*liveSession{},
	}
	go s.cleanupLoop()
	return s, nil
}

func (s *postgresStore) Get(sid string) (*liveSession, bool) {
	s.memMu.RLock()
	if sess, ok := s.memCache[sid]; ok {
		s.memMu.RUnlock()
		return sess, true
	}
	s.memMu.RUnlock()
	var blob []byte
	err := s.db.QueryRow(`SELECT blob FROM sky_sessions WHERE sid = $1`, sid).Scan(&blob)
	if err != nil {
		return nil, false
	}
	sess, err := decodeSession(blob)
	if err != nil {
		log.Printf("[sky.live] postgres: failed to decode session %s: %v", sid, err)
		return nil, false
	}
	_, _ = s.db.Exec(`UPDATE sky_sessions SET last_seen = $1 WHERE sid = $2`,
		time.Now().Unix(), sid)
	return sess, true
}

func (s *postgresStore) Set(sid string, sess *liveSession) {
	sess.lastSeen = time.Now()
	s.memMu.Lock()
	s.memCache[sid] = sess
	s.memMu.Unlock()
	blob, err := encodeSession(sess)
	if err != nil {
		logOnce("pg-encode-"+sid, func() {
			log.Printf("[sky.live] postgres: session %s not persistable (%v); using in-memory fallback", sid, err)
		})
		return
	}
	_, err = s.db.Exec(`
		INSERT INTO sky_sessions (sid, blob, last_seen) VALUES ($1, $2, $3)
		ON CONFLICT (sid) DO UPDATE SET blob = EXCLUDED.blob, last_seen = EXCLUDED.last_seen`,
		sid, blob, sess.lastSeen.Unix())
	if err != nil {
		log.Printf("[sky.live] postgres: failed to save session %s: %v", sid, err)
	}
}

func (s *postgresStore) Delete(sid string) {
	s.memMu.Lock()
	delete(s.memCache, sid)
	s.memMu.Unlock()
	_, _ = s.db.Exec(`DELETE FROM sky_sessions WHERE sid = $1`, sid)
}

func (s *postgresStore) NewID() string { return generateSkySessionID() }

func (s *postgresStore) Close() error {
	close(s.stop)
	return s.db.Close()
}

func (s *postgresStore) cleanupLoop() {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case now := <-t.C:
			_, _ = s.db.Exec(`DELETE FROM sky_sessions WHERE last_seen < $1`,
				now.Add(-s.ttl).Unix())
		}
	}
}


// ═════════════════════════════════════════════════════════════════════
// Helpers
// ═════════════════════════════════════════════════════════════════════

// storableSession: gob-friendly subset of liveSession. Channels, mutexes,
// and handlers (which contain live goroutine-dispatching closures) don't
// round-trip, so we only persist the Model + prevTree. On Get we rebuild
// the missing runtime bits.
type storableSession struct {
	Model    any
	PrevTree *VNode
	LastSeen time.Time
}

func encodeSession(s *liveSession) ([]byte, error) {
	// Walk the value graph to discover + register every concrete struct
	// type at an interface boundary. Safe to call repeatedly — we cache
	// registered types.
	gobRegisterAll(s.model)
	gobRegisterAll(s.prevTree)
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(storableSession{
		Model:    s.model,
		PrevTree: s.prevTree,
		LastSeen: s.lastSeen,
	}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeSession(blob []byte) (*liveSession, error) {
	var st storableSession
	if err := gob.NewDecoder(bytes.NewReader(blob)).Decode(&st); err != nil {
		return nil, err
	}
	sess := &liveSession{
		model:     st.Model,
		prevTree:  st.PrevTree,
		handlers:  map[string]any{},
		sseCh:     make(chan string, 16),
		cancelSub: make(chan struct{}),
		lastSeen:  st.LastSeen,
	}
	return sess, nil
}


// chooseStore: honour a sky.toml Live-store override or the
// SKY_LIVE_STORE / SKY_LIVE_STORE_PATH env variables. Falls back to
// memory. TTL defaults to 30 minutes.
func chooseStore(kind, path string, ttl time.Duration) SessionStore {
	if kind == "" {
		kind = os.Getenv("SKY_LIVE_STORE")
	}
	if path == "" {
		path = os.Getenv("SKY_LIVE_STORE_PATH")
	}
	if ttl == 0 {
		ttl = 30 * time.Minute
	}
	switch kind {
	case "sqlite":
		if path == "" {
			path = "sky_sessions.db"
		}
		store, err := newSQLiteStore(path, ttl)
		if err != nil {
			log.Printf("[sky.live] sqlite store unavailable (%v); falling back to memory", err)
			return newMemoryStore(ttl)
		}
		log.Printf("[sky.live] session store: sqlite @ %s (ttl=%s)", path, ttl)
		return store
	case "postgres", "postgresql":
		if path == "" {
			path = os.Getenv("DATABASE_URL")
		}
		if path == "" {
			log.Printf("[sky.live] postgres store requested but no connection string; falling back to memory")
			return newMemoryStore(ttl)
		}
		store, err := newPostgresStore(path, ttl)
		if err != nil {
			log.Printf("[sky.live] postgres store unavailable (%v); falling back to memory", err)
			return newMemoryStore(ttl)
		}
		log.Printf("[sky.live] session store: postgres (ttl=%s)", ttl)
		return store
	default:
		log.Printf("[sky.live] session store: memory (ttl=%s)", ttl)
		return newMemoryStore(ttl)
	}
}


// generateSkySessionID: 256-bit URL-safe random.
func generateSkySessionID() string {
	b := make([]byte, 32)
	if _, err := cryptoRandRead(b); err != nil {
		// Fall back to time-based; should never hit in practice.
		return fmt.Sprintf("sid-%d", time.Now().UnixNano())
	}
	return urlBase64(b)
}
