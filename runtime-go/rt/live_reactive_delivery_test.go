package rt

// live_reactive_delivery_test.go — Phase-4c HEADLESS end-to-end live-delivery test (deliverable 3;
// no browser, no Playwright per the repo's flaky-browser guidance). It exercises the FULL reactive
// path a real Sky.Live session runs:
//
//   write (identity-stamped Embedded_put kernel) → engine change-feed → bluedb precise fan-out →
//   the session's reactiveLoop → reactiveRefreshOnce (re-run the Sky query, fold into Model, render,
//   diff, push ONE SSE frame) — and asserts the session's rendered body + the emitted SSE frame
//   reflect the write, query-scoped.
//
// This is the rt-level analogue of "two browsers see the update": a tenant-scoped write triggers the
// watching session's view to repaint with the new row, through the same locked-dispatch + SSE tail a
// Cmd.perform completion uses (no new fan-out path).

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"sky-app/bluedb"
)

// queryOpenOrderIDs reads the current order ids from the backend (the reactive query the binding
// re-runs on each change). Sorted for a deterministic render.
func queryOpenOrderIDs(t *testing.T, b *bluedb.EmbeddedBackend) []string {
	t.Helper()
	cs := schemaFor(t)
	plan, _ := parseEmbeddedPlan("")
	rows, err := b.Query(cs, plan)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		var m map[string]any
		if json.Unmarshal(row, &m) == nil {
			if id, ok := m["id"].(string); ok {
				ids = append(ids, id)
			}
		}
	}
	sort.Strings(ids)
	return ids
}

// TestPhase4c_HeadlessLiveDelivery — a write on a tenant-A-stamped session live-updates that
// session's rendered view + emits an SSE frame carrying the new row, all through the real reactive
// loop.
func TestPhase4c_HeadlessLiveDelivery(t *testing.T) {
	b, storeID := openReactiveBackend(t)
	cs := schemaFor(t)
	b.Register(cs)

	// The view renders the current order-id list as <li> rows — so the rendered body literally
	// contains each order id the query returns.
	view := func(model any) any {
		ids, _ := model.([]string)
		kids := make([]any, 0, len(ids))
		for _, id := range ids {
			kids = append(kids, velement("li", nil, []any{vtext(id)}))
		}
		return velement("ul", nil, kids)
	}
	app := &liveApp{
		update:      func(msg, model any) any { return SkyTuple2{V0: model, V1: cmdT{kind: "none"}} },
		view:        view,
		store:       newMemoryStore(30 * time.Minute),
		locker:      newSessionLocker(),
		msgTags:     map[string]int{},
		skyIDPrefix: "r",
	}

	// The reactive binding's re-query closure: () -> Task Error (model -> model). It re-reads the
	// current order ids and folds them into the Model (typed decode happens IN Sky in production; a
	// plain []string here). Captures the backend, exactly as a real liveInto binding closes over
	// its Conn.
	run := func(_ any) any {
		return SkyTask[any, any](func() SkyResult[any, any] {
			ids := queryOpenOrderIDs(t, b)
			fold := func(_ any) any { return any(ids) }
			return Ok[any, any](any(fold))
		})
	}
	binding := reactiveBindingRT{
		store:  storeID,
		coll:   "orders",
		schema: reactiveTestSchema,
		plan:   "",
		run:    run,
	}

	sess := sessionWithTenant("acme")
	sess.sid = "sess-live"
	sess.model = []string{}
	sess.handlers = map[string]any{}
	sess.sseCh = make(chan sseFrame, 8)

	done := make(chan struct{})
	defer close(done)
	go app.reactiveLoop(sess, binding, done)

	// Wait for the initial fill to settle (empty list rendered), then drain any startup frame(s).
	waitFor(t, 2*time.Second, func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.lastShippedBody != "" // initial render happened
	})
	drainFrames(sess.sseCh, 100*time.Millisecond)

	// A tenant-A-stamped write of a new order — the exact production write path.
	putRowAs(t, sess, storeID, `{"id":"live-1","status":"open"}`)

	// Assert an SSE frame carrying the new row is emitted (live delivery), timeout-bounded.
	frame, ok := awaitFrameContaining(sess.sseCh, "live-1", 3*time.Second)
	if !ok {
		t.Fatalf("no SSE frame carrying the new row within timeout; last frame: %q", frame.data)
	}

	// And the session's rendered body reflects the write (query-scoped repaint).
	sess.mu.Lock()
	body := sess.lastShippedBody
	sess.mu.Unlock()
	if !strings.Contains(body, "live-1") {
		t.Fatalf("session body did not repaint with the new row: %q", body)
	}
}

// TestPhase4c_CrossTenantNoLiveDelivery — a tenant-B write does NOT live-update a tenant-A session
// (the fail-closed tenant gate holds end-to-end through the reactive loop): the tenant-A session's
// view never repaints with tenant-B's row.
func TestPhase4c_CrossTenantNoLiveDelivery(t *testing.T) {
	b, storeID := openReactiveBackend(t)
	cs := schemaFor(t)
	b.Register(cs)

	view := func(model any) any {
		ids, _ := model.([]string)
		kids := make([]any, 0, len(ids))
		for _, id := range ids {
			kids = append(kids, velement("li", nil, []any{vtext(id)}))
		}
		return velement("ul", nil, kids)
	}
	app := &liveApp{
		update:      func(msg, model any) any { return SkyTuple2{V0: model, V1: cmdT{kind: "none"}} },
		view:        view,
		store:       newMemoryStore(30 * time.Minute),
		locker:      newSessionLocker(),
		msgTags:     map[string]int{},
		skyIDPrefix: "r",
	}
	run := func(_ any) any {
		return SkyTask[any, any](func() SkyResult[any, any] {
			ids := queryOpenOrderIDs(t, b)
			return Ok[any, any](any(func(_ any) any { return any(ids) }))
		})
	}
	binding := reactiveBindingRT{store: storeID, coll: "orders", schema: reactiveTestSchema, plan: "", run: run}

	// Session watches as tenant "acme".
	sess := sessionWithTenant("acme")
	sess.sid = "sess-acme"
	sess.model = []string{}
	sess.handlers = map[string]any{}
	sess.sseCh = make(chan sseFrame, 8)

	done := make(chan struct{})
	defer close(done)
	go app.reactiveLoop(sess, binding, done)
	waitFor(t, 2*time.Second, func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return sess.lastShippedBody != ""
	})
	drainFrames(sess.sseCh, 100*time.Millisecond)

	// A DIFFERENT tenant ("beta") writes a row on ITS own stamped session.
	putRowAs(t, sessionWithTenant("beta"), storeID, `{"id":"beta-row","status":"open"}`)

	// The tenant-A session must NOT receive a frame carrying tenant-B's row.
	if frame, ok := awaitFrameContaining(sess.sseCh, "beta-row", 800*time.Millisecond); ok {
		t.Fatalf("cross-tenant LEAK: tenant-A session got a frame with tenant-B's row: %q", frame.data)
	}
	sess.mu.Lock()
	body := sess.lastShippedBody
	sess.mu.Unlock()
	if strings.Contains(body, "beta-row") {
		t.Fatalf("cross-tenant LEAK: tenant-A body repainted with tenant-B's row: %q", body)
	}
}

// ── test helpers ─────────────────────────────────────────────────────────────────────────────

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition not met within %s", d)
	}
}

func drainFrames(ch <-chan sseFrame, quiet time.Duration) {
	for {
		select {
		case <-ch:
		case <-time.After(quiet):
			return
		}
	}
}

func awaitFrameContaining(ch <-chan sseFrame, needle string, d time.Duration) (sseFrame, bool) {
	deadline := time.After(d)
	var last sseFrame
	for {
		select {
		case f := <-ch:
			last = f
			if strings.Contains(f.data, needle) {
				return f, true
			}
		case <-deadline:
			return last, false
		}
	}
}
