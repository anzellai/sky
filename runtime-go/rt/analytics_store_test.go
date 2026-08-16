package rt

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func resetAnalyticsStore() {
	// Stop the buffered writer BEFORE closing the handle it writes through:
	// the flusher goroutine holds `db`, and closing underneath it would race
	// an in-flight batch against `sql.DB.Close`.
	if analyticsWriterInst != nil {
		analyticsWriterInst.shutdown(context.Background())
		analyticsWriterInst = nil
	}
	// On PostgreSQL the handle is a REFERENCE into the shared registry, so it
	// is released rather than closed — closing the *sql.DB directly would
	// take the pool from any other consumer holding it and leave a stale
	// registry entry pointing at a closed pool. On SQLite there is no handle
	// and the store owns its file outright.
	if analyticsPool != nil {
		analyticsPool.Close()
		analyticsPool = nil
		analyticsStoreDB = nil
	} else if analyticsStoreDB != nil {
		analyticsStoreDB.Close()
		analyticsStoreDB = nil
	}
	analyticsStoreOnce = sync.Once{}
	analyticsWriteErrWarnOnce = sync.Once{}
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
	// Writes are buffered (analytics_writer.go). This test reads the handle
	// directly rather than through a read path that flushes for itself, so it
	// asks the writer to drain — the same call console_analytics.go makes.
	analyticsFlushPending()

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
// the project-local default (so analytics works out-of-the-box).
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
	if got := analyticsStorePath(); got != analyticsDefaultStorePath {
		t.Errorf("no path configured should fall back to the default: %q", got)
	}
}

// TestAnalyticsOpenStore verifies the openStore kernel hands back a working
// Std.Db handle over the analytics events table (the read/query/update side of
// Std.Db.Store analytics), and that a row tracked through the normal path is
// visible via that handle.
func TestAnalyticsOpenStore(t *testing.T) {
	defer resetAnalyticsStore()
	resetAnalyticsStore()
	t.Setenv("SKY_ANALYTICS_DB_PATH", t.TempDir()+"/analytics.db")
	analyticsStoreInsert(map[string]any{
		"event": "purchase", "ts": int64(1), "anonymous_id": "a1", "user_id": "u1",
	})
	res := Analytics_openStore(nil).(func() any)()
	tag, payload, _ := anyResultView(res)
	if tag != 0 {
		t.Fatalf("openStore returned Err: %v", payload)
	}
	db, ok := payload.(*SkyDb)
	if !ok || db.conn == nil {
		t.Fatalf("openStore did not return a *SkyDb with a conn: %T", payload)
	}
	var n int
	if err := db.conn.QueryRow(`SELECT count(*) FROM analytics_events WHERE event='purchase'`).Scan(&n); err != nil {
		t.Fatalf("query via openStore handle failed: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 purchase event via the Store handle, got %d", n)
	}
}

// TestAnalyticsRetentionWindow pins analyticsRetentionWindow's output as an
// EXACT duration for every input form it accepts.
//
// Why exact durations and not "non-zero": the day form multiplies by 24
// (`time.Duration(n) * 24 * time.Hour`). Drop the `* 24` and "90d" quietly
// becomes 90 HOURS — the startup prune then deletes ~96% of the analytics
// rows an operator asked to keep, with no error, no log line and no failing
// test. Nothing else in the package called this function, so the whole Go
// suite stayed green through that mutation.
//
// Note the parse ORDER this table also locks in: the "<n>d" suffix branch is
// checked BEFORE time.ParseDuration, so "90d" is 90 days and never reaches
// Go's duration parser (which rejects "d" outright).
func TestAnalyticsRetentionWindow(t *testing.T) {
	name := skyEnvName("ANALYTICS_RETENTION")

	cases := []struct {
		label string
		unset bool
		value string
		want  time.Duration
		why   string
	}{
		{label: "unset", unset: true, want: 0, why: "no retention configured keeps everything"},
		{label: "empty", value: "", want: 0, why: "empty is the same as unset"},

		// The day form. These are the numbers the `* 24` carries.
		{label: "90d", value: "90d", want: 2160 * time.Hour, why: "90 days = 90 * 24h"},
		{label: "180d", value: "180d", want: 4320 * time.Hour, why: "180 days = 180 * 24h"},

		// The Go-duration form passes straight through, unmultiplied.
		{label: "720h", value: "720h", want: 720 * time.Hour, why: "a Go duration is used as written"},

		// Everything that must mean "keep all".
		{label: "0d", value: "0d", want: 0, why: "n must be > 0"},
		{label: "-5d", value: "-5d", want: 0, why: "negative day counts are rejected"},
		{label: "abc", value: "abc", want: 0, why: "unparseable"},
		{label: "d", value: "d", want: 0, why: "bare suffix, no number"},
		{label: "90days", value: "90days", want: 0, why: "not the 'd' suffix, and not a Go duration"},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			// t.Setenv registers the restore even when we then unset, so the
			// truly-absent case is reachable without leaking across tests.
			t.Setenv(name, tc.value)
			if tc.unset {
				os.Unsetenv(name)
			}
			got := analyticsRetentionWindow()
			if got != tc.want {
				t.Errorf("analyticsRetentionWindow() with %s=%q = %v (%d ns), want %v (%d ns) — %s",
					name, tc.value, got, int64(got), tc.want, int64(tc.want), tc.why)
			}
		})
	}
}

// TestAnalyticsRetentionDayMultiplierIsTwentyFour states the day multiplier
// as its own claim, so a failure names the multiplier rather than leaving the
// reader to divide two durations. 1d must be 24h, and the window must scale
// linearly in the day count.
func TestAnalyticsRetentionDayMultiplierIsTwentyFour(t *testing.T) {
	name := skyEnvName("ANALYTICS_RETENTION")

	t.Setenv(name, "1d")
	one := analyticsRetentionWindow()
	if one != 24*time.Hour {
		t.Errorf("%s=1d = %v, want 24h0m0s — the day multiplier in analyticsRetentionWindow "+
			"(`time.Duration(n) * 24 * time.Hour`) is %v per day, not 24h", name, one, one)
	}

	t.Setenv(name, "90d")
	ninety := analyticsRetentionWindow()
	if ninety != 90*one {
		t.Errorf("%s=90d = %v, want 90 x %v = %v", name, ninety, one, 90*one)
	}
	if ninety != 2160*time.Hour {
		t.Errorf("%s=90d = %v, want 2160h0m0s (90 days). A retention of %v would delete "+
			"%.1f%% of the rows the operator asked to keep",
			name, ninety, ninety, 100*(1-float64(ninety)/float64(2160*time.Hour)))
	}
}
