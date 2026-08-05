package rt

import (
	"net/http/httptest"
	"reflect"
	"testing"
)

// TestLiveWithIdentify_StoresFnVerbatim asserts the builder stores the
// callback verbatim under the exact "Identify" key that liveAppRun /
// newLiveAppFromCfg read (invariant 2: never assert to a Go func type). A
// config that never calls withIdentify has NO "Identify" key, so Field returns
// nil and the mint path stays gated off — byte-identical to before.
func TestLiveWithIdentify_StoresFnVerbatim(t *testing.T) {
	base := Live_config(map[string]any{
		"Init":          nil,
		"Update":        nil,
		"View":          nil,
		"Subscriptions": nil,
		"Routes":        nil,
		"NotFound":      nil,
	})

	// Unset → gated off: Field(cfg,"Identify") is nil.
	if got := Field(base, "Identify"); got != nil {
		t.Fatalf("unset withIdentify: expected nil Identify, got %v", got)
	}

	fn := func(req any) any { return nil }
	cfg := Live_withIdentify(fn, base)

	stored := Field(cfg, "Identify")
	if stored == nil {
		t.Fatalf("withIdentify: expected fn stored under Identify, got nil")
	}
	if reflect.ValueOf(stored).Pointer() != reflect.ValueOf(fn).Pointer() {
		t.Fatalf("withIdentify: stored fn is not the callback passed in (should be verbatim)")
	}
	// Shallow-clone invariant 3: the base map is untouched.
	if got := Field(base, "Identify"); got != nil {
		t.Fatalf("withIdentify mutated the base cfg map (should shallow-clone)")
	}
}

// mkIdentityCallback builds a Sky-shaped `Request -> Task Error (Maybe
// Identity)` callback value the mint path consumes: a func returning a Task
// (`func() any`) that yields a Result whose Ok wraps the given Maybe payload.
// `maybe` is the map-shaped ADT (`{"Tag":..., "_0":...}`) the polymorphic
// `Maybe a` return lowers to.
func mkIdentityCallback(maybe any) any {
	return func(req any) any {
		return func() any { return Ok[any, any](maybe) }
	}
}

// TestResolveIdentityFromCallback_Just — happy path: the callback returns
// `Ok (Just identity)`; resolveIdentityFromCallback (the reused console-auth
// invoke path) returns the identity with ok=true and every field round-trips.
func TestResolveIdentityFromCallback_Just(t *testing.T) {
	idRecord := map[string]any{
		"Subject": "bob",
		"Email":   "bob@example.com",
		"Claims":  map[string]any{"tenant": "acme", "role": "editor"},
	}
	just := map[string]any{"Tag": "Just", "_0": idRecord}
	cb := mkIdentityCallback(just)

	r := httptest.NewRequest("GET", "/", nil)
	id, ok := resolveIdentityFromCallback(cb, r)
	if !ok {
		t.Fatalf("expected ok=true for Just identity, got false")
	}
	if id.Subject != "bob" {
		t.Errorf("Subject: want bob, got %q", id.Subject)
	}
	if id.Email != "bob@example.com" {
		t.Errorf("Email: want bob@example.com, got %q", id.Email)
	}
	if id.Claims["tenant"] != "acme" || id.Claims["role"] != "editor" {
		t.Errorf("Claims round-trip failed: %v", id.Claims)
	}
}

// TestResolveIdentityFromCallback_Nothing — fail-closed: `Ok Nothing` leaves
// the session anonymous (ok=false → identityValid stays false at the mint site).
func TestResolveIdentityFromCallback_Nothing(t *testing.T) {
	nothing := map[string]any{"Tag": "Nothing"}
	cb := mkIdentityCallback(nothing)

	r := httptest.NewRequest("GET", "/", nil)
	if _, ok := resolveIdentityFromCallback(cb, r); ok {
		t.Fatalf("expected ok=false for Nothing (fail-closed), got true")
	}
}

// TestResolveIdentityFromCallback_NilCallback — a nil callback (the app never
// set withIdentify) is a no-op deny; the mint path gates on app.identify != nil
// so this is defence-in-depth.
func TestResolveIdentityFromCallback_NilCallback(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if _, ok := resolveIdentityFromCallback(nil, r); ok {
		t.Fatalf("expected ok=false for nil callback, got true")
	}
}

// mkErrCallback builds a `Request -> Task Error (Maybe Identity)` callback whose
// Task yields an `Err` (int-tagged SkyResult{Tag:1}) — the shape a real
// identify callback produces on a failed JWT verify (Task.fail / Task.fromResult
// (Err …)). This is the fail-OPEN regression: without the positive-Just guard,
// the string-tagged consoleIsResultErr misses the int tag and the resolver
// returns an empty-but-valid identity.
func mkErrCallback(errVal any) any {
	return func(req any) any {
		return func() any { return Err[any, any](errVal) }
	}
}

// TestResolveIdentityFromCallback_Err — SECURITY fail-closed: an Err result must
// leave the session anonymous (ok=false), NOT grant an empty valid identity.
func TestResolveIdentityFromCallback_Err(t *testing.T) {
	errVal := map[string]any{"Tag": "Unexpected", "_0": "jwt verify failed"}
	cb := mkErrCallback(errVal)

	r := httptest.NewRequest("GET", "/", nil)
	id, ok := resolveIdentityFromCallback(cb, r)
	if ok {
		t.Fatalf("SECURITY: Err result granted an identity (fail-OPEN): id=%+v", id)
	}
	if id.Subject != "" || len(id.Claims) != 0 {
		t.Fatalf("Err result must yield an empty identity, got %+v", id)
	}
}
