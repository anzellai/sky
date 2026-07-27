package rt

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func analyticsOkInt(t *testing.T, task any) int {
	t.Helper()
	res := any(anyTaskInvoke(task))
	sr, ok := res.(SkyResult[any, any])
	if !ok || sr.Tag != 0 {
		t.Fatalf("task failed: %v", res)
	}
	return AsInt(sr.OkValue)
}

func analyticsOkList(t *testing.T, task any) []any {
	t.Helper()
	res := any(anyTaskInvoke(task))
	sr, ok := res.(SkyResult[any, any])
	if !ok || sr.Tag != 0 {
		t.Fatalf("task failed: %v", res)
	}
	l, _ := sr.OkValue.([]any)
	return l
}

// TestAnalyticsQueries — the aggregate helpers over the store.
func TestAnalyticsQueries(t *testing.T) {
	defer resetAnalyticsStore()
	resetAnalyticsStore()
	path := filepath.Join(t.TempDir(), "q.db")
	t.Setenv("SKY_ANALYTICS_DB_PATH", path)
	_ = analyticsStore()

	analyticsStoreInsert(map[string]any{"ts": int64(1), "event": "page_view", "anonymous_id": "a1", "user_id": "u1"})
	analyticsStoreInsert(map[string]any{"ts": int64(2), "event": "page_view", "anonymous_id": "a1", "user_id": "u1"})
	analyticsStoreInsert(map[string]any{"ts": int64(3), "event": "page_view", "anonymous_id": "a2"})
	analyticsStoreInsert(map[string]any{"ts": int64(4), "event": "signup", "anonymous_id": "a2", "user_id": "u2", "props": map[string]any{"plan": "pro"}})

	if n := analyticsOkInt(t, Analytics_totalEvents(nil)); n != 4 {
		t.Errorf("totalEvents = %d, want 4", n)
	}
	if n := analyticsOkInt(t, Analytics_uniqueUsers(nil)); n != 2 {
		t.Errorf("uniqueUsers = %d, want 2 (u1,u2)", n)
	}

	counts := analyticsOkList(t, Analytics_eventCounts(nil))
	if len(counts) != 2 {
		t.Fatalf("eventCounts len = %d, want 2", len(counts))
	}
	top, _ := counts[0].(SkyTuple2)
	if top.V0 != "page_view" || AsInt(top.V1) != 3 {
		t.Errorf("top count = (%v, %v), want (page_view, 3)", top.V0, top.V1)
	}

	recent := analyticsOkList(t, Analytics_recentEvents(int64(10)))
	if len(recent) != 4 {
		t.Fatalf("recentEvents len = %d, want 4", len(recent))
	}
	newest, _ := recent[0].(string)
	if !strings.Contains(newest, `"event":"signup"`) {
		t.Errorf("newest should be signup: %s", newest)
	}
	if !strings.Contains(newest, `"plan":"pro"`) {
		t.Errorf("props not embedded as JSON object: %s", newest)
	}
}

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

// TestAnalyticsErase — right-to-erasure deletes every event for an anonymous
// OR user id and returns the count.
func TestAnalyticsErase(t *testing.T) {
	defer resetAnalyticsStore()
	resetAnalyticsStore()
	path := filepath.Join(t.TempDir(), "e.db")
	t.Setenv("SKY_ANALYTICS_DB_PATH", path)
	db := analyticsStore()

	analyticsStoreInsert(map[string]any{"ts": int64(1), "event": "x", "anonymous_id": "anon_a"})
	analyticsStoreInsert(map[string]any{"ts": int64(2), "event": "y", "anonymous_id": "anon_a", "user_id": "u1"})
	analyticsStoreInsert(map[string]any{"ts": int64(3), "event": "z", "anonymous_id": "anon_b"})

	erased := analyticsEraseResult(t, "anon_a")
	if erased != 2 {
		t.Errorf("erased %d for anon_a, want 2", erased)
	}
	var remaining int
	db.QueryRow(`SELECT count(*) FROM analytics_events`).Scan(&remaining)
	if remaining != 1 {
		t.Errorf("remaining %d, want 1", remaining)
	}
	// user id u1's row was under anon_a → already gone → 0.
	if again := analyticsEraseResult(t, "u1"); again != 0 {
		t.Errorf("erased %d for already-gone u1, want 0", again)
	}
}

func analyticsEraseResult(t *testing.T, id string) int {
	t.Helper()
	res := any(anyTaskInvoke(Analytics_erase(id)))
	sr, ok := res.(SkyResult[any, any])
	if !ok || sr.Tag != 0 {
		t.Fatalf("erase(%q) failed: %v", id, res)
	}
	return AsInt(sr.OkValue)
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
