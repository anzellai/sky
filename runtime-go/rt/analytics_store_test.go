package rt

import (
	"path/filepath"
	"sync"
	"testing"
)

func resetAnalyticsStore() {
	if analyticsStoreDB != nil {
		analyticsStoreDB.Close()
		analyticsStoreDB = nil
	}
	analyticsStoreOnce = sync.Once{}
}

// TestAnalyticsStore — events persist to the resolved SQLite store with
// structured columns; props/context serialise as JSON TEXT.
func TestAnalyticsStore(t *testing.T) {
	defer resetAnalyticsStore()
	resetAnalyticsStore()
	path := filepath.Join(t.TempDir(), "a.db")
	t.Setenv("SKY_ANALYTICS_DB_PATH", path)

	db := analyticsStore()
	if db == nil {
		t.Fatal("store not opened for a configured path")
	}
	analyticsStoreInsert(map[string]any{
		"ts": int64(123), "event": "e1", "anonymous_id": "anon_x",
		"props": map[string]any{"k": "v"},
	})
	analyticsStoreInsert(map[string]any{
		"ts": int64(456), "event": "e2", "anonymous_id": "anon_x",
		"user_id": "u9", "context": map[string]any{"ip": "1.2.3.0"},
	})

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM analytics_events`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
	var event, props string
	if err := db.QueryRow(`SELECT event, props FROM analytics_events WHERE ts=123`).Scan(&event, &props); err != nil {
		t.Fatalf("select: %v", err)
	}
	if event != "e1" || props != `{"k":"v"}` {
		t.Errorf("row = (%q, %q), want (e1, {\"k\":\"v\"})", event, props)
	}
	// user_id NULL when absent; present when identified.
	var uid any
	db.QueryRow(`SELECT user_id FROM analytics_events WHERE ts=123`).Scan(&uid)
	if uid != nil {
		t.Errorf("user_id should be NULL for anonymous event, got %v", uid)
	}
	var uid2 string
	db.QueryRow(`SELECT user_id FROM analytics_events WHERE ts=456`).Scan(&uid2)
	if uid2 != "u9" {
		t.Errorf("user_id = %q, want u9", uid2)
	}
}

// TestAnalyticsStorePathResolution — override wins; else reuse console DB; else
// none.
func TestAnalyticsStorePathResolution(t *testing.T) {
	t.Setenv("SKY_ANALYTICS_DB_PATH", "/override.db")
	t.Setenv("SKY_CONSOLE_DB_PATH", "/console.db")
	if got := analyticsStorePath(); got != "/override.db" {
		t.Errorf("override should win: %q", got)
	}
	t.Setenv("SKY_ANALYTICS_DB_PATH", "")
	if got := analyticsStorePath(); got != "/console.db" {
		t.Errorf("should reuse console DB: %q", got)
	}
	t.Setenv("SKY_CONSOLE_DB_PATH", "")
	if got := analyticsStorePath(); got != "" {
		t.Errorf("no path configured should be empty: %q", got)
	}
}
