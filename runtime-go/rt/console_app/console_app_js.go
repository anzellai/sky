//go:build js

// The embedded Sky Console mini-app is server-only: it mounts HTTP routes and
// reads the telemetry hub. All of its real files are //go:build !js. This
// placeholder keeps the package non-empty under GOOS=js so `go build ./rt/...`
// does not fail with "build constraints exclude all Go files".
package console_app
