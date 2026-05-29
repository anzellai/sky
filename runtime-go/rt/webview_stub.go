// Stub for Sky.Webview on builds where cgo or the system webview is
// unavailable. Keeps the `rt.Webview_app` symbol present so Sky code
// using `import Std.Webview` compiles cleanly; calling it returns an
// `Err Error` with a clear remediation message instead of panicking
// with `undefined: rt.Webview_app` at link time.
//
// The real implementation lives in webview.go, which is gated by the
// inverse build tag — so exactly one of {webview.go, webview_stub.go}
// is compiled into any given binary.
//
// v0.1 ships macOS only: webview.go is built only on `cgo && darwin`.
// On every other build (no-cgo, or any OS other than darwin) this
// stub is compiled. v0.2 widens this once Linux + Windows smoke
// validation lands.

//go:build !cgo || !darwin

package rt

func Webview_app(cfg any) any {
	return func() any {
		_ = cfg
		return Err[any, any](ErrIo(
			"Webview.app: this Sky build was produced without cgo or for an " +
				"unsupported OS — Sky.Webview needs cgo + (macOS | Windows | Linux). " +
				"Rebuild with CGO_ENABLED=1 on a supported platform."))
	}
}
