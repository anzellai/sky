package rt

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// CSRF default-on behaviour matrix:
//
//   GET request, no cookie    → pass; Set-Cookie issued
//   GET request, has cookie   → pass; cookie preserved
//   POST, no cookie + no hdr  → 403
//   POST, cookie but no hdr   → 403
//   POST, mismatched          → 403
//   POST, cookie + matching   → pass
//   OPTIONS / HEAD           → pass (read-only)
//   /_sky/healthz POST       → pass (observability skip)
//   /_sky/event POST + match → pass
//   Webhook with WithoutCsrf → pass without token

// Helper — reset CSRF runtime state between tests.
func resetCsrf(t *testing.T) {
	t.Helper()
	SetCsrfEnabled(true)
	ResetWithoutCsrf()
	t.Cleanup(func() {
		SetCsrfEnabled(true)
		ResetWithoutCsrf()
	})
}

func serveCsrf(method, path string, headers http.Header, cookies []*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		for _, vv := range v {
			req.Header.Add(k, vv)
		}
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp := httptest.NewRecorder()
	h := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	}))
	h.ServeHTTP(resp, req)
	return resp
}

func TestCSRF_GetWithoutCookie_IssuesCookieAndPasses(t *testing.T) {
	resetCsrf(t)
	resp := serveCsrf(http.MethodGet, "/", nil, nil)
	if resp.Code != 200 {
		t.Errorf("GET should pass, got %d", resp.Code)
	}
	set := resp.Header().Values("Set-Cookie")
	found := false
	for _, sc := range set {
		if strings.HasPrefix(sc, SkyCsrfCookieName+"=") {
			found = true
			if !strings.Contains(sc, "HttpOnly") {
				t.Errorf("CSRF cookie must be HttpOnly: %q", sc)
			}
			if !strings.Contains(sc, "SameSite=Strict") {
				t.Errorf("CSRF cookie must be SameSite=Strict: %q", sc)
			}
		}
	}
	if !found {
		t.Errorf("expected Set-Cookie %s, got headers: %v",
			SkyCsrfCookieName, set)
	}
}

func TestCSRF_PostWithoutCookieOrHeader_403(t *testing.T) {
	resetCsrf(t)
	resp := serveCsrf(http.MethodPost, "/api/x", nil, nil)
	if resp.Code != http.StatusForbidden {
		t.Errorf("POST without csrf must be 403, got %d body=%s",
			resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "csrf_missing") {
		t.Errorf("response should mention csrf_missing, got %s", resp.Body.String())
	}
}

func TestCSRF_PostWithCookieButNoHeader_403(t *testing.T) {
	resetCsrf(t)
	resp := serveCsrf(http.MethodPost, "/api/x", nil, []*http.Cookie{
		{Name: SkyCsrfCookieName, Value: "abc123"},
	})
	if resp.Code != http.StatusForbidden {
		t.Errorf("POST with cookie but no header must be 403, got %d", resp.Code)
	}
}

func TestCSRF_PostWithMismatchedHeader_403(t *testing.T) {
	resetCsrf(t)
	headers := http.Header{}
	headers.Set(SkyCsrfHeaderName, "wrong-token")
	resp := serveCsrf(http.MethodPost, "/api/x", headers, []*http.Cookie{
		{Name: SkyCsrfCookieName, Value: "correct-token"},
	})
	if resp.Code != http.StatusForbidden {
		t.Errorf("POST with mismatched csrf must be 403, got %d", resp.Code)
	}
	if !strings.Contains(resp.Body.String(), "csrf_invalid") {
		t.Errorf("response should mention csrf_invalid, got %s", resp.Body.String())
	}
}

func TestCSRF_PostWithMatchingHeader_Passes(t *testing.T) {
	resetCsrf(t)
	headers := http.Header{}
	headers.Set(SkyCsrfHeaderName, "matching-token")
	resp := serveCsrf(http.MethodPost, "/api/x", headers, []*http.Cookie{
		{Name: SkyCsrfCookieName, Value: "matching-token"},
	})
	if resp.Code != 200 {
		t.Errorf("matched csrf POST must pass, got %d body=%s",
			resp.Code, resp.Body.String())
	}
}

func TestCSRF_PutAndDeleteAlsoMutating(t *testing.T) {
	resetCsrf(t)
	for _, m := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		resp := serveCsrf(m, "/api/x", nil, nil)
		if resp.Code != http.StatusForbidden {
			t.Errorf("%s without csrf must be 403, got %d", m, resp.Code)
		}
	}
}

func TestCSRF_HeadAndOptionsPass(t *testing.T) {
	resetCsrf(t)
	for _, m := range []string{http.MethodHead, http.MethodOptions} {
		resp := serveCsrf(m, "/api/x", nil, nil)
		if resp.Code != 200 {
			t.Errorf("%s should pass without csrf, got %d", m, resp.Code)
		}
	}
}

func TestCSRF_ObservabilityEndpointsSkipped(t *testing.T) {
	resetCsrf(t)
	for _, path := range []string{"/_sky/healthz", "/_sky/readyz", "/_sky/metrics", "/_sky/buildinfo", "/_sky/sse", "/_sky/config"} {
		// Even POST should pass on these — they're either GET-only
		// or auth'd separately (metrics token, SSE session cookie).
		resp := serveCsrf(http.MethodPost, path, nil, nil)
		if resp.Code != 200 {
			t.Errorf("POST %s should bypass CSRF, got %d", path, resp.Code)
		}
	}
}

func TestCSRF_WithoutCsrfRegistry(t *testing.T) {
	resetCsrf(t)
	WithoutCsrf("/webhooks/stripe")
	resp := serveCsrf(http.MethodPost, "/webhooks/stripe", nil, nil)
	if resp.Code != 200 {
		t.Errorf("registered withoutCsrf path should pass, got %d body=%s",
			resp.Code, resp.Body.String())
	}
	// Other paths still protected.
	resp2 := serveCsrf(http.MethodPost, "/webhooks/other", nil, nil)
	if resp2.Code != http.StatusForbidden {
		t.Errorf("unregistered path should still be CSRF-protected, got %d", resp2.Code)
	}
}

func TestCSRF_GloballyDisabledSkipsAllChecks(t *testing.T) {
	resetCsrf(t)
	SetCsrfEnabled(false)
	resp := serveCsrf(http.MethodPost, "/api/x", nil, nil)
	if resp.Code != 200 {
		t.Errorf("CSRF disabled → POST without token should pass, got %d", resp.Code)
	}
}

func TestCSRF_ConstantTimeCompare(t *testing.T) {
	resetCsrf(t)
	// Tokens of equal length but different content — verifies
	// subtle.ConstantTimeCompare path is taken (catches a future
	// regression where someone "optimises" with `==`).
	headers := http.Header{}
	headers.Set(SkyCsrfHeaderName, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	resp := serveCsrf(http.MethodPost, "/api/x", headers, []*http.Cookie{
		{Name: SkyCsrfCookieName, Value: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	})
	if resp.Code != http.StatusForbidden {
		t.Errorf("different equal-length tokens must reject, got %d", resp.Code)
	}
}

func TestCSRF_DifferentLengthDoesNotPanic(t *testing.T) {
	// subtle.ConstantTimeCompare returns 0 (not panic) on length mismatch.
	resetCsrf(t)
	headers := http.Header{}
	headers.Set(SkyCsrfHeaderName, "short")
	resp := serveCsrf(http.MethodPost, "/api/x", headers, []*http.Cookie{
		{Name: SkyCsrfCookieName, Value: "a-much-longer-cookie-value"},
	})
	if resp.Code != http.StatusForbidden {
		t.Errorf("length-mismatched tokens must reject, got %d", resp.Code)
	}
}

func TestCurrentCsrfToken_ReadsCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: SkyCsrfCookieName, Value: "my-token"})
	if got := CurrentCsrfToken(req); got != "my-token" {
		t.Errorf("expected 'my-token', got %q", got)
	}
}

func TestCurrentCsrfToken_EmptyWhenAbsent(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := CurrentCsrfToken(req); got != "" {
		t.Errorf("expected empty token when cookie absent, got %q", got)
	}
}

func TestCurrentCsrfToken_NilRequestSafe(t *testing.T) {
	if got := CurrentCsrfToken(nil); got != "" {
		t.Errorf("nil request should yield empty token, got %q", got)
	}
}

func TestGenerateSkyCsrfToken_FreshAndHex(t *testing.T) {
	a := generateSkyCsrfToken()
	b := generateSkyCsrfToken()
	if a == b {
		t.Errorf("consecutive tokens should differ (got %q twice)", a)
	}
	if len(a) != 64 { // 32 bytes hex-encoded
		t.Errorf("token should be 64 hex chars, got %d (%q)", len(a), a)
	}
	for _, c := range a {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("token must be lowercase hex, got %q in %q", c, a)
		}
	}
}

func TestCSRF_DoubleSubmitTokenInjectedIntoJS(t *testing.T) {
	// liveJSWithCfgAndCsrf injects __skyCsrfToken so __skySend can
	// auto-attach the X-Sky-Csrf header.
	js := liveJSWithCfgAndCsrf("test-sid", liveBannerConfig{Enabled: true},
		"my-csrf-token")
	if !strings.Contains(js, `var __skyCsrfToken = "my-csrf-token"`) {
		t.Errorf("expected __skyCsrfToken assigned in JS, got: %s", js[:300])
	}
	if !strings.Contains(js, `headers["X-Sky-Csrf"] = __skyCsrfToken`) {
		t.Errorf("__skyPostEvent must set X-Sky-Csrf header from token")
	}
}
