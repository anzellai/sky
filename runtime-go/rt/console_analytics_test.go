package rt

import (
	"path/filepath"
	"testing"
)

// TestAnalyticsRecentEventsPath — the console recent stream lifts `path` out of
// the props JSON so a `page_view` shows WHICH page, and events without a path
// prop leave it empty. Newest-first ordering is preserved.
func TestAnalyticsRecentEventsPath(t *testing.T) {
	defer resetAnalyticsStore()
	resetAnalyticsStore()
	path := filepath.Join(t.TempDir(), "recent.db")
	t.Setenv("SKY_ANALYTICS_DB_PATH", path)
	db := analyticsStore()

	analyticsStoreInsert(map[string]any{"ts": int64(1), "event": "page_view", "props": map[string]any{"path": "/shop"}})
	analyticsStoreInsert(map[string]any{"ts": int64(2), "event": "page_view", "props": map[string]any{"path": "/shop/necklaces", "referrer": "/shop"}})
	analyticsStoreInsert(map[string]any{"ts": int64(3), "event": "signup", "props": map[string]any{"plan": "pro"}}) // no path

	recent := analyticsRecentEvents(db)
	if len(recent) != 3 {
		t.Fatalf("want 3 recent events, got %d: %+v", len(recent), recent)
	}
	// Newest first (ORDER BY id DESC): signup, then /shop/necklaces, then /shop.
	if recent[0].Event != "signup" || recent[0].Path != "" {
		t.Errorf("recent[0] = (%q, path=%q), want (signup, no path)", recent[0].Event, recent[0].Path)
	}
	if recent[1].Event != "page_view" || recent[1].Path != "/shop/necklaces" {
		t.Errorf("recent[1] path = %q, want /shop/necklaces (lifted from props)", recent[1].Path)
	}
	if recent[2].Path != "/shop" {
		t.Errorf("recent[2] path = %q, want /shop", recent[2].Path)
	}
}
