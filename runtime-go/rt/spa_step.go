package rt

// spa_step.go — the panic-guarded core of the Sky.Spa client TEA step, factored
// out of the js-only driver (live_wasm.go) so it is unit-tested on the host
// without a wasm/browser build. Same seam as spa_neterror.go: the DECISION is
// build-tag-free here; the js wiring (console.error, the real render + Cmd
// interpretation) lives in live_wasm.go.
//
// Why it exists. Sky.Live's server dispatch recovers a panicking update/view to
// a 500 and the session survives (live.go dispatch). The wasm client had no
// such net on its PRIMARY event path: a classified panic (a DivisionByZero from
// `10.0 / 0.0`, or an rt.Coerce panic) thrown by the user's update or view from
// a click or a keystroke killed the whole Go/wasm instance and the page went
// blank. The perform / timer / topic paths already recover; this restores the
// same guarantee to the click/keystroke path.
//
// Semantics on a panic:
//   - The model is rolled back to the last good value when the panic happened
//     BEFORE the render committed, so the on-screen tree and the driver's model
//     never diverge — a half-updated model is never repainted.
//   - The panic is reported through onPanic (a [sky.spa] console.error).
//   - The instance stays alive: spaTransition returns normally, so the next
//     event still dispatches.
//
// Stages, in order: update (pure), then view+paint (render), then post (URL
// sync, Cmd interpretation, subscription reconciliation). update and view can
// roll the model back; once render commits, a later post panic keeps the new
// model (the screen already shows it) and is still reported.
func spaTransition(
	msg, cur any,
	update func(msg, model any) SkyTuple2,
	render func(model any),
	post func(cmd any),
	onPanic func(stage string, r any),
) (kept any) {
	prev := cur
	kept = cur
	committed := false
	stage := "update"
	defer func() {
		if r := recover(); r != nil {
			if !committed {
				// The panic escaped update or view before the paint committed:
				// discard the partial transition and keep the last good model.
				kept = prev
			}
			if onPanic != nil {
				onPanic(stage, r)
			}
		}
	}()
	pair := update(msg, cur)
	kept = pair.V0
	cmd := pair.V1
	stage = "view"
	render(pair.V0)
	committed = true
	stage = "cmd"
	post(cmd)
	return kept
}

// spaRenderGuard is the render-only counterpart of spaTransition, for the
// DEFAULT sky-nav / popstate path (spaNavigate with no onNavigate hook). That
// path runs no `update` — it re-renders the current model after a pure URL
// change — so it does NOT go through spaTransition, and a view panic there used
// to kill the Go/wasm instance and white-screen the app (Judge finding 4).
//
// On a panic it rolls the driver's model back to `prev` (the paint did not
// commit, so the on-screen tree still shows the previous view), reports through
// onPanic, and returns so the next event still dispatches. Factored here, apart
// from the js render wiring, so it is unit-tested on the host.
func spaRenderGuard(prev any, render func(), restore func(prev any), onPanic func(stage string, r any)) {
	defer func() {
		if r := recover(); r != nil {
			if restore != nil {
				restore(prev)
			}
			if onPanic != nil {
				onPanic("navigate-render", r)
			}
		}
	}()
	render()
}
