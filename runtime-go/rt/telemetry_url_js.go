//go:build js

package rt

// telemetrySkyPath on the wasm client. Middleware.withLogging is a
// Sky.Http.Server middleware and has no request to serve in the browser, so
// the client never records a path (telemetry_url.go has the server rules).
func telemetrySkyPath(string) string { return "(not recorded)" }
