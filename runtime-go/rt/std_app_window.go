//go:build !js

package rt

// Std_App_livePort returns the port the Sky.Live server of a Std.App
// actually binds, given the port the app passes to `Live.withPort`
// (WebOpts.port). It runs the SAME resolver as Live_app (resolveLivePort:
// the <PREFIX>_LIVE_PORT env var, then the builder, then sky.toml), so the
// desktop window (runLiveWindow) waits on and opens the port the server
// listens on — not the WebOpts value an env override replaced (SA-9).
func Std_App_livePort(builderPort any) any {
	return resolveLivePort(map[string]any{"Port": builderPort})
}
