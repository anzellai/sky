//go:build !js

package rt

// handleCallerAllowed reports whether the calling goroutine may use a handle
// owned by `owner` (process_handle_id.go). A caller with no Sky.Live session
// in scope (a Task program, an HTTP handler, a durable worker) holds the
// handle as a capability and is allowed. A caller inside a session may use
// only the handles that session owns: never another session's, and never a
// handle made outside any session (the old "sessionless fallback", through
// which a stale id in a restored model reached a background feed's socket).
// Ownership is the *liveSession itself, so the sign-in rotation (which
// re-keys the same session) keeps it.
func handleCallerAllowed(owner *liveSession) bool {
	caller := currentLiveSession()
	return caller == nil || caller == owner
}
