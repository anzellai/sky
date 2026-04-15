package rt

// Audit P1-5: production-mode hardening. Two concerns gated on
// SKY_ENV=prod:
//   1. Cookies issued via Server_withCookie / setCookieHeader /
//      Server_csrfIssue get "Secure" appended automatically so the
//      browser refuses to send them over plain HTTP. Defence against
//      a forgotten-to-redirect-to-HTTPS deployment.
//   2. Panic recovery writes a compact method+path+kind line to
//      stderr (no stack trace leak in aggregated logs) and appends
//      the full frame to .skylog/panic.log. Dev mode keeps the
//      existing full-trace-on-stderr behaviour.

import (
	"os"
	"strings"
	"testing"
)

func withProdEnv(t *testing.T, fn func()) {
	t.Helper()
	prev, had := os.LookupEnv("SKY_ENV")
	if err := os.Setenv("SKY_ENV", "prod"); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	defer func() {
		if had {
			_ = os.Setenv("SKY_ENV", prev)
		} else {
			_ = os.Unsetenv("SKY_ENV")
		}
	}()
	fn()
}

func TestWithCookie_AddsSecureInProd(t *testing.T) {
	withProdEnv(t, func() {
		resp := SkyResponse{Status: 200}
		out := Server_withCookie("session", "tok123", resp).(SkyResponse)
		sc := out.Headers["Set-Cookie"]
		if !strings.Contains(sc, "Secure") {
			t.Fatalf("prod cookie should include Secure: %q", sc)
		}
	})
}

func TestWithCookie_NoSecureInDev(t *testing.T) {
	// Belt-and-braces: SKY_ENV unset.
	_ = os.Unsetenv("SKY_ENV")
	resp := SkyResponse{Status: 200}
	out := Server_withCookie("session", "tok123", resp).(SkyResponse)
	sc := out.Headers["Set-Cookie"]
	if strings.Contains(sc, "Secure") {
		t.Fatalf("dev cookie should NOT include Secure: %q", sc)
	}
}

func TestWithCookie_DoesNotDoubleAddSecure(t *testing.T) {
	// Caller already opted in; don't duplicate.
	withProdEnv(t, func() {
		resp := SkyResponse{Status: 200}
		out := Server_withCookie("session", "tok",
			"Path=/; HttpOnly; Secure", resp).(SkyResponse)
		sc := out.Headers["Set-Cookie"]
		// Exactly one "Secure" occurrence.
		if strings.Count(strings.ToLower(sc), "secure") != 1 {
			t.Fatalf("expected exactly one Secure: %q", sc)
		}
	})
}

func TestCsrfIssue_SecureInProd(t *testing.T) {
	withProdEnv(t, func() {
		resp := SkyResponse{Status: 200}
		out := Server_csrfIssue(resp).(SkyTuple2)
		updated := out.V1.(SkyResponse)
		sc := updated.Headers["Set-Cookie"]
		if !strings.Contains(sc, "Secure") {
			t.Fatalf("prod csrf cookie should include Secure: %q", sc)
		}
	})
}

func TestIsProd_ReadsEnvVar(t *testing.T) {
	_ = os.Unsetenv("SKY_ENV")
	if isProd() {
		t.Fatal("isProd() should be false when SKY_ENV is unset")
	}
	withProdEnv(t, func() {
		if !isProd() {
			t.Fatal("isProd() should be true when SKY_ENV=prod")
		}
	})
	_ = os.Setenv("SKY_ENV", "staging")
	if isProd() {
		t.Fatal("isProd() should be false for SKY_ENV=staging")
	}
	_ = os.Unsetenv("SKY_ENV")
}
