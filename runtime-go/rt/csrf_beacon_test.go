package rt

// sendBeacon vs CSRF — regression suite for the unload-flush 403.
//
// THE DEFECT (pre-fix): `navigator.sendBeacon(url, blob)` takes exactly two
// arguments. There is no init/headers parameter, so the beacon PHYSICALLY
// CANNOT set `X-Sky-Csrf`. Sky's unload flush
// (__skyFlushPendingBeacon, live.go) posts a `application/json` Blob to
// /_sky/event carrying the pending debounce batch + the final inputState
// snapshot. CSRFMiddleware requires the header for every mutating POST and
// only falls back to a `__sky_csrf` FORM field for
// application/x-www-form-urlencoded / multipart bodies — never for JSON. So
// the beacon is rejected `csrf_missing` 403 on EVERY CSRF-enabled Sky app
// (CSRF is ON by default), and the user's last debounced keystrokes are
// silently dropped on tab close / external-link click.
//
// THE FIX: the beacon controls its own body, so it carries the token there
// (`"csrf": <token>`), and the middleware accepts a body-borne token for
// exactly this shape. That keeps a real double-submit bind — a cross-origin
// page cannot read the victim's `__sky_csrf` cookie, so it cannot populate
// the body half — rather than exempting the path.
//
// WHY NOT urlencoded: switching the Blob to
// application/x-www-form-urlencoded would hit the EXISTING form-field
// fallback with a one-line change, and it is UNSOUND. urlencoded is a
// CORS-safelisted content type, so a cross-origin sendBeacon with that type
// fires with NO preflight. application/json is not safelisted, so the
// cross-origin beacon is preflighted and refused by the browser before it
// is ever sent. TestBeaconCSRF_UrlencodedStaysRejectedWithoutToken pins
// that we did not take the tempting shortcut.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// beaconRequest builds exactly what navigator.sendBeacon puts on the wire:
// POST, a Blob of type application/json, the full cookie jar, and NO custom
// header of any kind (the API cannot set one).
func beaconRequest(body, cookies string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/_sky/event", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookies)
	return req
}

// csrfOn turns the global CSRF switch on for the duration of a test.
func csrfOn(t *testing.T) {
	t.Helper()
	prev := csrfEnabled.Load()
	csrfEnabled.Store(true)
	t.Cleanup(func() { csrfEnabled.Store(prev) })
}

// TestBeaconCSRF_UnloadFlushIsNot403d is the defect. A beacon carrying a
// valid __sky_csrf cookie and the SAME token in its JSON body must reach the
// handler. Pre-fix the middleware 403'd it because there was no header and
// no body-token path.
func TestBeaconCSRF_UnloadFlushIsNot403d(t *testing.T) {
	csrfOn(t)
	const tok = "beacon-csrf-token-0123456789abcdef"

	reached := false
	h := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}))

	body := `{"sessionId":"sid-beacon","csrf":"` + tok +
		`","batch":[{"msg":"NoopMsg","args":[],"handlerId":""}]}`
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, beaconRequest(body, "sky_sid=sid-beacon; __sky_csrf="+tok))

	if rr.Code == http.StatusForbidden {
		t.Errorf("BEACON 403'd — the unload batch is silently dropped on every "+
			"CSRF-enabled app: %s", rr.Body.String())
	}
	if !reached {
		t.Errorf("beacon never reached handleEvent (status %d)", rr.Code)
	}
}

// TestBeaconCSRF_BodyIsStillReadableByTheHandler — the middleware has to peek
// the body to find the token, so it MUST restore it. handleEvent reads the
// whole body itself (io.ReadAll under a MaxBytesReader); a consumed body
// would turn every beacon into an empty-JSON 400.
func TestBeaconCSRF_BodyIsStillReadableByTheHandler(t *testing.T) {
	csrfOn(t)
	const tok = "beacon-csrf-token-0123456789abcdef"
	body := `{"sessionId":"sid-beacon","csrf":"` + tok + `","batch":[]}`

	var seen string
	h := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, len(body)+16)
		n, _ := r.Body.Read(b)
		seen = string(b[:n])
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, beaconRequest(body, "sky_sid=sid-beacon; __sky_csrf="+tok))

	if seen != body {
		t.Errorf("body not restored for the handler:\n got %q\nwant %q", seen, body)
	}
}

// TestBeaconCSRF_ForgedBodyTokenIsRejected — the bind must be REAL. A body
// token that does not match the cookie is a forgery attempt (a cross-origin
// page guessing, since it cannot read the cookie) and must still 403.
func TestBeaconCSRF_ForgedBodyTokenIsRejected(t *testing.T) {
	csrfOn(t)

	h := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("forged beacon reached the handler")
	}))
	body := `{"sessionId":"sid-beacon","csrf":"attacker-guessed-token","batch":[]}`
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, beaconRequest(body, "sky_sid=sid-beacon; __sky_csrf=the-real-token-value"))

	if rr.Code != http.StatusForbidden {
		t.Errorf("forged body token accepted: status %d, body %s", rr.Code, rr.Body.String())
	}
}

// TestBeaconCSRF_MissingBodyTokenIsRejected — a JSON POST with no header AND
// no body token stays rejected. The fix must not become a blanket
// "application/json skips CSRF" hole.
func TestBeaconCSRF_MissingBodyTokenIsRejected(t *testing.T) {
	csrfOn(t)

	h := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("token-less JSON POST reached the handler")
	}))
	body := `{"sessionId":"sid-beacon","batch":[]}`
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, beaconRequest(body, "sky_sid=sid-beacon; __sky_csrf=the-real-token-value"))

	if rr.Code != http.StatusForbidden {
		t.Errorf("token-less JSON POST accepted: status %d", rr.Code)
	}
}

// TestBeaconCSRF_UrlencodedStaysRejectedWithoutToken — guard against the
// tempting-but-unsound shortcut of switching the beacon Blob to a
// CORS-safelisted content type. A urlencoded POST with no token must still
// 403; nothing about this fix loosens that path.
func TestBeaconCSRF_UrlencodedStaysRejectedWithoutToken(t *testing.T) {
	csrfOn(t)

	h := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("token-less urlencoded POST reached the handler")
	}))
	req := httptest.NewRequest(http.MethodPost, "/_sky/event",
		strings.NewReader("sessionId=sid-beacon"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", "__sky_csrf=the-real-token-value")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("token-less urlencoded POST accepted: status %d", rr.Code)
	}
}

// TestBeaconCSRF_HeaderStillWins — the fetch path is unchanged: a valid
// header is accepted without the middleware ever touching the body.
func TestBeaconCSRF_HeaderStillWins(t *testing.T) {
	csrfOn(t)
	const tok = "fetch-path-token-value"

	body := `{"sessionId":"sid-x","msg":"Noop","args":[]}`
	var seen string
	h := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, len(body)+16)
		n, _ := r.Body.Read(b)
		seen = string(b[:n])
	}))
	req := beaconRequest(body, "sky_sid=sid-x; __sky_csrf="+tok)
	req.Header.Set(SkyCsrfHeaderName, tok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code == http.StatusForbidden {
		t.Fatalf("header path rejected: %s", rr.Body.String())
	}
	if seen != body {
		t.Errorf("header path body altered:\n got %q\nwant %q", seen, body)
	}
}

// TestBeaconCSRF_OversizeBodyIsRejectedNotBuffered — the body peek must be
// bounded. /_sky/event legitimately carries multi-MB base64 images, but only
// ever via fetch (which sets the header, so the peek never runs). A
// header-less JSON POST larger than the peek ceiling must be rejected
// outright rather than buffered into middleware memory.
func TestBeaconCSRF_OversizeBodyIsRejectedNotBuffered(t *testing.T) {
	csrfOn(t)

	h := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("oversize header-less JSON POST reached the handler")
	}))
	huge := `{"csrf":"x","pad":"` + strings.Repeat("A", csrfBodyPeekMax+1024) + `"}`
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, beaconRequest(huge, "__sky_csrf=the-real-token-value"))

	if rr.Code != http.StatusForbidden {
		t.Errorf("oversize header-less JSON POST accepted: status %d", rr.Code)
	}
}

// TestBeaconCSRF_NonJSONBodyIsUntouched — the peek is scoped to JSON. A
// header-less POST of some other content type must not have its body read.
func TestBeaconCSRF_NonJSONBodyIsUntouched(t *testing.T) {
	csrfOn(t)

	h := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("token-less text/plain POST reached the handler")
	}))
	req := httptest.NewRequest(http.MethodPost, "/_sky/event", strings.NewReader(`{"csrf":"tok"}`))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Cookie", "__sky_csrf=tok")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("text/plain body-token POST accepted — the peek must be JSON-scoped: status %d",
			rr.Code)
	}
}
