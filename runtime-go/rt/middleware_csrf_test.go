package rt

import (
	"strings"
	"testing"
)

// task #663 — Sky.Http.Middleware.withCsrf double-submit cookie
// pattern.  These tests pin the three paths:
//
//   1. Safe method (GET) without cookie → handler runs; response
//      gets a Set-Cookie header for `__Host-sky_csrf`.
//   2. Safe method (GET) with cookie already → handler runs;
//      response does NOT get a fresh Set-Cookie (idempotent).
//   3. Unsafe method (POST) without cookie → 403 + "missing token cookie".
//   4. Unsafe method (POST) with cookie but no token in body/header → 403.
//   5. Unsafe method (POST) with cookie + matching form token → handler runs.
//   6. Unsafe method (POST) with cookie + matching X-Csrf-Token header → handler runs.
//   7. Unsafe method (POST) with cookie + MISMATCHING token → 403 + "token mismatch".

func runCsrfMiddleware(t *testing.T, req SkyRequest) SkyResponse {
	t.Helper()
	handlerCalled := false
	handler := func(r any) any {
		return func() any {
			handlerCalled = true
			return Ok[any, any](SkyResponse{Status: 200, Body: "ok"})
		}
	}
	wrapped := Middleware_withCsrf(handler)
	taskFn := wrapped.(func(any) any)(req)
	result := taskFn.(func() any)()
	// Result is `Ok[any, any](SkyResponse{...})` — unwrap.
	resp, _ := unwrapOkSkyResponse(result)
	t.Logf("handler called: %v, status: %d", handlerCalled, resp.Status)
	return resp
}

func unwrapOkSkyResponse(r any) (SkyResponse, bool) {
	// SkyResult[any, any] with Tag=0 (Ok) and OkValue=SkyResponse.
	switch v := r.(type) {
	case SkyResult[any, any]:
		if v.Tag == 0 {
			if sr, ok := v.OkValue.(SkyResponse); ok {
				return sr, true
			}
			if srPtr, ok := v.OkValue.(*SkyResponse); ok {
				return *srPtr, true
			}
		}
	case SkyResponse:
		return v, true
	}
	return SkyResponse{}, false
}

func TestCsrf_SafeMethodSetsCookie(t *testing.T) {
	req := SkyRequest{
		Method:  "GET",
		Path:    "/",
		Headers: map[string]any{},
		Cookies: map[string]string{},
	}
	resp := runCsrfMiddleware(t, req)
	if resp.Status != 200 {
		t.Fatalf("GET should pass through: got %d", resp.Status)
	}
	setCookie := resp.Headers["Set-Cookie"]
	if !strings.Contains(setCookie, "__Host-sky_csrf=") {
		t.Fatalf("expected Set-Cookie with __Host-sky_csrf=, got %q", setCookie)
	}
	if !strings.Contains(setCookie, "SameSite=Lax") {
		t.Fatalf("expected SameSite=Lax, got %q", setCookie)
	}
}

func TestCsrf_SafeMethodWithCookieDoesNotResetIt(t *testing.T) {
	req := SkyRequest{
		Method:  "GET",
		Path:    "/",
		Headers: map[string]any{},
		Cookies: map[string]string{"__Host-sky_csrf": "existing-token-value"},
	}
	resp := runCsrfMiddleware(t, req)
	if resp.Status != 200 {
		t.Fatalf("GET should pass through: got %d", resp.Status)
	}
	if setCookie := resp.Headers["Set-Cookie"]; setCookie != "" {
		t.Fatalf("expected no Set-Cookie when cookie already present, got %q", setCookie)
	}
}

func TestCsrf_UnsafeMethodWithoutCookieRejects(t *testing.T) {
	req := SkyRequest{
		Method:  "POST",
		Path:    "/transfer",
		Headers: map[string]any{},
		Cookies: map[string]string{},
		Form:    map[string]string{},
	}
	resp := runCsrfMiddleware(t, req)
	if resp.Status != 403 {
		t.Fatalf("POST without cookie should be 403: got %d", resp.Status)
	}
	if !strings.Contains(resp.Body, "missing token cookie") {
		t.Fatalf("expected 'missing token cookie' diagnostic, got %q", resp.Body)
	}
}

func TestCsrf_UnsafeMethodWithCookieMissingTokenRejects(t *testing.T) {
	req := SkyRequest{
		Method:  "POST",
		Path:    "/transfer",
		Headers: map[string]any{},
		Cookies: map[string]string{"__Host-sky_csrf": "abc123"},
		Form:    map[string]string{},
	}
	resp := runCsrfMiddleware(t, req)
	if resp.Status != 403 {
		t.Fatalf("POST without token should be 403: got %d", resp.Status)
	}
	if !strings.Contains(resp.Body, "missing token in request") {
		t.Fatalf("expected 'missing token in request' diagnostic, got %q", resp.Body)
	}
}

func TestCsrf_UnsafeMethodWithMatchingFormTokenAccepts(t *testing.T) {
	req := SkyRequest{
		Method:  "POST",
		Path:    "/transfer",
		Headers: map[string]any{},
		Cookies: map[string]string{"__Host-sky_csrf": "abc123"},
		Form:    map[string]string{"_csrf": "abc123"},
	}
	resp := runCsrfMiddleware(t, req)
	if resp.Status != 200 {
		t.Fatalf("POST with matching form token should be 200: got %d body=%q", resp.Status, resp.Body)
	}
}

func TestCsrf_UnsafeMethodWithMatchingHeaderTokenAccepts(t *testing.T) {
	req := SkyRequest{
		Method:  "POST",
		Path:    "/transfer",
		Headers: map[string]any{"X-Csrf-Token": "abc123"},
		Cookies: map[string]string{"__Host-sky_csrf": "abc123"},
		Form:    map[string]string{},
	}
	resp := runCsrfMiddleware(t, req)
	if resp.Status != 200 {
		t.Fatalf("POST with matching header token should be 200: got %d body=%q", resp.Status, resp.Body)
	}
}

func TestCsrf_UnsafeMethodWithMismatchedTokenRejects(t *testing.T) {
	req := SkyRequest{
		Method:  "POST",
		Path:    "/transfer",
		Headers: map[string]any{},
		Cookies: map[string]string{"__Host-sky_csrf": "abc123"},
		Form:    map[string]string{"_csrf": "WRONG"},
	}
	resp := runCsrfMiddleware(t, req)
	if resp.Status != 403 {
		t.Fatalf("POST with mismatched token should be 403: got %d", resp.Status)
	}
	if !strings.Contains(resp.Body, "token mismatch") {
		t.Fatalf("expected 'token mismatch' diagnostic, got %q", resp.Body)
	}
}

func TestCsrf_HeadAndOptionsAreSafe(t *testing.T) {
	for _, method := range []string{"HEAD", "OPTIONS"} {
		req := SkyRequest{
			Method:  method,
			Path:    "/",
			Headers: map[string]any{},
			Cookies: map[string]string{},
		}
		resp := runCsrfMiddleware(t, req)
		if resp.Status != 200 {
			t.Fatalf("%s should pass through: got %d", method, resp.Status)
		}
	}
}

func TestCsrf_TokenIsBase64URLSafe(t *testing.T) {
	req := SkyRequest{
		Method:  "GET",
		Path:    "/",
		Headers: map[string]any{},
		Cookies: map[string]string{},
	}
	resp := runCsrfMiddleware(t, req)
	setCookie := resp.Headers["Set-Cookie"]
	// Extract token value: "__Host-sky_csrf=<token>; Path=..."
	const prefix = "__Host-sky_csrf="
	idx := strings.Index(setCookie, prefix)
	if idx < 0 {
		t.Fatalf("no token in Set-Cookie: %q", setCookie)
	}
	rest := setCookie[idx+len(prefix):]
	end := strings.Index(rest, ";")
	if end < 0 {
		t.Fatalf("malformed Set-Cookie: %q", setCookie)
	}
	token := rest[:end]
	// Base64-URL-RawEncoding: only [A-Za-z0-9_-]+
	for _, r := range token {
		if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			t.Fatalf("token contains non-URL-safe char %q in %q", r, token)
		}
	}
	// 32 bytes → base64-URL-no-padding = 43 chars
	if len(token) != 43 {
		t.Fatalf("expected 43-char base64-URL token, got %d (%q)", len(token), token)
	}
}
