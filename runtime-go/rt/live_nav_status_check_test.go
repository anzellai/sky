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

// TestSkyNavPushesUrlBeforePatch regression-locks the ordering of the sky-nav
// click handler's `history.pushState` vs `__skyPatch`. __skyPatch runs the
// data-sky-path URL-sync handler, which pushes a NEW history entry whenever
// location.pathname doesn't already match the patched view's data-sky-path.
//
// Symptom that drove the fix: an app using BOTH `sky-nav` links AND a
// `data-sky-path` urlSync element (the documented URL-from-Page pattern) got
// TWO history entries per navigation — the data-sky-path handler pushed the
// new path (location.pathname still stale mid-patch), then the click handler
// pushed it again. Result: the browser Back button needed two presses to move
// one page, so page transitions looked stuck/broken.
//
// Fix: push the URL FIRST, so when __skyPatch runs the data-sky-path handler,
// location.pathname already matches and it replaceState()s (a no-op) instead
// of pushing a duplicate. This test pins `pushState(...href...)` before
// `__skyPatch(t)` in the click handler.
func TestSkyNavPushesUrlBeforePatch(t *testing.T) {
	js := liveJSWithCfgAndCsrfWithBase("test-sid", liveBannerConfig{}, "csrf-token", "")

	push := strings.Index(js, `pushState({}, "", href)`)
	if push < 0 {
		t.Fatal("sky-nav click handler missing its history.pushState(href) — handler stripped?")
	}
	// The `__skyPatch(t)` that pairs with the click handler's fetch must come
	// AFTER that pushState. (The other __skyPatch sites — SSE apply, popstate —
	// don't carry the `href` var, so anchoring on the href pushState is exact.)
	patchAfter := strings.Index(js[push:], "__skyPatch(t)")
	if patchAfter < 0 {
		t.Fatal("sky-nav click handler missing __skyPatch(t) after pushState")
	}
	// And there must be NO `__skyPatch(t)` BEFORE the pushState within the
	// click handler — guard against a regression that reintroduces the
	// patch-then-push order. Search the window between the fetch's r.ok gate
	// and the pushState.
	okGate := strings.LastIndex(js[:push], "if (!r.ok) { window.location.href = href")
	if okGate < 0 {
		t.Fatal("could not locate the sky-nav click handler's r.ok gate")
	}
	if strings.Contains(js[okGate:push], "__skyPatch(t)") {
		t.Error("sky-nav click handler patches the body BEFORE pushing the URL — " +
			"this double-pushes history when the view carries a data-sky-path " +
			"element, breaking the browser Back button (needs two presses per page)")
	}
}

// TestSkyRunPathsIntentIsExplicit locks the elegant, order-independent half of
// the fix: __skyRunPaths (the data-sky-path URL sync) takes an explicit `push`
// intent instead of guessing from a pathname race.
//
//   - The full-body patch caller (__skyPatch — sky-nav click / popstate /
//     mount) passes push=false: the address bar is already correct, so it may
//     only replaceState, never mint a duplicate entry. This is what makes the
//     double-push structurally impossible regardless of interleaving.
//   - The SSE apply caller (a programmatic Navigate Msg) passes push=true: a
//     real navigation that earns a Back-able history entry.
//
// A regression that drops the arg, flips either call site, or reverts
// __skyRunPaths to an unconditional pushState reintroduces the broken-Back bug.
func TestSkyRunPathsIntentIsExplicit(t *testing.T) {
	js := liveJSWithCfgAndCsrfWithBase("test-sid", liveBannerConfig{}, "csrf-token", "")

	// __skyRunPaths must branch its history call on the intent, not always push.
	if !strings.Contains(js, `history[push ? "pushState" : "replaceState"]`) {
		t.Error("__skyRunPaths no longer branches pushState/replaceState on the " +
			"push intent — a full-body patch could double-push history again")
	}
	// Full-body patch reconciles without a new entry.
	if !strings.Contains(js, "__skyRunPaths(root, false)") {
		t.Error("__skyPatch must call __skyRunPaths(root, false) — a full-body " +
			"patch (sky-nav/popstate/mount) must never mint a history entry")
	}
	// SSE-driven programmatic Navigate earns a Back-able entry.
	if !strings.Contains(js, "__skyRunPaths(document, true)") {
		t.Error("the SSE apply path must call __skyRunPaths(document, true) — " +
			"otherwise a programmatic Navigate Msg leaves the URL stale / " +
			"un-Back-able")
	}
}
