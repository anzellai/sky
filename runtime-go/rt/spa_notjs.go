//go:build !js

package rt

// spaRun (host build) — a Sky.Spa app targets GOOS=js GOARCH=wasm; there is no
// DOM on the host. `sky build` still go-builds the emitted app for the host as
// part of `sky check`, so this stub keeps the link resolving. The real driver
// is in live_wasm.go (//go:build js). Running a Spa binary on the host just
// reports where it belongs, rather than silently doing nothing.
func spaRun(cfg any) any {
	logEmit(logLevelWarn, "warn",
		"Sky.Spa app: this binary is a client (TEA) app — build it for the "+
			"browser with `GOOS=js GOARCH=wasm go build -o main.wasm` on the "+
			"emitted Go and serve it with wasm_exec.js.", nil)
	return nil
}
