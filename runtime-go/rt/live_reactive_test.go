package rt

// live_reactive_test.go — Phase-4b HEADLESS verification of the two load-bearing properties (no
// browser, no Playwright): (1) write-time tenant tagging derives the tag from the WRITER's verified
// session (§3.4), and (2) the reactive fan-out is fail-closed per tenant — a tenant-A write's Change
// is delivered ONLY to tenant-A subscriptions, never tenant-B (NB-2), while two SAME-tenant
// subscriptions BOTH receive it (query-scoped live update). The write travels through the real
// rt.Embedded_put kernel on a goroutine stamped with runWithLiveSession, so the session→tenant
// derivation is exercised exactly as production does it.

import (
	"testing"
	"time"

	"sky-app/bluedb"
)

const reactiveTestSchema = `{"name":"orders","key":"id","cols":[` +
	`{"name":"id","type":"text","unique":false,"generated":false},` +
	`{"name":"status","type":"text","unique":false,"generated":false}],` +
	`"indexes":[{"name":"status","col":"status","type":"text","unique":false}]}`

// openReactiveBackend opens a fresh embedded backend at a temp dir and registers it in the kernel
// handle registry, returning the backend + its handle id (what the Sky Conn carries).
func openReactiveBackend(t *testing.T) (*bluedb.EmbeddedBackend, int64) {
	t.Helper()
	eng, err := bluedb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("bluedb.Open: %v", err)
	}
	b := bluedb.NewEmbeddedBackend(eng)
	t.Cleanup(func() { _ = b.Close() })
	id := embeddedRegister(b)
	t.Cleanup(func() { embeddedUnregister(id) })
	return b, id
}

// sessionWithTenant builds a minimal liveSession carrying a framework-VERIFIED identity whose
// Claims["tenant"] is `tenant` — the shape currentSessionTenant() resolves.
func sessionWithTenant(tenant string) *liveSession {
	return &liveSession{
		identity:      ConsoleIdentity{Claims: map[string]string{"tenant": tenant}},
		identityValid: true,
	}
}

// putRowAs runs rt.Embedded_put through the real kernel on a goroutine stamped with `sess` (or, if
// sess is nil, UNSTAMPED — an out-of-session writer). Returns the Ok/Err tag (0 == Ok).
func putRowAs(t *testing.T, sess *liveSession, storeID int64, rowJSON string) {
	t.Helper()
	run := func() {
		res := anyTaskInvoke(Embedded_put(int(storeID), reactiveTestSchema, rowJSON))
		if res.Tag != 0 {
			t.Errorf("Embedded_put failed: %+v", res.ErrValue)
		}
	}
	if sess != nil {
		runWithLiveSession(sess, run)
	} else {
		run()
	}
}

// drainChanges collects Changes off a subscription channel until it goes quiet for `d`.
func drainChanges(ch <-chan bluedb.Change, d time.Duration) []bluedb.Change {
	var out []bluedb.Change
	for {
		select {
		case c, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, c)
		case <-time.After(d):
			return out
		}
	}
}

func schemaFor(t *testing.T) bluedb.CollSchema {
	t.Helper()
	cs, err := parseEmbeddedSchema(reactiveTestSchema)
	if err != nil {
		t.Fatalf("parseEmbeddedSchema: %v", err)
	}
	return cs
}

// TestPhase4b_WriteTimeTenantTag — the write-time tag is the WRITER's verified session tenant on a
// stamped goroutine, and "" on an unstamped one (fail-closed). Observed on the raw engine feed, so
// this asserts CommitReq.Tenant itself, not just the downstream fan-out.
func TestPhase4b_WriteTimeTenantTag(t *testing.T) {
	b, id := openReactiveBackend(t)
	cs := schemaFor(t)
	b.Register(cs)

	feed, cancel := b.Subscribe(16)
	defer cancel()

	// Stamped write on tenant "acme".
	putRowAs(t, sessionWithTenant("acme"), id, `{"id":"o1","status":"open"}`)
	// Unstamped write (no live session in scope — background/CLI/raw handler).
	putRowAs(t, nil, id, `{"id":"o2","status":"open"}`)

	tags := map[string]string{}
	deadline := time.After(2 * time.Second)
	for len(tags) < 2 {
		select {
		case batch := <-feed:
			for _, ch := range batch.Changes {
				tags[string(ch.Pk)] = batch.Tenant
			}
		case <-deadline:
			t.Fatalf("timed out; got tags=%v", tags)
		}
	}
	// Keys carry the collection prefix (collName\x1Fpk); assert by suffix match on the pk.
	got := map[string]string{}
	for k, v := range tags {
		got[k[len(k)-2:]] = v // last 2 bytes are "o1"/"o2"
	}
	if got["o1"] != "acme" {
		t.Errorf("stamped write tenant = %q, want %q", got["o1"], "acme")
	}
	if got["o2"] != "" {
		t.Errorf("unstamped write tenant = %q, want \"\" (fail-closed)", got["o2"])
	}
}

// TestPhase4b_TwoTenantIsolation (NB-2) — a tenant-A-stamped write's Change is delivered ONLY to the
// tenant-A subscription, NEVER the tenant-B subscription on the SAME collection. This is the reactive
// analogue of the v0.16.6 SQL-WHERE tenant gate, proven at the rt/session boundary.
func TestPhase4b_TwoTenantIsolation(t *testing.T) {
	b, id := openReactiveBackend(t)
	cs := schemaFor(t)
	b.Register(cs)

	plan, _ := parseEmbeddedPlan("") // match-all footprint

	subA, _, err := b.WatchTenant(cs, plan, "tenantA")
	if err != nil {
		t.Fatalf("WatchTenant A: %v", err)
	}
	defer subA.Close()
	subB, _, err := b.WatchTenant(cs, plan, "tenantB")
	if err != nil {
		t.Fatalf("WatchTenant B: %v", err)
	}
	defer subB.Close()

	// Write on tenant-A's identity-stamped goroutine → tagged "tenantA".
	putRowAs(t, sessionWithTenant("tenantA"), id, `{"id":"o1","status":"open"}`)

	got := drainChanges(subA.Changes(), 500*time.Millisecond)
	if len(got) != 1 || got[0].Transition != bluedb.ChangeEnter {
		t.Fatalf("tenant-A sub: want 1 Enter, got %d changes: %+v", len(got), got)
	}
	leaked := drainChanges(subB.Changes(), 500*time.Millisecond)
	if len(leaked) != 0 {
		t.Fatalf("tenant-B sub LEAKED %d tenant-A change(s): %+v", len(leaked), leaked)
	}
}

// TestPhase4b_SameTenantLiveDelivery — two DIFFERENT sessions on the SAME tenant both watch the same
// query; a change matching the query live-delivers to BOTH subscriptions (query-scoped live update
// across same-tenant sessions).
func TestPhase4b_SameTenantLiveDelivery(t *testing.T) {
	b, id := openReactiveBackend(t)
	cs := schemaFor(t)
	b.Register(cs)

	plan, _ := parseEmbeddedPlan("")

	sub1, _, err := b.WatchTenant(cs, plan, "acme")
	if err != nil {
		t.Fatalf("WatchTenant sub1: %v", err)
	}
	defer sub1.Close()
	sub2, _, err := b.WatchTenant(cs, plan, "acme")
	if err != nil {
		t.Fatalf("WatchTenant sub2: %v", err)
	}
	defer sub2.Close()

	// A different acme session issues the write (its own identity-stamped goroutine).
	putRowAs(t, sessionWithTenant("acme"), id, `{"id":"o9","status":"open"}`)

	g1 := drainChanges(sub1.Changes(), 500*time.Millisecond)
	g2 := drainChanges(sub2.Changes(), 500*time.Millisecond)
	if len(g1) != 1 || g1[0].Transition != bluedb.ChangeEnter {
		t.Fatalf("sub1: want 1 Enter, got %+v", g1)
	}
	if len(g2) != 1 || g2[0].Transition != bluedb.ChangeEnter {
		t.Fatalf("sub2: want 1 Enter, got %+v", g2)
	}
}
