//go:build js

package rt

import (
	"fmt"
	"strconv"
	"syscall/js"
)

// bridgeSeq numbers async Android bridge callbacks. wasm is single-threaded
// (cooperative), so a plain counter is race-free.
var bridgeSeq int

// blockOnJsPromise bridges a JS Promise into a blocking Sky Result. It registers
// then/catch callbacks, blocks the calling (perform) goroutine on a channel they
// fill, releases them, and returns the settled Result. `onResolve` maps the
// resolved value(s) to a Sky Result; a rejection becomes Err(ErrFfi(message)).
// MUST be called from a perform goroutine so the block yields to the browser
// event loop that settles the Promise — same contract as fetchBlocking.
func blockOnJsPromise(promise js.Value, onResolve func(a []js.Value) SkyResult[any, any]) SkyResult[any, any] {
	if !promise.Truthy() || promise.Type() != js.TypeObject {
		return Err[any, any](ErrFfi("native: call did not return a promise"))
	}
	ch := make(chan SkyResult[any, any], 1)
	var onOk, onErr js.Func
	cleanup := func() {
		onOk.Release()
		onErr.Release()
	}
	onOk = js.FuncOf(func(this js.Value, a []js.Value) any {
		ch <- onResolve(a)
		cleanup()
		return nil
	})
	onErr = js.FuncOf(func(this js.Value, a []js.Value) any {
		msg := "native call rejected"
		if len(a) > 0 && a[0].Truthy() {
			if m := a[0].Get("message"); m.Truthy() {
				msg = m.String()
			}
		}
		ch <- Err[any, any](ErrPermissionDenied(msg))
		cleanup()
		return nil
	})
	promise.Call("then", onOk).Call("catch", onErr)
	return <-ch
}

// nativeInt coerces a kernel's any-boxed Sky Int arg to a Go int.
func nativeInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

// safeJsCall invokes a JS method that MAY throw (localStorage quota / private-mode
// block, a security exception) and converts the thrown value into an error rather
// than letting the panic escape. syscall/js turns a JS exception into a Go panic;
// recovering it here is what keeps a well-typed Sky `Task` returning `Err` instead
// of crashing the client.
func safeJsCall(recv js.Value, method string, args ...any) (res js.Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	res = recv.Call(method, args...)
	return
}

// Native_geolocation is the Std.Native.geolocation kernel. It returns a Task
// thunk that, when forced on its perform goroutine, asks the platform's
// Geolocation API for the current position and BLOCKS until it settles —
// returning Ok(NativeCoords) or an Err. Same promise/callback-bridging shape as
// fetchBlocking (http_wasm.go): register js.Func callbacks, block on a channel
// they fill, release them, return the settled Sky Result.
func Native_geolocation(_ any) any {
	return func() any {
		nav := js.Global().Get("navigator")
		if !nav.Truthy() {
			return Err[any, any](ErrNetwork("geolocation: no navigator"))
		}
		geo := nav.Get("geolocation")
		if !geo.Truthy() {
			return Err[any, any](ErrNetwork("geolocation: unavailable in this runtime"))
		}
		ch := make(chan SkyResult[any, any], 1)
		var onOk, onErr js.Func
		cleanup := func() {
			onOk.Release()
			onErr.Release()
		}
		onOk = js.FuncOf(func(this js.Value, a []js.Value) any {
			c := a[0].Get("coords")
			ch <- Ok[any, any](NativeCoords{
				Lat:      c.Get("latitude").Float(),
				Lng:      c.Get("longitude").Float(),
				Accuracy: c.Get("accuracy").Float(),
			})
			cleanup()
			return nil
		})
		onErr = js.FuncOf(func(this js.Value, a []js.Value) any {
			msg := "geolocation request failed"
			if len(a) > 0 && a[0].Truthy() {
				if m := a[0].Get("message"); m.Truthy() {
					msg = "geolocation: " + m.String()
				}
			}
			ch <- Err[any, any](ErrPermissionDenied(msg))
			cleanup()
			return nil
		})
		geo.Call("getCurrentPosition", onOk, onErr)
		return <-ch
	}
}

// Native_clipboardWrite is the Std.Native.clipboardWrite kernel
// (`String -> Task Error ()`). It writes the text via navigator.clipboard and
// blocks until the write settles.
func Native_clipboardWrite(text any) any {
	s := fmt.Sprintf("%v", text)
	return func() any {
		clip := js.Global().Get("navigator").Get("clipboard")
		if !clip.Truthy() {
			return Err[any, any](ErrFfi("clipboard: unavailable in this runtime"))
		}
		return blockOnJsPromise(clip.Call("writeText", s), func(a []js.Value) SkyResult[any, any] {
			return Ok[any, any](struct{}{})
		})
	}
}

// Native_clipboardRead is the Std.Native.clipboardRead kernel
// (`() -> Task Error String`). It reads the clipboard text and blocks until the
// read settles, yielding Ok(text) or Err on denial.
func Native_clipboardRead(_ any) any {
	return func() any {
		clip := js.Global().Get("navigator").Get("clipboard")
		if !clip.Truthy() {
			return Err[any, any](ErrFfi("clipboard: unavailable in this runtime"))
		}
		return blockOnJsPromise(clip.Call("readText"), func(a []js.Value) SkyResult[any, any] {
			text := ""
			if len(a) > 0 && a[0].Truthy() {
				text = a[0].String()
			}
			return Ok[any, any](text)
		})
	}
}

// Native_vibrate is the Std.Native.vibrate kernel (`Int -> Task Error ()`).
// navigator.vibrate is synchronous and returns a bool; on hardware that cannot
// vibrate it simply returns false, which we treat as a clean no-op Ok so a UI
// can fire it unconditionally.
func Native_vibrate(ms any) any {
	d := nativeInt(ms)
	return func() any {
		nav := js.Global().Get("navigator")
		if !nav.Truthy() || nav.Get("vibrate").Type() != js.TypeFunction {
			return Ok[any, any](struct{}{})
		}
		nav.Call("vibrate", d)
		return Ok[any, any](struct{}{})
	}
}

// Native_share is the Std.Native.share kernel (`ShareContent -> Task Error ()`).
// The Sky record arrives as an rt.ShareContent (the runtime-backed-record alias),
// which we hand to navigator.share and block until the share sheet settles.
func Native_share(content any) any {
	c, ok := content.(ShareContent)
	if !ok {
		return func() any { return Err[any, any](ErrFfi("share: bad content")) }
	}
	return func() any {
		nav := js.Global().Get("navigator")
		if !nav.Truthy() || nav.Get("share").Type() != js.TypeFunction {
			return Err[any, any](ErrFfi("share: no share sheet in this runtime"))
		}
		data := js.Global().Get("Object").New()
		if c.Title != "" {
			data.Set("title", c.Title)
		}
		if c.Text != "" {
			data.Set("text", c.Text)
		}
		if c.Url != "" {
			data.Set("url", c.Url)
		}
		return blockOnJsPromise(nav.Call("share", data), func(a []js.Value) SkyResult[any, any] {
			return Ok[any, any](struct{}{})
		})
	}
}

// Native_storageSet is the Std.Native.storageSet kernel
// (`String -> String -> Task Error ()`). Writes to localStorage; a quota /
// private-mode throw becomes Err (via safeJsCall), never a crash.
func Native_storageSet(key any, val any) any {
	k := fmt.Sprintf("%v", key)
	v := fmt.Sprintf("%v", val)
	return func() any {
		ls := js.Global().Get("localStorage")
		if !ls.Truthy() {
			return Err[any, any](ErrFfi("localStorage: unavailable in this runtime"))
		}
		if _, e := safeJsCall(ls, "setItem", k, v); e != nil {
			return Err[any, any](ErrFfi("localStorage.setItem: " + e.Error()))
		}
		return Ok[any, any](struct{}{})
	}
}

// Native_storageGet is the Std.Native.storageGet kernel
// (`String -> Task Error (Maybe String)`). A missing key is Ok(Nothing), NOT an
// error; only a storage failure is Err.
func Native_storageGet(key any) any {
	k := fmt.Sprintf("%v", key)
	return func() any {
		ls := js.Global().Get("localStorage")
		if !ls.Truthy() {
			return Err[any, any](ErrFfi("localStorage: unavailable in this runtime"))
		}
		val, e := safeJsCall(ls, "getItem", k)
		if e != nil {
			return Err[any, any](ErrFfi("localStorage.getItem: " + e.Error()))
		}
		// getItem returns a string for a present key, null for a missing one.
		if val.Type() == js.TypeString {
			return Ok[any, any](Just[any](val.String()))
		}
		return Ok[any, any](Nothing[any]())
	}
}

// Native_storageRemove is the Std.Native.storageRemove kernel
// (`String -> Task Error ()`). Removing an absent key is a no-op Ok.
func Native_storageRemove(key any) any {
	k := fmt.Sprintf("%v", key)
	return func() any {
		ls := js.Global().Get("localStorage")
		if !ls.Truthy() {
			return Err[any, any](ErrFfi("localStorage: unavailable in this runtime"))
		}
		if _, e := safeJsCall(ls, "removeItem", k); e != nil {
			return Err[any, any](ErrFfi("localStorage.removeItem: " + e.Error()))
		}
		return Ok[any, any](struct{}{})
	}
}

// Native_isOnline is the Std.Native.isOnline kernel (`() -> Task Error Bool`).
func Native_isOnline(_ any) any {
	return func() any {
		nav := js.Global().Get("navigator")
		if !nav.Truthy() {
			return Err[any, any](ErrFfi("navigator: unavailable in this runtime"))
		}
		return Ok[any, any](nav.Get("onLine").Bool())
	}
}

// Native_language is the Std.Native.language kernel (`() -> Task Error String`).
func Native_language(_ any) any {
	return func() any {
		nav := js.Global().Get("navigator")
		if !nav.Truthy() {
			return Err[any, any](ErrFfi("navigator: unavailable in this runtime"))
		}
		lang := nav.Get("language")
		if lang.Type() != js.TypeString {
			return Ok[any, any]("en")
		}
		return Ok[any, any](lang.String())
	}
}

// Native_setTitle is the Std.Native.setTitle kernel (`String -> Task Error ()`).
func Native_setTitle(title any) any {
	t := fmt.Sprintf("%v", title)
	return func() any {
		doc := js.Global().Get("document")
		if !doc.Truthy() {
			return Err[any, any](ErrFfi("document: unavailable in this runtime"))
		}
		doc.Set("title", t)
		return Ok[any, any](struct{}{})
	}
}

// Native_prefersDarkMode is the Std.Native.prefersDarkMode kernel
// (`() -> Task Error Bool`) — reads the prefers-color-scheme media query.
func Native_prefersDarkMode(_ any) any {
	return func() any {
		win := js.Global()
		mm := win.Get("matchMedia")
		if mm.Type() != js.TypeFunction {
			return Err[any, any](ErrFfi("matchMedia: unavailable in this runtime"))
		}
		res, e := safeJsCall(win, "matchMedia", "(prefers-color-scheme: dark)")
		if e != nil {
			return Err[any, any](ErrFfi("matchMedia: " + e.Error()))
		}
		return Ok[any, any](res.Get("matches").Bool())
	}
}

// Native_openUrl is the Std.Native.openUrl kernel (`String -> Task Error ()`) —
// opens the URL in a new tab / the system browser. A suppressed popup (null
// return) is Err.
func Native_openUrl(url any) any {
	u := fmt.Sprintf("%v", url)
	return func() any {
		win := js.Global()
		if win.Get("open").Type() != js.TypeFunction {
			return Err[any, any](ErrFfi("window.open: unavailable in this runtime"))
		}
		// `noopener` severs window.opener so the opened page can't drive our tab —
		// but per the HTML spec it also makes window.open ALWAYS return null, so a
		// null return is NOT a failure signal here. A genuinely blocked popup
		// throws (or is silently dropped); we treat a non-throwing call as issued.
		if _, e := safeJsCall(win, "open", u, "_blank", "noopener"); e != nil {
			return Err[any, any](ErrFfi("window.open: " + e.Error()))
		}
		return Ok[any, any](struct{}{})
	}
}

// Native_notify is the Std.Native.notify kernel (`String -> String -> Task Error
// ()`). It shows a real device notification, preferring a NATIVE bridge the
// generated mobile shells install over the Web Notification API:
//
//   1. iOS  — `window.webkit.messageHandlers.skyNative` (a
//      WKScriptMessageHandlerWithReply the WKWebView shell registers). The
//      shell's Swift handler drives `UNUserNotificationCenter`, so a real local
//      notification fires even though iOS WKWebView disables the Web
//      Notification API. postMessage returns a Promise → Ok/Err.
//   2. Android — `window.SkyNative.notify(title, body)` (an @JavascriptInterface
//      the WebView shell installs) drives `NotificationManager`; returns a bool.
//   3. Web / desktop — the Web Notification API (requestPermission + new
//      Notification), the original path.
func Native_notify(title any, body any) any {
	t := fmt.Sprintf("%v", title)
	b := fmt.Sprintf("%v", body)
	return func() any {
		win := js.Global()

		// 1. iOS native bridge (WKScriptMessageHandlerWithReply → Promise).
		if webkit := win.Get("webkit"); webkit.Truthy() {
			if mh := webkit.Get("messageHandlers"); mh.Truthy() {
				if sky := mh.Get("skyNative"); sky.Truthy() &&
					sky.Get("postMessage").Type() == js.TypeFunction {
					msg := win.Get("Object").New()
					msg.Set("type", "notify")
					msg.Set("title", t)
					msg.Set("body", b)
					reply := sky.Call("postMessage", msg)
					// A reply handler returns a Promise; a plain handler returns
					// undefined — fall through to the Web path if so.
					if reply.Truthy() && reply.Type() == js.TypeObject {
						return blockOnJsPromise(reply, func(a []js.Value) SkyResult[any, any] {
							return Ok[any, any](struct{}{})
						})
					}
				}
			}
		}

		// 2. Android native bridge (@JavascriptInterface, synchronous bool). The
		// injected object is a Java-reflection proxy: its methods are CALLABLE but
		// property access (`SkyNative.notify` as a value) does not always report
		// js.TypeFunction, so gate on the object's presence and attempt the call
		// under recover rather than introspecting the method.
		if sn := win.Get("SkyNative"); sn.Truthy() {
			called := false
			ok := false
			func() {
				defer func() { recover() }()
				res := sn.Call("notify", t, b)
				called = true
				// A bool false = the device denied / failed; undefined = the proxy
				// returned nothing but did not throw (treat as issued).
				ok = res.Type() != js.TypeBoolean || res.Bool()
			}()
			if called {
				if ok {
					return Ok[any, any](struct{}{})
				}
				return Err[any, any](ErrPermissionDenied("notify: the device denied notifications"))
			}
			// The call threw — fall through to the Web path.
		}

		// 3. Web Notification API (browser / desktop webview).
		ctor := win.Get("Notification")
		if !ctor.Truthy() {
			return Err[any, any](ErrFfi("notifications: unavailable in this runtime"))
		}
		show := func() SkyResult[any, any] {
			opts := js.Global().Get("Object").New()
			opts.Set("body", b)
			// `new Notification(title, opts)` — js.Value.New is the `new` operator;
			// it panics if the constructor throws, so guard it.
			var built bool
			func() {
				defer func() { recover() }()
				ctor.New(t, opts)
				built = true
			}()
			if !built {
				return Err[any, any](ErrFfi("notifications: could not be shown"))
			}
			return Ok[any, any](struct{}{})
		}
		perm := ctor.Get("permission").String()
		switch perm {
		case "granted":
			return show()
		case "denied":
			return Err[any, any](ErrPermissionDenied("notifications: permission denied"))
		default:
			// "default" — request permission (Promise<string>), then show.
			req := ctor.Get("requestPermission")
			if req.Type() != js.TypeFunction {
				return Err[any, any](ErrFfi("notifications: cannot request permission"))
			}
			return blockOnJsPromise(ctor.Call("requestPermission"), func(a []js.Value) SkyResult[any, any] {
				if len(a) > 0 && a[0].Type() == js.TypeString && a[0].String() == "granted" {
					return show()
				}
				return Err[any, any](ErrPermissionDenied("notifications: permission not granted"))
			})
		}
	}
}

// nativePickFile drives an off-screen <input type="file"> and blocks until the
// user picks a file (read as a base64 data: URL) or cancels. `accept` filters the
// picker (e.g. "image/*" for the gallery); `capture` opens the camera on mobile.
// A cancel returns Err (via the modern `cancel` event) rather than hanging — no
// silent strand. MUST run on the perform goroutine so the block yields to the
// event loop, like fetchBlocking. NOTE: opening a file picker needs a user
// gesture; whether the TEA perform path preserves one is exercised e2e.
func nativePickFile(accept string, capture bool) SkyResult[any, any] {
	global := js.Global()
	doc := global.Get("document")
	if !doc.Truthy() {
		return Err[any, any](ErrFfi("file picker: no document in this runtime"))
	}
	input := doc.Call("createElement", "input")
	input.Set("type", "file")
	if accept != "" {
		input.Set("accept", accept)
	}
	if capture {
		input.Call("setAttribute", "capture", "environment")
	}
	input.Get("style").Set("display", "none")
	doc.Get("body").Call("appendChild", input)

	ch := make(chan SkyResult[any, any], 1)
	resolved := false
	finish := func(r SkyResult[any, any]) {
		if resolved {
			return
		}
		resolved = true
		input.Call("remove")
		ch <- r
	}
	var funcs []js.Func
	addFunc := func(fn func(args []js.Value)) js.Func {
		f := js.FuncOf(func(this js.Value, args []js.Value) any {
			fn(args)
			return nil
		})
		funcs = append(funcs, f)
		return f
	}

	onChange := addFunc(func(args []js.Value) {
		files := input.Get("files")
		if !files.Truthy() || files.Get("length").Int() == 0 {
			finish(Err[any, any](ErrFfi("file picker: no file chosen")))
			return
		}
		file := files.Index(0)
		reader := global.Get("FileReader").New()
		onLoad := addFunc(func(a []js.Value) {
			finish(Ok[any, any](PickedFile{
				Name:    file.Get("name").String(),
				Mime:    file.Get("type").String(),
				DataUrl: reader.Get("result").String(),
			}))
		})
		onLoadErr := addFunc(func(a []js.Value) {
			finish(Err[any, any](ErrFfi("file picker: could not read the file")))
		})
		reader.Set("onload", onLoad)
		reader.Set("onerror", onLoadErr)
		reader.Call("readAsDataURL", file)
	})
	onCancel := addFunc(func(args []js.Value) {
		finish(Err[any, any](ErrPermissionDenied("file picker: cancelled")))
	})
	input.Call("addEventListener", "change", onChange)
	input.Call("addEventListener", "cancel", onCancel)
	input.Call("click")

	result := <-ch
	for _, f := range funcs {
		f.Release()
	}
	return result
}

// Native_pickFile — Std.Native.pickFile (`() -> Task Error PickedFile`): pick any
// file.
func Native_pickFile(_ any) any {
	return func() any { return nativePickFile("", false) }
}

// Native_pickImage — Std.Native.pickImage (`() -> Task Error PickedFile`): pick an
// image from the gallery (accept="image/*").
func Native_pickImage(_ any) any {
	return func() any { return nativePickFile("image/*", false) }
}

// Native_capturePhoto — Std.Native.capturePhoto (`() -> Task Error PickedFile`):
// take a photo with the camera on mobile (capture="environment"); a plain file
// picker on desktop.
func Native_capturePhoto(_ any) any {
	return func() any { return nativePickFile("image/*", true) }
}

// Native_batteryStatus is the Std.Native.batteryStatus kernel
// (`() -> Task Error BatteryStatus`). navigator.getBattery() returns a Promise.
func Native_batteryStatus(_ any) any {
	return func() any {
		nav := js.Global().Get("navigator")
		if !nav.Truthy() || nav.Get("getBattery").Type() != js.TypeFunction {
			return Err[any, any](ErrFfi("battery: unavailable in this runtime"))
		}
		return blockOnJsPromise(nav.Call("getBattery"), func(a []js.Value) SkyResult[any, any] {
			if len(a) == 0 || !a[0].Truthy() {
				return Err[any, any](ErrFfi("battery: no status returned"))
			}
			bm := a[0]
			return Ok[any, any](BatteryStatus{
				Charging: bm.Get("charging").Bool(),
				Level:    bm.Get("level").Float(),
			})
		})
	}
}

// Native_bridge is the Std.Native.bridge kernel (`String -> String -> Task Error
// String`) — the USER-EXTENSIBLE native call. It sends `{name, payload}` (payload
// a JSON string) to a handler the app registers, and returns the handler's JSON
// reply. Resolution order:
//
//  1. Web/desktop — `window.skyNativeHandlers[name](payload)`, a JS function the
//     app registers (may return a value or a Promise). Checked FIRST so a user's
//     explicit handler wins — and it works in EVERY webview (web, iOS, Android),
//     so a capability JS can do (e.g. the Payment Request API) needs no native
//     code. It also makes a custom capability testable in a plain browser.
//  2. iOS — `window.webkit.messageHandlers.skyNative` (a
//     WKScriptMessageHandlerWithReply); the shell's Swift `default` case routes
//     unknown types to the user's native extension. postMessage returns a Promise.
//  3. Android — `window.SkyNative.call(name, payload, cbId)`; the Java shell does
//     the (possibly async) native work and calls back `window.__skyBridgeCb[cbId]`.
//
// `Err` when no handler is registered for `name` — never a hang.
func Native_bridge(name any, payload any) any {
	n := fmt.Sprintf("%v", name)
	p := fmt.Sprintf("%v", payload)
	return func() any {
		global := js.Global()

		// 1. Web / desktop JS handler (app-registered; wins everywhere).
		if handlers := global.Get("skyNativeHandlers"); handlers.Truthy() {
			if h := handlers.Get(n); h.Type() == js.TypeFunction {
				res := h.Invoke(p)
				if res.Type() == js.TypeObject && res.Get("then").Type() == js.TypeFunction {
					return blockOnJsPromise(res, func(a []js.Value) SkyResult[any, any] {
						s := ""
						if len(a) > 0 {
							if a[0].Type() == js.TypeString {
								s = a[0].String()
							} else if a[0].Truthy() {
								s = global.Get("JSON").Call("stringify", a[0]).String()
							}
						}
						return Ok[any, any](s)
					})
				}
				if res.Type() == js.TypeString {
					return Ok[any, any](res.String())
				}
				if res.Truthy() {
					return Ok[any, any](global.Get("JSON").Call("stringify", res).String())
				}
				return Ok[any, any]("")
			}
		}

		// 2. iOS reply-Promise bridge.
		if webkit := global.Get("webkit"); webkit.Truthy() {
			if mh := webkit.Get("messageHandlers"); mh.Truthy() {
				if sky := mh.Get("skyNative"); sky.Truthy() &&
					sky.Get("postMessage").Type() == js.TypeFunction {
					msg := global.Get("Object").New()
					msg.Set("type", n)
					msg.Set("payload", p)
					reply := sky.Call("postMessage", msg)
					if reply.Truthy() && reply.Type() == js.TypeObject {
						return blockOnJsPromise(reply, func(a []js.Value) SkyResult[any, any] {
							s := ""
							if len(a) > 0 && a[0].Type() == js.TypeString {
								s = a[0].String()
							}
							return Ok[any, any](s)
						})
					}
				}
			}
		}

		// 3. Android async callback bridge.
		if sn := global.Get("SkyNative"); sn.Truthy() && sn.Get("call").Truthy() {
			reg := global.Get("__skyBridgeCb")
			if !reg.Truthy() {
				reg = global.Get("Object").New()
				global.Set("__skyBridgeCb", reg)
			}
			bridgeSeq++
			cbId := "cb" + strconv.Itoa(bridgeSeq)
			ch := make(chan SkyResult[any, any], 1)
			var resolver js.Func
			resolver = js.FuncOf(func(this js.Value, a []js.Value) any {
				ok := len(a) > 0 && a[0].Truthy()
				data := ""
				if len(a) > 1 && a[1].Type() == js.TypeString {
					data = a[1].String()
				}
				reg.Delete(cbId)
				resolver.Release()
				if ok {
					ch <- Ok[any, any](data)
				} else {
					ch <- Err[any, any](ErrFfi("native bridge '" + n + "': " + data))
				}
				return nil
			})
			reg.Set(cbId, resolver)
			called := false
			func() {
				defer func() { recover() }()
				sn.Call("call", n, p, cbId)
				called = true
			}()
			if called {
				return <-ch
			}
			reg.Delete(cbId)
			resolver.Release()
			// fall through — this SkyNative has no `call`.
		}

		return Err[any, any](ErrFfi("native bridge: no handler registered for '" + n + "'"))
	}
}
