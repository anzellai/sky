package rt

// Cycle 3 P50b / Gap C11 — client adapter for the SSE event:patches
// transport.
//
// The producer (P50a) ships event:patches for any render whose diff
// against the previous tree fits in a small patch list. This file
// pins the client-side handler wiring:
//
//   (a) liveJS() emits the addEventListener("patches", ...) call so
//       a real browser routes event:patches frames to the
//       __skyApplyPatches consumer.
//   (b) The handler parses the JSON envelope and threads seq +
//       ackInputs through __skyHandleResponse for the same monotonic
//       gating the HTTP path uses.
//   (c) The legacy addEventListener("patch", ...) wiring is
//       UNCHANGED — pre-P50a frames (and the P50a fallback shapes:
//       first-render, full-replace) keep working.
//   (d) handleSSE writes `event: patches\n` for sseFrame{event:
//       "patches"} payloads (server-side wiring — the Go test can
//       drive handleSSE and assert the wire bytes).
//
// Playwright-style end-to-end driver is OUT OF SCOPE for the Go test
// suite (Sky.Live's Playwright probes live in scripts/verify-all-web.sh
// and run in CI); this file is the unit-test mate that catches a
// liveJS regression at the runtime level. The Playwright probe added
// alongside this commit covers focus-preservation + DOM-mutation
// behaviour from the browser side.

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ─── (a) + (b) + (c) JS wiring ─────────────────────────────────

// TestLiveJS_EmitsPatchesEventListener pins that the embedded JS
// contains the addEventListener call for the new event type AND
// retains the legacy one. A grep-style assertion is sufficient — the
// JS is one long string literal, the call shape is stable.
func TestLiveJS_EmitsPatchesEventListener(t *testing.T) {
	js := liveJS("test-sid")
	wantPatches := `addEventListener("patches"`
	wantLegacyPatch := `addEventListener("patch"`
	if !strings.Contains(js, wantPatches) {
		t.Fatalf("liveJS missing %q — P50b client adapter not wired", wantPatches)
	}
	if !strings.Contains(js, wantLegacyPatch) {
		t.Fatalf("liveJS missing legacy %q — pre-P50a fallback broken", wantLegacyPatch)
	}
	// The patches handler MUST call __skyApplyPatches with the
	// frame's patches array. Substring-match the call to catch a
	// rename / argument-order regression.
	if !strings.Contains(js, "__skyApplyPatches(frame.patches)") {
		t.Fatalf("patches handler must invoke __skyApplyPatches(frame.patches); not found in liveJS")
	}
	// And it MUST gate via __skyHandleResponse for the seq +
	// ackInputs monotonic check (the same guard the HTTP path uses
	// — out-of-order frames need dropping at the same point).
	if !strings.Contains(js, "__skyHandleResponse(frame.seq, frame.ackInputs") {
		t.Fatalf("patches handler must seq-gate via __skyHandleResponse(frame.seq, frame.ackInputs, ...)")
	}
}

// ─── (d) Server-side wire emission ─────────────────────────────

// TestHandleSSE_EmitsEventPatchesForPatchesFrame drives handleSSE
// directly, pushes an sseFrame with event="patches" onto sess.sseCh,
// and asserts the response stream contains `event: patches\n` lines.
//
// This is the integration counterpart to the producer test in
// live_sse_diff_producer_test.go — it confirms that the chosen event
// name travels end-to-end through the SSE response writer.
func TestHandleSSE_EmitsEventPatchesForPatchesFrame(t *testing.T) {
	app := &liveApp{
		store:  newMemoryStore(30 * time.Minute),
		locker: newSessionLocker(),
		view: func(model any) any {
			// Minimal view — handleSSE's reconnect-resync calls
			// view(model) and emits an `event: patch` frame first;
			// we want to see our subsequent `event: patches` frame
			// after the resync settles.
			return velement("div", nil, []any{vtext("baseline")})
		},
	}
	sess := &liveSession{
		sseCh:     make(chan sseFrame, 4),
		cancelSub: make(chan struct{}),
		model:     "model-state",
		handlers:  map[string]any{},
	}
	app.store.Set("sid-patches", sess)

	// Queue an event:patches frame BEFORE the request starts, so
	// the SSE writer picks it up immediately after the reconnect-
	// resync push.
	sess.sseCh <- sseFrame{
		event: "patches",
		data:  `{"seq":42,"patches":[{"id":"r/0","text":"new"}]}`,
	}

	srv := httptest.NewServer(http.HandlerFunc(app.handleSSE))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.AddCookie(&http.Cookie{Name: "sky_sid", Value: "sid-patches"})
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)

	resp, err := http.DefaultClient.Do(req)
	if err != nil && !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("SSE GET failed: %v", err)
	}
	if resp == nil {
		t.Fatal("no response from SSE handler")
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	sawEventPatches := false
	sawPatchesData := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: patches") {
			sawEventPatches = true
			continue
		}
		if sawEventPatches && strings.HasPrefix(line, "data: ") {
			if strings.Contains(line, `"seq":42`) && strings.Contains(line, `"patches"`) {
				sawPatchesData = true
				break
			}
		}
	}
	if !sawEventPatches {
		t.Fatalf("handleSSE did not emit `event: patches` for sseFrame{event:patches}")
	}
	if !sawPatchesData {
		t.Fatalf("handleSSE emitted event:patches but data line missing seq+patches")
	}
}

// TestHandleSSE_EmitsEventPatchForPatchFrame is the legacy-path
// regression mate — sseFrame{event: "patch"} (the pre-P50a shape, or
// the P50a fallback for first-render / full-replace) MUST still
// produce `event: patch` on the wire so existing addEventListener("patch")
// listeners are unaffected.
func TestHandleSSE_EmitsEventPatchForPatchFrame(t *testing.T) {
	app := &liveApp{
		store:  newMemoryStore(30 * time.Minute),
		locker: newSessionLocker(),
		view: func(model any) any {
			return velement("div", nil, []any{vtext("baseline")})
		},
	}
	sess := &liveSession{
		sseCh:     make(chan sseFrame, 4),
		cancelSub: make(chan struct{}),
		model:     "model-state",
		handlers:  map[string]any{},
	}
	app.store.Set("sid-patch-legacy", sess)

	// Queue a legacy full-body frame.
	sess.sseCh <- sseFrame{
		event: "patch",
		data:  `{"seq":7,"body":"<div>full</div>"}`,
	}

	srv := httptest.NewServer(http.HandlerFunc(app.handleSSE))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.AddCookie(&http.Cookie{Name: "sky_sid", Value: "sid-patch-legacy"})
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)

	resp, err := http.DefaultClient.Do(req)
	if err != nil && !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("SSE GET failed: %v", err)
	}
	if resp == nil {
		t.Fatal("no response from SSE handler")
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	// We need to see the SPECIFIC `event: patch` for seq=7 (the
	// reconnect-resync also emits an event:patch first; assert the
	// data line carries our seq=7 payload).
	sawPatchSeqSeven := false
	inPatchEvent := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: patch") && !strings.HasPrefix(line, "event: patches") {
			inPatchEvent = true
			continue
		}
		if inPatchEvent && strings.HasPrefix(line, "data: ") {
			if strings.Contains(line, `"seq":7`) {
				sawPatchSeqSeven = true
				break
			}
			inPatchEvent = false
		}
	}
	if !sawPatchSeqSeven {
		t.Fatalf("handleSSE didn't emit our queued event:patch frame (seq=7)")
	}
}

// ─── (e) Idempotent setAttribute guard (#568) ───────────────────
//
// __skyApplyPatches must guard setAttribute(k, v) with a getAttribute
// equality check. Without the guard, an SSE patch carrying an
// unchanged attribute value still calls setAttribute, which browsers
// treat as a "navigate / refetch" event for src/href-bearing elements
// (most painfully: <iframe src="…"> reloads on ANY assignment, even
// to the same string).
//
// Production regression: dev.skydeploy.app/_sky/console rendered
// inside the dashboard via <iframe src=…>. The dashboard Tick (4s)
// re-rendered, sent SSE attribute patches that touched iframe.src
// with the unchanged value, the iframe reloaded every 4s, every
// reload re-fired the cross-origin handshake form-POST to
// <slug>.<host>/_sky/console/_login, and each handshake opened a
// fresh long-lived SSE on the tenant Cloud Run instance. With
// max_instances=1 and containerConcurrency=80, the tenant saturated
// in minutes and Cloud Run's GFE responded "Rate exceeded." to
// every subsequent request.
//
// The runtime fix is a one-line guard in the JS patch loop. This
// regression pins the guard string-contents so a future rewrite of
// __skyApplyPatches can't silently drop it.
func TestLiveJS_IdempotentSetAttributeGuard(t *testing.T) {
	js := liveJS("test-sid")
	// The guarded pattern is "if (el.getAttribute(k) !== v) {" — a
	// substring search is robust against whitespace tweaks but tight
	// enough to catch a logic flip (e.g. someone "simplifying" the
	// guard back to unconditional setAttribute).
	wantGuard := "if (el.getAttribute(k) !== v) {"
	if !strings.Contains(js, wantGuard) {
		t.Fatalf("__skyApplyPatches must guard setAttribute with %q; not found in liveJS — "+
			"see runtime-go/rt/live.go and docs for issue #568. Without this guard, "+
			"every Sky.Live SSE patch that re-emits an unchanged iframe/img/link/script "+
			"src or href triggers a browser refetch, breaking embedded console / "+
			"image-heavy apps under any Tick subscription.", wantGuard)
	}
}

// TestLiveJS_IframePreservedAcrossHTMLReplace pins the SECOND iframe-
// reload path closed by #568. The attribute-patch guard
// (TestLiveJS_IdempotentSetAttributeGuard) handles the case where the
// diff emits an attrs patch for an unchanged iframe.src. But the
// diff also emits *HTML-replace* patches when a parent subtree's
// structure changes (sibling sky-id reorder, child-count change,
// etc.). The HTML-replace path goes through
// __skyReplaceHTMLPreservingFocus, which historically only spliced
// inputs/textareas/selects — iframes inside the swapped subtree got
// removeChild'd and recreated from the new HTML, triggering a fresh
// navigation regardless of whether the URL changed.
//
// Production manifestation: the 438ms back-to-back POSTs to
// /_sky/console/_login that confirmed the loop was not just a 4s
// Tick. Each parent-tree diff round that touched any sibling of the
// iframe destroyed and re-created the iframe, re-firing the
// handshake form-submit.
//
// The fix splices iframes alongside inputs/textareas/selects with
// the live iframe's src treated as user-state authority — the live
// document, internal SSE, navigation history, scroll position
// belong to the iframe. Placeholder contributes only non-src attrs.
func TestLiveJS_IframePreservedAcrossHTMLReplace(t *testing.T) {
	js := liveJS("test-sid")
	wantIframeQuery := `container.querySelectorAll("iframe")`
	if !strings.Contains(js, wantIframeQuery) {
		t.Fatalf("__skyReplaceHTMLPreservingFocus must walk iframes via %q; "+
			"not found in liveJS — see runtime-go/rt/live.go and #568. "+
			"Without this, any HTML-replace patch on a subtree containing "+
			"an iframe destroys and re-creates the iframe, triggering a "+
			"fresh navigation even when the src is unchanged. Breaks "+
			"embedded console / multi-tab dashboards / preview iframes.",
			wantIframeQuery)
	}
	// SRC-EQUALITY GATE — without this, the splice would freeze the
	// iframe at its first-loaded URL forever. Sky.Live's sky-id is
	// purely structural (path + position + tag, plus form-name for
	// inputs); it does NOT encode src. So two renders that emit
	// <iframe src=A> then <iframe src=B> at the same structural
	// position share a sky-id, and naive splicing matches the
	// placeholder by sky-id and preserves the live iframe, dropping
	// the new src on the floor. The gate compares liveSrc vs phSrc
	// and skips the splice when they differ — fall-through to the
	// default innerHTML path destroys the live iframe and creates a
	// fresh one at the new URL. Caught in adversarial review of #568.
	wantSrcGate := "if (liveSrc !== phSrc) continue;"
	if !strings.Contains(js, wantSrcGate) {
		t.Fatalf("iframe splice must guard with src-equality (%q); "+
			"not found in liveJS — without it, the iframe is FROZEN at "+
			"its first URL forever because sky-id doesn't encode src. "+
			"See runtime-go/rt/live.go and #568 adversarial review.",
			wantSrcGate)
	}
	// The splice must strip src from the placeholder so the
	// follow-on __skyCopyAttrsExceptAuthority call doesn't write
	// src onto the live iframe (which would trigger the same
	// navigation we're solving). A precise substring matches the
	// guard.
	wantStripSrc := `phFr.removeAttribute("src")`
	if !strings.Contains(js, wantStripSrc) {
		t.Fatalf("iframe splice must strip src from placeholder via %q "+
			"so __skyCopyAttrsExceptAuthority doesn't navigate the live "+
			"iframe; not found in liveJS — see runtime-go/rt/live.go.",
			wantStripSrc)
	}
}
