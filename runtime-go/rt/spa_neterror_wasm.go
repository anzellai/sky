//go:build js

package rt

import "syscall/js"

// Built-in Sky.Spa connection-error overlay: when a Cmd.perform (an auto-split
// RPC, or any Http call) fails because the server is unreachable, the runtime
// shows a fixed banner — "Can't reach the server. [Retry]" — instead of the
// client silently stranding with no update. Retry re-runs the SAME failed
// perform, so the app resumes exactly where it stalled; any successful perform
// hides the banner. This is injected by the runtime, so every Sky.Spa app gets
// it with zero app code (spaIsNetworkErr in spa_neterror.go decides when).
//
// EVERY failed perform is kept, in the order it failed (spaRetryQueue): Retry
// re-runs all of them, one after another, so no failed request is forgotten
// because a later one also failed. A queued server-branch RPC that has not been
// sent yet is not in this list — it waits behind the failed RPC in the RPC
// queue (spa_rpcqueue.go) and is sent once that one settles.

var (
	spaNetErrEl    js.Value // the overlay element, created lazily and reused
	spaNetErrBtnFn js.Func  // the Retry button's click listener (created once)
	spaRetryQueue  []func() // every pending retry action, in failure order
)

// spaShowRetryOverlay displays the connection banner and appends `retry` to the
// actions the Retry button runs.
func spaShowRetryOverlay(retry func()) {
	spaRetryQueue = spaAppendRetry(spaRetryQueue, retry)
	defer spaConnNotify()
	doc := js.Global().Get("document")
	if !doc.Truthy() {
		return
	}
	body := doc.Get("body")
	if !body.Truthy() {
		return
	}
	if !spaNetErrEl.Truthy() {
		el := doc.Call("createElement", "div")
		el.Set("id", "sky-spa-neterror")
		el.Get("style").Set("cssText",
			"position:fixed;left:0;right:0;bottom:0;z-index:2147483647;"+
				"display:flex;align-items:center;justify-content:center;gap:14px;"+
				"padding:calc(12px + env(safe-area-inset-bottom)) 16px 12px;"+
				"background:#b91c1c;color:#fff;"+
				"font:600 14px/1.4 system-ui,-apple-system,Segoe UI,Roboto,sans-serif;"+
				"box-shadow:0 -1px 10px rgba(0,0,0,.28)")
		msg := doc.Call("createElement", "span")
		msg.Set("textContent", "Can't reach the server.")
		msg.Get("style").Set("fontWeight", "400")
		btn := doc.Call("createElement", "button")
		btn.Set("textContent", "Retry")
		btn.Set("type", "button")
		btn.Get("style").Set("cssText",
			"background:#fff;color:#b91c1c;border:0;border-radius:6px;"+
				"padding:6px 16px;font:inherit;font-weight:600;cursor:pointer")
		spaNetErrBtnFn = js.FuncOf(func(this js.Value, args []js.Value) any {
			rs := spaRetryQueue
			spaHideRetryOverlay()
			// Re-run every failed perform, in failure order, on one goroutine:
			// each blocks until it settles, so the order is kept. One that fails
			// again re-arms the overlay with itself.
			go spaRunRetries(rs)
			return nil
		})
		btn.Call("addEventListener", "click", spaNetErrBtnFn)
		el.Call("appendChild", msg)
		el.Call("appendChild", btn)
		body.Call("appendChild", el)
		spaNetErrEl = el
	} else {
		spaNetErrEl.Get("style").Set("display", "flex")
	}
}

// spaHideRetryOverlay hides the banner and clears the pending retries.
func spaHideRetryOverlay() {
	defer spaConnNotify()
	if spaNetErrEl.Truthy() {
		spaNetErrEl.Get("style").Set("display", "none")
	}
	spaRetryQueue = nil
}

// spaHideRetryOverlayIfIdle hides the banner after a successful perform ONLY
// when no failed perform is still waiting for Retry — a later success must not
// silently drop an earlier failure.
func spaHideRetryOverlayIfIdle() {
	if len(spaRetryQueue) == 0 {
		spaHideRetryOverlay()
	}
}

// ---- v0.27.3: quiet reconnecting indicator, connection state, recovery ----
//
// A transient failure is retried by the transport (spa_retry.go) and stays
// invisible until the outage is proven longer than spaReconnectGrace (a
// re-send that late failed too). Then a small "Reconnecting…" pill shows; it does not block taps (pointer-events: none). The red bar above is
// shown only once a request's retry budget is spent. Every change of state is
// also delivered to an app's `Sub.connection` leaf.

var (
	spaReconnEl     js.Value // the "Reconnecting…" pill, created lazily
	spaConnToMsg    any      // the app's Sub.connection toMsg (reconcileSubs)
	spaConnLastCode = spaConnOnline
	spaConnLastPend = 0
	spaReconnInd    = &spaIndicator{
		show: func() { spaReconnShow(true) },
		hide: func() { spaReconnShow(false) },
	}
)

func spaReconnShow(on bool) {
	doc := js.Global().Get("document")
	if !doc.Truthy() || !doc.Get("body").Truthy() {
		return
	}
	if !spaReconnEl.Truthy() {
		if !on {
			return
		}
		el := doc.Call("createElement", "div")
		el.Set("id", "sky-spa-reconnecting")
		el.Call("setAttribute", "role", "status")
		el.Call("setAttribute", "aria-live", "polite")
		el.Set("textContent", "Reconnecting…")
		el.Get("style").Set("cssText",
			"position:fixed;left:50%;transform:translateX(-50%);"+
				"bottom:calc(16px + env(safe-area-inset-bottom));z-index:2147483646;"+
				"pointer-events:none;padding:6px 14px;border-radius:999px;"+
				"background:rgba(17,24,39,.85);color:#fff;"+
				"font:500 13px/1.4 system-ui,-apple-system,Segoe UI,Roboto,sans-serif;"+
				"box-shadow:0 2px 8px rgba(0,0,0,.2)")
		doc.Get("body").Call("appendChild", el)
		spaReconnEl = el
		return
	}
	if on {
		spaReconnEl.Get("style").Set("display", "block")
	} else {
		spaReconnEl.Get("style").Set("display", "none")
	}
}

// spaConnNotify recomputes the connection state after any change of the retry
// queue or the red bar's queue: it drives the indicator and the app's
// Sub.connection leaf (only on a change).
func spaConnNotify() {
	code, pending := spaConnState(spaCoord.queued(), len(spaRetryQueue))
	spaReconnInd.update(code, spaCoord.outage())
	if code == spaConnLastCode && pending == spaConnLastPend {
		return
	}
	spaConnLastCode, spaConnLastPend = code, pending
	if tm := spaConnToMsg; tm != nil {
		// Synchronously, so state changes reach update in the order they
		// happened (drain is not re-entrant: inside a step it only queues).
		spaSch.dispatchUrgent(spaConnMsg(tm, code, pending), step)
	}
}

// spaRecoverNow is a recovery signal: re-send the head of the retry queue now
// (fresh budget), re-run the red bar's failed requests in order, and give each
// Sub.every interval that owes a tick its one fresh tick.
func spaRecoverNow() {
	spaCoord.recover()
	if len(spaRetryQueue) > 0 {
		rs := spaRetryQueue
		spaHideRetryOverlay()
		go spaRunRetries(rs)
	}
}

// spaInstallRecovery listens for the signals that mean "the network may be
// back": online, focus, pageshow, and visibilitychange to visible (also
// Page Lifecycle resume). Hidden and freeze stop network-bearing ticks
// (spa_tick.go). Hidden, freeze and pagehide also stop the retry budgets
// (spaCoord.setHidden): no request is given up while the user cannot see the
// page, and the return gives each a fresh budget and re-sends the head now.
func spaInstallRecovery() {
	win := js.Global()
	doc := win.Get("document")
	if !doc.Truthy() {
		return
	}
	visible := func() {
		spaCoord.setHidden(false)
		spaRecoverNow()
		for _, ms := range spaTicks.setHidden(false) {
			spaFreshTick(ms)
		}
	}
	on := func(target js.Value, name string, f func()) {
		target.Call("addEventListener", name, js.FuncOf(func(this js.Value, args []js.Value) any {
			f()
			return nil
		}))
	}
	on(win, "online", spaRecoverNow)
	on(win, "focus", spaRecoverNow)
	on(win, "pageshow", visible)
	on(doc, "resume", visible)
	hidden := func() {
		spaTicks.setHidden(true)
		spaCoord.setHidden(true)
	}
	on(doc, "freeze", hidden)
	on(win, "pagehide", hidden)
	on(doc, "visibilitychange", func() {
		if doc.Get("hidden").Truthy() {
			hidden()
			return
		}
		visible()
	})
	if doc.Get("hidden").Truthy() {
		hidden()
	}
}
