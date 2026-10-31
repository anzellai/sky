package rt

import (
	"strings"
	"testing"
	"time"
)

// The Sky.Spa session cookie `sky_spa` lives as long as the signed token it
// carries. Without a Max-Age it was a browser-session cookie: a browser that
// clears session cookies on quit (or a mobile browser the system reclaims)
// dropped a sign-in whose token had weeks to run. The lifetime is 30 days by
// default, `App.withSessionLifetime` sets it in code (applied as a server
// setting), and SKY_SPA_SESSION_LIFETIME overrides both.

func TestSpaSessionCookieCarriesTheTokenLifetime(t *testing.T) {
	resetEnvFor(t, "SPA_SESSION_LIFETIME")
	if got := spaSessionLifetime(); got != 30*24*time.Hour {
		t.Fatalf("default lifetime: %v", got)
	}
	attrs := AsString(Spa_sessionCookieAttrs(struct{}{}))
	if !strings.Contains(attrs, "Max-Age=2592000") || !strings.HasPrefix(attrs, "Path=/; HttpOnly; SameSite=Lax") {
		t.Fatalf("sky_spa attributes: %q", attrs)
	}
}

func TestSpaSessionLifetimeBuilderAndEnv(t *testing.T) {
	resetEnvFor(t, "SPA_SESSION_LIFETIME")
	// The builder layer: App.withSessionLifetime reaches the runtime as a
	// server setting (Spa_applyServerSettings).
	_ = Spa_applyServerSettings([]any{SkyTuple2{V0: "SPA_SESSION_LIFETIME", V1: "604800"}}).(func() any)()
	if got := spaSessionLifetime(); got != 7*24*time.Hour {
		t.Fatalf("builder lifetime: %v", got)
	}
	if attrs := AsString(Spa_sessionCookieAttrs(struct{}{})); !strings.Contains(attrs, "Max-Age=604800") {
		t.Fatalf("builder Max-Age: %q", attrs)
	}
	// The operator's environment wins over the builder.
	t.Setenv(skyEnvName("SPA_SESSION_LIFETIME"), "24h")
	if got := spaSessionLifetime(); got != 24*time.Hour {
		t.Fatalf("env lifetime: %v", got)
	}
}

func TestSpaSignedTokenExpiresWithTheLifetime(t *testing.T) {
	withMemoryRevocations(t)
	resetEnvFor(t, "SPA_SESSION_LIFETIME")
	t.Setenv(skyEnvName("SPA_SESSION_LIFETIME"), "3600")
	tok := spaSign(t, "", spaSignedIn())
	claims, ok := okClaims(Spa_verifySession(spaTestSecret, tok))
	if !ok {
		t.Fatalf("the token does not verify")
	}
	left := int64(time.Until(claimExp(claims)).Seconds())
	if left < 3590 || left > 3600 {
		t.Fatalf("token exp is %ds from now, want about 3600", left)
	}
}

func TestSpaLegacyMoveSetsMaxAgeOnSkySpa(t *testing.T) {
	withMemoryRevocations(t)
	resetSpaLegacyCache(t)
	resetEnvFor(t, "SPA_SESSION_LIFETIME")
	current := spaSign(t, "", spaSignedIn())
	old := spaRequestWith(map[string]string{"sky_sid": current})
	out := Spa_sessionCookies(spaTestSecret, old, SkyResponse{Status: 200}).(SkyResponse)
	var spa string
	for _, c := range out.Cookies {
		if strings.HasPrefix(c, "sky_spa=") {
			spa = c
		}
	}
	if !strings.Contains(spa, "Max-Age=2592000") {
		t.Fatalf("the moved sky_spa cookie has no lifetime: %q", spa)
	}
}
