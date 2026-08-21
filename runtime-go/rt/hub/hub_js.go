//go:build js

// The telemetry hub is server-only: it stores logs/metrics/traces (SQLite +
// Postgres) and mounts HTTP receivers. All real files are //go:build !js.
// This placeholder keeps the package non-empty under GOOS=js so
// `go build ./rt/...` does not fail with "build constraints exclude all Go
// files".
package hub
