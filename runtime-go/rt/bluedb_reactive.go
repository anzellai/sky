package rt

// bluedb_reactive.go — Phase-4b: the rt-layer reactive integration that connects the Phase-4a
// bluedb delta-match engine (runtime-go/bluedb) to Sky.Live sessions. Two jobs live here, both of
// which bluedb legally CANNOT do (bluedb imports no rt — the pump + the identity resolution live
// in rt, which imports bluedb, the legal direction):
//
//   1. WRITE-TIME TENANT RESOLUTION (§3.4). currentSessionTenant() reads the VERIFIED tenant of the
//      goroutine's live session — SessionIdentity(currentLiveSession()).Claims["tenant"]. The
//      Embedded_put/insert/delete/transaction kernels stamp this onto CommitReq.Tenant just before
//      the engine Commit, so the write-time tag travels WITH the committed delta to the engine
//      change-feed. It is NEVER re-derived on the pump goroutine (which has no session → nil), and
//      NEVER read from record data (forgeable). An unstamped writer (raw Http.Server handler,
//      background, CLI) → "" (fail-closed: routes ONLY to the "" bucket, never the tenant union).
//
//   2. THE PER-SESSION REACTIVE LOOP (§3.2/§4.2). One identity-stamped goroutine per registered
//      Live.watch binding drains a bluedb precise, tenant-scoped subscription (Backend.WatchTenant,
//      whose channel is fed by the ONE engine change-feed pump bluedb already runs internally —
//      "one pump per engine"). On each matched Change (coalesced), it RE-RUNS the binding's Sky
//      query task (typed decode happens IN Sky via the codec) and folds the fresh List into the
//      Model — reusing runPerformBody's exact locked-dispatch tail (sess.mu → render → diff →
//      SSE frame). No new fan-out path; the per-session mutex + SSE emission are the same the
//      Cmd.perform completion path uses.
//
// SECURITY CRUX (NB-2, the fail-closed tenant gate): a subscription registered with tenant=A is
// visited ONLY for tenant-A-tagged deltas (bluedb's byCollTenant strict partition, reactive.go).
// A tenant-A write on A's identity-stamped goroutine delivers ONLY to A's subscriptions, never B's.
// Proven headless in live_reactive_test.go.

import (
	"reflect"
	"sync"

	"sky-app/bluedb"
)

// init wires the bluedb-free hooks in live_reactive_hooks.go to the real implementations. This file
// imports sky-app/bluedb and is GATED out of non-Persist projects (build.rs); when it's absent the
// hooks stay no-ops, so live.go never depends on the Pebble engine.
func init() {
	reactiveEnsureStartedHook = func(app *liveApp, sess *liveSession) { app.ensureReactiveStarted(sess) }
	reactiveTeardownHook = func(sess *liveSession) { reactiveTeardown(sess) }
}

// currentSessionTenant resolves the VERIFIED tenant of the calling goroutine's live session, or ""
// when there is no session in scope OR the session carries no framework-verified identity
// (fail-closed, §3.4). The tenant is the standard `Claims["tenant"]` a `Live.withIdentify`/auth gate
// populates. NEVER derive a tenant from record columns — this is the framework-verified claim of
// the goroutine that performed the write, the reactive analogue of the v0.16.6 SQL-WHERE gate.
func currentSessionTenant() string {
	sess := currentLiveSession()
	if sess == nil {
		return ""
	}
	id, ok := SessionIdentity(sess)
	if !ok {
		return ""
	}
	return id.Claims["tenant"]
}

// ── per-session reactive registry ───────────────────────────────────────────────────────────────
//
// Kept in a package-level map keyed by *liveSession (rather than growing the liveSession struct) so
// the integration is additive. Guarded by reactiveStateMu; each session's loops register their
// cancel here and markDone → reactiveTeardown sweeps them (mirrors the activeSubs teardown).

var (
	reactiveStateMu sync.Mutex
	reactiveState   = map[*liveSession]*sessReactive{}
)

type sessReactive struct {
	started bool
	cancels []func()
}

// reactiveTeardown stops every reactive loop bound to a session (called from markDone). Idempotent.
func reactiveTeardown(sess *liveSession) {
	reactiveStateMu.Lock()
	st := reactiveState[sess]
	delete(reactiveState, sess)
	reactiveStateMu.Unlock()
	if st == nil {
		return
	}
	for _, c := range st.cancels {
		if c != nil {
			c()
		}
	}
}

// reactiveBindingRT is one Live.watch binding decoded from a `Std.Persist.LiveBinding` record: the
// embedded store handle id, the collection name, the schema + plan JSON (to build the precise
// WatchTenant footprint), and the Sky re-query closure `() -> Task Error (model -> model)`.
type reactiveBindingRT struct {
	store  int64
	coll   string
	schema string
	plan   string
	run    any
}

// reactiveBindingsFor evaluates the app's `reactiveBindings model` accessor (a Sky
// `model -> List (LiveBinding model)`) and decodes the result into typed binding descriptors.
func (app *liveApp) reactiveBindingsFor(model any) []reactiveBindingRT {
	if app.reactiveBindings == nil {
		return nil
	}
	lst := AsList(sky_call(app.reactiveBindings, model))
	out := make([]reactiveBindingRT, 0, len(lst))
	for _, it := range lst {
		run := recordField(it, "Run", "run")
		if run == nil {
			continue
		}
		out = append(out, reactiveBindingRT{
			store:  int64(AsInt(recordField(it, "Store", "store"))),
			coll:   AsString(recordField(it, "Coll", "coll")),
			schema: AsString(recordField(it, "Schema", "schema")),
			plan:   AsString(recordField(it, "Plan", "plan")),
			run:    run,
		})
	}
	return out
}

// ensureReactiveStarted spins up the session's reactive loops exactly once, after the session's
// initial Model exists (called from setupSubscriptions, which runs post-init on every dispatch).
// No-op when the app declares no reactive bindings.
func (app *liveApp) ensureReactiveStarted(sess *liveSession) {
	if app.reactiveBindings == nil {
		return
	}
	reactiveStateMu.Lock()
	st := reactiveState[sess]
	if st != nil && st.started {
		reactiveStateMu.Unlock()
		return
	}
	if st == nil {
		st = &sessReactive{}
		reactiveState[sess] = st
	}
	st.started = true
	reactiveStateMu.Unlock()

	sess.mu.Lock()
	model := sess.model
	sess.mu.Unlock()

	for _, b := range app.reactiveBindingsFor(model) {
		done := make(chan struct{})
		var once sync.Once
		cancel := func() { once.Do(func() { close(done) }) }
		reactiveStateMu.Lock()
		if s2 := reactiveState[sess]; s2 != nil {
			s2.cancels = append(s2.cancels, cancel)
		}
		reactiveStateMu.Unlock()
		go app.reactiveLoop(sess, b, done)
	}
}

// reactiveLoop owns ONE binding's live refresh on one identity-stamped goroutine (so the re-query's
// tenant resolves from this session, and WatchTenant is scoped to it). It fills the Model once at
// start (paint-then-fill), opens a precise tenant-scoped subscription on the embedded backend, then
// re-queries on each matched Change (coalescing bursts into one refresh). Exits on the binding
// cancel or session teardown, releasing the subscription (its watermark token).
func (app *liveApp) reactiveLoop(sess *liveSession, b reactiveBindingRT, done chan struct{}) {
	setGoroutineLiveSession(sess)
	defer clearGoroutineLiveSession()
	defer func() { _ = recover() }() // a wedged binding must never crash the process

	var changes <-chan bluedb.Change
	var closeSub func()
	// Reactive watch is EMBEDDED-only in Phase 4b (b.store is a KvConn handle; a SqlConn passes -1).
	// The relational LISTEN/NOTIFY trigger lands in Phase 4c; a SqlConn binding still does the
	// initial fill below, just no live updates.
	if backend, ok := embeddedBackend(b.store); ok {
		cs, err1 := parseEmbeddedSchema(b.schema)
		plan, err2 := parseEmbeddedPlan(b.plan)
		if err1 == nil && err2 == nil {
			// The subscription tenant IS this session's verified tenant — so only same-tenant
			// writes (tagged by their writer, §3.4) trigger this session's refresh. Cross-tenant
			// writes are never delivered here (bluedb byCollTenant strict partition, NB-2).
			sub, _, err := backend.WatchTenant(cs, plan, currentSessionTenant())
			if err == nil {
				changes = sub.Changes()
				closeSub = sub.Close
			}
		}
	}
	if closeSub != nil {
		defer closeSub()
	}

	app.reactiveRefreshOnce(sess, b) // initial fill

	if changes == nil {
		<-done // no live trigger (SQL arm / setup failure) — hold until teardown
		return
	}
	for {
		select {
		case <-done:
			return
		case <-sess.done:
			return
		case _, ok := <-changes:
			if !ok {
				return
			}
			drainReactiveBurst(changes) // coalesce a bulk write into ONE refresh
			app.reactiveRefreshOnce(sess, b)
		}
	}
}

// drainReactiveBurst non-blockingly drains any queued Changes so a bulk write triggers one re-query.
func drainReactiveBurst(ch <-chan bluedb.Change) {
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		default:
			return
		}
	}
}

// reactiveRefreshOnce runs the binding's Sky query task OUTSIDE sess.mu (it decodes rows via the
// codec IN Sky), then applies the resulting `model -> model` fold + re-renders + pushes one SSE
// frame INSIDE sess.mu — mirroring runPerformBody's lock discipline + frame tail exactly, so the
// per-session mutex + SSE emission are the SAME machinery a Cmd.perform completion uses (no new
// fan-out path). A fold that panics rolls the session back (Model + handlers) and ships nothing.
func (app *liveApp) reactiveRefreshOnce(sess *liveSession, b reactiveBindingRT) {
	task := sky_call(b.run, nil) // () -> Task Error (model -> model)
	res := anyTaskInvoke(task)
	fold, ok := reactiveFoldFromResult(res)
	if !ok {
		return // Err (transient query failure) or a shape mismatch — next change self-heals
	}

	sess.mu.Lock()
	prevShipped := sess.lastShippedBody
	prevTree := sess.prevTree
	prevModel := sess.model
	prevComputed := sess.lastComputedBody
	prevHandlers := sess.handlers

	var vn VNode
	body, okRender := func() (bod string, okR bool) {
		defer func() {
			if r := recover(); r != nil {
				sess.model = prevModel
				sess.lastComputedBody = prevComputed
				sess.handlers = prevHandlers
				okR = false
			}
		}()
		sess.model = sky_call(fold, sess.model)
		sess.handlers = map[string]any{}
		v, _ := app.safeViewCall(sess.model)
		assignSkyIDs(&v, app.skyIDPrefixOrDefault())
		applyStyleInjections(&v)
		vn = v
		return renderVNode(v, sess.handlers), true
	}()
	if !okRender {
		sess.mu.Unlock()
		return
	}
	sess.commitRender(&vn, body)

	var snap frameSnapshot
	var patches []Patch
	haveFrame := false
	if body != "" && body != prevShipped {
		snap = sess.prepareFrameSnapshot(body)
		sess.lastShippedBody = body
		if prevTree != nil {
			patches = diffTrees(prevTree, &vn, nil)
		}
		haveFrame = true
	}
	sess.mu.Unlock()
	if !haveFrame {
		return
	}
	frame := chooseSSEFrame(snap, prevTree, patches)
	select {
	case sess.sseCh <- frame:
	default:
		recordSseDrop(sess.sid)
		sess.markAllConnsOutOfSync()
	}
}

// reactiveFoldFromResult extracts the `model -> model` fold from a run task's SkyResult. Returns
// (nil,false) on an Err or a non-result shape. The concrete SkyResult[Error, fn] can't be
// type-asserted to SkyResult[any,any] (generic identity), so read Tag + OkValue reflectively.
func reactiveFoldFromResult(r any) (any, bool) {
	rv := reflect.ValueOf(r)
	if rv.Kind() != reflect.Struct {
		return nil, false
	}
	tag := rv.FieldByName("Tag")
	okv := rv.FieldByName("OkValue")
	if !tag.IsValid() || !okv.IsValid() {
		return nil, false
	}
	if tag.Int() != 0 {
		return nil, false // Err
	}
	return okv.Interface(), true
}
