//go:build js

package rt

// Std_App_livePort — the wasm client never serves Sky.Live; the port it is
// given is the only one it can name.
func Std_App_livePort(builderPort any) any { return builderPort }
