//go:build !js

package rt

// Goroutine-local SSR-settle guard (fix 2). The SSR backend settles a route's
// GET-safe reads server-side (Spa_ssrSettle) so a crawler sees real per-route
// content. The settle folds whatever `Cmd` the app's `init` / `onRequest` /
// `onNavigate` produce — and those commands may, in a batch, contain a
// DESTRUCTIVE effect (a Db write, a File write, an outbound POST) alongside the
// read we want. "A GET must never mutate" is the soundness rule, so those
// destructive kernels SELF-SUPPRESS while a settle is in flight: the read leaves
// settle normally, the write returns a classified `Err` WITHOUT touching the
// store — the update's error branch folds it and the model is simply left
// without the write. This is what lets the settle fire `onNavigate` (per-route
// data) and relax the all-or-nothing init allowlist to per-command soundly.
//
// The flag is per-goroutine (keyed on the goroutine id, reusing
// currentGoroutineID) because SSR renders on the request goroutine and several
// requests run concurrently — a process-wide flag would wrongly suppress an
// unrelated RPC handler's write on another goroutine. The settle is fully
// SYNCHRONOUS (spaSsrSettleRound calls sky_call inline, spawning no goroutines),
// so the flag reliably covers every effect the settle runs and nothing else.
//
// SCOPE (reported, not hidden): only DESTRUCTIVE kernels are suppressed — the
// ones that mutate an external store or send an outbound mutation (Db writes,
// File writes, Http.post). Non-deterministic-but-non-destructive kernels
// (Time.now / Uuid / Random / Crypto.random) are NOT suppressed here: they
// mutate nothing, so running one during a settle only freezes a server-chosen
// value into the embedded `#sky-model` the client boots from. Making the settle
// fully deterministic (suppressing those too) is the documented follow-on.

import "sync"

var ssrSettleGoroutines sync.Map // map[int64]*ssrSettleState

// ssrSettleState records what happened inside ONE settle on one goroutine.
// suppressed is set when a destructive kernel self-suppressed: that effect did
// not run, so the settle is not complete and the page must not tell the client
// that the command already ran (spa_ssr_notjs.go Spa_ssrSettleFull).
type ssrSettleState struct {
	suppressed bool
}

// InSsrSettle reports whether the calling goroutine is inside an SSR settle.
// Destructive write kernels consult it to self-suppress (a GET must not mutate).
func InSsrSettle() bool {
	gid := currentGoroutineID()
	_, ok := ssrSettleGoroutines.Load(gid)
	return ok
}

// enterSsrSettle marks the calling goroutine as inside an SSR settle and returns
// the settle's fresh state record. Pairs with `defer exitSsrSettle()`.
func enterSsrSettle() *ssrSettleState {
	st := &ssrSettleState{}
	ssrSettleGoroutines.Store(currentGoroutineID(), st)
	return st
}

// exitSsrSettle clears the mark for the calling goroutine.
func exitSsrSettle() { ssrSettleGoroutines.Delete(currentGoroutineID()) }

// ssrSuppressedWrite is the guard every destructive kernel calls as the FIRST
// statement inside its task thunk. It returns a classified `Err` result (the
// value the thunk yields, i.e. `Result Error a`) when a settle is in flight, and
// `nil` otherwise. A nil return means "not suppressed — run the effect".
//
//	return func() any {
//	    if r := ssrSuppressedWrite("db.exec"); r != nil { return r }
//	    ... perform the effect ...
//	}
func ssrSuppressedWrite(op string) any {
	v, ok := ssrSettleGoroutines.Load(currentGoroutineID())
	if !ok {
		return nil
	}
	if st, ok := v.(*ssrSettleState); ok {
		st.suppressed = true
	}
	return Err[any, any](ErrIo(
		"effect suppressed during server-side render (a GET must not mutate): " + op))
}
