package rt

// Goal #5 (grill B2): the admin Data-access gate must be FAIL-CLOSED. The old
// reused pattern was fail-OPEN — a session with no tenant claim read EVERY tenant.
// These pin the corrected matrix + the collection enumeration.

import (
	"strings"
	"testing"

	"sky-app/bluedb"
)

func TestConsoleDataAccess_FailClosedMatrix(t *testing.T) {
	cases := []struct {
		name       string
		prod       bool
		verified   bool
		superAdmin bool
		tenant     string
		wantAllow  bool
		wantScoped bool
	}{
		{"dev unscoped", false, false, false, "", true, false},
		{"prod no identity → DENY", true, false, false, "", false, false},
		{"prod verified + tenant → scoped", true, true, false, "acme", true, true},
		{"prod verified super-admin no tenant → full", true, true, true, "", true, false},
		// The B2 fix: verified, NO tenant, NOT super-admin → DENY (not all-tenants).
		{"prod verified no-tenant no-super → DENY (B2)", true, true, false, "", false, false},
		// A super-admin WITH a tenant is still scoped (least privilege).
		{"prod super-admin WITH tenant → scoped", true, true, true, "acme", true, true},
	}
	for _, c := range cases {
		d := consoleDataAccess(c.prod, c.verified, c.superAdmin, c.tenant)
		if d.Allowed != c.wantAllow {
			t.Fatalf("%s: Allowed=%v want %v (%s)", c.name, d.Allowed, c.wantAllow, d.Reason)
		}
		if d.Allowed && d.Scoped != c.wantScoped {
			t.Fatalf("%s: Scoped=%v want %v (%s)", c.name, d.Scoped, c.wantScoped, d.Reason)
		}
		if d.Scoped && d.Tenant != c.tenant {
			t.Fatalf("%s: Tenant=%q want %q", c.name, d.Tenant, c.tenant)
		}
	}
}

// Explicit non-vacuous check that the B2 leak is closed: verified-but-no-tenant is
// NOT granted all-tenant access.
func TestConsoleDataAccess_NoTenantNeverAllTenants(t *testing.T) {
	d := consoleDataAccess(true /*prod*/, true /*verified*/, false /*superAdmin*/, "" /*tenant*/)
	if d.Allowed {
		t.Fatalf("B2 REGRESSION: a verified session with no tenant claim was granted access "+
			"(would read ALL tenants); must fail closed. reason=%q", d.Reason)
	}
}

func TestAdminEmbeddedCollections_Enumerates(t *testing.T) {
	eng, err := bluedb.Open(t.TempDir())
	if err != nil {
		t.Skipf("bluedb.Open unavailable: %v", err)
	}
	defer eng.Close()
	be := bluedb.NewEmbeddedBackend(eng)
	be.Register(bluedb.CollSchema{Name: "todos"})
	be.Register(bluedb.CollSchema{Name: "users"})
	id := embeddedRegister(be)
	defer embeddedUnregister(id)

	all := adminEmbeddedCollections()
	got := all[id]
	if len(got) != 2 || got[0] != "todos" || got[1] != "users" {
		t.Fatalf("enumeration = %v, want sorted [todos users]", got)
	}
}

func TestAdminReadRows_ReadsSeededRows(t *testing.T) {
	eng, err := bluedb.Open(t.TempDir())
	if err != nil {
		t.Skipf("bluedb.Open unavailable: %v", err)
	}
	defer eng.Close()
	be := bluedb.NewEmbeddedBackend(eng)
	cs := bluedb.CollSchema{Name: "notes"}
	be.Register(cs)
	if err := be.Put(cs, "1", []byte(`{"id":"1","text":"alpha"}`), nil); err != nil {
		t.Fatalf("put 1: %v", err)
	}
	if err := be.Put(cs, "2", []byte(`{"id":"2","text":"beta"}`), nil); err != nil {
		t.Fatalf("put 2: %v", err)
	}
	rows, err := adminReadRows(be, "notes", 100)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("read %d rows, want 2", len(rows))
	}
	// Rows are the stored bytes; both seeded records must come back.
	joined := string(rows[0]) + string(rows[1])
	if !strings.Contains(joined, "alpha") || !strings.Contains(joined, "beta") {
		t.Fatalf("rows missing seeded content: %q %q", rows[0], rows[1])
	}
}
