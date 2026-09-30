//go:build !js

package rt

// runOutsideLiveSession runs f with the calling goroutine's Sky.Live session
// stamp cleared, and restores the stamp afterwards (LazyCaf.Get: a CAF is
// process-owned, never owned by the session that first forced it).
func runOutsideLiveSession(f func()) {
	if sess := currentLiveSession(); sess != nil {
		clearGoroutineLiveSession()
		defer setGoroutineLiveSession(sess)
	}
	f()
}
