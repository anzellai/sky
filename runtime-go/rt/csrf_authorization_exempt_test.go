package rt

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A POST carrying an Authorization header is a credentialed-API call, not an
// ambient-cookie browser request — it must be exempt from CSRF so JSON/Bearer
// APIs work without SKY_CSRF=off. A cookie-session POST with NO Authorization
// header and no token still 403s (protection intact).
func TestCsrfAuthorizationHeaderExempt(t *testing.T) {
	prior := IsCsrfEnabled()
	t.Cleanup(func() { SetCsrfEnabled(prior) })
	SetCsrfEnabled(true)

	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(200)
	})
	mw := CSRFMiddleware(next)

	// 1. POST with Authorization → passes through (exempt).
	req := httptest.NewRequest(http.MethodPost, "/api/orders", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer abc123")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if !reached || rec.Code != 200 {
		t.Fatalf("Authorization-bearing POST should be CSRF-exempt: reached=%v code=%d", reached, rec.Code)
	}

	// 2. POST without Authorization and without a token → 403 (protected).
	reached = false
	req2 := httptest.NewRequest(http.MethodPost, "/api/orders", strings.NewReader("{}"))
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	mw.ServeHTTP(rec2, req2)
	if reached || rec2.Code != http.StatusForbidden {
		t.Fatalf("cookie-session POST without token should 403: reached=%v code=%d", reached, rec2.Code)
	}
	// The 403 body must name the escape hatches.
	body := rec2.Body.String()
	if !strings.Contains(body, "Authorization") || !strings.Contains(body, "SKY_CSRF=off") {
		t.Fatalf("403 body should hint at the escape hatches, got: %s", body)
	}
}
