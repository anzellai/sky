package rt

// The BOOT SNAPSHOT of the production gate, and the divergence it used to
// carry.
//
// `isProd()` (rt.go) reads the environment live. `isProductionMode()`
// (observability.go) reads a snapshot the two serving paths take at startup —
// `Server_listen` (rt.go) and `liveAppRun` (live.go), both
// `SetProductionMode(productionFromEnv())`.
//
// While that snapshot was a plain `atomic.Bool`, NEVER SET was
// indistinguishable from EXPLICITLY FALSE and the default was the open one. So
// under `ENV=production` in a process that had not reached either serving path,
// `isProd()` was true and `isProductionMode()` was false — and it is
// `isProductionMode()` that guards `/_sky/metrics` admin auth, the
// `consoleAuthModeDevOpen` re-tightening in console_auth_v2.go, and the mode
// the console reports.
//
// Processes that reach that state exist: `startWebviewLoopback` serves HTTP and
// never calls `SetProductionMode`; a sub-app installed by
// `MountLiveSubAppInProcess` relies on some parent having called it; and every
// `go test` binary starts there.
//
// `TestOneProductionPredicate` did not cover any of this. It asserted
// `isProd() != productionFromEnv()`, and `isProd()` IS `productionFromEnv()` —
// `x != x`, an assertion that cannot fail.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// theProductionSpellingMatrix is the same input set TestOneProductionPredicate
// uses, kept here so this file's claims are about every spelling rather than
// about `production` alone.
var theProductionSpellingMatrix = []struct {
	key, value string
	want       bool
}{
	{"ENV", "", false},
	{"ENV", "dev", false},
	{"ENV", "development", false},
	{"ENV", "local", false},
	{"ENV", "prod", true},
	{"ENV", "production", true},
	{"ENV", "Production", true},
	{"ENV", "staging", true},
	{"SKY_ENV", "production", true},
	{"SKY_ENV", "dev", false},
}

// TestProductionSnapshotAgreesWithTheLivePredicateWhenUnset — the intended
// relationship, stated positively.
//
// An UNSET snapshot is not evidence that this is not production. It now falls
// back to the live predicate, so the two agree on every input rather than
// diverging in the fail-open direction on whichever inputs nobody checked.
func TestProductionSnapshotAgreesWithTheLivePredicateWhenUnset(t *testing.T) {
	checked := 0
	for _, c := range theProductionSpellingMatrix {
		t.Run(c.key+"="+c.value, func(t *testing.T) {
			clearEnvFlags(t)
			t.Setenv(c.key, c.value)
			clearProductionMode()
			checked++
			if got := isProductionMode(); got != c.want {
				t.Errorf("with the boot snapshot NEVER SET, isProductionMode() = %v, "+
					"want %v (isProd() = %v). An absent snapshot must not read as "+
					"\"not production\": it is what gates /_sky/metrics",
					got, c.want, isProd())
			}
			if isProductionMode() != isProd() {
				t.Errorf("isProductionMode() = %v disagrees with isProd() = %v — "+
					"two production predicates, one of them wrong",
					isProductionMode(), isProd())
			}
		})
	}
	if checked != len(theProductionSpellingMatrix) {
		t.Fatalf("checked %d of %d spellings; a loop that asserts nothing passes",
			checked, len(theProductionSpellingMatrix))
	}
}

// TestProductionSnapshotHonoursAnExplicitOverride — the other half, and the
// reason the snapshot is a TRI-STATE rather than "env always wins".
//
// `SetProductionMode` is exported and both serving paths call it; an embedder
// may call it too. An explicit answer must beat the environment IN BOTH
// DIRECTIONS, or the fallback would silently disarm the override and the
// existing metrics/console tests would be asserting against the environment
// they happen to run under.
func TestProductionSnapshotHonoursAnExplicitOverride(t *testing.T) {
	clearEnvFlags(t)
	t.Setenv("ENV", "production")
	SetProductionMode(false)
	defer clearProductionMode()
	if isProductionMode() {
		t.Errorf("explicit SetProductionMode(false) under ENV=production: "+
			"isProductionMode() = true, want false — the override was ignored "+
			"(isProd() = %v)", isProd())
	}

	t.Setenv("ENV", "dev")
	SetProductionMode(true)
	if !isProductionMode() {
		t.Errorf("explicit SetProductionMode(true) under ENV=dev: " +
			"isProductionMode() = false, want true — the override was ignored")
	}
}

// TestMetricsRequireAuthUnderProductionWithoutBoot — the security property the
// divergence actually cost, asserted end to end rather than through the
// predicate.
//
// `/_sky/metrics` exposes the whole Prometheus snapshot. Its ONLY gate is
// `isProductionMode() && !hasAdminAuth(r)`. Before the tri-state, this test
// returned 200: `ENV=production` was set, no serving path had taken the
// snapshot, so the atomic read false and the endpoint was open.
func TestMetricsRequireAuthUnderProductionWithoutBoot(t *testing.T) {
	resetReadiness(t)
	clearEnvFlags(t)
	t.Setenv("ENV", "production")
	t.Setenv("SKY_ADMIN_TOKEN", "supersecret")
	// No SetProductionMode call — that is the whole point.
	clearProductionMode()
	defer clearProductionMode()

	resp := serveOnce(HandleMetrics, http.MethodGet, "/_sky/metrics")
	if resp.Code != http.StatusUnauthorized {
		t.Errorf("ENV=production with the boot snapshot never taken: "+
			"/_sky/metrics returned %d, want 401. The metrics endpoint is gated by "+
			"isProductionMode() alone, and an unset snapshot must not read as dev",
			resp.Code)
	}
	if resp.Header().Get("WWW-Authenticate") == "" {
		t.Error("a 401 must carry the WWW-Authenticate challenge")
	}

	// CONTROL: with the right bearer the same request is served, so the
	// assertion above is about AUTH and not about the endpoint being broken.
	req := httptest.NewRequest(http.MethodGet, "/_sky/metrics", nil)
	req.Header.Set("Authorization", "Bearer supersecret")
	rr := httptest.NewRecorder()
	HandleMetrics(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("with a valid admin bearer, /_sky/metrics returned %d, want 200 "+
			"— body %q", rr.Code, rr.Body.String())
	}
}
