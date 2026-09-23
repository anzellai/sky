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
