//go:build js

package rt

// runOutsideLiveSession: a wasm client has no Sky.Live session stamp, so a
// CAF is computed as is (lazycaf_session.go is the server side).
func runOutsideLiveSession(f func()) { f() }
