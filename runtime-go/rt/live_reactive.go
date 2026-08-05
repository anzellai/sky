package rt

// Reactive query bindings — the runtime behind `Persist.live` + `Live.withReactive`.
//
// A per-session registry: one broker subscription per watched collection that, on
// a relevant write, re-runs the collection's reactive queries and folds each
// result into the session Model — then the normal diff repaints the tabs. No app
// subscription code, no Msg. See docs/bluedb/reactive-sync-design.md.
//
// v1 refreshes at COLLECTION scope (any change to a collection re-runs its
// bindings) — always correct; the P3 overlap engine (result-pk narrowing) is a
// later optimization the `run` payload already carries the pks for.

import (
	"log"
	"reflect"
	"time"
)

// reactiveRetryBase/Max bound the self-healing retry when a reactive query errors
// (a transient DB blip): the query-execution layer has no broker/feed resync, so
// the loop re-arms itself with capped exponential backoff instead of leaving the
// view silently stale until the next unrelated write.
const (
	reactiveRetryBase = 250 * time.Millisecond
	reactiveRetryMax  = 5 * time.Second
)

// reactiveBindingRT is one binding decoded from a Persist.Live record.
type reactiveBindingRT struct {
	coll string
	run  any // Sky Task: () -> Result Error (model->model, []pk)
}

// reactiveState is a session's reactive registry: the broker-subscription cancels
// + a done signal. All of a collection's refreshes run on its ONE loop goroutine
// (initial fill, then drain-coalesced change events), so there is no concurrent
// refresh to synchronize.
type reactiveState struct {
	cancels []func()
	done    chan struct{}
}

func (app *liveApp) hasReactive() bool {
	return app.reactive != nil && isFunc(app.reactive)
}

// reactiveBindingsFor evaluates the app's reactiveQueries(model) and decodes the
// resulting list of Live records into (coll, run) tuples.
func (app *liveApp) reactiveBindingsFor(model any) []reactiveBindingRT {
	if !app.hasReactive() {
		return nil
	}
	var out []reactiveBindingRT
	for _, it := range AsList(SkyCall(app.reactive, model)) {
		coll := AsString(recordField(it, "Coll", "coll"))
		run := recordField(it, "Run", "run")
		if coll != "" && run != nil {
			out = append(out, reactiveBindingRT{coll: coll, run: run})
		}
	}
	return out
}

// startReactive wires a session's reactive subscriptions once (after mount) and
// runs each binding to fill the Model (paint-then-fill). No-op without bindings.
func (app *liveApp) startReactive(sess *liveSession) {
	if !app.hasReactive() || app.topics == nil {
		return
	}
	sess.mu.Lock()
	model := sess.model
	already := sess.reactive != nil
	sess.mu.Unlock()
	if already {
		return
	}

	bindings := app.reactiveBindingsFor(model)
	if len(bindings) == 0 {
		return
	}
	colls := map[string]bool{}
	for _, b := range bindings {
		colls[b.coll] = true
	}

	// Subscribe on the session's PER-TENANT reactive topic (the security boundary):
	// a verified tenant only receives its own tenant's write nudges. No verified
	// tenant → the collection topic (unauth/dev/single-tenant), unchanged.
	id, ok := SessionIdentity(sess)

	rs := &reactiveState{done: make(chan struct{})}
	for coll := range colls {
		topic := reactiveTenantTopic(id, ok, coll)
		ch, cancel := app.topics.Subscribe(topic)
		rs.cancels = append(rs.cancels, cancel)
		go app.reactiveLoop(sess, rs, coll, ch)
	}

	// Claim, or discard if we lost a concurrent start. startReactive is now
	// reachable from handleSSE (a re-establish on reconnect), so two tabs
	// reconnecting at once could both pass the `already` check above and both
	// build a registry. The FIRST to set sess.reactive wins; the loser tears its
	// own registry down — close(done) exits its loops (they select on it), and the
	// cancels release its topic subscriptions — so exactly one live registry
	// survives and nothing leaks. (A loser loop may do one idempotent refresh
	// before seeing done; harmless replace-fold under sess.mu.)
	sess.mu.Lock()
	if sess.reactive != nil {
		sess.mu.Unlock()
		close(rs.done)
		for _, cancel := range rs.cancels {
			cancel()
		}
		return
	}
	sess.reactive = rs
	sess.mu.Unlock()
}

// teardownReactive tears a session's reactive registry down (called on session
// end from markDone). Safe to call more than once.
func (sess *liveSession) teardownReactive() {
	sess.mu.Lock()
	rs := sess.reactive
	sess.reactive = nil
	sess.mu.Unlock()
	if rs == nil {
		return
	}
	close(rs.done)
	for _, c := range rs.cancels {
		c()
	}
}

// reactiveLoop owns ALL refreshes for one collection on one goroutine: it fills
// the Model once at start (paint-then-fill), then refreshes on each change,
// draining any burst of buffered change events into a single refresh (coalescing)
// so a bulk write doesn't re-query per row. Sequential by construction — no
// concurrent refresh, nothing to lock.
func (app *liveApp) reactiveLoop(sess *liveSession, rs *reactiveState, coll string, ch <-chan SessionEvent) {
	// Stamp the session onto THIS goroutine for its whole life (runtime-grill 3a).
	// Every reactiveRefreshOnce runs the binding's query via sky_call(b.run, nil);
	// an identity-scoped re-derive resolves the tenant from SessionIdentity(
	// currentLiveSession()). Without the stamp currentLiveSession() is nil →
	// fail-closed to empty rows (or unscoped). dispatch/handleInitial stamp their
	// own goroutines; the reactive loop is a distinct long-lived goroutine and must
	// too. One loop == one session, so a lifetime stamp is correct.
	setGoroutineLiveSession(sess)
	defer clearGoroutineLiveSession()

	app.reactiveRefreshWithRetry(sess, rs, coll, ch) // initial fill (self-healing)
	for {
		select {
		case <-rs.done:
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
			drainChangeBurst(ch)
			app.reactiveRefreshWithRetry(sess, rs, coll, ch)
		}
	}
}

// drainChangeBurst collapses a burst of buffered change events (a bulk write) so
// we re-query once, not per row.
func drainChangeBurst(ch <-chan SessionEvent) {
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

// reactiveRefreshWithRetry runs one refresh and, if the query errored (a transient
// failure), re-arms with capped exponential backoff until it succeeds, a NEWER
// change supersedes it, or the session ends — so a reactive view can't sit
// silently stale under a healthy connection when the DB briefly hiccups. Stays on
// this collection's single loop goroutine (no concurrent refresh).
func (app *liveApp) reactiveRefreshWithRetry(sess *liveSession, rs *reactiveState, coll string, ch <-chan SessionEvent) {
	if !app.reactiveRefreshOnce(sess, coll) {
		return // succeeded
	}
	backoff := reactiveRetryBase
	for {
		select {
		case <-rs.done:
			return
		case <-time.After(backoff):
			if !app.reactiveRefreshOnce(sess, coll) {
				return
			}
			if backoff *= 2; backoff > reactiveRetryMax {
				backoff = reactiveRetryMax
			}
		case _, ok := <-ch:
			if !ok {
				return
			}
			drainChangeBurst(ch)
			if !app.reactiveRefreshOnce(sess, coll) {
				return // a newer change healed it
			}
			backoff = reactiveRetryBase // reset budget on a fresh change
		}
	}
}

// reactiveFoldFromResult extracts the model->model fold from a run task's
// SkyResult (nil,false on Err or a shape mismatch). The concrete SkyResult[Error,
// Tuple] can't be type-asserted to SkyResult[any,any] (generic identity), so read
// Tag + OkValue reflectively.
// Returns (fold, ok, errored): ok=true → a usable fold; errored=true → the task
// returned an Err (a transient failure worth retrying, as opposed to a shape
// mismatch which is not).
func reactiveFoldFromResult(r any) (any, bool, bool) {
	// Read Tag/OkValue from the RAW SkyResult — do NOT unwrapAny (it unwraps an Ok
	// straight to its OkValue, losing the Tag). Concrete SkyResult[Error, Tuple]
	// can't be type-asserted to SkyResult[any,any] (generic identity), so reflect.
	rv := reflect.ValueOf(r)
	if rv.Kind() != reflect.Struct {
		return nil, false, false
	}
	tag := rv.FieldByName("Tag")
	okv := rv.FieldByName("OkValue")
	if !tag.IsValid() || !okv.IsValid() {
		return nil, false, false // not a result
	}
	if tag.Int() != 0 {
		return nil, false, true // Err — retry-worthy
	}
	return tupleFirst(okv.Interface()), true, false // OkValue is the (fold, pks) tuple
}

// reactiveRefreshOnce re-derives the bindings from the CURRENT model (so a
// model-dependent query filter stays fresh), runs each binding-on-coll's task
// OUTSIDE sess.mu, then applies the folds + re-renders INSIDE sess.mu and pushes
// one SSE frame — mirroring runPerformBody's lock discipline + frame tail.
// reactiveRefreshOnce returns retry=true when a binding's query ERRORED (so the
// loop should re-arm) — otherwise false (succeeded, or nothing to do).
func (app *liveApp) reactiveRefreshOnce(sess *liveSession, coll string) (retry bool) {
	sess.mu.Lock()
	model := sess.model
	sess.mu.Unlock()

	var folds []any
	errored := false
	for _, b := range app.reactiveBindingsFor(model) {
		if b.coll != coll {
			continue
		}
		fold, ok, isErr := reactiveFoldFromResult(sky_call(b.run, nil))
		if isErr {
			errored = true
			continue
		}
		if ok {
			folds = append(folds, fold)
		}
	}
	if len(folds) == 0 {
		return errored // retry only if a query erred (not merely no folds)
	}

	sess.mu.Lock()
	prevShipped := sess.lastShippedBody
	prevTree := sess.prevTree
	prevModel := sess.model
	prevComputed := sess.lastComputedBody
	prevHandlers := sess.handlers

	// Apply folds + render with panic-rollback: the fold is user code + a coerce
	// on decoded rows and can panic; on panic restore the pre-fold state (INCLUDING
	// handlers — cleared below before the panic-prone render; without restoring it,
	// prevTree's handler IDs would dangle and every click would be a silent no-op).
	var vn VNode
	body, ok := func() (b string, ok bool) {
		defer func() {
			if r := recover(); r != nil {
				sess.model = prevModel
				sess.lastComputedBody = prevComputed
				sess.handlers = prevHandlers
				logReactivePanic(r)
				ok = false
			}
		}()
		m := sess.model
		for _, fold := range folds {
			m = sky_call(fold, m)
		}
		sess.model = m
		sess.handlers = map[string]any{}
		v, _ := app.safeViewCall(sess.model)
		assignSkyIDs(&v, app.skyIDPrefixOrDefault())
		applyStyleInjections(&v)
		vn = v
		return renderVNode(v, sess.handlers), true
	}()
	if !ok {
		sess.mu.Unlock()
		return errored
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
	app.persistSession(sess) // R1: persist the reactive refresh's Model mutation
	sess.mu.Unlock()
	if !haveFrame {
		return errored
	}

	frame := chooseSSEFrame(snap, prevTree, patches)
	select {
	case sess.sseCh <- frame:
	default:
		recordSseDrop(sess.sid)
		sess.markAllConnsOutOfSync()
	}
	return errored
}

func logReactivePanic(r any) {
	log.Printf("[sky.reactive] refresh panic recovered (session unchanged): %v", r)
}
