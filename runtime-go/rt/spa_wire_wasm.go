//go:build js

package rt

// spa_wire_wasm.go — the client half of the wire handshake (spa_wire.go):
// the page's wire hash, the reload on a 409 `X-Sky-Status: reload`, the
// reload guard, and the notice after the reload.

import (
	"strconv"
	"sync"
	"syscall/js"
)

const (
	spaWireReloadAtKey = "sky:wire-reload-at" // localStorage: last wire reload (ms)
	spaWireReloadedKey = "sky:wire-reloaded"  // sessionStorage: show the notice once
)

var (
	spaWireOnce sync.Once
	spaWirePage string
)

// spaPageWireHash reads `<meta name="sky-wire" content="…">` once.
func spaPageWireHash() string {
	spaWireOnce.Do(func() {
		defer func() { _ = recover() }()
		doc := js.Global().Get("document")
		if !doc.Truthy() {
			return
		}
		m := doc.Call("querySelector", `meta[name="sky-wire"]`)
		if m.Truthy() {
			spaWirePage = m.Call("getAttribute", "content").String()
		}
	})
	return spaWirePage
}

// spaStorageGet / spaStorageSet read and write web storage; storage can be
// missing or throw (a private window, blocked site data), which reads as "".
func spaStorageGet(area, key string) (v string) {
	defer func() {
		if recover() != nil {
			v = ""
		}
	}()
	s := js.Global().Get(area)
	if !s.Truthy() {
		return ""
	}
	r := s.Call("getItem", key)
	if r.Type() != js.TypeString {
		return ""
	}
	return r.String()
}

func spaStorageSet(area, key, val string) {
	defer func() { _ = recover() }()
	if s := js.Global().Get(area); s.Truthy() {
		s.Call("setItem", key, val)
	}
}

func spaStorageRemove(area, key string) {
	defer func() { _ = recover() }()
	if s := js.Global().Get(area); s.Truthy() {
		s.Call("removeItem", key)
	}
}

// spaWireReload handles a 409 `X-Sky-Status: reload`: reload the page when
// the guard allows it, else show a notice with a Reload button. The Msg that
// was refused is not replayed.
func spaWireReload() {
	now := int64(js.Global().Get("Date").Call("now").Float())
	last, _ := strconv.ParseInt(spaStorageGet("localStorage", spaWireReloadAtKey), 10, 64)
	if spaWireShouldReload(last, now) {
		spaStorageSet("localStorage", spaWireReloadAtKey, strconv.FormatInt(now, 10))
		spaStorageSet("sessionStorage", spaWireReloadedKey, "1")
		js.Global().Get("location").Call("reload")
		return
	}
	spaShowWireNotice("This app was updated. Reload the page to continue; your last action was not sent.", true)
}

// spaShowWireNotice shows a fixed notice (with a Reload button when reload).
func spaShowWireNotice(text string, reload bool) {
	defer func() { _ = recover() }()
	doc := js.Global().Get("document")
	body := doc.Get("body")
	if !body.Truthy() {
		return
	}
	el := doc.Call("getElementById", "sky-spa-wire")
	if !el.Truthy() {
		el = doc.Call("createElement", "div")
		el.Set("id", "sky-spa-wire")
		el.Call("setAttribute", "role", "status")
		el.Get("style").Set("cssText",
			"position:fixed;left:0;right:0;top:0;z-index:2147483647;"+
				"display:flex;align-items:center;justify-content:center;gap:14px;"+
				"padding:12px 16px;background:#1e3a8a;color:#fff;"+
				"font:600 14px/1.4 system-ui,-apple-system,Segoe UI,Roboto,sans-serif")
		body.Call("appendChild", el)
	}
	el.Set("textContent", "")
	msg := doc.Call("createElement", "span")
	msg.Set("textContent", text)
	el.Call("appendChild", msg)
	btn := doc.Call("createElement", "button")
	btn.Set("type", "button")
	btn.Get("style").Set("cssText",
		"background:#fff;color:#1e3a8a;border:0;border-radius:6px;padding:6px 16px;font:inherit;cursor:pointer")
	if reload {
		btn.Set("textContent", "Reload")
		btn.Call("addEventListener", "click", js.FuncOf(func(js.Value, []js.Value) any {
			js.Global().Get("location").Call("reload")
			return nil
		}))
	} else {
		btn.Set("textContent", "OK")
		btn.Call("addEventListener", "click", js.FuncOf(func(js.Value, []js.Value) any {
			el.Get("style").Set("display", "none")
			return nil
		}))
	}
	el.Call("appendChild", btn)
}

// After a wire reload, tell the user once that their last action was not
// sent. Runs when the wasm client starts.
func init() {
	if spaStorageGet("sessionStorage", spaWireReloadedKey) == "" {
		return
	}
	spaStorageRemove("sessionStorage", spaWireReloadedKey)
	show := func() {
		spaShowWireNotice("This app was updated while it was open. Your last action was not sent: please do it again.", false)
	}
	doc := js.Global().Get("document")
	if doc.Truthy() && doc.Get("readyState").String() == "loading" {
		doc.Call("addEventListener", "DOMContentLoaded", js.FuncOf(func(js.Value, []js.Value) any {
			show()
			return nil
		}))
		return
	}
	show()
}
