//go:build js

package rt

import "syscall/js"

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
