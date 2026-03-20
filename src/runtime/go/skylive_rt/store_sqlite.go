package skylive_rt

import (
	"database/sql"
	"encoding/json"
	"log"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStore is a persistent session store backed by SQLite.
// It implements the SessionStore interface using modernc.org/sqlite
// (pure Go, no CGo dependency).
type SQLiteStore struct {
	db  *sql.DB
	ttl time.Duration
}

// NewSQLiteStore opens (or creates) a SQLite database at dbPath and
// initialises the sessions table. A background goroutine periodically
// removes sessions that have not been seen within ttl.
func NewSQLiteStore(dbPath string, ttl time.Duration) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	// Enable WAL mode for better concurrent read/write performance.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}

	// Create the sessions table if it does not already exist.
	const createTable = `
		CREATE TABLE IF NOT EXISTS sessions (
			sid           TEXT PRIMARY KEY,
			model_json    TEXT    NOT NULL,
			prev_view_html TEXT   NOT NULL,
			created_at    INTEGER NOT NULL,
			last_seen     INTEGER NOT NULL
		)
	`
	if _, err := db.Exec(createTable); err != nil {
		db.Close()
		return nil, err
	}

	store := &SQLiteStore{db: db, ttl: ttl}
	go store.cleanup()
	return store, nil
}

// Get loads a session from the database. The model is deserialised from
// JSON into map[string]any and the previous view tree is reconstructed
// by parsing the stored HTML via ParseHTML.
func (s *SQLiteStore) Get(sid string) (*Session, bool) {
	row := s.db.QueryRow(
		"SELECT model_json, prev_view_html, created_at, last_seen FROM sessions WHERE sid = ?",
		sid,
	)

	var modelJSON string
	var prevViewHTML string
	var createdAt int64
	var lastSeen int64

	if err := row.Scan(&modelJSON, &prevViewHTML, &createdAt, &lastSeen); err != nil {
		return nil, false
	}

	// Deserialise model
	var model map[string]any
	if err := json.Unmarshal([]byte(modelJSON), &model); err != nil {
		return nil, false
	}
	// JSON unmarshals numbers as float64; convert whole numbers back to int
	// since Sky's compiled Go code uses int type assertions.
	// Also reconstruct ADT structs from their map representation.
	fixJSONNumbers(model)

	// Deserialise previous view tree
	var prevView *VNode
	if prevViewHTML != "" {
		prevView = ParseHTML(prevViewHTML)
	}

	sess := &Session{
		Model:    model,
		PrevView: prevView,
		Created:  time.Unix(createdAt, 0),
		LastSeen: time.Unix(lastSeen, 0),
	}

	// Touch last_seen
	now := time.Now()
	sess.LastSeen = now
	s.db.Exec("UPDATE sessions SET last_seen = ? WHERE sid = ?", now.Unix(), sid)

	return sess, true
}

// Set serialises the session model as JSON and the previous view tree as
// HTML, then upserts the row into the sessions table.
func (s *SQLiteStore) Set(sid string, sess *Session) {
	now := time.Now()
	sess.LastSeen = now

	modelBytes, err := json.Marshal(sess.Model)
	if err != nil {
		log.Printf("skylive_rt: SQLiteStore.Set: failed to marshal model: %v", err)
		return
	}

	var prevViewHTML string
	if sess.PrevView != nil {
		prevViewHTML = RenderToString(sess.PrevView)
	}

	createdAt := sess.Created.Unix()
	if createdAt == 0 {
		createdAt = now.Unix()
	}

	_, err = s.db.Exec(
		`INSERT INTO sessions (sid, model_json, prev_view_html, created_at, last_seen)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(sid) DO UPDATE SET
		   model_json     = excluded.model_json,
		   prev_view_html = excluded.prev_view_html,
		   last_seen      = excluded.last_seen`,
		sid, string(modelBytes), prevViewHTML, createdAt, now.Unix(),
	)
	if err != nil {
		log.Printf("skylive_rt: SQLiteStore.Set: failed to upsert session: %v", err)
	}
}

// Delete removes a session from the database.
func (s *SQLiteStore) Delete(sid string) {
	s.db.Exec("DELETE FROM sessions WHERE sid = ?", sid)
}

// NewID generates a cryptographically random 256-bit session identifier.
func (s *SQLiteStore) NewID() string {
	return generateSessionID()
}

// cleanup periodically deletes expired sessions from the database.
func (s *SQLiteStore) cleanup() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-s.ttl).Unix()
		s.db.Exec("DELETE FROM sessions WHERE last_seen < ?", cutoff)
	}
}

// fixJSONNumbers recursively converts float64 values that represent whole
// numbers back to int. This is needed because Go's encoding/json unmarshals
// all JSON numbers into float64 when the target is any/interface{}, but
// Sky's compiled code uses int type assertions for integer values.
func fixJSONNumbers(m map[string]any) {
	for k, v := range m {
		switch val := v.(type) {
		case float64:
			if val == float64(int(val)) {
				m[k] = int(val)
			}
		case map[string]any:
			fixJSONNumbers(val)
		case []any:
			fixJSONSlice(val)
		}
	}
}

func fixJSONSlice(s []any) {
	for i, v := range s {
		switch val := v.(type) {
		case float64:
			if val == float64(int(val)) {
				s[i] = int(val)
			}
		case map[string]any:
			fixJSONNumbers(val)
		case []any:
			fixJSONSlice(val)
		}
	}
}
