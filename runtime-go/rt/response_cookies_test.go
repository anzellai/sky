package rt

// Defect 2 regression — "`withCsrf` destroys the handler's cookie".
//
// `SkyResponse.Headers` is a `map[string]string`: ONE slot per header
// name. Every cookie helper assigned `Headers["Set-Cookie"] = …`, so the
// second cookie on a response overwrote the first. `Middleware.withCsrf`
// mints `__Host-sky_csrf` on a safe method when the visitor has no CSRF
// cookie yet — exactly the first GET on which a handler sets its own
// session cookie — and the user's cookie disappeared without a trace.
//
//	no middleware : sky_session=SECRET-SESSION-ID; Path=/; HttpOnly; SameSite=Lax
//	with withCsrf : __Host-sky_csrf=…; Path=/; Secure; SameSite=Lax
//
// The nine tests in middleware_csrf_test.go all share one handler that
// returns `SkyResponse{Status:200, Body:"ok"}` with NO cookie, so the
// whole file is structurally blind to this. Assertions below parse the
// emitted headers with `(&http.Response{…}).Cookies()` rather than
// matching substrings.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// emittedCookies runs a SkyResponse through the runtime's real
// header-emission path and returns the cookies a browser would see.
func emittedCookies(t *testing.T, resp SkyResponse) []*http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	applySkyResponseHeaders(rec.Header(), resp)
	return rec.Result().Cookies()
}

func cookieNamed(cs []*http.Cookie, name string) *http.Cookie {
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// ── Defect 2a: two cookies on one response both survive ──────────

func TestWithCookie_SecondCookieDoesNotEvictTheFirst(t *testing.T) {
	resp := SkyResponse{Status: 200}
	out := Server_withCookie("sky_session", "SECRET-SESSION-ID", resp)
	out = Server_withCookie("theme", "dark", out)

	sr, ok := asSkyResponse(out)
	if !ok {
		t.Fatal("withCookie did not return a response")
	}
	cs := emittedCookies(t, sr)
	if len(cs) != 2 {
		t.Fatalf("expected 2 cookies on the wire, got %d: %v", len(cs), cs)
	}
	if c := cookieNamed(cs, "sky_session"); c == nil || c.Value != "SECRET-SESSION-ID" {
		t.Fatalf("session cookie lost: %v", cs)
	}
	if c := cookieNamed(cs, "theme"); c == nil || c.Value != "dark" {
		t.Fatalf("theme cookie lost: %v", cs)
	}
}

// ── Defect 2b: the reported trigger ──────────────────────────────
//
// GET + no `__Host-sky_csrf` cookie yet + a handler that sets a cookie.

func TestWithCsrf_PreservesHandlerCookie(t *testing.T) {
	handler := func(any) any {
		return func() any {
			resp := Server_withCookie("sky_session", "SECRET-SESSION-ID",
				SkyResponse{Status: 200, Body: "ok"})
			return Ok[any, any](resp)
		}
	}
	wrapped := Middleware_withCsrf(handler)
	out := wrapped.(func(any) any)(SkyRequest{Method: "GET", Path: "/"}).(func() any)()

	sr, ok := unwrapOkSkyResponse(out)
	if !ok {
		t.Fatalf("withCsrf did not return an Ok-wrapped response: %#v", out)
	}
	cs := emittedCookies(t, sr)
	if c := cookieNamed(cs, "sky_session"); c == nil || c.Value != "SECRET-SESSION-ID" {
		t.Fatalf("withCsrf destroyed the handler's session cookie; on the wire: %v", cs)
	}
	if c := cookieNamed(cs, "__Host-sky_csrf"); c == nil || c.Value == "" {
		t.Fatalf("withCsrf did not issue its own cookie; on the wire: %v", cs)
	}
}

// Same trigger, but the handler returns the TYPED Sky record shape
// (`Sky.Http.Server.Response`) rather than the runtime struct. The
// middleware's unwrap was a raw `okResult.OkValue.(SkyResponse)`
// assertion, so this shape fell through to a `setCookieHeader(resp, …)`
// call whose argument was the Ok WRAPPER, not the response — and the
// CSRF cookie was dropped entirely.
type typedSkyResponseForTest struct {
	Status      int
	Body        string
	Headers     map[string]string
	ContentType string
}

func TestWithCsrf_IssuesCookieForTypedResponseShape(t *testing.T) {
	handler := func(any) any {
		return func() any {
			return Ok[any, any](any(typedSkyResponseForTest{
				Status:  200,
				Body:    "ok",
				Headers: map[string]string{"Set-Cookie": "sky_session=SECRET-SESSION-ID; Path=/; HttpOnly"},
			}))
		}
	}
	wrapped := Middleware_withCsrf(handler)
	out := wrapped.(func(any) any)(SkyRequest{Method: "GET", Path: "/"}).(func() any)()

	sr, ok := unwrapOkSkyResponseAny(out)
	if !ok {
		t.Fatalf("withCsrf did not return an Ok-wrapped response: %#v", out)
	}
	cs := emittedCookies(t, sr)
	if c := cookieNamed(cs, "sky_session"); c == nil {
		t.Fatalf("handler's session cookie lost; on the wire: %v", cs)
	}
	if c := cookieNamed(cs, "__Host-sky_csrf"); c == nil || c.Value == "" {
		t.Fatalf("withCsrf issued no cookie for the typed response shape; on the wire: %v", cs)
	}
}

// unwrapOkSkyResponseAny unwraps an Ok result whose payload is any
// response-shaped value (runtime struct OR Sky record).
func unwrapOkSkyResponseAny(r any) (SkyResponse, bool) {
	if v, ok := r.(SkyResult[any, any]); ok && v.Tag == 0 {
		return asSkyResponse(v.OkValue)
	}
	return asSkyResponse(r)
}

// ── Defect 2c: Set-Cookie is a repeated field, never comma-folded ─

func TestSetCookie_IsARepeatedHeaderNotAJoin(t *testing.T) {
	resp := SkyResponse{Status: 200}
	out, _ := asSkyResponse(Server_withCookie("a", "1", resp))
	out, _ = asSkyResponse(Server_withCookie("b", "2", out))

	rec := httptest.NewRecorder()
	applySkyResponseHeaders(rec.Header(), out)
	lines := rec.Result().Header.Values("Set-Cookie")
	if len(lines) != 2 {
		t.Fatalf("expected 2 Set-Cookie header lines, got %d: %v", len(lines), lines)
	}
}

// A literal `Set-Cookie` a Sky handler put straight into the headers
// Dict must still reach the wire, exactly once, alongside runtime-minted
// cookies.
func TestSetCookie_UserSuppliedHeaderStillEmitted(t *testing.T) {
	resp := SkyResponse{
		Status:  200,
		Headers: map[string]string{"Set-Cookie": "manual=1; Path=/"},
	}
	out, _ := asSkyResponse(Server_withCookie("minted", "2", resp))
	cs := emittedCookies(t, out)
	if len(cs) != 2 {
		t.Fatalf("expected 2 cookies, got %d: %v", len(cs), cs)
	}
	if cookieNamed(cs, "manual") == nil || cookieNamed(cs, "minted") == nil {
		t.Fatalf("expected both manual and minted cookies: %v", cs)
	}
}

func TestSetCookie_SingleCookieIsNotDuplicated(t *testing.T) {
	out, _ := asSkyResponse(Server_withCookie("only", "1", SkyResponse{Status: 200}))
	rec := httptest.NewRecorder()
	applySkyResponseHeaders(rec.Header(), out)
	if lines := rec.Result().Header.Values("Set-Cookie"); len(lines) != 1 {
		t.Fatalf("single cookie must emit exactly one header line, got %v", lines)
	}
}
