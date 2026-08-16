package rt

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
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

	// Recent timestamps: the stream is windowed to `consoleAnalyticsWindow`,
	// so ts=1 would be a 1970 event and correctly outside it.
	now := time.Now().UnixMilli()
	analyticsStoreInsert(map[string]any{"ts": now + 1, "event": "page_view", "props": map[string]any{"path": "/shop"}})
	analyticsStoreInsert(map[string]any{"ts": now + 2, "event": "page_view", "props": map[string]any{"path": "/shop/necklaces", "referrer": "/shop"}})
	analyticsStoreInsert(map[string]any{"ts": now + 3, "event": "signup", "props": map[string]any{"plan": "pro"}}) // no path

	// Buffered writer — drain before reading the handle directly.
	analyticsFlushPending()

	recent := analyticsRecentEvents(db, consoleAnalyticsCutoff())
	if len(recent) != 3 {
		t.Fatalf("want 3 recent events, got %d: %+v", len(recent), recent)
	}
	// Newest first (ORDER BY ts DESC): signup, then /shop/necklaces, then /shop.
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

// TestTheConsoleEndpointDrainsBeforeItReads is the read-your-writes gate for
// the console's Analytics tab.
//
// Buffering made writes asynchronous, and every read path is supposed to ask
// the writer for a synchronous drain first. `Analytics.erase` has a gate for
// that (its version is a COMPLIANCE property). The console endpoint had none:
// deleting the `analyticsFlushPending()` line from `HandleConsoleAnalytics`
// left both suites green while the tab showed a total that lagged whatever the
// app had just emitted — the operator opens the console precisely because
// something is happening NOW, and is shown the state of a quarter-second ago,
// or of nothing at all on a quiet app whose events have not filled a batch.
//
// Driven over `httptest` through the real handler, with NO explicit drain, so
// the drain has to come from the handler itself.
func TestTheConsoleEndpointDrainsBeforeItReads(t *testing.T) {
	t.Cleanup(resetAnalyticsStore)
	resetAnalyticsStore()
	t.Setenv("SKY_ANALYTICS_DB_PATH", filepath.Join(t.TempDir(), "console-drain.db"))
	t.Setenv("SKY_ADMIN_TOKEN", "admin-token-32-bytes-of-testdata-xxxx")
	if analyticsStore() == nil {
		t.Fatal("analytics store did not open")
	}

	const n = 9 // fewer than analyticsBatchSize, so only a drain can flush them
	now := time.Now().UnixMilli()
	for i := 0; i < n; i++ {
		analyticsStoreInsert(map[string]any{
			"ts": now + int64(i), "event": "page_view", "anonymous_id": "anon",
			"props": map[string]any{"path": "/live"},
		})
	}

	req := httptest.NewRequest("GET", "/_sky/console/api/analytics", nil)
	req.Header.Set("Authorization", "Bearer admin-token-32-bytes-of-testdata-xxxx")
	rec := httptest.NewRecorder()
	HandleConsoleAnalytics(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var out consoleAnalyticsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if out.Total != n {
		t.Fatalf("the console reports %d events immediately after %d were emitted — the "+
			"endpoint reads the store without asking the buffered writer to drain, so the "+
			"Analytics tab shows a state that is up to a flush interval stale (and shows "+
			"nothing at all on an app too quiet to fill a batch)", out.Total, n)
	}
	if len(out.Recent) != n {
		t.Errorf("the recent stream carries %d of %d events", len(out.Recent), n)
	}
}
