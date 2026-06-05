// App-auth callback registry for the Sky Console Hub (v0.16.4 B8).
//
// When `sky console serve --auth app` is run, the hub gates every
// /console/* request through a Go-side callback that returns an
// rt.ConsoleIdentity (subject + email + claims). Operators (e.g.,
// SkyDeploy's hub-host harness) wire a `func(*http.Request)
// (rt.ConsoleIdentity, bool)` at boot via `RegisterAppAuthCallback`.
//
// Why a Go-side registry, not a Sky-side `consoleAuth` field on
// console_app's cfg:
//   - console_app is the BUNDLED console — its Sky source is
//     authoritative for every hub deployment. Adding an operator-
//     specific auth field there would force every consumer to fork
//     console_app or accept the bundle's default.
//   - A registry-based hook lets the hub binary's main package
//     (sky-hub) OR an operator-built host (SkyDeploy) inject the
//     auth callback at init() time without modifying console_app.
//   - Token-mode (the default) needs no Sky-side knowledge at all
//     and stays in authMiddleware.
//
// OTLP receivers are unaffected — they continue to use the token
// machine-to-machine. App mode only governs the UI surface.
//
// Lifecycle:
//   - `RegisterAppAuthCallback(cb)` — replace the callback.
//     Last-writer-wins; designed for init() registration but safe
//     to call multiple times during testing.
//   - `appAuthCallback()` — returns the registered callback or nil.
//     The hub's mount-gate translates nil to 503 (the operator
//     selected app-mode without registering a callback — fail
//     closed, never silently fall through to open access).

package hub

import (
	"net/http"
	"sync"

	rt "sky-app/rt"
)

// AppAuthCallback is the Go-side hook for `--auth app`. Returns
// (identity, true) on allow, (_, false) on deny. The callback
// runs INSIDE the request goroutine — it must be safe under load
// (no shared mutable state without locks, no blocking I/O without
// a context-aware timeout).
type AppAuthCallback func(*http.Request) (rt.ConsoleIdentity, bool)

var (
	appAuthMu sync.RWMutex
	appAuthCb AppAuthCallback
)

// RegisterAppAuthCallback installs the callback used by `--auth app`.
// Call from the host binary's init() (typical) or before Run() in
// programmatic embeddings. Idempotent — subsequent calls replace.
func RegisterAppAuthCallback(cb AppAuthCallback) {
	appAuthMu.Lock()
	appAuthCb = cb
	appAuthMu.Unlock()
}

// appAuthCallback returns the currently-registered callback, or
// nil when the operator hasn't registered one. The mount gate
// translates nil → 503 with the explicit `auth-app-unregistered`
// reason so the operator gets a clear diagnostic, not a silent
// open mount.
func appAuthCallback() AppAuthCallback {
	appAuthMu.RLock()
	cb := appAuthCb
	appAuthMu.RUnlock()
	return cb
}

// consoleGateApp is the mount-gate for /console/* under `--auth
// app`. Wired by buildMux when cfg.AuthMode == "app".
//
// Returns true → request proceeds to the console_app sub-app.
// Returns false → response already written (401/403/503), the
// sub-app's handler is skipped.
//
// Failure semantics:
//   - No callback registered at boot → 503 Service Unavailable +
//     `WWW-Authenticate: SkyHubApp realm="sky-hub", reason=
//     auth-app-unregistered"`. Fail closed.
//   - Callback returns (_, false) → 401 + the same SkyHubApp
//     scheme. Indistinguishable from rejection-by-callback so a
//     probe can't tell whether the callback ran.
//   - Callback panics → recover, log via stderr, 401. The
//     ConsoleAuth equivalent in `rt` does the same.
func consoleGateApp(w http.ResponseWriter, r *http.Request) bool {
	cb := appAuthCallback()
	if cb == nil {
		w.Header().Set("WWW-Authenticate", `SkyHubApp realm="sky-hub", reason="auth-app-unregistered"`)
		http.Error(w, "", http.StatusServiceUnavailable)
		return false
	}
	identity, ok := safeInvokeAppAuth(cb, r)
	if !ok {
		w.Header().Set("WWW-Authenticate", `SkyHubApp realm="sky-hub"`)
		http.Error(w, "", http.StatusUnauthorized)
		return false
	}
	// Stash the identity on the request context so downstream
	// store-query kernels can read the tenant claim. Threaded via
	// rt's ConsoleIdentity helpers (same shape as inline mode).
	_ = identity // tenant-claim threading lands in B8b
	return true
}

// safeInvokeAppAuth runs the callback under a defer/recover so a
// bad callback can't crash the hub. Mirrors the rt.runWithRecover
// pattern used by every Ffi kernel.
func safeInvokeAppAuth(cb AppAuthCallback, r *http.Request) (id rt.ConsoleIdentity, ok bool) {
	defer func() {
		if rec := recover(); rec != nil {
			id, ok = rt.ConsoleIdentity{}, false
		}
	}()
	return cb(r)
}
