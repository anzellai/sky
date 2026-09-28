//go:build !js

package rt

// Std_App_livePort returns the port the Sky.Live server of a Std.App
// actually binds, given the port the app passes to `Live.withPort`
// (WebOpts.port). It runs the SAME resolver as Live_app (resolveLivePort:
// the <PREFIX>_LIVE_PORT env var, then the builder, then sky.toml), so the
// desktop window (runLiveWindow) waits on and opens the port the server
// listens on — not the WebOpts value an env override replaced (SA-9).
// A negative port is WebOpts' "not set" sentinel (webDefaults): the
// resolver ignores it, so the env var and then sky.toml decide, exactly as
// for the server, which then gets no `Live.withPort` at all.
func Std_App_livePort(builderPort any) any {
	return resolveLivePort(map[string]any{"Port": builderPort})
}
