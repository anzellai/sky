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

// TestIsProd_ReadsEnvVar is gone with `isProd()`. It asserted
// `SKY_ENV=staging` is NOT production, i.e. it pinned the divergence
// between the cookie path's own predicate and `productionFromEnv()` —
// the documented gate, under which staging IS production and therefore
// gets Secure cookies. The single predicate is covered end-to-end by
// TestCookieSecurePredicate_MatchesProductionGate (cookie_secure_test.go),
// which asserts the cookie decision and the production gate agree for
// every ENV / SKY_ENV combination.
