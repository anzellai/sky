package rt

// Tests for Middleware_withCors (rt.go ~8919).
//
// Implemented contract (asserted here, not assumed):
//   - Config is a list of allowed origins; "*" enables allow-all.
//   - The incoming Origin is read from the EXACT header key "Origin".
//   - allow = "*" when allow-all; else the echoed origin when it is in
//     the configured set; else "" (no ACAO header).
//   - Preflight (method OPTIONS) short-circuits to 204. CORS headers
//     (Allow-Origin / -Methods / -Headers / Max-Age) are attached only
//     when allow != "". The inner handler is NOT invoked on OPTIONS.
//   - Non-OPTIONS requests ALWAYS delegate to the inner handler (CORS is
//     browser-enforced; a disallowed origin is not blocked server-side).
//     An ACAO header is added to the response only when allow != "".
//
// The middleware never sets Access-Control-Allow-Credentials, so an
// allow-all "*" cannot be paired with credentials (no reflection-with-
// credentials vuln). See report for the Vary: Origin observation.

import "testing"

// corsInner builds an inner handler that records whether it was reached
// and returns a fixed 200 response.
func corsInner(reached *bool) func(any) any {
	return func(_ any) any {
		*reached = true
		return func() any { return Ok[any, any](SkyResponse{Status: 200, Body: "inner"}) }
	}
}

// invokeCors wraps corsInner with the CORS middleware, runs one request,
// and returns the resolved response plus whether the inner was reached.
func invokeCors(t *testing.T, origins []any, req SkyRequest) (SkyResponse, bool) {
	t.Helper()
	reached := false
	mw := Middleware_withCors(origins, corsInner(&reached)).(func(any) any)
	res := anyTaskInvoke(mw(req))
	if res.Tag != 0 {
		t.Fatalf("CORS middleware must return Ok, got Tag=%d", res.Tag)
	}
	resp, ok := res.OkValue.(SkyResponse)
	if !ok {
		t.Fatalf("expected SkyResponse in Ok, got %T", res.OkValue)
	}
	return resp, reached
}

func TestCors_PreflightAllowedOrigin_EchoesAndShortCircuits(t *testing.T) {
	resp, reached := invokeCors(t,
		[]any{"https://good.com"},
		SkyRequest{Method: "OPTIONS", Path: "/api", Headers: map[string]any{"Origin": "https://good.com"}},
	)
	if reached {
		t.Error("preflight OPTIONS must NOT reach the inner handler")
	}
	if resp.Status != 204 {
		t.Errorf("preflight should be 204, got %d", resp.Status)
	}
	if got := resp.Headers["Access-Control-Allow-Origin"]; got != "https://good.com" {
		t.Errorf("ACAO should echo the allowed origin, got %q", got)
	}
	if got := resp.Headers["Access-Control-Allow-Methods"]; got != "GET, POST, PUT, DELETE, OPTIONS" {
		t.Errorf("unexpected Allow-Methods: %q", got)
	}
	if got := resp.Headers["Access-Control-Allow-Headers"]; got != "Content-Type, Authorization" {
		t.Errorf("unexpected Allow-Headers: %q", got)
	}
	if got := resp.Headers["Access-Control-Max-Age"]; got != "3600" {
		t.Errorf("unexpected Max-Age: %q", got)
	}
}

func TestCors_PreflightDisallowedOrigin_NoCorsHeaders(t *testing.T) {
	resp, reached := invokeCors(t,
		[]any{"https://good.com"},
		SkyRequest{Method: "OPTIONS", Path: "/api", Headers: map[string]any{"Origin": "https://evil.com"}},
	)
	if reached {
		t.Error("preflight OPTIONS must NOT reach the inner handler even for a disallowed origin")
	}
	if resp.Status != 204 {
		t.Errorf("preflight should still be 204, got %d", resp.Status)
	}
	if got, ok := resp.Headers["Access-Control-Allow-Origin"]; ok {
		t.Errorf("disallowed origin must NOT get an ACAO header, got %q", got)
	}
}

func TestCors_AllowedOriginNormalRequest_HeaderPlusInner(t *testing.T) {
	resp, reached := invokeCors(t,
		[]any{"https://a.com", "https://good.com"},
		SkyRequest{Method: "GET", Path: "/api", Headers: map[string]any{"Origin": "https://good.com"}},
	)
	if !reached {
		t.Error("normal request with allowed origin must reach the inner handler")
	}
	if resp.Status != 200 {
		t.Errorf("expected inner 200, got %d", resp.Status)
	}
	if got := resp.Headers["Access-Control-Allow-Origin"]; got != "https://good.com" {
		t.Errorf("ACAO should echo the allowed origin on normal request, got %q", got)
	}
}

func TestCors_DisallowedOriginNormalRequest_InnerReachedNoAcao(t *testing.T) {
	// Documents the actual (correct) behaviour: the server still
	// processes the request; the browser is what enforces CORS. No ACAO
	// header is emitted, so a browser blocks the cross-origin read.
	resp, reached := invokeCors(t,
		[]any{"https://good.com"},
		SkyRequest{Method: "GET", Path: "/api", Headers: map[string]any{"Origin": "https://evil.com"}},
	)
	if !reached {
		t.Error("disallowed origin normal request still delegates to inner (server-side not blocked)")
	}
	if got, ok := resp.Headers["Access-Control-Allow-Origin"]; ok {
		t.Errorf("disallowed origin must NOT receive an ACAO header, got %q", got)
	}
}

func TestCors_NoOriginHeader_NoAcao(t *testing.T) {
	resp, reached := invokeCors(t,
		[]any{"https://good.com"},
		SkyRequest{Method: "GET", Path: "/api", Headers: map[string]any{}},
	)
	if !reached {
		t.Error("request without Origin still reaches inner")
	}
	if got, ok := resp.Headers["Access-Control-Allow-Origin"]; ok {
		t.Errorf("no Origin header → no ACAO, got %q", got)
	}
}

func TestCors_WildcardAllowAll_NormalRequest(t *testing.T) {
	resp, reached := invokeCors(t,
		[]any{"*"},
		SkyRequest{Method: "GET", Path: "/api", Headers: map[string]any{"Origin": "https://anything.example"}},
	)
	if !reached {
		t.Error("wildcard config must still reach the inner handler")
	}
	if got := resp.Headers["Access-Control-Allow-Origin"]; got != "*" {
		t.Errorf("wildcard config should emit ACAO=*, got %q", got)
	}
}

func TestCors_WildcardAllowAll_Preflight(t *testing.T) {
	resp, reached := invokeCors(t,
		[]any{"*"},
		SkyRequest{Method: "OPTIONS", Path: "/api", Headers: map[string]any{"Origin": "https://anything.example"}},
	)
	if reached {
		t.Error("wildcard preflight must NOT reach inner")
	}
	if resp.Status != 204 {
		t.Errorf("wildcard preflight should be 204, got %d", resp.Status)
	}
	if got := resp.Headers["Access-Control-Allow-Origin"]; got != "*" {
		t.Errorf("wildcard preflight should emit ACAO=*, got %q", got)
	}
	// The impl never sets Access-Control-Allow-Credentials, so "*" here
	// is safe — a credentialed request cannot pair with a wildcard ACAO.
	if _, ok := resp.Headers["Access-Control-Allow-Credentials"]; ok {
		t.Error("SECURITY: wildcard ACAO must never be paired with Allow-Credentials")
	}
}

func TestCors_WildcardNoOrigin_StillStar(t *testing.T) {
	// allowAll does not depend on the Origin header being present.
	resp, _ := invokeCors(t,
		[]any{"*"},
		SkyRequest{Method: "GET", Path: "/api", Headers: map[string]any{}},
	)
	if got := resp.Headers["Access-Control-Allow-Origin"]; got != "*" {
		t.Errorf("wildcard with no Origin should still emit ACAO=*, got %q", got)
	}
}
