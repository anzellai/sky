package rt

// Regression tests for the cookie-authenticated RPC route kind
// (Server.rpc) that the Sky.Spa auto-split mounts every `/_rpc/<Msg>`
// and `/_rpc/__spaSignOut` on, plus the method-keyed CSRF exemptions
// and the Secure attribute on a TLS request.
//
// The defect these pin: every split RPC was registered with
// `Server.api`, which exempts the path from CSRF and applies no other
// check. A `Content-Type: text/plain` POST carrying a foreign Origin (a
// CORS-simple request, so the browser sends it without a preflight and
// attaches the ambient `sky_sid` cookie) reached the handler.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// rpcTestServer mounts one Server.rpc route behind the same middleware
// the real listener uses (CSRF outermost of the Sky layers), so the test
// exercises the CSRF exemption and the guard together.
func rpcTestServer(t *testing.T, spec string) (http.Handler, *int) {
	t.Helper()
	resetCsrf(t)
	hits := 0
	handler := func(req SkyRequest) any {
		return func() any {
			hits++
			return SkyResponse{Status: 200, Body: "ran", ContentType: "text/plain"}
		}
	}
	route := Server_rpc(spec, handler)
	mux, _ := serverRouteMux([]any{route})
	return CSRFMiddleware(mux), &hits
}

func rpcPost(h http.Handler, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{}`))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRpcGuard_SameOriginJSONPasses(t *testing.T) {
	h, hits := rpcTestServer(t, "POST /_rpc/Save")
	rec := rpcPost(h, "http://app.example/_rpc/Save", map[string]string{
		"Content-Type":   "application/json",
		"Sec-Fetch-Site": "same-origin",
		"Origin":         "http://app.example",
	})
	if rec.Code != 200 || *hits != 1 {
		t.Fatalf("same-origin JSON RPC: status=%d hits=%d body=%q, want 200 and one run", rec.Code, *hits, rec.Body.String())
	}
}

func TestRpcGuard_OriginMatchingHostPassesWithoutFetchMetadata(t *testing.T) {
	h, hits := rpcTestServer(t, "POST /_rpc/Save")
	rec := rpcPost(h, "http://app.example/_rpc/Save", map[string]string{
		"Content-Type": "application/json",
		"Origin":       "http://app.example",
	})
	if rec.Code != 200 || *hits != 1 {
		t.Fatalf("Origin == Host: status=%d hits=%d, want 200", rec.Code, *hits)
	}
}

// The live probe that found the defect: text/plain + a foreign Origin.
func TestRpcGuard_TextPlainForeignOriginIs403(t *testing.T) {
	h, hits := rpcTestServer(t, "POST /_rpc/Save")
	rec := rpcPost(h, "http://app.example/_rpc/Save", map[string]string{
		"Content-Type": "text/plain",
		"Origin":       "https://evil.example",
	})
	if rec.Code != http.StatusForbidden || *hits != 0 {
		t.Fatalf("text/plain + foreign Origin: status=%d hits=%d, want 403 and no run", rec.Code, *hits)
	}
}

func TestRpcGuard_ForeignOriginJSONIs403(t *testing.T) {
	h, hits := rpcTestServer(t, "POST /_rpc/Save")
	rec := rpcPost(h, "http://app.example/_rpc/Save", map[string]string{
		"Content-Type":   "application/json",
		"Sec-Fetch-Site": "cross-site",
		"Origin":         "https://evil.example",
	})
	if rec.Code != http.StatusForbidden || *hits != 0 {
		t.Fatalf("foreign Origin: status=%d hits=%d, want 403", rec.Code, *hits)
	}
	if !strings.Contains(rec.Body.String(), "SKY_PUBLIC_URL") {
		t.Errorf("403 body must name the setting to change, got %q", rec.Body.String())
	}
}

// A same-site sibling (another subdomain, another localhost port) is
// cross-ORIGIN: SameSite=Lax still sends the cookie, so the guard is
// what stops it.
func TestRpcGuard_SameSiteSiblingIs403(t *testing.T) {
	h, hits := rpcTestServer(t, "POST /_rpc/Save")
	rec := rpcPost(h, "http://localhost:8000/_rpc/Save", map[string]string{
		"Content-Type":   "application/json",
		"Sec-Fetch-Site": "same-site",
		"Origin":         "http://localhost:9999",
	})
	if rec.Code != http.StatusForbidden || *hits != 0 {
		t.Fatalf("same-site sibling: status=%d hits=%d, want 403", rec.Code, *hits)
	}
}

func TestRpcGuard_NullOriginIs403(t *testing.T) {
	h, hits := rpcTestServer(t, "POST /_rpc/Save")
	rec := rpcPost(h, "http://app.example/_rpc/Save", map[string]string{
		"Content-Type": "application/json",
		"Origin":       "null",
	})
	if rec.Code != http.StatusForbidden || *hits != 0 {
		t.Fatalf("Origin: null: status=%d hits=%d, want 403", rec.Code, *hits)
	}
}

func TestRpcGuard_SameOriginTextPlainIs403(t *testing.T) {
	h, hits := rpcTestServer(t, "POST /_rpc/Save")
	rec := rpcPost(h, "http://app.example/_rpc/Save", map[string]string{
		"Content-Type":   "text/plain",
		"Sec-Fetch-Site": "same-origin",
	})
	if rec.Code != http.StatusForbidden || *hits != 0 {
		t.Fatalf("text/plain same-origin: status=%d hits=%d, want 403 (JSON only)", rec.Code, *hits)
	}
}

func TestRpcGuard_NoContentTypeIs403(t *testing.T) {
	h, hits := rpcTestServer(t, "POST /_rpc/Save")
	rec := rpcPost(h, "http://app.example/_rpc/Save", nil)
	if rec.Code != http.StatusForbidden || *hits != 0 {
		t.Fatalf("no Content-Type: status=%d hits=%d, want 403", rec.Code, *hits)
	}
}

// A non-browser client (curl, a server) carries no victim cookie and
// sends no Origin / Sec-Fetch-Site: allowed with the JSON content type.
func TestRpcGuard_NoOriginJSONPasses(t *testing.T) {
	h, hits := rpcTestServer(t, "POST /_rpc/Save")
	rec := rpcPost(h, "http://app.example/_rpc/Save", map[string]string{
		"Content-Type": "application/json; charset=utf-8",
	})
	if rec.Code != 200 || *hits != 1 {
		t.Fatalf("no Origin + JSON: status=%d hits=%d, want 200", rec.Code, *hits)
	}
}

// A browser that sends Sec-Fetch-Site: cross-site but no Origin is not
// trusted: fail closed.
func TestRpcGuard_CrossSiteWithoutOriginIs403(t *testing.T) {
	h, hits := rpcTestServer(t, "POST /_rpc/Save")
	rec := rpcPost(h, "http://app.example/_rpc/Save", map[string]string{
		"Content-Type":   "application/json",
		"Sec-Fetch-Site": "cross-site",
	})
	if rec.Code != http.StatusForbidden || *hits != 0 {
		t.Fatalf("cross-site, no Origin: status=%d hits=%d, want 403", rec.Code, *hits)
	}
}

func TestRpcGuard_WrongMethodIs405(t *testing.T) {
	h, hits := rpcTestServer(t, "POST /_rpc/Save")
	req := httptest.NewRequest(http.MethodGet, "http://app.example/_rpc/Save", nil)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed || *hits != 0 {
		t.Fatalf("GET on an RPC route: status=%d hits=%d, want 405", rec.Code, *hits)
	}
}

// Behind a proxy that rewrites Host (or a tunnel), SKY_PUBLIC_URL names
// the origin the browser sees.
func TestRpcGuard_PublicURLHonoured(t *testing.T) {
	t.Setenv("SKY_PUBLIC_URL", "https://app.example/")
	h, hits := rpcTestServer(t, "POST /_rpc/Save")
	rec := rpcPost(h, "http://127.0.0.1:8000/_rpc/Save", map[string]string{
		"Content-Type": "application/json",
		"Origin":       "https://app.example",
	})
	if rec.Code != 200 || *hits != 1 {
		t.Fatalf("Origin == SKY_PUBLIC_URL: status=%d hits=%d body=%q, want 200", rec.Code, *hits, rec.Body.String())
	}
	// With SKY_PUBLIC_URL set, it is the only accepted origin: an Origin
	// that merely echoes the (attacker-reachable) Host no longer passes.
	rec = rpcPost(h, "http://127.0.0.1:8000/_rpc/Save", map[string]string{
		"Content-Type": "application/json",
		"Origin":       "http://127.0.0.1:8000",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Origin != SKY_PUBLIC_URL: status=%d, want 403", rec.Code)
	}
}

func TestRpcGuard_PublicURLListAndDefaultPort(t *testing.T) {
	t.Setenv("SKY_PUBLIC_URL", "https://a.example:443, https://b.example")
	h, hits := rpcTestServer(t, "POST /_rpc/Save")
	for _, origin := range []string{"https://a.example", "https://b.example:443"} {
		rec := rpcPost(h, "http://internal/_rpc/Save", map[string]string{
			"Content-Type": "application/json",
			"Origin":       origin,
		})
		if rec.Code != 200 {
			t.Fatalf("Origin %s: status=%d, want 200", origin, rec.Code)
		}
	}
	if *hits != 2 {
		t.Fatalf("hits=%d, want 2", *hits)
	}
}

// Behind a TLS-terminating proxy that preserves Host (Caddy's default),
// the browser's Origin is https while the hop to the app is http. The
// proxy's X-Forwarded-Proto names the scheme.
func TestRpcGuard_ForwardedProtoSchemeBehindProxy(t *testing.T) {
	h, hits := rpcTestServer(t, "POST /_rpc/Save")
	rec := rpcPost(h, "http://app.example/_rpc/Save", map[string]string{
		"Content-Type":      "application/json",
		"Origin":            "https://app.example",
		"X-Forwarded-Proto": "https",
	})
	if rec.Code != 200 || *hits != 1 {
		t.Fatalf("https Origin behind proxy: status=%d hits=%d, want 200", rec.Code, *hits)
	}
}

// The RPC route stays exempt from the double-submit token (the wasm
// client has no readable token) — the guard replaces it, it does not
// stack on top of it.
func TestRpcGuard_DoesNotNeedCsrfToken(t *testing.T) {
	h, hits := rpcTestServer(t, "POST /_rpc/Save")
	rec := rpcPost(h, "http://app.example/_rpc/Save", map[string]string{
		"Content-Type":   "application/json",
		"Sec-Fetch-Site": "same-origin",
		"Cookie":         "sky_sid=abc",
	})
	if rec.Code != 200 || *hits != 1 {
		t.Fatalf("RPC without CSRF token: status=%d hits=%d, want 200", rec.Code, *hits)
	}
}

// ─── method-keyed CSRF exemptions ─────────────────────────────

func TestCSRF_ExemptionIsMethodKeyed(t *testing.T) {
	resetCsrf(t)
	_ = Server_api("GET /report", fakeHandler("x"))
	if rec := serveCsrf(http.MethodPost, "/report", nil, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("`Server.api \"GET /report\"` must not exempt POST /report: got %d, want 403", rec.Code)
	}
	_ = Server_api("POST /hook", fakeHandler("x"))
	if rec := serveCsrf(http.MethodPost, "/hook", nil, nil); rec.Code != 200 {
		t.Fatalf("`Server.api \"POST /hook\"` must exempt POST /hook: got %d, want 200", rec.Code)
	}
	if rec := serveCsrf(http.MethodPut, "/hook", nil, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("`Server.api \"POST /hook\"` must not exempt PUT /hook: got %d, want 403", rec.Code)
	}
}

func TestCSRF_MethodlessExemptionCoversAllMethods(t *testing.T) {
	resetCsrf(t)
	_ = Server_api("/any-verb", fakeHandler("x"))
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if rec := serveCsrf(m, "/any-verb", nil, nil); rec.Code != 200 {
			t.Fatalf("method-less Server.api must exempt %s: got %d", m, rec.Code)
		}
	}
	WithoutCsrf("/webhooks/stripe")
	if rec := serveCsrf(http.MethodPost, "/webhooks/stripe", nil, nil); rec.Code != 200 {
		t.Fatalf("WithoutCsrf(path) must still exempt every method: got %d", rec.Code)
	}
}

func TestCSRF_LiveApiExemptionIsMethodKeyed(t *testing.T) {
	resetCsrf(t)
	_ = Live_api("GET /feed", fakeHandler("x"))
	if rec := serveCsrf(http.MethodPost, "/feed", nil, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("`Live.api \"GET /feed\"` must not exempt POST /feed: got %d, want 403", rec.Code)
	}
	_ = Live_api("post /cb", fakeHandler("x"))
	if rec := serveCsrf(http.MethodPost, "/cb", nil, nil); rec.Code != 200 {
		t.Fatalf("`Live.api \"post /cb\"` must exempt POST /cb (method is case-insensitive): got %d", rec.Code)
	}
}

// ─── Secure on a TLS request without the production flag ─────

func TestSpaSessionCookie_SecureOnTLSWithoutEnv(t *testing.T) {
	t.Setenv("ENV", "")
	t.Setenv("SKY_ENV", "")
	resetCsrf(t)
	handler := func(req SkyRequest) any {
		return func() any {
			return Server_withCookie("sky_sid", "tok", "Path=/; HttpOnly; SameSite=Lax", Server_json("{}"))
		}
	}
	mux, _ := serverRouteMux([]any{Server_rpc("POST /_rpc/SignIn", handler)})
	h := CSRFMiddleware(mux)

	var sid string
	req := httptest.NewRequest(http.MethodPost, "https://app.example/_rpc/SignIn", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	for _, line := range rec.Header().Values("Set-Cookie") {
		if strings.HasPrefix(line, "sky_sid=") {
			sid = line
		}
	}
	if sid == "" {
		t.Fatalf("no sky_sid Set-Cookie on the TLS response (status %d)", rec.Code)
	}
	for _, want := range []string{"Secure", "HttpOnly", "SameSite=Lax"} {
		if !strings.Contains(sid, want) {
			t.Errorf("sky_sid over TLS must carry %s, got %q", want, sid)
		}
	}

	req = httptest.NewRequest(http.MethodPost, "http://app.example/_rpc/SignIn", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	for _, line := range rec.Header().Values("Set-Cookie") {
		if strings.HasPrefix(line, "sky_sid=") && strings.Contains(line, "Secure") {
			t.Errorf("plain-http dev request must not get Secure (the browser would drop it): %q", line)
		}
	}
}

// ─── /_sky/sub topic authorisation ────────────────────────────

func TestSpaSubAllowsTopic(t *testing.T) {
	subs := Sub_batch([]any{
		Sub_every(1000, func(any) any { return nil }),
		Sub_subscribeTopic("chat", func(any) any { return nil }),
		Sub_batch([]any{Sub_subscribeTopic("user:42", func(any) any { return nil })}),
	})
	cases := []struct {
		topic string
		want  bool
	}{
		{"chat", true},
		{"user:42", true},
		{"user:43", false},
		{"", false},
		{"Chat", false},
	}
	for _, c := range cases {
		if got := Spa_subAllowsTopic(subs, c.topic); got != c.want {
			t.Errorf("topic %q: got %v, want %v", c.topic, got, c.want)
		}
	}
	if Spa_subAllowsTopic(Sub_none(), "chat") != false {
		t.Error("Sub.none authorises no topic")
	}
	if Spa_subAllowsTopic(nil, "chat") != false {
		t.Error("a non-Sub value authorises no topic (fail closed)")
	}
}
