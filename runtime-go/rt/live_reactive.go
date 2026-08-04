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

	rs := &reactiveState{done: make(chan struct{})}
	for coll := range colls {
		ch, cancel := app.topics.Subscribe(bluedbCollTopic(coll))
		rs.cancels = append(rs.cancels, cancel)
		go app.reactiveLoop(sess, rs, coll, ch)
	}

	sess.mu.Lock()
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
	app.reactiveRefreshOnce(sess, coll) // initial fill
	for {
		select {
		case <-rs.done:
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
			// Coalesce a burst: drain what's already buffered, then one refresh.
			for draining := true; draining; {
				select {
				case _, ok := <-ch:
					draining = ok
				default:
					draining = false
				}
			}
			app.reactiveRefreshOnce(sess, coll)
		}
	}
}

// reactiveFoldFromResult extracts the model->model fold from a run task's
// SkyResult (nil,false on Err or a shape mismatch). The concrete SkyResult[Error,
// Tuple] can't be type-asserted to SkyResult[any,any] (generic identity), so read
// Tag + OkValue reflectively.
func reactiveFoldFromResult(r any) (any, bool) {
	// Read Tag/OkValue from the RAW SkyResult — do NOT unwrapAny (it unwraps an Ok
	// straight to its OkValue, losing the Tag). Concrete SkyResult[Error, Tuple]
	// can't be type-asserted to SkyResult[any,any] (generic identity), so reflect.
	rv := reflect.ValueOf(r)
	if rv.Kind() != reflect.Struct {
		return nil, false
	}
	tag := rv.FieldByName("Tag")
	okv := rv.FieldByName("OkValue")
	if !tag.IsValid() || !okv.IsValid() || tag.Int() != 0 {
		return nil, false // not a result, or Err
	}
	return tupleFirst(okv.Interface()), true // OkValue is the (fold, pks) tuple
}

// reactiveRefreshOnce re-derives the bindings from the CURRENT model (so a
// model-dependent query filter stays fresh), runs each binding-on-coll's task
// OUTSIDE sess.mu, then applies the folds + re-renders INSIDE sess.mu and pushes
// one SSE frame — mirroring runPerformBody's lock discipline + frame tail.
func (app *liveApp) reactiveRefreshOnce(sess *liveSession, coll string) {
	sess.mu.Lock()
	model := sess.model
	sess.mu.Unlock()

	var folds []any
	for _, b := range app.reactiveBindingsFor(model) {
		if b.coll != coll {
			continue
		}
		if fold, ok := reactiveFoldFromResult(sky_call(b.run, nil)); ok {
			folds = append(folds, fold)
		}
	}
	if len(folds) == 0 {
		return
	}

	sess.mu.Lock()
	prevShipped := sess.lastShippedBody
	prevTree := sess.prevTree
	prevModel := sess.model
	prevComputed := sess.lastComputedBody

	// Apply folds + render with panic-rollback: the fold is user code + a coerce
	// on decoded rows and can panic; on panic restore the pre-fold state and skip.
	var vn VNode
	body, ok := func() (b string, ok bool) {
		defer func() {
			if r := recover(); r != nil {
				sess.model = prevModel
				sess.lastComputedBody = prevComputed
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

func logReactivePanic(r any) {
	log.Printf("[sky.reactive] refresh panic recovered (session unchanged): %v", r)
}
