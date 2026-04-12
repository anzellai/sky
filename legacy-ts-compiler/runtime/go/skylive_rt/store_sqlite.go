package skylive_rt

import (
	"database/sql"
	"encoding/json"
	"log"
	"time"

	sky_wrappers "sky-out/sky_wrappers"
	_ "modernc.org/sqlite"
)

// SQLiteStore is a persistent session store backed by SQLite.
// It implements the SessionStore interface using modernc.org/sqlite
// (pure Go, no CGo dependency).
type SQLiteStore struct {
	db    *sql.DB
	ttl   time.Duration
	stop  chan struct{}
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
			last_seen     INTEGER NOT NULL,
			version       INTEGER NOT NULL DEFAULT 0
		)
	`
	// Add version column if upgrading from older schema
	db.Exec("ALTER TABLE sessions ADD COLUMN version INTEGER NOT NULL DEFAULT 0")
	if _, err := db.Exec(createTable); err != nil {
		db.Close()
		return nil, err
	}

	store := &SQLiteStore{db: db, ttl: ttl, stop: make(chan struct{})}
	go store.cleanup()
	return store, nil
}

// Get loads a session from the database. The model is deserialised from
// JSON into map[string]any and the previous view tree is reconstructed
// by parsing the stored HTML via ParseHTML.
func (s *SQLiteStore) Get(sid string) (*Session, bool) {
	row := s.db.QueryRow(
		"SELECT model_json, prev_view_html, created_at, last_seen, version FROM sessions WHERE sid = ?",
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
		Version:  version,
	}

	// Touch last_seen
	now := time.Now()
	sess.LastSeen = now
	s.db.Exec("UPDATE sessions SET last_seen = ? WHERE sid = ?", now.Unix(), sid)

	return sess, true
}

// Set serialises the session model as JSON and the previous view tree as
// HTML, then upserts the row into the sessions table.
func (s *SQLiteStore) Set(sid string, sess *Session) bool {
	now := time.Now()
	sess.LastSeen = now

	modelBytes, err := json.Marshal(sess.Model)
	if err != nil {
		log.Printf("skylive_rt: SQLiteStore.Set: failed to marshal model: %v", err)
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

	// For new sessions (version 0), insert. For existing, use optimistic locking.
	if sess.Version == 0 {
		_, err = s.db.Exec(
			`INSERT OR IGNORE INTO sessions (sid, model_json, prev_view_html, created_at, last_seen, version)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			sid, string(modelBytes), prevViewHTML, createdAt, now.Unix(), newVersion,
		)
	} else {
		var result sql.Result
		result, err = s.db.Exec(
			`UPDATE sessions SET model_json = ?, prev_view_html = ?, last_seen = ?, version = ?
			 WHERE sid = ? AND version = ?`,
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
		log.Printf("skylive_rt: SQLiteStore.Set: failed to upsert session: %v", err)
		return false
	}
	sess.Version = newVersion
	return true
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
	for {
		select {
		case <-ticker.C:
			cutoff := time.Now().Add(-s.ttl).Unix()
			if _, err := s.db.Exec("DELETE FROM sessions WHERE last_seen < ?", cutoff); err != nil {
				log.Printf("skylive_rt: SQLiteStore.cleanup: failed to delete expired sessions: %v", err)
			}
		case <-s.stop:
			return
		}
	}
}

// fixJSONNumbers recursively converts float64 values that represent whole
// numbers back to int, and reconstructs SkyMaybe/SkyResult structs from
// their map representations. This is needed because Go's encoding/json
// unmarshals all JSON numbers into float64 and all structs into
// map[string]any when the target is any/interface{}.
func fixJSONNumbers(m map[string]any) {
	for k, v := range m {
		switch val := v.(type) {
		case float64:
			if val == float64(int(val)) {
				m[k] = int(val)
			}
		case map[string]any:
			if rebuilt := RebuildADT(val); rebuilt != nil {
				m[k] = rebuilt
			} else {
				fixJSONNumbers(val)
			}
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
			if rebuilt := RebuildADT(val); rebuilt != nil {
				s[i] = rebuilt
			} else {
				fixJSONNumbers(val)
			}
		case []any:
			fixJSONSlice(val)
		}
	}
}

// RebuildADT checks if a map is a serialised ADT (has Tag + SkyName keys)
// and reconstructs the proper named Go struct (SkyMaybe or SkyResult).
// Returns nil if the map is not an ADT.
func RebuildADT(m map[string]any) any {
	skyName, hasSkyName := m["SkyName"]
	if !hasSkyName {
		return nil
	}
	name, ok := skyName.(string)
	if !ok {
		return nil
	}
	switch name {
	case "Just":
		val := m["JustValue"]
		// Recursively fix nested values
		if inner, ok := val.(map[string]any); ok {
			if rebuilt := RebuildADT(inner); rebuilt != nil {
				val = rebuilt
			} else {
				fixJSONNumbers(inner)
			}
		} else if inner, ok := val.([]any); ok {
			fixJSONSlice(inner)
		}
		return sky_wrappers.SkyJust(val)
	case "Nothing":
		return sky_wrappers.SkyNothing()
	case "Ok":
		val := m["OkValue"]
		if inner, ok := val.(map[string]any); ok {
			if rebuilt := RebuildADT(inner); rebuilt != nil {
				val = rebuilt
			} else {
				fixJSONNumbers(inner)
			}
		} else if inner, ok := val.([]any); ok {
			fixJSONSlice(inner)
		}
		return sky_wrappers.SkyOk(val)
	case "Err":
		val := m["ErrValue"]
		return sky_wrappers.SkyErr(val)
	default:
		return nil
	}
}

// Close stops the cleanup goroutine and closes the database connection.
func (s *SQLiteStore) Close() error {
	// Signal cleanup goroutine to stop
	select {
	case <-s.stop:
		// Already closed
	default:
		close(s.stop)
	}
	// Close the database connection
	return s.db.Close()
}
