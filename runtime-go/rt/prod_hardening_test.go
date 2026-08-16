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

// TestWithCookie_DoesNotDoubleAddSecure used to assert with
//
//	strings.Count(strings.ToLower(sc), "secure") != 1
//
// — the SAME substring predicate as the code it was verifying, so it
// agreed with the bug instead of catching it (`Path=/secure` counts).
// A test must not re-implement the predicate it verifies: idempotency is
// now asserted on the PARSED cookie in
// TestSecurifyCookieAttrs_DoesNotDoubleAddSecure (cookie_secure_test.go),
// which counts Secure ATTRIBUTES and checks `c.Secure` through
// `(&http.Response{…}).Cookies()`.

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
	t.Setenv("ENV", "")
	t.Setenv("SKY_ENV", "")
	if isProd() {
		t.Fatal("isProd() should be false when the env flag is unset")
	}
	withProdEnv(t, func() {
		if !isProd() {
			t.Fatal("isProd() should be true when SKY_ENV=prod")
		}
	})

	// SKY_ENV=staging now gates as production, and this assertion was
	// INVERTED to say so.
	//
	// The previous expectation ("staging is not prod") codified the
	// defect rather than a requirement. productionFromEnv() — the
	// documented gate, observability.go — deliberately biases to gate:
	// dev/development/local are the only non-production markers, and
	// everything else, staging included, is treated as real. Bothering
	// to set the flag at all signals a non-casual deployment.
	//
	// For cookies that is plainly the right answer: a staging deploy is
	// served over HTTPS and its session cookie should carry Secure.
	// Nothing depended on the narrower reading except this line.
	t.Setenv("SKY_ENV", "staging")
	if !isProd() {
		t.Fatal("isProd() should be true for SKY_ENV=staging (bias-to-gate)")
	}
}
