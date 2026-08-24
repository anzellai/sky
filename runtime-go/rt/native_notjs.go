//go:build !js

package rt

// Native_geolocation on a NON-client build (native CLI / server). Geolocation is
// a client capability — it needs the webview/browser's platform location
// service — so there is nothing to call here; return Err rather than pretend.
// The real implementation is the wasm build (native_wasm.go).
func Native_geolocation(_ any) any {
	return func() any {
		return Err[any, any](ErrNetwork(
			"Native.geolocation is a client-only capability (no location service in this runtime)"))
	}
}
