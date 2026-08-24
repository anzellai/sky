//go:build js

package rt

import (
	"fmt"
	"syscall/js"
)

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
