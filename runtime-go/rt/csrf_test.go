package rt

// Audit P1-1: CSRF protection via double-submit cookie pattern.
// Server.csrfIssue produces a fresh token + Set-Cookie header on the
// response. Server.csrfVerify returns true iff the request's __csrf
// cookie equals its __csrf form field. The pattern defeats CSRF
// because an attacker on a different origin can neither read the
// HttpOnly cookie value nor inject a matching form field.

import (
	"strings"
	"testing"
)

func TestCsrfIssue_AttachesCookieAndReturnsToken(t *testing.T) {
	resp := SkyResponse{Status: 200, Body: "<form>", ContentType: "text/html"}
	out := Server_csrfIssue(resp)
	tup, ok := out.(SkyTuple2)
	if !ok {
		t.Fatalf("Server_csrfIssue should return SkyTuple2, got %T", out)
	}
	token, _ := tup.V0.(string)
	if len(token) < 32 {
		t.Fatalf("token too short for crypto-grade randomness: %d chars", len(token))
	}
	updated, ok := tup.V1.(SkyResponse)
	if !ok {
		t.Fatalf("Server_csrfIssue second slot should be SkyResponse, got %T", tup.V1)
	}
	setCookie, ok := updated.Headers["Set-Cookie"]
	if !ok {
		t.Fatal("Server_csrfIssue should set a __csrf cookie on the response")
	}
	if !strings.Contains(setCookie, "__csrf=") {
		t.Fatalf("Set-Cookie should name __csrf: %q", setCookie)
	}
	if !strings.Contains(setCookie, "HttpOnly") {
		t.Fatalf("__csrf cookie must be HttpOnly: %q", setCookie)
	}
	if !strings.Contains(setCookie, "SameSite=Strict") {
		t.Fatalf("__csrf cookie must be SameSite=Strict: %q", setCookie)
	}
	// Cookie value must equal the returned token (double-submit
	// pattern depends on this — the form embeds the token, the
	// browser sends the cookie, server compares).
	if !strings.Contains(setCookie, "__csrf="+token) {
		t.Fatalf("cookie value should match returned token: cookie=%q token=%q", setCookie, token)
	}
}

func TestCsrfVerify_AcceptsMatchingTokens(t *testing.T) {
	tok := "abc123def456"
	req := SkyRequest{
		Method:  "POST",
		Cookies: map[string]string{"__csrf": tok},
		Form:    map[string]string{"__csrf": tok},
	}
	if !asBool(Server_csrfVerify(req)) {
		t.Fatal("csrfVerify should accept matching cookie + form __csrf")
	}
}

func TestCsrfVerify_RejectsMismatch(t *testing.T) {
	req := SkyRequest{
		Method:  "POST",
		Cookies: map[string]string{"__csrf": "cookie-token"},
		Form:    map[string]string{"__csrf": "form-token"},
	}
	if asBool(Server_csrfVerify(req)) {
		t.Fatal("csrfVerify should reject when cookie != form")
	}
}

func TestCsrfVerify_RejectsMissingCookie(t *testing.T) {
	// Attacker scenario: no cookie set for the target site, but the
	// attacker tries to POST with a guessed form value. Verify must
	// reject.
	req := SkyRequest{
		Method: "POST",
		Form:   map[string]string{"__csrf": "guessed"},
	}
	if asBool(Server_csrfVerify(req)) {
		t.Fatal("csrfVerify must reject when __csrf cookie is missing")
	}
}

func TestCsrfVerify_RejectsMissingForm(t *testing.T) {
	req := SkyRequest{
		Method:  "POST",
		Cookies: map[string]string{"__csrf": "valid"},
	}
	if asBool(Server_csrfVerify(req)) {
		t.Fatal("csrfVerify must reject when __csrf form field is missing")
	}
}

func TestCsrfIssue_FreshTokenEachCall(t *testing.T) {
	// Same response, two calls — tokens MUST differ. Otherwise an
	// attacker who learns one token can replay forever.
	r := SkyResponse{Status: 200}
	tup1 := Server_csrfIssue(r).(SkyTuple2)
	tup2 := Server_csrfIssue(r).(SkyTuple2)
	if tup1.V0 == tup2.V0 {
		t.Fatal("csrfIssue must generate a fresh token on each call")
	}
}
