package rt

import (
	"testing"
)

// fakeTenantStore implements HubStoreReader AND
// HubStoreReaderWithTenant.  The "WithTenant" methods record the
// arguments they were called with so tests can assert the kernel
// dispatched into the v0.16.6 path.
type fakeTenantStore struct {
	HubStoreReader // anonymous embed: legacy methods come from a stub

	gotLogsSvc, gotLogsTenant, gotLogsFilter string
	gotMetSvc, gotMetTenant                  string
	gotSpnSvc, gotSpnTenant                  string
	gotErrSvc, gotErrTenant                  string

	logsJSON string // canned response, default "[]"
	metJSON  string
	spnJSON  string
	errJSON  string
}

func (f *fakeTenantStore) QueryFilteredLogsJSONWithTenant(svc, tenant, filter string) (string, error) {
	f.gotLogsSvc, f.gotLogsTenant, f.gotLogsFilter = svc, tenant, filter
	if f.logsJSON == "" {
		return "[]", nil
	}
	return f.logsJSON, nil
}
func (f *fakeTenantStore) QueryFilteredMetricsJSONWithTenant(svc, tenant string) (string, error) {
	f.gotMetSvc, f.gotMetTenant = svc, tenant
	if f.metJSON == "" {
		return "[]", nil
	}
	return f.metJSON, nil
}
func (f *fakeTenantStore) QueryFilteredSpansJSONWithTenant(svc, tenant string) (string, error) {
	f.gotSpnSvc, f.gotSpnTenant = svc, tenant
	if f.spnJSON == "" {
		return "[]", nil
	}
	return f.spnJSON, nil
}
func (f *fakeTenantStore) QueryFilteredErrorsJSONWithTenant(svc, tenant string) (string, error) {
	f.gotErrSvc, f.gotErrTenant = svc, tenant
	if f.errJSON == "" {
		return "[]", nil
	}
	return f.errJSON, nil
}

// legacyOnlyStore implements ONLY the v0.16.4 HubStoreReader so we
// can verify the kernel falls through cleanly when the runtime
// hasn't been upgraded to the WithTenant variant.
type legacyOnlyStore struct {
	HubStoreReader
	gotLogsSvc, gotLogsFilter string
	gotMetSvc                 string
}

func (l *legacyOnlyStore) QueryFilteredLogsJSON(svc, filter string) (string, error) {
	l.gotLogsSvc, l.gotLogsFilter = svc, filter
	return "[]", nil
}
func (l *legacyOnlyStore) QueryFilteredMetricsJSON(svc string) (string, error) {
	l.gotMetSvc = svc
	return "[]", nil
}
func (l *legacyOnlyStore) QueryFilteredSpansJSON(svc string) (string, error) {
	return "[]", nil
}
func (l *legacyOnlyStore) QueryFilteredErrorsJSON(svc string) (string, error) {
	return "[]", nil
}

// TestTenantScope_RoutesToWithTenantWhenSessionHasTenant verifies
// the happy path: session has a tenant claim, kernel routes through
// the WithTenant variant with the prefix attached.
func TestTenantScope_RoutesToWithTenantWhenSessionHasTenant(t *testing.T) {
	store := &fakeTenantStore{}
	SetHubStore(store)
	defer SetHubStore(nil)

	sess := &liveSession{
		identity: ConsoleIdentity{
			Subject: "alice",
			Email:   "alice@example.com",
			Claims:  map[string]string{"tenant": "customer-42-"},
		},
		identityValid: true,
	}

	runWithLiveSession(sess, func() {
		// Caller passes "" → kernel uses tenant alone as scope.
		task := Hub_readFilteredLogs("/tmp/x", "", nil).(func() any)
		res := task().(SkyResult[any, any])
		if res.Tag != 0 {
			t.Fatalf("expected Ok, got Err %v", res.ErrValue)
		}
		if store.gotLogsTenant != "customer-42-" {
			t.Errorf("expected tenant=customer-42-, got %q", store.gotLogsTenant)
		}
		if store.gotLogsSvc != "" {
			t.Errorf("expected empty svc with tenant-only scope, got %q", store.gotLogsSvc)
		}
	})
}

// TestTenantScope_RejectsCrossTenantSvc verifies the kernel refuses
// a service-name that doesn't start with the tenant prefix even
// when the bundled console mis-threads it.
func TestTenantScope_RejectsCrossTenantSvc(t *testing.T) {
	store := &fakeTenantStore{}
	SetHubStore(store)
	defer SetHubStore(nil)

	sess := &liveSession{
		identity: ConsoleIdentity{
			Subject: "alice",
			Claims:  map[string]string{"tenant": "customer-42-"},
		},
		identityValid: true,
	}

	runWithLiveSession(sess, func() {
		// Caller passes "customer-99-billing" — different tenant.
		task := Hub_readFilteredLogs("/tmp/x", "customer-99-billing", nil).(func() any)
		res := task().(SkyResult[any, any])
		if res.Tag != 1 {
			t.Fatalf("expected Err on cross-tenant svc, got Ok %v", res.OkValue)
		}
		if store.gotLogsTenant != "" {
			t.Errorf("kernel should NOT have dispatched to store, got tenant=%q", store.gotLogsTenant)
		}
	})
}

// TestTenantScope_AllowsInTenantSvc verifies an explicit
// service-name that DOES start with the tenant prefix is forwarded.
func TestTenantScope_AllowsInTenantSvc(t *testing.T) {
	store := &fakeTenantStore{}
	SetHubStore(store)
	defer SetHubStore(nil)

	sess := &liveSession{
		identity: ConsoleIdentity{
			Subject: "alice",
			Claims:  map[string]string{"tenant": "customer-42-"},
		},
		identityValid: true,
	}

	runWithLiveSession(sess, func() {
		// In-tenant: customer-42-billing starts with customer-42-.
		task := Hub_readFilteredMetrics("/tmp/x", "customer-42-billing").(func() any)
		res := task().(SkyResult[any, any])
		if res.Tag != 0 {
			t.Fatalf("expected Ok, got Err %v", res.ErrValue)
		}
		if store.gotMetSvc != "customer-42-billing" {
			t.Errorf("expected svc forwarded as-is, got %q", store.gotMetSvc)
		}
		if store.gotMetTenant != "customer-42-" {
			t.Errorf("expected tenant=customer-42-, got %q", store.gotMetTenant)
		}
	})
}

// TestTenantScope_PassThroughWhenNoTenantClaim verifies the kernel
// is byte-identical to v0.16.4 behaviour when the session has no
// tenant claim — the legacy QueryFiltered* path is taken.
func TestTenantScope_PassThroughWhenNoTenantClaim(t *testing.T) {
	store := &legacyOnlyStore{}
	SetHubStore(store)
	defer SetHubStore(nil)

	sess := &liveSession{
		identity: ConsoleIdentity{
			Subject: "alice",
			Claims:  map[string]string{}, // no tenant
		},
		identityValid: true,
	}

	runWithLiveSession(sess, func() {
		task := Hub_readFilteredLogs("/tmp/x", "billing", nil).(func() any)
		res := task().(SkyResult[any, any])
		if res.Tag != 0 {
			t.Fatalf("expected Ok, got Err %v", res.ErrValue)
		}
		if store.gotLogsSvc != "billing" {
			t.Errorf("expected svc=billing forwarded, got %q", store.gotLogsSvc)
		}
	})
}

// TestTenantScope_FallsThroughWhenStoreIsLegacy verifies the
// kernel falls back to QueryFilteredLogsJSON when the runtime
// store doesn't implement the WithTenant interface — important for
// downstream consumers that haven't upgraded their StoreReader.
func TestTenantScope_FallsThroughWhenStoreIsLegacy(t *testing.T) {
	store := &legacyOnlyStore{}
	SetHubStore(store)
	defer SetHubStore(nil)

	sess := &liveSession{
		identity: ConsoleIdentity{
			Subject: "alice",
			Claims:  map[string]string{"tenant": "customer-42-"},
		},
		identityValid: true,
	}

	runWithLiveSession(sess, func() {
		task := Hub_readFilteredLogs("/tmp/x", "", nil).(func() any)
		res := task().(SkyResult[any, any])
		if res.Tag != 0 {
			t.Fatalf("expected Ok, got Err %v", res.ErrValue)
		}
		// Legacy fall-through: gets svc="" without tenant scoping.
		// (The tenant gate at the kernel still refuses cross-tenant
		// svc, but if svc is empty the legacy path doesn't enforce
		// anything — this is the documented downgrade case.)
		if store.gotLogsSvc != "" {
			t.Errorf("expected legacy fall-through with svc=\"\", got %q", store.gotLogsSvc)
		}
	})
}

// TestEscapeLikePrefix_NoOpOnCleanInputs verifies the helper in the
// hub package is a no-op when there are no SQL wildcards.
func TestEscapeLikePrefix_NoOpOnCleanInputs(t *testing.T) {
	// This test is in rt package, can't directly call hub.escapeLikePrefix.
	// Coverage of the helper lives in rt/hub/store_test.go if present.
	// Here we cover the rt-side rejectCrossTenantSvc helper.
	cases := []struct {
		svc, tenant string
		wantSvc     string
		wantOk      bool
	}{
		{"", "", "", true},
		{"foo", "", "foo", true},
		{"", "tenant-", "", true},
		{"tenant-foo", "tenant-", "tenant-foo", true},
		{"other-foo", "tenant-", "", false},
		{"tenant", "tenant-", "", false}, // prefix match must be strict
	}
	for i, c := range cases {
		got, ok := rejectCrossTenantSvc(c.svc, c.tenant)
		if got != c.wantSvc || ok != c.wantOk {
			t.Errorf("case %d (%q, %q): got (%q, %v), want (%q, %v)",
				i, c.svc, c.tenant, got, ok, c.wantSvc, c.wantOk)
		}
	}
}
