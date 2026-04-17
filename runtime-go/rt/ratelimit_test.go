package rt

// Audit P1-2: rate-limit middleware must cap requests from a single
// client IP within a sliding window. On overflow, return 429 without
// invoking the wrapped handler. Pre-fix the server had no such
// protection — a simple curl loop could exhaust a Sky.Live or
// Sky.Http.Server deployment.

import (
	"testing"
)

// Helper: simulate N requests from a given IP through the middleware
// and return the count that was allowed to pass (as opposed to
// receiving 429).
func invokeRateLimited(limit int, ip string, calls int) (allowed int, rejected int) {
	innerCalled := 0
	okResp := SkyResponse{Status: 200, Body: "ok"}
	inner := func(_ any) any {
		innerCalled++
		return func() any { return Ok[any, any](okResp) }
	}
	mw := Middleware_rateLimit(limit, inner)
	handler := mw.(func(any) any)
	for i := 0; i < calls; i++ {
		req := SkyRequest{Method: "GET", Path: "/", RemoteAddr: ip}
		taskAny := handler(req)
		res := anyTaskInvoke(taskAny)
		if res.Tag != 0 {
			// Shouldn't happen — our middleware always returns Ok.
			rejected++
			continue
		}
		resp, _ := res.OkValue.(SkyResponse)
		if resp.Status == 429 {
			rejected++
		} else {
			allowed++
		}
	}
	return
}

func TestRateLimit_AllowsUpToLimit(t *testing.T) {
	rateLimitReset()
	allowed, rejected := invokeRateLimited(3, "10.0.0.1:1234", 3)
	if allowed != 3 || rejected != 0 {
		t.Fatalf("3-call cap with 3 calls: want 3 allowed / 0 rejected, got %d / %d", allowed, rejected)
	}
}

func TestRateLimit_RejectsOverCap(t *testing.T) {
	rateLimitReset()
	allowed, rejected := invokeRateLimited(3, "10.0.0.2:4321", 5)
	if allowed != 3 || rejected != 2 {
		t.Fatalf("3-call cap with 5 calls: want 3 allowed / 2 rejected, got %d / %d", allowed, rejected)
	}
}

func TestRateLimit_PerIPIsolation(t *testing.T) {
	// Two clients at the same cap — each gets their own quota.
	rateLimitReset()
	a, _ := invokeRateLimited(2, "10.0.0.3:1", 2)
	b, _ := invokeRateLimited(2, "10.0.0.4:1", 2)
	if a != 2 || b != 2 {
		t.Fatalf("two clients under their own cap should both be allowed: got %d + %d", a, b)
	}
}

func TestRateLimit_MissingIPBypasses(t *testing.T) {
	// No RemoteAddr (unit-test shape): bypass rate limiting so
	// in-process test harnesses aren't accidentally throttled.
	rateLimitReset()
	inner := func(_ any) any {
		return func() any { return Ok[any, any](SkyResponse{Status: 200}) }
	}
	mw := Middleware_rateLimit(1, inner).(func(any) any)
	for i := 0; i < 10; i++ {
		res := anyTaskInvoke(mw(SkyRequest{Method: "GET"}))
		resp, _ := res.OkValue.(SkyResponse)
		if resp.Status != 200 {
			t.Fatalf("no-IP call %d was throttled (status %d) — bypass broken", i, resp.Status)
		}
	}
}

func TestRateLimit_ResponseShape(t *testing.T) {
	// 429 response must carry Retry-After: 60 and the Too Many Requests
	// body so callers / browsers / monitoring can distinguish it from
	// generic 5xx failures.
	rateLimitReset()
	inner := func(_ any) any {
		return func() any { return Ok[any, any](SkyResponse{Status: 200}) }
	}
	mw := Middleware_rateLimit(0, inner).(func(any) any)
	res := anyTaskInvoke(mw(SkyRequest{Method: "GET", RemoteAddr: "1.2.3.4:5678"}))
	resp, ok := res.OkValue.(SkyResponse)
	if !ok {
		t.Fatal("expected SkyResponse in Ok")
	}
	if resp.Status != 429 {
		t.Fatalf("expected 429, got %d", resp.Status)
	}
	if resp.Headers["Retry-After"] != "60" {
		t.Fatalf("expected Retry-After: 60, got %q", resp.Headers["Retry-After"])
	}
	if resp.Body == "" {
		t.Fatal("429 should have a body (Too Many Requests)")
	}
}
