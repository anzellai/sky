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

var (
	spaNetErrEl    js.Value  // the overlay element, created lazily and reused
	spaNetErrBtnFn js.Func   // the Retry button's click listener (created once)
	spaRetry       func()    // the pending retry action (re-run the failed perform)
)

// spaShowRetryOverlay displays the connection banner and arms Retry with `retry`.
func spaShowRetryOverlay(retry func()) {
	spaRetry = retry
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
			r := spaRetry
			spaHideRetryOverlay()
			if r != nil {
				go r() // re-run the failed perform on its own goroutine
			}
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

// spaHideRetryOverlay hides the banner and clears the pending retry.
func spaHideRetryOverlay() {
	if spaNetErrEl.Truthy() {
		spaNetErrEl.Get("style").Set("display", "none")
	}
	spaRetry = nil
}
