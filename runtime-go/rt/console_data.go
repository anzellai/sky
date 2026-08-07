package rt

// Sky Console — read-only admin Data access (goal #5: "built-in Sky Console admin
// access to records"). This file holds the FAIL-CLOSED access decision (grill B2)
// and the collection enumeration. The rendering (a Data tab in the console
// mini-app) consumes these; it is deliberately read-only (edit is gated on the
// goty.rs record-fieldset codegen bug and ships later).

import "sky-app/bluedb"

// consoleDataDecision is the outcome of the admin data-access gate.
type consoleDataDecision struct {
	Allowed bool
	Scoped  bool   // true → restrict rows to Tenant; false → unscoped (dev or platform admin)
	Tenant  string // the tenant to scope to when Scoped
	Reason  string // human-readable, for the audit log
}

// consoleDataAccess decides read access to the admin Data view. It is FAIL-CLOSED
// — the fix for grill B2, where the reused hub tenant pattern was fail-OPEN (a
// session with no tenant claim was treated as in-scope for EVERY tenant, i.e. a
// cross-tenant read):
//
//   - dev (console open, not productionFromEnv): unscoped access for the single
//     developer.
//   - prod + no verified console identity: DENIED (nothing).
//   - prod + verified + a tenant claim: scoped to that tenant.
//   - prod + verified + NO tenant claim + an explicit super-admin marker: unscoped
//     platform access.
//   - prod + verified + NO tenant claim + NO super-admin marker: DENIED. This is
//     the B2 fix — a missing tenant defaults to NOTHING, never all-tenants.
func consoleDataAccess(prod, verified, superAdmin bool, tenant string) consoleDataDecision {
	if !prod {
		return consoleDataDecision{Allowed: true, Scoped: false, Reason: "dev console — unscoped"}
	}
	if !verified {
		return consoleDataDecision{Allowed: false, Reason: "no verified console identity"}
	}
	if tenant != "" {
		return consoleDataDecision{Allowed: true, Scoped: true, Tenant: tenant, Reason: "tenant-scoped"}
	}
	if superAdmin {
		return consoleDataDecision{Allowed: true, Scoped: false, Reason: "platform super-admin"}
	}
	// verified, no tenant, not super-admin → fail-closed (B2: refuse all-tenant access).
	return consoleDataDecision{Allowed: false,
		Reason: "no tenant claim and no super-admin marker — refusing all-tenant access (fail-closed)"}
}

// adminEmbeddedCollections enumerates the registered collections across every open
// embedded backend (the in-process registry), for the Data view's collection list.
// Read-only. Non-embedded (SQL) backends are not in this registry; SQL admin is a
// follow-on. Keyed by connection id; names sorted.
func adminEmbeddedCollections() map[int64][]string {
	out := map[int64][]string{}
	embeddedRegistryMu.Lock()
	ids := make([]int64, 0, len(embeddedByID))
	backends := make([]*bluedb.EmbeddedBackend, 0, len(embeddedByID))
	for id, v := range embeddedByID {
		if be, ok := v.(*bluedb.EmbeddedBackend); ok {
			ids = append(ids, id)
			backends = append(backends, be)
		}
	}
	embeddedRegistryMu.Unlock() // don't hold the registry lock across CollectionNames (takes the backend's lock)
	for i, id := range ids {
		out[id] = backends[i].CollectionNames()
	}
	return out
}

// adminReadRows reads up to `limit` rows (the stored JSON/codec bytes) from a
// collection for the read-only admin Data view. This is the UNSCOPED read — it is
// only reached when consoleDataAccess granted UNSCOPED access (dev or an explicit
// platform super-admin). A tenant-scoped read applies a per-collection tenant-column
// filter (grill B2's row filter) and is a follow-on; until it lands, a tenant-scoped
// decision must NOT call this. The collection must already be registered on the
// backend (every app write registers it); passing a bare {Name} reuses the
// registered schema (ensureRegistered is set-if-absent).
func adminReadRows(be *bluedb.EmbeddedBackend, collName string, limit int) ([][]byte, error) {
	if limit <= 0 {
		limit = 100
	}
	return be.Query(bluedb.CollSchema{Name: collName}, bluedb.QueryPlan{Limit: limit})
}
