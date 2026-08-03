// live_store.go — SessionStore abstraction + memory / SQLite / Postgres /
// Redis implementations. The store persists the raw Go `any` model +
// rendered VNode tree between HTTP requests for the same session id.
//
// Wire protocol: every Session is encoded with encoding/gob. Gob handles
// arbitrary Go values without needing a schema, including the compiled
// ADT struct types. Concrete types seen in one binary will always round-
// trip back to the same concrete types because the gob stream embeds
// the type descriptors on first encode.
//
// Selected via sky.toml (or Live.app config):
//   store     = "memory" | "sqlite" | "postgres" | "redis"
//   storePath = "sessions.db"                    (sqlite)
//            = "postgres://user:pass@host/db"    (postgres)
//            = "redis://:password@host:6379/0"   (redis; or bare "host:6379")
//   ttl       = "30m"                            (Go duration or bare-int seconds; default 30m)

package rt

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/gob"
	"errors"
	"fmt"
	"log"
	"os"
	"reflect"
	"sky-app/rt/telemetry"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
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

// GobRegisterTypeGraph walks a TYPE definition tree (not a value) and
// registers every concrete Sky wrapper instantiation it finds. Unlike
// the value-walker, this catches SkyMaybe[User_R] even when the init
// model has Nothing (which is SkyMaybe[any]{Tag:1} at runtime).
func GobRegisterTypeGraph(root reflect.Type) {
	gobRegMu.Lock()
	defer gobRegMu.Unlock()
	seen := map[reflect.Type]bool{}
	walkGobType(root, seen)
}

// RegisterSkyGobTypes registers a whole-binary list of Sky-minted zero-values
// (every record-alias struct + ADT constructor struct) with gob, so a session
// that stores one of these in an `any`-typed Model field both ENCODES and, after
// a process restart, DECODES. The compiler emits this into main.go's boot so
// EVERY process has the registration independent of the init value's reachable
// type graph — an `any` field that is nil at init hides its future concrete type
// from the boot walk (its static type is interface{}), and gob's name→type
// registry is process-local, so the decoding process must have registered the
// type itself. Idempotent + nil-safe; walks each value's full type graph under
// the shared gob mutex, and (via tryGobRegisterVal) never caches a failed
// registration.
func RegisterSkyGobTypes(vals []any) {
	gobRegMu.Lock()
	defer gobRegMu.Unlock()
	seen := map[reflect.Type]bool{}
	for _, v := range vals {
		if v == nil {
			continue
		}
		walkGobType(reflect.TypeOf(v), seen)
	}
}

// tryGobRegisterVal registers v's type with gob, recovering from gob.Register's
// panic (a conflicting name→type, or an unnamed type). Returns true ONLY if
// registration succeeded. Callers must set their `gobRegistered[t]` dedup flag
// on true only — otherwise a panicked (failed) registration gets cached as
// "done", so the type is never actually registered, every later encodeSession
// that names it fails, and the session silently drops to memory-only. Must be
// called under gobRegMu (gob.Register mutates a process-global registry and is
// not concurrent-safe).
func tryGobRegisterVal(v any) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			ok = false
		}
	}()
	gob.Register(v)
	return true
}

func walkGobType(t reflect.Type, seen map[reflect.Type]bool) {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if seen[t] {
		return
	}
	seen[t] = true

	if isSkyWrapperType(t) && !gobRegistered[t] {
		if tryGobRegisterVal(reflect.Zero(t).Interface()) {
			gobRegistered[t] = true
		}
	}

	if t.PkgPath() != "" && t.Kind() == reflect.Struct && !gobRegistered[t] {
		if tryGobRegisterVal(reflect.Zero(t).Interface()) {
			gobRegistered[t] = true
		}
	}

	switch t.Kind() {
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			walkGobType(t.Field(i).Type, seen)
		}
	case reflect.Slice, reflect.Array:
		walkGobType(t.Elem(), seen)
	case reflect.Map:
		walkGobType(t.Key(), seen)
		walkGobType(t.Elem(), seen)
	}
}

func isSkyWrapperType(t reflect.Type) bool {
	name := t.Name()
	return strings.HasPrefix(name, "SkyMaybe[") ||
		strings.HasPrefix(name, "SkyResult[") ||
		strings.HasPrefix(name, "SkyTuple2[") ||
		strings.HasPrefix(name, "SkyTuple3[") ||
		strings.HasPrefix(name, "SkyTask[")
}

// Audit P2-5: pre-register the Sky-canonical container types so
// gob can encode them at an `any` interface boundary. Without
// these, encoding a `map[string]any` top-level model (the typical
// Sky.Live shape pre-typed-codegen) fails with "gob: type not
// registered for interface: map[string]interface {}".
func init() {
	gob.Register(map[string]any{})
	gob.Register([]any{})
	gob.Register(SkyMaybe[any]{})
	gob.Register(SkyResult[any, any]{})
	gob.Register(SkyTuple2{})
	gob.Register(SkyTuple3{})
}

func walkGob(v reflect.Value) {
	walkGobSeen(v, make(map[reflect.Type]bool, 16), 0)
}

// walkGobSeen: depth-bounded + type-set guarded. Sky-side Model values
// sometimes carry opaque FFI handles (`*sql.DB`, `*SkyDb`, Stripe
// customers, Firestore clients). Their internal fields form pointer
// cycles — `*sql.DB.connector → *pool → *DB` and so on — so a naïve
// recursive walk overflows the goroutine stack. Skip types we've
// already visited and cap recursion at 64 levels so adversarial or
// accidental cycles can't crash the server during session persistence.
func walkGobSeen(v reflect.Value, seenTypes map[reflect.Type]bool, depth int) {
	if !v.IsValid() || depth > 64 {
		return
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Ptr:
		if !v.IsNil() {
			walkGobSeen(v.Elem(), seenTypes, depth+1)
		}
	case reflect.Struct:
		t := v.Type()
		if seenTypes[t] {
			return
		}
		seenTypes[t] = true
		if t.PkgPath() != "" && !gobRegistered[t] {
			if tryGobRegisterVal(reflect.New(t).Elem().Interface()) {
				gobRegistered[t] = true
			}
		}
		for i := 0; i < v.NumField(); i++ {
			walkGobSeen(v.Field(i), seenTypes, depth+1)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walkGobSeen(v.Index(i), seenTypes, depth+1)
		}
	case reflect.Map:
		it := v.MapRange()
		for it.Next() {
			walkGobSeen(it.Value(), seenTypes, depth+1)
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
//
// Audit P3-4: used for Live app config (Store backend name, StorePath).
// Sky type system guarantees these are String at the source level; we
// still fall back to %v if the boundary hands us a non-string so a
// mis-encoded config surfaces as a visibly-wrong path rather than a
// runtime panic. No secret material flows here.
func stringField(cfg any, name string) string {
	v := Field(cfg, name)
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// parseTTL — resolve a TTL value from env > sky.toml > default in
// precedence order. Each layer accepts EITHER a Go-duration string
// ("30m", "24h", "1h30m") OR a bare integer interpreted as seconds.
// Empty or unparseable values fall through to the next layer.
//
// History: the pre-fix implementation read only the env var AND
// accepted only bare-integer seconds via strconv.Atoi.  So both
// `SKY_LIVE_TTL=24h` AND any `ttl = "24h"` in sky.toml's [live]
// section silently fell back to the 30-minute default — at odds
// with the documented `30m`-style default in CLAUDE.md.  This
// helper makes the documented shape the canonical one while
// preserving bare-integer-seconds for backward compatibility.
func parseTTL(envVal, tomlVal string, def time.Duration) time.Duration {
	for _, raw := range []string{envVal, tomlVal} {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		// Duration-string form first (more specific — "24h" parses
		// as duration, NOT as the integer 24).
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			return d
		}
		// Bare-integer fallback — interpreted as seconds.
		if secs, err := strconv.Atoi(s); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
		// Unparseable at this layer — fall through to the next.
	}
	return def
}

// SessionStore: common interface for the three backends. The runtime
// reads/writes via `Get`, `Set`, `Delete`, and generates IDs via
// `NewID`. Callers are responsible for per-session locking (the runtime
// uses a SessionLocker to serialise event handling + SSE writes).
//
// Cycle 3 P46 (pub/sub) — SessionStore also exposes a Broker accessor.
// The v0.15.x default impl returns an in-process *topicRegistry shared
// across all sessions on the app; v0.16+ cross-process backends (Redis
// Pub/Sub, Cloud Pub/Sub, Postgres LISTEN/NOTIFY, NATS JetStream — see
// docs/skylive/pubsub-design.md §11.2.5) override Broker() to return
// their own implementation. The Broker interface lives in
// runtime-go/rt/live_topics.go.
type SessionStore interface {
	Get(sid string) (*liveSession, bool)
	Set(sid string, sess *liveSession)
	Delete(sid string)
	NewID() string
	Close() error
	// Broker returns the pub/sub broker bound to this store. v0.15.x
	// default: in-process *topicRegistry. Future cross-process
	// backends override.
	Broker() Broker
	// Ping reports store health for /_sky/readyz. Durable backends ping
	// the underlying DB/client (a dead backend → readyz 503 so the
	// orchestrator stops routing to a broken replica); the in-memory
	// store is always healthy and returns nil. Wired in chooseStore via
	// RegisterReadinessProbe — the fix for the "readyz lies while the
	// store is down / silently fell back to memory" class.
	Ping() error
}

// ═════════════════════════════════════════════════════════════════════
// Memory store — default; in-process, lost on restart.
// ═════════════════════════════════════════════════════════════════════

type memoryStore struct {
	mu       sync.RWMutex
	sessions map[string]*liveSession
	ttl      time.Duration
	stop     chan struct{}
	// broker — pub/sub registry. Cycle 3 P46. Default in-process
	// *topicRegistry; future cross-process tiers swap the pointer.
	// Stored as the Broker interface so test fixtures + memory-store
	// alternatives slot in via the SAME field.
	broker Broker
}

func newMemoryStore(ttl time.Duration) *memoryStore {
	s := &memoryStore{
		sessions: map[string]*liveSession{},
		ttl:      ttl,
		stop:     make(chan struct{}),
		broker:   newTopicRegistry(0),
	}
	go s.cleanupLoop()
	return s
}

func (s *memoryStore) Broker() Broker { return s.broker }

// Ping — the in-memory store is always healthy.
func (s *memoryStore) Ping() error { return nil }

func (s *memoryStore) Get(sid string) (*liveSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[sid]
	if ok {
		// Task #326: atomic.Int64 store keeps Get race-free under the
		// RLock-only gate. Two concurrent Get calls used to race on the
		// `sess.lastSeen` struct field (visible under `go test -race
		// -run TestConcurrentEventsSerialise`); the atomic field
		// closes that without escalating Get to a write lock.
		sess.touchLastSeen()
	}
	return sess, ok
}

func (s *memoryStore) Set(sid string, sess *liveSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess.touchLastSeen()
	s.sessions[sid] = sess
}

func (s *memoryStore) Delete(sid string) {
	s.mu.Lock()
	sess := s.sessions[sid]
	delete(s.sessions, sid)
	s.mu.Unlock()
	// Cycle 3 P36 / Gap C4: signal terminal teardown OUTSIDE the
	// store lock so any Time.every / runPerformBody goroutine that
	// is currently blocked on `sess.mu` can resolve (close is
	// idempotent via doneOnce so concurrent Delete + cleanupLoop
	// can't double-close).
	if sess != nil {
		sess.markDone()
	}
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
			// Cycle 3 P36 / Gap C4: collect expired sessions under
			// the lock, but signal their terminal teardown OUTSIDE
			// the lock — markDone is fast (a sync.Once gate + a
			// close on an unbuffered channel) but conceptually it
			// hands control to whatever goroutines are blocked on
			// `sess.done`, and we don't want to hold `s.mu` while
			// those resume.
			s.mu.Lock()
			var expired []*liveSession
			for id, sess := range s.sessions {
				if now.Sub(sess.lastSeenTime()) > s.ttl {
					expired = append(expired, sess)
					delete(s.sessions, id)
				}
			}
			s.mu.Unlock()
			for _, sess := range expired {
				sess.markDone()
			}
		}
	}
}

// ═════════════════════════════════════════════════════════════════════
// SQLite store — persistent sessions on disk, zero-op setup.
// Uses modernc.org/sqlite (pure Go, no CGO).
// ═════════════════════════════════════════════════════════════════════

type sqliteStore struct {
	db   *sql.DB
	ttl  time.Duration
	stop chan struct{}
	// memCache is a pointer cache so sessions that fail to gob-encode
	// (anonymous struct types the Sky compiler emits for records) still
	// behave correctly within a single process. Restart forgets them,
	// which is the same trade-off the memoryStore makes.
	memMu    sync.RWMutex
	memCache map[string]*liveSession
	// broker — pub/sub registry. Cycle 3 P46.
	broker Broker
}

func (s *sqliteStore) Broker() Broker { return s.broker }

// Ping — health-check the sqlite handle for /_sky/readyz.
func (s *sqliteStore) Ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.db.PingContext(ctx)
}

func newSQLiteStore(path string, ttl time.Duration) (*sqliteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// SQLite concurrency defaults — the same three-part config Db.connect
	// applies (v0.17.10). WITHOUT these the session store was fragile under
	// the Sky.Live load pattern: every request reads + writes the session, so
	// the default unbounded pool opened many modernc connections against one
	// WAL file. Under navigation load those contend on the WAL writer lock and
	// stall (the "page transition hangs" symptom); at open time a lock held by
	// a still-exiting previous process surfaced as the bare `unable to open
	// database file (14)` and dropped the WHOLE store to memory, silently
	// losing session persistence.
	//
	//   MaxOpenConns=1 — SQLite has a single global writer; serialising on one
	//   connection removes the multi-conn WAL contention entirely.
	//   busy_timeout=5000 — wait out a transient lock (fast restart, a
	//   concurrent write) instead of erroring immediately.
	//   synchronous=NORMAL — safe under WAL, cheaper commits.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, pErr := db.Exec(pragma); pErr != nil {
			// A PRAGMA failure must NOT nuke the whole store — a path that
			// rejects WAL (NFS/SMB, :memory:) still works in rollback-journal
			// mode. Warn and carry on; only a genuine open/CREATE failure below
			// falls back to memory.
			Log_warn("live session store: " + pragma + " failed: " + pErr.Error())
		}
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
		broker:   newTopicRegistry(0),
	}
	go s.cleanupLoop()
	return s, nil
}

func (s *sqliteStore) Get(sid string) (*liveSession, bool) {
	// Memory cache hit: current-process sessions we couldn't encode.
	s.memMu.RLock()
	if sess, ok := s.memCache[sid]; ok {
		s.memMu.RUnlock()
		// L4: a read slides the TTL, matching memoryStore.Get. Without this,
		// only writes (events) kept a DB-backed session alive, so a read-heavy
		// idle session (dashboard, SSE-only) got evicted under a live view.
		sess.touchLastSeen()
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
	// Task #326: atomic.Int64 store — safe to write from any goroutine
	// without holding s.memMu (sibling memCache readers under RLock no
	// longer race on the field).
	sess.touchLastSeen()
	// Always keep the live pointer in memory so intra-process requests
	// find the session even when the value isn't gob-encodable.
	s.memMu.Lock()
	s.memCache[sid] = sess
	s.memMu.Unlock()
	blob, err := encodeSession(sess)
	if err != nil {
		// Log ONCE per session (not every event) — the alternative is
		// spamming logs for every onInput keystroke.
		telemetry.Default().Inc("sky_live_session_encode_fail_total", map[string]string{"store": "sqlite"})
		logOnce("sqlite-encode-"+sid, func() {
			log.Printf("[sky.live] sqlite: session %s not persistable (%v); using in-memory fallback", sid, err)
		})
		return
	}
	_, err = s.db.Exec(`
		INSERT INTO sky_sessions (sid, blob, last_seen) VALUES (?, ?, ?)
		ON CONFLICT(sid) DO UPDATE SET blob=excluded.blob, last_seen=excluded.last_seen`,
		sid, blob, sess.lastSeenTime().Unix())
	if err != nil {
		log.Printf("[sky.live] sqlite: failed to save session %s: %v", sid, err)
	}
}

func (s *sqliteStore) Delete(sid string) {
	s.memMu.Lock()
	sess := s.memCache[sid]
	delete(s.memCache, sid)
	s.memMu.Unlock()
	// Cycle 3 P36 / Gap C4: signal terminal teardown for the in-memory
	// pointer so any subscription goroutine bound to it exits. The
	// blob in SQLite owns no goroutines (it's just a checkpoint), so
	// only the live pointer needs the signal.
	if sess != nil {
		sess.markDone()
	}
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
			// Cycle 3 P36 / Gap C4: also evict the matching memCache
			// entries and signal terminal teardown. The memCache holds
			// the LIVE pointer (the one that owns Time.every goroutines);
			// without this, a session whose blob expires on disk still
			// keeps its in-process pointer + subscription goroutines alive
			// for the lifetime of the process.
			cutoff := now.Add(-s.ttl)
			s.memMu.Lock()
			var expired []*liveSession
			for sid, sess := range s.memCache {
				if sess.lastSeenTime().Before(cutoff) {
					expired = append(expired, sess)
					delete(s.memCache, sid)
				}
			}
			s.memMu.Unlock()
			for _, sess := range expired {
				sess.markDone()
			}
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
	// broker — pub/sub registry. Cycle 3 P46.
	broker Broker
}

func (s *postgresStore) Broker() Broker { return s.broker }

// Ping — health-check the postgres pool for /_sky/readyz.
func (s *postgresStore) Ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.db.PingContext(ctx)
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
		broker:   newTopicRegistry(0),
	}
	go s.cleanupLoop()
	return s, nil
}

func (s *postgresStore) Get(sid string) (*liveSession, bool) {
	s.memMu.RLock()
	if sess, ok := s.memCache[sid]; ok {
		s.memMu.RUnlock()
		sess.touchLastSeen() // L4: a read slides the TTL, matching memoryStore.Get
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
	sess.touchLastSeen()
	s.memMu.Lock()
	s.memCache[sid] = sess
	s.memMu.Unlock()
	blob, err := encodeSession(sess)
	if err != nil {
		telemetry.Default().Inc("sky_live_session_encode_fail_total", map[string]string{"store": "postgres"})
		logOnce("pg-encode-"+sid, func() {
			log.Printf("[sky.live] postgres: session %s not persistable (%v); using in-memory fallback", sid, err)
		})
		return
	}
	_, err = s.db.Exec(`
		INSERT INTO sky_sessions (sid, blob, last_seen) VALUES ($1, $2, $3)
		ON CONFLICT (sid) DO UPDATE SET blob = EXCLUDED.blob, last_seen = EXCLUDED.last_seen`,
		sid, blob, sess.lastSeenTime().Unix())
	if err != nil {
		log.Printf("[sky.live] postgres: failed to save session %s: %v", sid, err)
	}
}

func (s *postgresStore) Delete(sid string) {
	s.memMu.Lock()
	sess := s.memCache[sid]
	delete(s.memCache, sid)
	s.memMu.Unlock()
	// Cycle 3 P36 / Gap C4: see sqliteStore.Delete for rationale.
	if sess != nil {
		sess.markDone()
	}
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
			// Cycle 3 P36 / Gap C4: also evict the matching memCache
			// entries and signal terminal teardown. See sqliteStore
			// cleanupLoop for the full rationale.
			cutoff := now.Add(-s.ttl)
			s.memMu.Lock()
			var expired []*liveSession
			for sid, sess := range s.memCache {
				if sess.lastSeenTime().Before(cutoff) {
					expired = append(expired, sess)
					delete(s.memCache, sid)
				}
			}
			s.memMu.Unlock()
			for _, sess := range expired {
				sess.markDone()
			}
		}
	}
}

// ═════════════════════════════════════════════════════════════════════
// Redis store — multi-instance deployments (Cloud Run, ECS, k8s). Uses
// native Redis TTL for expiry, so there's no cleanup goroutine. Sessions
// are stored under key "sky:sess:<sid>" as a gob-encoded blob, the same
// wire format as SQLite/Postgres.
// ═════════════════════════════════════════════════════════════════════

type redisStore struct {
	client   *redis.Client
	ttl      time.Duration
	ctx      context.Context
	cancel   context.CancelFunc
	memMu    sync.RWMutex
	memCache map[string]*liveSession
	// broker — pub/sub registry. Cycle 3 P46. v0.15.x: still
	// in-process *topicRegistry; a v0.16+ RedisPubsubBroker would
	// override this field to use `redis.PSubscribe` for cross-process
	// fan-out (design doc §11.2.5 tier 1).
	broker Broker
}

func (s *redisStore) Broker() Broker { return s.broker }

// Ping — health-check the redis client for /_sky/readyz.
func (s *redisStore) Ping() error {
	ctx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
	defer cancel()
	return s.client.Ping(ctx).Err()
}

// redisKey: namespace session ids under a fixed prefix so the Redis
// instance can be shared with other workloads.
func redisKey(sid string) string { return "sky:sess:" + sid }

// newRedisStore: accepts either a full Redis URL
// ("redis://:password@host:6379/0") or a bare "host:port" address.
// Pings before returning so a misconfigured URL surfaces as a startup
// error rather than silently falling back to memory on first write.
func newRedisStore(addr string, ttl time.Duration) (*redisStore, error) {
	var opt *redis.Options
	if strings.Contains(addr, "://") {
		parsed, err := redis.ParseURL(addr)
		if err != nil {
			return nil, fmt.Errorf("redis: parse URL: %w", err)
		}
		opt = parsed
	} else {
		opt = &redis.Options{Addr: addr}
	}
	client := redis.NewClient(opt)
	ctx, cancel := context.WithCancel(context.Background())
	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pingCancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		cancel()
		_ = client.Close()
		return nil, fmt.Errorf("redis: ping: %w", err)
	}
	return &redisStore{
		client:   client,
		ttl:      ttl,
		ctx:      ctx,
		cancel:   cancel,
		memCache: map[string]*liveSession{},
		// Phase 2: multi-instance deploys use the cross-instance broker
		// so Cmd.publish / Sub.subscribeTopic (and same-user cross-session
		// sync) fan out across every instance, not just the publisher's.
		// Shares this store's *redis.Client (ownsClient=false) — the
		// store's Close owns the client. SKY_LIVE_BROKER=inprocess forces
		// the in-process registry back (escape hatch); see chooseBroker.
		broker: brokerForRedisStore(client),
	}, nil
}

func (s *redisStore) Get(sid string) (*liveSession, bool) {
	s.memMu.RLock()
	if sess, ok := s.memCache[sid]; ok {
		s.memMu.RUnlock()
		sess.touchLastSeen() // L4: a read slides the TTL, matching memoryStore.Get
		return sess, true
	}
	s.memMu.RUnlock()
	blob, err := s.client.Get(s.ctx, redisKey(sid)).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			log.Printf("[sky.live] redis: get session %s: %v", sid, err)
		}
		return nil, false
	}
	sess, err := decodeSession(blob)
	if err != nil {
		log.Printf("[sky.live] redis: failed to decode session %s: %v", sid, err)
		return nil, false
	}
	// Touch TTL so an active session doesn't expire mid-conversation.
	if err := s.client.Expire(s.ctx, redisKey(sid), s.ttl).Err(); err != nil {
		log.Printf("[sky.live] redis: refresh TTL for %s: %v", sid, err)
	}
	return sess, true
}

func (s *redisStore) Set(sid string, sess *liveSession) {
	sess.touchLastSeen()
	// Keep an in-process pointer so values that fail gob encoding
	// (closures, channels) still work within this instance. They won't
	// survive a restart or cross-instance routing, which is the same
	// trade-off SQLite/Postgres make.
	s.memMu.Lock()
	s.memCache[sid] = sess
	s.memMu.Unlock()
	blob, err := encodeSession(sess)
	if err != nil {
		telemetry.Default().Inc("sky_live_session_encode_fail_total", map[string]string{"store": "redis"})
		logOnce("redis-encode-"+sid, func() {
			log.Printf("[sky.live] redis: session %s not persistable (%v); using in-memory fallback", sid, err)
		})
		return
	}
	if err := s.client.Set(s.ctx, redisKey(sid), blob, s.ttl).Err(); err != nil {
		log.Printf("[sky.live] redis: failed to save session %s: %v", sid, err)
	}
}

func (s *redisStore) Delete(sid string) {
	s.memMu.Lock()
	sess := s.memCache[sid]
	delete(s.memCache, sid)
	s.memMu.Unlock()
	// Cycle 3 P36 / Gap C4: signal terminal teardown for the in-memory
	// pointer. Redis uses native TTL (no cleanupLoop), so per-key
	// expiry races with Redis itself; in-process memCache eviction is
	// what frees the Go-side goroutines.
	if sess != nil {
		sess.markDone()
	}
	if err := s.client.Del(s.ctx, redisKey(sid)).Err(); err != nil {
		log.Printf("[sky.live] redis: delete session %s: %v", sid, err)
	}
}

func (s *redisStore) NewID() string { return generateSkySessionID() }

func (s *redisStore) Close() error {
	s.cancel()
	// Close the broker BEFORE the client — the cross-instance broker's
	// Pub/Sub connection rides this client; tearing it down first stops
	// the receive loop cleanly. The broker shares the client
	// (ownsClient=false) so it won't double-close it.
	if s.broker != nil {
		_ = s.broker.Close()
	}
	return s.client.Close()
}

// ═════════════════════════════════════════════════════════════════════
// Helpers
// ═════════════════════════════════════════════════════════════════════

// storableSession: gob-friendly subset of liveSession. Channels, mutexes,
// and handlers (which contain live goroutine-dispatching closures) don't
// round-trip, so we only persist the Model + the seq counters. On Get
// we rebuild the missing runtime bits.
//
// OutSeq must persist: the client tracks the largest seq it has applied
// (__skyLastAppliedSeq) and silently drops any frame with seq ≤ that.
// Without this field, after a server restart the new process's localSeq
// would reset to 0 and every frame would be classified stale by the
// client — including the reconnect-resync push that's supposed to
// refresh stale-view DOM after `sky watch` rebuilds. By persisting the
// counter, the new process continues climbing past whatever the client
// last saw, so resync frames register as fresh and apply.
//
// Cycle 3 P47 (pub/sub prereq 2 — see docs/skylive/pubsub-design.md
// §3.2): liveSession's in-memory field has been renamed outSeq →
// localSeq, but the GOB-persisted name MUST stay OutSeq so existing
// SQLite / Postgres / Redis / Firestore session blobs decode cleanly.
// globalSeq is app-wide (atomic.Int64 on liveApp) — NOT serialised
// per-session; on restart the new process restarts globalSeq from 0,
// and the client's __skyLastGlobalSeq guard is benign (a fresh
// broadcast cycle is its own monotonic series).
type storableSession struct {
	Model any
	// PrevTree excluded: VNode.Events holds function values which
	// gob can't encode. The tree is rebuilt from view(model) on
	// restore — handleEvent already handles empty prevTree.
	LastSeen time.Time
	OutSeq   int64
	// v0.16.5 #493 — auth identity stashed at mint time by
	// dispatchRoot from IdentityFromContext(r.Context()). Round-trips
	// through gob so DB-backed stores survive restart/replica
	// reshuffles with identity intact. Existing persisted sessions
	// (pre-v0.16.5) decode with the zero ConsoleIdentity + false —
	// matches "no gate ran" semantics, no migration required.
	Identity      ConsoleIdentity
	IdentityValid bool
	// v0.19 — persist Std.Analytics identity (consent posture + anon/user id) so
	// a DB-backed store keeps an identified analytics user across restart /
	// replica reshuffle, matching the Identity round-trip above. HasAnalytics
	// distinguishes "analytics was used" from "never used"; pre-v0.19 blobs decode
	// with HasAnalytics=false → state recreated fresh on first use, no migration.
	HasAnalytics     bool
	AnalyticsConsent int
	// AnalyticsConsentExplicit: the consent above was set by the app (setConsent),
	// not just the framework default. On restore, a non-explicit consent follows
	// the CURRENT default so a default change reaches persisted sessions (see
	// restoreAnalyticsState). Pre-flag blobs decode false → adopt the default.
	AnalyticsConsentExplicit bool
	AnalyticsAnonID          string
	AnalyticsUserID          string
}

func encodeSession(s *liveSession) ([]byte, error) {
	// Audit P2-5: validate the value graph against the session-safe
	// whitelist BEFORE handing it to gob. Gob silently skips func /
	// chan / unexported fields, so a model that contains a closure,
	// a channel, or an FFI opaque handle would round-trip as
	// garbage on the next load — fine in the in-memory store which
	// keeps values by reference, but corrupting for SQLite /
	// Postgres / Redis deployments. Rejecting up front gives a
	// diagnosable error before bad data lands in the store.
	if err := validateSessionValue(s.model, "model"); err != nil {
		return nil, err
	}
	// Walk the value graph to discover + register every concrete struct
	// type at an interface boundary. Safe to call repeatedly — we cache
	// registered types.
	gobRegisterAll(s.model)
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	blob := storableSession{
		Model:         s.model,
		LastSeen:      s.lastSeenTime(),
		OutSeq:        s.localSeq,
		Identity:      s.identity,
		IdentityValid: s.identityValid,
	}
	if s.analytics != nil {
		c, anon, user := s.analytics.snapshot()
		blob.HasAnalytics = true
		blob.AnalyticsConsent = int(c)
		blob.AnalyticsConsentExplicit = s.analytics.consentExplicit()
		blob.AnalyticsAnonID = anon
		blob.AnalyticsUserID = user
	}
	if err := enc.Encode(blob); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// validateSessionValue walks v recursively and rejects kinds that
// gob can't meaningfully persist for Sky programs:
//   - reflect.Func    — closures don't round-trip; the new instance
//     would decode as nil and crash on first call.
//   - reflect.Chan    — runtime-only.
//   - reflect.UnsafePointer — never safe to persist.
//   - unexported struct fields containing any of the above.
//
// Accepted: numeric primitives, bool, string, slice/array/map/struct
// whose elements are themselves session-safe, pointer to the same,
// and typed nil interface values.
//
// Returns nil for the whole-graph-safe case; otherwise a descriptive
// error naming the offending path (e.g. "model.Handlers[0].Fn: func").
func validateSessionValue(v any, path string) error {
	return walkValidateGob(reflect.ValueOf(v), path, make(map[uintptr]bool))
}

func walkValidateGob(v reflect.Value, path string, seen map[uintptr]bool) error {
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Func:
		return fmt.Errorf("session value at %s is a func — not session-safe (closures can't round-trip)", path)
	case reflect.Chan:
		return fmt.Errorf("session value at %s is a chan — not session-safe", path)
	case reflect.UnsafePointer:
		return fmt.Errorf("session value at %s is unsafe.Pointer — not session-safe", path)
	case reflect.Ptr, reflect.Interface:
		if v.IsNil() {
			return nil
		}
		// Ptr: break cycles.
		if v.Kind() == reflect.Ptr {
			p := v.Pointer()
			if seen[p] {
				return nil
			}
			seen[p] = true
		}
		return walkValidateGob(v.Elem(), path, seen)
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			childPath := path + "." + t.Field(i).Name
			if err := walkValidateGob(v.Field(i), childPath, seen); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if err := walkValidateGob(v.Index(i), fmt.Sprintf("%s[%d]", path, i), seen); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		it := v.MapRange()
		for it.Next() {
			k := fmt.Sprintf("%v", it.Key().Interface())
			if err := walkValidateGob(it.Value(), path+"["+k+"]", seen); err != nil {
				return err
			}
		}
		return nil
	}
	// Primitives (int*, uint*, float*, bool, string) are always OK.
	return nil
}

func decodeSession(blob []byte) (*liveSession, error) {
	var st storableSession
	if err := gob.NewDecoder(bytes.NewReader(blob)).Decode(&st); err != nil {
		return nil, err
	}
	sess := &liveSession{
		model:         st.Model,
		identity:      st.Identity,
		identityValid: st.IdentityValid,
		prevTree:      nil, // rebuilt on next render via handleEvent
		handlers:      map[string]any{},
		sseCh:         make(chan sseFrame, sseChanBuffer),
		cancelSub:     make(chan struct{}),
		// Cycle 3 P36 / Gap C4: provision the terminal-teardown
		// channel so persistent-store rehydrates can also be cleanly
		// stopped by markDone when the session is later evicted.
		done:     make(chan struct{}),
		localSeq: st.OutSeq,
	}
	// Task #326: lastSeen is now an atomic.Int64 — can't be set in a
	// struct literal, so seed it after construction.
	sess.setLastSeenTime(st.LastSeen)
	// v0.19 — rehydrate analytics identity so an identified user survives a
	// restart / replica reshuffle on a DB-backed store.
	if st.HasAnalytics {
		sess.analytics = restoreAnalyticsState(st.AnalyticsConsent, st.AnalyticsConsentExplicit, st.AnalyticsAnonID, st.AnalyticsUserID)
	}
	return sess, nil
}

// chooseStore: honour a sky.toml Live-store override or the
// <PREFIX>_LIVE_STORE / <PREFIX>_LIVE_STORE_PATH env variables.
// Falls back to memory. TTL defaults to 30 minutes. Standard
// fallbacks DATABASE_URL / REDIS_URL are NOT prefixed (they're not
// in Sky's namespace) — they're consulted only when the
// Sky-prefixed override is unset.
// ── Durable-store connect resilience (fix: silent memory fallback) ──────────
//
// A store the user EXPLICITLY configured (postgres/sqlite/redis) that fails to
// connect at boot must NOT silently degrade to an in-memory store — that turns
// a transient boot race or a misconfig into "sessions randomly die on every
// restart", which is invisible (the app looks healthy; healthz/readyz stay
// green). Instead:
//  1. retry with bounded backoff, to ride out the common systemd/container
//     boot race (`After=postgresql` waits for the unit to START, not to
//     ACCEPT connections);
//  2. if still unreachable, FAIL LOUD in production (refuse to start so the
//     orchestrator restarts + the operator sees it) — never a silent memory
//     fallback. In dev (ENV unset/dev/local) fall back to memory with a loud
//     warning so `sky run` works without a DB.
//
// `store=memory` (or unset) is unaffected — memory IS the contract there, and
// `SKY_LIVE_STORE=memory` is the explicit opt-in for memory-in-prod.
var (
	storeConnectAttempts = 5                      // rides a typical DB warmup
	storeConnectBaseWait = 500 * time.Millisecond // 0.5,1,2,4 → ~7.5s total
	storeConnectMaxWait  = 4 * time.Second
	// storeFatalf is the fail-loud action; overridable in tests so the
	// production path can be asserted without exiting the test process.
	storeFatalf = log.Fatalf
	// storeSleep is the retry backoff sleep; overridable in tests.
	storeSleep = time.Sleep
)

// connectStoreWithRetry calls mk up to storeConnectAttempts times with
// exponential backoff, returning the first success or the last error.
func connectStoreWithRetry(kind string, mk func() (SessionStore, error)) (SessionStore, error) {
	wait := storeConnectBaseWait
	var last error
	for attempt := 1; attempt <= storeConnectAttempts; attempt++ {
		store, err := mk()
		if err == nil {
			if attempt > 1 {
				log.Printf("[sky.live] %s store connected on attempt %d/%d", kind, attempt, storeConnectAttempts)
			}
			return store, nil
		}
		last = err
		if attempt < storeConnectAttempts {
			log.Printf("[sky.live] %s store connect attempt %d/%d failed (%v); retrying in %s",
				kind, attempt, storeConnectAttempts, err, wait)
			storeSleep(wait)
			if wait *= 2; wait > storeConnectMaxWait {
				wait = storeConnectMaxWait
			}
		}
	}
	return nil, last
}

// failDurableStore handles an explicitly-configured durable store that stayed
// unreachable after retries. Production → FATAL (refuse to start). Dev → loud
// WARN + memory fallback so a DB-less `sky run` still works.
func failDurableStore(kind string, err error, ttl time.Duration) SessionStore {
	if productionFromEnv() {
		storeFatalf("[sky.live] FATAL: session store %q is configured but unreachable "+
			"after %d attempts (%v). Refusing to start with a silent in-memory fallback in "+
			"production — sessions would be lost on every restart. Fix the connection (check the "+
			"connection string and that the database accepts connections), or set "+
			"SKY_LIVE_STORE=memory to opt in to the in-memory store deliberately.",
			kind, storeConnectAttempts, err)
		// storeFatalf is log.Fatalf in prod (never returns); a test override may
		// return, so fall through to a memory store to keep a valid value.
	}
	log.Printf("┌─ [sky.live] WARNING ────────────────────────────────────────")
	log.Printf("│ session store %q unreachable (%v)", kind, err)
	log.Printf("│ DEV fallback → in-memory sessions: lost on restart, single-instance only.")
	log.Printf("│ In PRODUCTION (ENV set) this is a HARD failure — the app refuses to start.")
	log.Printf("└─────────────────────────────────────────────────────────────")
	return newMemoryStore(ttl)
}

func chooseStore(kind, path string, ttl time.Duration) SessionStore {
	if kind == "" {
		kind = skyGetenv("LIVE_STORE")
	}
	if path == "" {
		path = skyGetenv("LIVE_STORE_PATH")
	}
	if ttl == 0 {
		ttl = 30 * time.Minute
	}
	switch kind {
	case "sqlite":
		if path == "" {
			path = "sky_sessions.db"
		}
		store, err := connectStoreWithRetry("sqlite", func() (SessionStore, error) {
			s, e := newSQLiteStore(path, ttl)
			if e != nil {
				return nil, e
			}
			return s, nil
		})
		if err != nil {
			return failDurableStore("sqlite", err, ttl)
		}
		log.Printf("[sky.live] session store: sqlite @ %s (ttl=%s)", path, ttl)
		return store
	case "postgres", "postgresql":
		if path == "" {
			path = os.Getenv("DATABASE_URL")
		}
		if path == "" {
			// An explicit postgres store with no connection string is a config
			// error, not a connect failure — fail loud in prod, not silent RAM.
			return failDurableStore("postgres",
				fmt.Errorf("no connection string (set DATABASE_URL or [live] storePath)"), ttl)
		}
		store, err := connectStoreWithRetry("postgres", func() (SessionStore, error) {
			s, e := newPostgresStore(path, ttl)
			if e != nil {
				return nil, e
			}
			return s, nil
		})
		if err != nil {
			return failDurableStore("postgres", err, ttl)
		}
		log.Printf("[sky.live] session store: postgres (ttl=%s)", ttl)
		return store
	case "redis", "valkey":
		if path == "" {
			path = os.Getenv("REDIS_URL")
		}
		if path == "" {
			path = "localhost:6379"
		}
		store, err := connectStoreWithRetry("redis", func() (SessionStore, error) {
			s, e := newRedisStore(path, ttl)
			if e != nil {
				return nil, e
			}
			return s, nil
		})
		if err != nil {
			return failDurableStore("redis", err, ttl)
		}
		log.Printf("[sky.live] session store: redis @ %s (ttl=%s)", path, ttl)
		return store
	case "bluedb":
		if path == "" {
			path = "sky_sessions.blue"
		}
		store, err := connectStoreWithRetry("bluedb", func() (SessionStore, error) {
			s, e := newBlueDBStore(path, ttl)
			if e != nil {
				return nil, e
			}
			return s, nil
		})
		if err != nil {
			return failDurableStore("bluedb", err, ttl)
		}
		log.Printf("[sky.live] session store: bluedb @ %s (ttl=%s)", path, ttl)
		return store
	case "", "memory":
		log.Printf("[sky.live] session store: memory (ttl=%s)", ttl)
		return newMemoryStore(ttl)
	default:
		// An explicitly-configured store kind we don't recognise: a typo
		// ("postgress", "psql"), or a DOCUMENTED-BUT-UNIMPLEMENTED backend
		// ("firestore" is listed as a store option in the docs + sky.toml but
		// has no branch here). Silently falling back to memory would lose every
		// session on restart and never share across replicas — the exact
		// silent-degrade class the v0.19.4 fail-loud work targets, and it slipped
		// through because that work only covered KNOWN stores that fail to
		// connect, not UNKNOWN store names. Fail loud in production; warn + memory
		// in dev.
		if productionFromEnv() {
			storeFatalf("[sky.live] FATAL: unknown session store %q — valid kinds are "+
				"memory, sqlite, postgres, redis, bluedb. Refusing to start with a silent in-memory "+
				"fallback in production (sessions would be lost on every restart and never "+
				"shared across replicas). Fix [live] store / SKY_LIVE_STORE, or set it to "+
				"\"memory\" to opt in to the in-memory store deliberately.", kind)
			// storeFatalf is log.Fatalf in prod (never returns); a test override
			// may return, so fall through to a memory store to keep a valid value.
		}
		log.Printf("┌─ [sky.live] WARNING ────────────────────────────────────────")
		log.Printf("│ unknown session store %q — valid: memory, sqlite, postgres, redis, bluedb", kind)
		log.Printf("│ DEV fallback → in-memory sessions: lost on restart, single-instance only.")
		log.Printf("│ In PRODUCTION (ENV set) this is a HARD failure — the app refuses to start.")
		log.Printf("└─────────────────────────────────────────────────────────────")
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
