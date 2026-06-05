// Tests for v0.16.4 B8 — hub auth-app mode.
//
// Covers:
//   - HubConfig.Validate accepts "app" mode with a token set
//     (OTLP receivers still use bearer)
//   - HubConfig.Validate rejects "app" mode without a token
//   - HubConfig.Validate rejects unknown mode strings
//   - consoleGateApp returns 503 when no callback is registered
//   - consoleGateApp returns 401 when the callback rejects
//   - consoleGateApp returns true (passes) when the callback allows
//   - safeInvokeAppAuth recovers from a panicking callback
//
// Run order is sensitive because RegisterAppAuthCallback writes to
// a package-level slot. Tests clean up with `defer
// RegisterAppAuthCallback(nil)` so they don't leak state into the
// shared mux tests in mount_test.go (which run AFTER these in
// alphabetical order — but defensive cleanup is cheap).

package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	rt "sky-app/rt"
)

func baseAppCfg() HubConfig {
	return HubConfig{
		Port:            0,
		DataDir:         "./irrelevant-validate-only",
		AuthMode:        "app",
		Token:           "secret-token-32-chars-long-XXXXXX",
		MaxPayloadBytes: DefaultMaxPayloadBytes,
		RetentionHours:  1,
		PruneInterval:   time.Hour,
	}
}

func TestValidate_AppModeAcceptsWithToken(t *testing.T) {
	cfg := baseAppCfg()
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate(app, token=set) = %v, want nil", err)
	}
}

func TestValidate_AppModeRejectsWithoutToken(t *testing.T) {
	cfg := baseAppCfg()
	cfg.Token = ""
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate(app, token=empty) = nil, want error")
	}
	want := "auth=app requires SKY_CONSOLE_HUB_TOKEN"
	if !contains(err.Error(), want) {
		t.Errorf("Validate error = %q, want substring %q", err, want)
	}
}

func TestValidate_RejectsUnknownMode(t *testing.T) {
	cfg := baseAppCfg()
	cfg.AuthMode = "bogus"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate(bogus) = nil, want error")
	}
	if !contains(err.Error(), `unknown auth mode "bogus"`) {
		t.Errorf("Validate error = %q, want unknown-mode complaint", err)
	}
}

// consoleGateApp behavior — exercised in isolation via httptest's
// ResponseRecorder so we don't need to spin up a full mux. The
// gate writes the response on deny and returns true/false; we
// assert on both.

func TestConsoleGateApp_NoCallback503(t *testing.T) {
	RegisterAppAuthCallback(nil)
	defer RegisterAppAuthCallback(nil)

	req := httptest.NewRequest(http.MethodGet, "/console/", nil)
	rec := httptest.NewRecorder()
	ok := consoleGateApp(rec, req)

	if ok {
		t.Error("consoleGateApp returned true with no callback; want false")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	wa := rec.Header().Get("WWW-Authenticate")
	if !contains(wa, "auth-app-unregistered") {
		t.Errorf("WWW-Authenticate = %q, want substring auth-app-unregistered", wa)
	}
}

func TestConsoleGateApp_CallbackDenies401(t *testing.T) {
	RegisterAppAuthCallback(func(r *http.Request) (rt.ConsoleIdentity, bool) {
		return rt.ConsoleIdentity{}, false
	})
	defer RegisterAppAuthCallback(nil)

	req := httptest.NewRequest(http.MethodGet, "/console/", nil)
	rec := httptest.NewRecorder()
	ok := consoleGateApp(rec, req)

	if ok {
		t.Error("consoleGateApp returned true on deny; want false")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	wa := rec.Header().Get("WWW-Authenticate")
	if !contains(wa, "SkyHubApp") || contains(wa, "auth-app-unregistered") {
		t.Errorf("WWW-Authenticate = %q, want SkyHubApp scheme without unregistered reason", wa)
	}
}

func TestConsoleGateApp_CallbackAllows(t *testing.T) {
	RegisterAppAuthCallback(func(r *http.Request) (rt.ConsoleIdentity, bool) {
		return rt.ConsoleIdentity{
			Subject: "alice",
			Email:   "alice@example.com",
			Claims:  map[string]string{"tenant": "customer-42"},
		}, true
	})
	defer RegisterAppAuthCallback(nil)

	req := httptest.NewRequest(http.MethodGet, "/console/", nil)
	rec := httptest.NewRecorder()
	ok := consoleGateApp(rec, req)

	if !ok {
		t.Errorf("consoleGateApp returned false on allow; want true (recorder code=%d)", rec.Code)
	}
	if rec.Code != http.StatusOK {
		// 200 is the default for ResponseRecorder when nothing was
		// written. The gate writes ONLY on deny.
		t.Errorf("status = %d, want 200 (unwritten by gate)", rec.Code)
	}
}

// TestConsoleGateApp_IdentityThreadsToContext — the v0.16.5 tenant
// gate's main invariant: after the gate allows, downstream handlers
// can read the ConsoleIdentity from r.Context() via
// IdentityFromContext. Without this, app-auth-mode can't filter
// queries by tenant.
func TestConsoleGateApp_IdentityThreadsToContext(t *testing.T) {
	expected := rt.ConsoleIdentity{
		Subject: "bob",
		Email:   "bob@example.com",
		Claims:  map[string]string{"tenant": "customer-99"},
	}
	RegisterAppAuthCallback(func(r *http.Request) (rt.ConsoleIdentity, bool) {
		return expected, true
	})
	defer RegisterAppAuthCallback(nil)

	req := httptest.NewRequest(http.MethodGet, "/console/", nil)
	rec := httptest.NewRecorder()
	ok := consoleGateApp(rec, req)

	if !ok {
		t.Fatalf("gate denied — wanted allow")
	}
	got, present := IdentityFromContext(req.Context())
	if !present {
		t.Fatal("IdentityFromContext returned not-present after gate allowed")
	}
	if got.Subject != expected.Subject {
		t.Errorf("Subject = %q, want %q", got.Subject, expected.Subject)
	}
	if got.Email != expected.Email {
		t.Errorf("Email = %q, want %q", got.Email, expected.Email)
	}
	if got.Claims["tenant"] != expected.Claims["tenant"] {
		t.Errorf("tenant claim = %q, want %q",
			got.Claims["tenant"], expected.Claims["tenant"])
	}
}

// TestIdentityFromContext_NoIdentity — pure helper test for the
// off-mode / no-auth-ran case. Returns (zero, false).
func TestIdentityFromContext_NoIdentity(t *testing.T) {
	ctx := context.Background()
	id, ok := IdentityFromContext(ctx)
	if ok {
		t.Errorf("IdentityFromContext on bare context returned ok=true; want false")
	}
	if id.Subject != "" || id.Email != "" || len(id.Claims) != 0 {
		t.Errorf("zero ConsoleIdentity expected on no-identity ctx, got %+v", id)
	}
}

// TestConsoleGateApp_CallbackPanicDeniesNotCrashes — a buggy
// callback must not take down the hub. The gate's defer/recover
// translates the panic to a deny (401), same as a regular reject.
func TestConsoleGateApp_CallbackPanicDeniesNotCrashes(t *testing.T) {
	RegisterAppAuthCallback(func(r *http.Request) (rt.ConsoleIdentity, bool) {
		panic("callback exploded")
	})
	defer RegisterAppAuthCallback(nil)

	req := httptest.NewRequest(http.MethodGet, "/console/", nil)
	rec := httptest.NewRecorder()
	// Wrap the call so a propagated panic surfaces as a test
	// failure rather than crashing the test binary.
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("consoleGateApp propagated panic: %v", rec)
		}
	}()
	ok := consoleGateApp(rec, req)
	if ok {
		t.Error("consoleGateApp returned true after callback panic; want false")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) &&
		(haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
