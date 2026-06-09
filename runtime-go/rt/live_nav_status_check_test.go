package rt

import (
	"strings"
	"testing"
)

// TestSkyNavFetchChecksOk regression-locks the sky-nav click handler's
// `r.ok` gate. Without it, a 404 body like "session not found" (server
// lost our session_id store entry after TTL expiry / store-restart /
// store-config change / cross-deploy cookie collision) would flow
// straight into __skyPatch and become the whole page body.
//
// Symptom that drove the fix: a SkyDeploy user reported the dashboard
// rendering "session not found" as plain text in the entire viewport
// after their signed-in token expired. Trace was: SSO cookie still
// valid → click an <a sky-nav> link → runtime fetched the URL → server
// returned 404 + "session not found" body (Sky.Live session_id no
// longer in store) → fetch handler called __skyPatch on the body
// without checking the status code.
//
// The gate's failure mode also covers the popstate path (Back/Forward
// to a URL after session loss); both paths share the same shape and
// both must check r.ok before calling __skyPatch.
func TestSkyNavFetchChecksOk(t *testing.T) {
	cfg := liveBannerConfig{
		Reconnecting: `"Reconnecting…"`,
		Offline:      `"Connection lost — refresh to retry"`,
	}
	js := liveJSWithCfgAndCsrfWithBase("test-sid", cfg, "csrf-token", "")

	// Two fetch sites use X-Sky-Nav: the sky-nav click handler and
	// the popstate Back/Forward handler. Each MUST gate its .then on
	// r.ok before invoking __skyPatch, otherwise a 404 body becomes
	// the whole page.
	const wantPattern = "if (!r.ok)"
	occurrences := strings.Count(js, wantPattern)
	if occurrences < 2 {
		t.Errorf("expected at least 2 occurrences of %q in runtime JS "+
			"(one for sky-nav click handler, one for popstate handler); "+
			"got %d.\n"+
			"Without this gate, a 404 'session not found' body "+
			"flows into __skyPatch and renders as the whole page.",
			wantPattern, occurrences)
	}

	// Both X-Sky-Nav fetches must have a same-page recovery path.
	// Click handler uses `window.location.href = href`; popstate
	// uses `window.location.href = window.location.href` (no `href`
	// var in scope). Either way, a full-page navigation triggers the
	// runtime's initial-page handler, which mints a fresh session_id
	// and re-runs init.
	if !strings.Contains(js, "X-Sky-Nav") {
		t.Fatal("runtime JS missing the X-Sky-Nav fetch — sky-nav handler stripped?")
	}
	if !strings.Contains(js, `window.location.href = href`) {
		t.Error("sky-nav click handler missing its non-OK fallback " +
			"'window.location.href = href' — without it, a 404 has no recovery")
	}
}
