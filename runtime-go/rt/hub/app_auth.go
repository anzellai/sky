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
	"context"
	"net/http"
	"sync"

	rt "sky-app/rt"
)

// IdentityFromContext re-exports rt.IdentityFromContext so existing
// hub package callers (and external code that already imports
// `hub.IdentityFromContext`) keep working after v0.16.5 moved the
// canonical implementation to package rt.
//
// The move was needed so Sky.Live's session-mint code in rt can read
// identity from the request context — hub can't import rt's
// internals, but rt can own the bridge helpers used by both sides.
//
// Going forward, prefer `rt.IdentityFromContext` directly.
func IdentityFromContext(ctx context.Context) (rt.ConsoleIdentity, bool) {
	return rt.IdentityFromContext(ctx)
}

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
	// Stash identity on the request context so downstream handlers
	// (the bundled console_app's Hub_* kernels via hub_bridge.go,
	// AND Sky.Live's session-mint code in rt.dispatchRoot) can read
	// identity.Claims["tenant"]. We mutate *r in place because the
	// surrounding `MountLiveSubAppInProcessWithGate` doesn't allow
	// the gate to return a modified request — the *r = *r.WithContext
	// idiom is the standard Go middleware pattern for this case.
	//
	// v0.16.5 #493: rt owns the key now (rt.IdentityContextKey) so
	// session-mint can read what we write here.
	ctx := context.WithValue(r.Context(), rt.IdentityContextKey, identity)
	*r = *r.WithContext(ctx)
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
