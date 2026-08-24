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
