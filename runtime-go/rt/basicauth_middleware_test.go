package rt

// Tests for Middleware_withBasicAuth (rt.go ~9027).
//
// Implemented contract (asserted here, not assumed):
//   - expectedUser / expectedPass are stringified via fmt.Sprintf.
//   - The Authorization header is read as Headers["Authorization"] and
//     type-asserted to string (a non-string value reads as "").
//   - Missing / non-"Basic " header → 401, body "authentication
//     required", WWW-Authenticate: Basic realm="Sky". Inner NOT reached.
//   - Malformed base64 → 401, body "invalid auth". No panic.
//   - Decoded value without a ':' separator → 401, body "invalid auth".
//   - Wrong user or wrong pass → 401, body "bad credentials". Inner NOT
//     reached. Compare is subtle.ConstantTimeCompare (constant-time).
//   - Correct Basic base64(user:pass) → inner handler reached.

import (
	"encoding/base64"
	"testing"
)

// baInner builds an inner handler that records reach and returns 200.
func baInner(reached *bool) func(any) any {
	return func(_ any) any {
		*reached = true
		return func() any { return Ok[any, any](SkyResponse{Status: 200, Body: "secret-page"}) }
	}
}

func invokeBasicAuth(t *testing.T, user, pass, authHeader string, hasHeader bool) (SkyResponse, bool) {
	t.Helper()
	reached := false
	mw := Middleware_withBasicAuth(user, pass, baInner(&reached)).(func(any) any)
	headers := map[string]any{}
	if hasHeader {
		headers["Authorization"] = authHeader
	}
	req := SkyRequest{Method: "GET", Path: "/protected", Headers: headers}
	res := anyTaskInvoke(mw(req))
	if res.Tag != 0 {
		t.Fatalf("BasicAuth middleware must return Ok, got Tag=%d", res.Tag)
	}
	resp, ok := res.OkValue.(SkyResponse)
	if !ok {
		t.Fatalf("expected SkyResponse in Ok, got %T", res.OkValue)
	}
	return resp, reached
}

func basicHeader(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func TestBasicAuth_NoHeader_401AndChallenge(t *testing.T) {
	resp, reached := invokeBasicAuth(t, "admin", "secret", "", false)
	if reached {
		t.Error("missing Authorization must NOT reach the inner handler")
	}
	if resp.Status != 401 {
		t.Errorf("expected 401, got %d", resp.Status)
	}
	if resp.Headers["WWW-Authenticate"] != `Basic realm="Sky"` {
		t.Errorf("expected WWW-Authenticate challenge, got %q", resp.Headers["WWW-Authenticate"])
	}
	if resp.Body != "authentication required" {
		t.Errorf("unexpected body: %q", resp.Body)
	}
}

func TestBasicAuth_NonBasicScheme_401(t *testing.T) {
	resp, reached := invokeBasicAuth(t, "admin", "secret", "Bearer sometoken", true)
	if reached {
		t.Error("non-Basic scheme must NOT reach inner")
	}
	if resp.Status != 401 {
		t.Errorf("expected 401, got %d", resp.Status)
	}
	if resp.Headers["WWW-Authenticate"] != `Basic realm="Sky"` {
		t.Errorf("non-Basic scheme should still emit the challenge, got %q", resp.Headers["WWW-Authenticate"])
	}
}

func TestBasicAuth_WrongPassword_401(t *testing.T) {
	resp, reached := invokeBasicAuth(t, "admin", "secret", basicHeader("admin", "wrong"), true)
	if reached {
		t.Error("wrong password must NOT reach inner")
	}
	if resp.Status != 401 {
		t.Errorf("expected 401, got %d", resp.Status)
	}
	if resp.Body != "bad credentials" {
		t.Errorf("expected 'bad credentials', got %q", resp.Body)
	}
}

func TestBasicAuth_WrongUsername_401(t *testing.T) {
	resp, reached := invokeBasicAuth(t, "admin", "secret", basicHeader("root", "secret"), true)
	if reached {
		t.Error("wrong username must NOT reach inner")
	}
	if resp.Status != 401 {
		t.Errorf("expected 401, got %d", resp.Status)
	}
	if resp.Body != "bad credentials" {
		t.Errorf("expected 'bad credentials', got %q", resp.Body)
	}
}

func TestBasicAuth_CorrectCredentials_InnerReached(t *testing.T) {
	resp, reached := invokeBasicAuth(t, "admin", "secret", basicHeader("admin", "secret"), true)
	if !reached {
		t.Error("correct credentials MUST reach the inner handler")
	}
	if resp.Status != 200 {
		t.Errorf("expected inner 200, got %d", resp.Status)
	}
	if resp.Body != "secret-page" {
		t.Errorf("expected inner body, got %q", resp.Body)
	}
}

func TestBasicAuth_MalformedBase64_401NoPanic(t *testing.T) {
	// "!!!" is not valid base64 → DecodeString errors → 401 "invalid auth".
	resp, reached := invokeBasicAuth(t, "admin", "secret", "Basic !!!not-base64!!!", true)
	if reached {
		t.Error("malformed base64 must NOT reach inner")
	}
	if resp.Status != 401 {
		t.Errorf("expected 401, got %d", resp.Status)
	}
	if resp.Body != "invalid auth" {
		t.Errorf("expected 'invalid auth', got %q", resp.Body)
	}
}

func TestBasicAuth_NoColonSeparator_401(t *testing.T) {
	// Valid base64 but no ':' → SplitN yields 1 part → 401 "invalid auth".
	enc := base64.StdEncoding.EncodeToString([]byte("no-colon-here"))
	resp, reached := invokeBasicAuth(t, "admin", "secret", "Basic "+enc, true)
	if reached {
		t.Error("credentials without ':' must NOT reach inner")
	}
	if resp.Status != 401 {
		t.Errorf("expected 401, got %d", resp.Status)
	}
	if resp.Body != "invalid auth" {
		t.Errorf("expected 'invalid auth', got %q", resp.Body)
	}
}

func TestBasicAuth_EmptyBasicValue_401(t *testing.T) {
	// "Basic " with an empty credential → decodes to "" → no ':' → 401.
	resp, reached := invokeBasicAuth(t, "admin", "secret", "Basic ", true)
	if reached {
		t.Error("empty Basic value must NOT reach inner")
	}
	if resp.Status != 401 {
		t.Errorf("expected 401, got %d", resp.Status)
	}
}

func TestBasicAuth_PasswordWithColon_SplitsOnFirst(t *testing.T) {
	// SplitN(…, ":", 2) keeps a password that itself contains ':'.
	resp, reached := invokeBasicAuth(t, "admin", "a:b:c", basicHeader("admin", "a:b:c"), true)
	if !reached {
		t.Error("password containing ':' must still authenticate (SplitN on first ':')")
	}
	if resp.Status != 200 {
		t.Errorf("expected 200, got %d", resp.Status)
	}
}

func TestBasicAuth_EqualLengthWrongCred_Rejected(t *testing.T) {
	// Equal-length but different credentials — exercises the
	// subtle.ConstantTimeCompare path (a future '==' regression that
	// short-circuits would still reject here, but this locks the intent).
	resp, reached := invokeBasicAuth(t, "admin", "secret", basicHeader("xxxxx", "yyyyyy"), true)
	if reached {
		t.Error("equal-length wrong credentials must NOT reach inner")
	}
	if resp.Status != 401 {
		t.Errorf("expected 401, got %d", resp.Status)
	}
}
