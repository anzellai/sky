package bluedb

// reactive_test.go — the Phase-4a `-race` gate (docs/bluedb/phase4-grill-findings.md §"Phase-4a
// gate"). Every test drives the commit-path reactive engine end-to-end through the EmbeddedBackend:
//   (a) two-tenant LOCAL isolation — a tagged write reaches ONLY its tenant's subs (the leak gate);
//   (b) tenant-not-durable — the tenant tag never enters the durable changelog payload;
//   (c) Enter/Leave/Stay incl. autocommit-blind update-out + in-range order-col re-sort;
//   (d) register-live-first — a commit in the Watch setup window is delivered at-least-once;
//   (e) delete → Leave; and an overflow/drop → resync latch (no permanent silent loss).
// Run under `go test ./bluedb/ -race`.

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// openPlan is the shared filter: status == 'open' (a declared range-optimized text index on the
// orders fixture → a PRECISE range footprint).
func openPlan() QueryPlan {
	return QueryPlan{Where: CondNode{Op: CondEq, Col: "status", Type: ColText, Val: TextVal("open")}, Limit: -1}
}

// newMemBackend is a fast in-memory engine + backend (no fsync) for the high-write overflow gate.
func newMemBackend(t *testing.T) *EmbeddedBackend {
	t.Helper()
	e, err := openWith(config{dir: "mem", fs: vfs.NewMem()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	b := NewEmbeddedBackend(e)
	b.Register(ordersSchema())
	return b
}

// drainChanges collects Changes until the channel is quiet for `quiet` (the pump is async).
func drainChanges(ch <-chan Change, quiet time.Duration) []Change {
	var out []Change
	timer := time.NewTimer(quiet)
	defer timer.Stop()
	for {
		select {
		case c, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, c)
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(quiet)
		case <-timer.C:
			return out
		}
	}
}

func openRow(id string, age int) []byte {
	return jsonRow(fmt.Sprintf(`{"id":%q,"status":"open","age":%d}`, id, age))
}
func closedRow(id string, age int) []byte {
	return jsonRow(fmt.Sprintf(`{"id":%q,"status":"closed","age":%d}`, id, age))
}

// pkContains reports whether a delivered Change's userKey pk carries the given bare-id marker,
// i.e. it is "orders" ‖ 0x1F ‖ <id…>. Used to tell tenant-X rows (x*) from tenant-Y rows (y*).
func pkHasSuffixMarker(pk []byte, marker byte) bool {
	i := bytes.IndexByte(pk, collSep)
	return i >= 0 && i+1 < len(pk) && pk[i+1] == marker
}

// ── (a) two-tenant LOCAL commit-path isolation (the cross-tenant leak gate, NB-2) ──────────────

func TestReactive_TwoTenantLocalIsolation(t *testing.T) {
	b := NewEmbeddedBackend(newSSIEngine(t))
	orders := ordersSchema()
	b.Register(orders)

	subX, _, err := b.WatchTenant(orders, openPlan(), "X")
	if err != nil {
		t.Fatalf("watch X: %v", err)
	}
	defer subX.Close()
	subY, _, err := b.WatchTenant(orders, openPlan(), "Y")
	if err != nil {
		t.Fatalf("watch Y: %v", err)
	}
	defer subY.Close()

	const N = 25
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			_ = b.PutTenant(orders, fmt.Sprintf("x%d", i), openRow(fmt.Sprintf("x%d", i), 10), nil, "X")
		}(i)
		go func(i int) {
			defer wg.Done()
			_ = b.PutTenant(orders, fmt.Sprintf("y%d", i), openRow(fmt.Sprintf("y%d", i), 10), nil, "Y")
		}(i)
	}
	wg.Wait()

	xs := drainChanges(subX.Changes(), 500*time.Millisecond)
	ys := drainChanges(subY.Changes(), 500*time.Millisecond)

	// The leak gate: tenant X's sub must NEVER see a tenant-Y (y*) delta, and vice-versa.
	for _, c := range xs {
		if !pkHasSuffixMarker(c.Pk, 'x') {
			t.Fatalf("CROSS-TENANT LEAK: tenant-X sub received a non-X delta pk=%q", c.Pk)
		}
	}
	for _, c := range ys {
		if !pkHasSuffixMarker(c.Pk, 'y') {
			t.Fatalf("CROSS-TENANT LEAK: tenant-Y sub received a non-Y delta pk=%q", c.Pk)
		}
	}
	if len(xs) != N {
		t.Fatalf("tenant-X expected %d Enters, got %d", N, len(xs))
	}
	if len(ys) != N {
		t.Fatalf("tenant-Y expected %d Enters, got %d", N, len(ys))
	}
}

// An EMPTY-tenant delta visits ONLY the "" bucket — never the union of tenants (the fail-closed
// gate, B#2). A ""-tagged write must not reach a tenant-X sub.
func TestReactive_EmptyTenantScopedToEmptyBucket(t *testing.T) {
	b := NewEmbeddedBackend(newSSIEngine(t))
	orders := ordersSchema()
	b.Register(orders)

	subX, _, _ := b.WatchTenant(orders, openPlan(), "X")
	defer subX.Close()
	subEmpty, _, _ := b.WatchTenant(orders, openPlan(), "")
	defer subEmpty.Close()

	if err := b.PutTenant(orders, "o1", openRow("o1", 10), nil, ""); err != nil {
		t.Fatalf("put: %v", err)
	}

	empties := drainChanges(subEmpty.Changes(), 400*time.Millisecond)
	xs := drainChanges(subX.Changes(), 400*time.Millisecond)
	if len(empties) != 1 || empties[0].Transition != ChangeEnter {
		t.Fatalf("empty-tenant sub expected 1 Enter, got %d", len(empties))
	}
	if len(xs) != 0 {
		t.Fatalf("fail-closed VIOLATION: tenant-X sub received %d deltas from a \"\"-tagged write", len(xs))
	}
}

// ── (b) tenant-not-durable — the tag is transient routing only, never in the changelog ─────────

func TestReactive_TenantNeverDurable(t *testing.T) {
	e := newSSIEngine(t)
	b := NewEmbeddedBackend(e)
	orders := ordersSchema()
	b.Register(orders)

	const secret = "tenant-SECRET-9f3a2b"

	// Observe the transient tag on the live change-feed (proves it travels), then assert it is
	// absent from the durable changelog (proves it is never written).
	feed, cancel := b.Subscribe(4)
	defer cancel()

	if err := b.PutTenant(orders, "o1", openRow("o1", 10), nil, secret); err != nil {
		t.Fatalf("put: %v", err)
	}

	select {
	case batch := <-feed:
		if batch.Tenant != secret {
			t.Fatalf("transient tenant tag not carried on the change-feed: got %q", batch.Tenant)
		}
	case <-time.After(time.Second):
		t.Fatal("no change-feed batch delivered")
	}

	entries, err := e.Changelog().Tail(HLC{})
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no durable changelog entries")
	}
	for _, en := range entries {
		if bytes.Contains(en.Payload, []byte(secret)) {
			t.Fatal("DURABILITY LEAK: the tenant tag appears in the durable changelog payload bytes")
		}
		if _, derr := DecodeChangelogPayload(en.Payload); derr != nil {
			t.Fatalf("decode durable payload: %v", derr)
		}
		// KeyChange carries no Tenant field by construction — nothing to inspect further.
	}
}

// ── (c) Enter / Leave / Stay incl. the autocommit-blind update-out + in-range order-col churn ──

func TestReactive_EnterLeaveStayAndOrderChurn(t *testing.T) {
	b := NewEmbeddedBackend(newSSIEngine(t))
	orders := ordersSchema()
	b.Register(orders)

	// Filter status='open' (precise range), ORDER BY age (a declared int index → order-witness).
	plan := openPlan()
	plan.Orders = []OrderSpec{{Col: "age"}}
	sub, _, err := b.WatchTenant(orders, plan, "")
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer sub.Close()

	// Enter (insert into range).
	must(t, b.Put(orders, "r1", openRow("r1", 30), nil))
	// Stay + order churn (in-range update; age 30→99 moves the ORDER-column coord → re-sort).
	must(t, b.Put(orders, "r1", openRow("r1", 99), nil))
	// Leave via the AUTOCOMMIT-BLIND update-out (status open→closed leaves the watched range).
	must(t, b.Put(orders, "r1", closedRow("r1", 99), nil))
	// A closed insert is NOT in range → no delivery; then update-into-range → Enter.
	must(t, b.Put(orders, "r2", closedRow("r2", 5), nil))
	must(t, b.Put(orders, "r2", openRow("r2", 5), nil))

	got := drainChanges(sub.Changes(), 600*time.Millisecond)
	assertTransitions(t, got, []expected{
		{pk: "r1", tr: ChangeEnter},
		{pk: "r1", tr: ChangeStay, order: true},
		{pk: "r1", tr: ChangeLeave},
		{pk: "r2", tr: ChangeEnter},
	})
}

// ── (d) register-live-first — a commit in the setup window is delivered at-least-once (A#2) ────

func TestReactive_RegisterLiveFirstNoMiss(t *testing.T) {
	b := NewEmbeddedBackend(newSSIEngine(t))
	orders := ordersSchema()
	b.Register(orders)

	// A baseline row so the baseline query is non-empty.
	must(t, b.Put(orders, "seed", openRow("seed", 1), nil))

	// The gate: deterministically commit a NEW matching row AFTER readTs is pinned and BEFORE the
	// baseline runs. Its commitTs > readTs ⇒ it is NOT in the baseline ⇒ it MUST be delivered from
	// the setup buffer (register-live-first: no miss window).
	b.afterPinHook = func() {
		must(t, b.Put(orders, "gap", openRow("gap", 2), nil))
	}

	sub, baseline, err := b.WatchTenant(orders, openPlan(), "")
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer sub.Close()

	// "gap" is NOT in the baseline (it committed after the pinned readTs).
	gapUK := string(dataUserKey("orders", "gap"))
	for _, row := range baseline {
		if pk, ok := pkUserKeyOfRow(b.schemaByName("orders"), row); ok && pk == gapUK {
			t.Fatal("baseline unexpectedly contained the post-readTs 'gap' row")
		}
	}

	got := drainChanges(sub.Changes(), 600*time.Millisecond)
	found := 0
	for _, c := range got {
		if string(c.Pk) == gapUK && c.Transition == ChangeEnter {
			found++
		}
	}
	if found < 1 {
		t.Fatalf("register-live-first MISSED the setup-window commit (delivered %d times, want >=1)", found)
	}
}

// ── (e) delete → Leave; and an overflow/drop → resync latch (no permanent silent loss) ─────────

func TestReactive_DeleteFiresLeave(t *testing.T) {
	b := NewEmbeddedBackend(newSSIEngine(t))
	orders := ordersSchema()
	b.Register(orders)

	sub, _, err := b.WatchTenant(orders, openPlan(), "")
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer sub.Close()

	must(t, b.Put(orders, "r1", openRow("r1", 10), nil))
	must(t, b.Delete(orders, "r1"))

	got := drainChanges(sub.Changes(), 500*time.Millisecond)
	assertTransitions(t, got, []expected{
		{pk: "r1", tr: ChangeEnter},
		{pk: "r1", tr: ChangeLeave},
	})
}

func TestReactive_OverflowSetsResyncNoPermanentLoss(t *testing.T) {
	b := newMemBackend(t) // in-memory: fast enough to overflow the 256-deep deliver buffer
	orders := ordersSchema()

	sub, _, err := b.WatchTenant(orders, openPlan(), "")
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer sub.Close()

	// Do NOT drain sub.Changes(): write enough matching rows to overflow its deliver buffer so at
	// least one delta is DROPPED. A drop must latch the resync flag (never a silent permanent loss).
	const writes = reactiveDeliverBuf + 200
	for i := 0; i < writes; i++ {
		must(t, b.Put(orders, fmt.Sprintf("r%d", i), openRow(fmt.Sprintf("r%d", i), 10), nil))
	}

	// Poll for the resync latch (delivery is async).
	deadline := time.Now().Add(3 * time.Second)
	resync := false
	for time.Now().Before(deadline) {
		if sub.NeedsResync() {
			resync = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !resync {
		t.Fatal("overflow did NOT latch the resync flag — a dropped delta could be silently lost")
	}

	// No permanent loss: a resync re-query sees the true state (all `writes` rows are 'open').
	rows, err := b.Query(orders, openPlan())
	if err != nil {
		t.Fatalf("resync query: %v", err)
	}
	if len(rows) != writes {
		t.Fatalf("resync re-query lost data: expected %d open rows, got %d", writes, len(rows))
	}
}

// ── shared assert helpers ──────────────────────────────────────────────────────────────────────

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("write: %v", err)
	}
}

type expected struct {
	pk    string
	tr    Transition
	order bool
}

func assertTransitions(t *testing.T, got []Change, want []expected) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("transition count: got %d %s, want %d", len(got), summarize(got), len(want))
	}
	for i, w := range want {
		wantUK := string(dataUserKey("orders", w.pk))
		if string(got[i].Pk) != wantUK {
			t.Fatalf("delta %d: pk got %q want %q", i, got[i].Pk, wantUK)
		}
		if got[i].Transition != w.tr {
			t.Fatalf("delta %d (%s): transition got %s want %s", i, w.pk, trName(got[i].Transition), trName(w.tr))
		}
		if w.order && !got[i].OrderChanged {
			t.Fatalf("delta %d (%s): expected OrderChanged (re-sort) on a Stay", i, w.pk)
		}
	}
}

func summarize(cs []Change) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for i, c := range cs {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "%s:%s", c.Pk, trName(c.Transition))
	}
	sb.WriteByte(']')
	return sb.String()
}

func trName(t Transition) string {
	switch t {
	case ChangeEnter:
		return "Enter"
	case ChangeLeave:
		return "Leave"
	case ChangeStay:
		return "Stay"
	}
	return "?"
}
