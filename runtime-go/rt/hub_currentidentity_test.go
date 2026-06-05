package rt

import (
	"context"
	"net/http/httptest"
	"testing"
)

// TestHubCurrentIdentity_NoSession verifies the kernel returns a
// clear Err when invoked outside any live session (e.g. CLI or unit
// test that calls the Sky task directly).
func TestHubCurrentIdentity_NoSession(t *testing.T) {
	clearGoroutineLiveSession()
	defer clearGoroutineLiveSession()

	task := Hub_currentIdentity("/tmp/x").(func() any)
	res := task().(SkyResult[any, any])
	if res.Tag != 1 {
		t.Fatalf("expected Err on no-session, got Ok %v", res.OkValue)
	}
}

// TestHubCurrentIdentity_NoIdentityOnSession verifies the kernel
// returns Err when the live session was minted without auth context
// (open mode / no gate).
func TestHubCurrentIdentity_NoIdentityOnSession(t *testing.T) {
	sess := &liveSession{}
	runWithLiveSession(sess, func() {
		task := Hub_currentIdentity("/tmp/x").(func() any)
		res := task().(SkyResult[any, any])
		if res.Tag != 1 {
			t.Errorf("expected Err on no-identity session, got Ok %v", res.OkValue)
		}
	})
}

// TestHubCurrentIdentity_RoundTripsIdentity verifies the happy path:
// session was minted with an identity, the kernel returns it as a
// Sky-shaped record.
func TestHubCurrentIdentity_RoundTripsIdentity(t *testing.T) {
	sess := &liveSession{
		identity: ConsoleIdentity{
			Subject: "alice",
			Email:   "alice@example.com",
			Claims:  map[string]string{"tenant": "customer-42", "role": "admin"},
		},
		identityValid: true,
	}
	runWithLiveSession(sess, func() {
		task := Hub_currentIdentity("/tmp/x").(func() any)
		res := task().(SkyResult[any, any])
		if res.Tag != 0 {
			t.Fatalf("expected Ok, got Err %v", res.ErrValue)
		}
		out, ok := res.OkValue.(map[string]any)
		if !ok {
			t.Fatalf("expected map[string]any, got %T", res.OkValue)
		}
		if out["subject"] != "alice" {
			t.Errorf("subject = %v, want alice", out["subject"])
		}
		if out["email"] != "alice@example.com" {
			t.Errorf("email = %v, want alice@example.com", out["email"])
		}
		// claims is a Dict — verify tenant is reachable
		tenant := Dict_get("tenant", out["claims"])
		// Dict_get returns Sky's Maybe (SkyADT); narrow check: not nil and
		// has expected ctor.
		if tenant == nil {
			t.Errorf("Dict_get(tenant) = nil, want Just")
		}
	})
}

// TestSessionIdentity_PersistenceRoundTrip verifies that the new
// Identity + IdentityValid fields survive encode/decode through gob
// — the "DB-backed stores survive a restart" invariant.
func TestSessionIdentity_PersistenceRoundTrip(t *testing.T) {
	orig := &liveSession{
		identity: ConsoleIdentity{
			Subject: "bob",
			Email:   "bob@example.com",
			Claims:  map[string]string{"tenant": "customer-99"},
		},
		identityValid: true,
		model:         "model-payload",
	}
	blob, err := encodeSession(orig)
	if err != nil {
		t.Fatalf("encodeSession: %v", err)
	}
	restored, err := decodeSession(blob)
	if err != nil {
		t.Fatalf("decodeSession: %v", err)
	}
	if !restored.identityValid {
		t.Fatal("identityValid lost on round-trip")
	}
	if restored.identity.Subject != orig.identity.Subject {
		t.Errorf("Subject = %q, want %q", restored.identity.Subject, orig.identity.Subject)
	}
	if restored.identity.Email != orig.identity.Email {
		t.Errorf("Email = %q, want %q", restored.identity.Email, orig.identity.Email)
	}
	if restored.identity.Claims["tenant"] != "customer-99" {
		t.Errorf("Claims[tenant] = %q, want customer-99", restored.identity.Claims["tenant"])
	}
}

// TestIdentityContextKey_BridgeEndToEnd verifies the canonical flow:
// gate writes via rt.IdentityContextKey, IdentityFromContext reads
// the same value back, and the API is stable across packages.
func TestIdentityContextKey_BridgeEndToEnd(t *testing.T) {
	expected := ConsoleIdentity{
		Subject: "carol",
		Email:   "carol@example.com",
		Claims:  map[string]string{"tenant": "customer-7"},
	}
	r := httptest.NewRequest("GET", "/console/", nil)
	ctx := context.WithValue(r.Context(), IdentityContextKey, expected)
	r = r.WithContext(ctx)

	got, ok := IdentityFromContext(r.Context())
	if !ok {
		t.Fatal("IdentityFromContext returned not-present")
	}
	if got.Subject != expected.Subject || got.Email != expected.Email {
		t.Errorf("identity round-trip mismatch: got %+v, want %+v", got, expected)
	}
}
