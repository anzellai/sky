package skylive_rt

import (
	"database/sql"
	"encoding/json"
	"log"
	"time"

	_ "github.com/lib/pq"
)

// PostgresStore is a session store backed by PostgreSQL.
// It implements the SessionStore interface using github.com/lib/pq.
type PostgresStore struct {
	db  *sql.DB
	ttl time.Duration
}

// NewPostgresStore connects to PostgreSQL at the given URL and initialises
// the sessions table. A background goroutine periodically removes expired sessions.
// url format: "postgres://user:pass@host:port/dbname?sslmode=disable"
func NewPostgresStore(url string, ttl time.Duration) (*PostgresStore, error) {
	db, err := sql.Open("postgres", url)
	if err != nil {
		return nil, err
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}

	// Create the sessions table if it does not already exist.
	const createTable = `
		CREATE TABLE IF NOT EXISTS sky_sessions (
			sid           TEXT PRIMARY KEY,
			model_json    TEXT    NOT NULL,
			prev_view_html TEXT   NOT NULL,
			created_at    BIGINT NOT NULL,
			last_seen     BIGINT NOT NULL,
			version       BIGINT NOT NULL DEFAULT 0
		)
	`
	// Add version column if upgrading from older schema
	db.Exec("ALTER TABLE sky_sessions ADD COLUMN version BIGINT NOT NULL DEFAULT 0")
	if _, err := db.Exec(createTable); err != nil {
		db.Close()
		return nil, err
	}

	// Create index on last_seen for cleanup queries
	db.Exec("CREATE INDEX IF NOT EXISTS idx_sky_sessions_last_seen ON sky_sessions (last_seen)")

	store := &PostgresStore{db: db, ttl: ttl}
	go store.cleanup()
	return store, nil
}

func (s *PostgresStore) Get(sid string) (*Session, bool) {
	row := s.db.QueryRow(
		"SELECT model_json, prev_view_html, created_at, last_seen, version FROM sky_sessions WHERE sid = $1",
		sid,
	)

	var modelJSON string
	var prevViewHTML string
	var createdAt int64
	var lastSeen int64
	var version int64

	if err := row.Scan(&modelJSON, &prevViewHTML, &createdAt, &lastSeen, &version); err != nil {
		return nil, false
	}

	// Deserialize model
	var model map[string]any
	if err := json.Unmarshal([]byte(modelJSON), &model); err != nil {
		return nil, false
	}
	fixJSONNumbers(model)

	// Deserialize previous view tree
	var prevView *VNode
	if prevViewHTML != "" {
		prevView = ParseHTML(prevViewHTML)
	}

	sess := &Session{
		Model:    model,
		PrevView: prevView,
		Created:  time.Unix(createdAt, 0),
		LastSeen: time.Unix(lastSeen, 0),
		Version:  version,
	}

	// Touch last_seen
	now := time.Now()
	sess.LastSeen = now
	s.db.Exec("UPDATE sky_sessions SET last_seen = $1 WHERE sid = $2", now.Unix(), sid)

	return sess, true
}

func (s *PostgresStore) Set(sid string, sess *Session) bool {
	now := time.Now()
	sess.LastSeen = now

	modelBytes, err := json.Marshal(sess.Model)
	if err != nil {
		log.Printf("skylive_rt: PostgresStore.Set: failed to marshal model: %v", err)
		return false
	}

	var prevViewHTML string
	if sess.PrevView != nil {
		prevViewHTML = RenderToString(sess.PrevView)
	}

	createdAt := sess.Created.Unix()
	if createdAt == 0 {
		createdAt = now.Unix()
	}

	newVersion := sess.Version + 1

	if sess.Version == 0 {
		_, err = s.db.Exec(
			`INSERT INTO sky_sessions (sid, model_json, prev_view_html, created_at, last_seen, version)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 ON CONFLICT (sid) DO NOTHING`,
			sid, string(modelBytes), prevViewHTML, createdAt, now.Unix(), newVersion,
		)
	} else {
		var result sql.Result
		result, err = s.db.Exec(
			`UPDATE sky_sessions SET model_json = $1, prev_view_html = $2, last_seen = $3, version = $4
			 WHERE sid = $5 AND version = $6`,
			string(modelBytes), prevViewHTML, now.Unix(), newVersion, sid, sess.Version,
		)
		if err == nil {
			rows, _ := result.RowsAffected()
			if rows == 0 {
				return false // version conflict
			}
		}
	}
	if err != nil {
		log.Printf("skylive_rt: PostgresStore.Set: failed to upsert session: %v", err)
		return false
	}
	sess.Version = newVersion
	return true
}

func (s *PostgresStore) Delete(sid string) {
	s.db.Exec("DELETE FROM sky_sessions WHERE sid = $1", sid)
}

func (s *PostgresStore) NewID() string {
	return generateSessionID()
}

func (s *PostgresStore) cleanup() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-s.ttl).Unix()
		s.db.Exec("DELETE FROM sky_sessions WHERE last_seen < $1", cutoff)
	}
}
