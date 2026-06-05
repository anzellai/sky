// v0.16.5 #493 — session identity threading for Sky.Live sub-apps.
//
// Background: when a Sky.Live sub-app is mounted behind an auth gate
// (e.g. rt.MountLiveSubAppInProcessWithGate + a custom callback that
// validates a JWT cookie), the gate has the authenticated `Identity`
// at HTTP-handler time but the Sky-side `init`/`update`/`view` don't.
// Sky.Live's TEA contract today is `init : () -> (Model, Cmd Msg)` —
// no Request, no cookies, no auth.
//
// This file is the bridge:
//
//   1. The gate writes the identity to r.Context() via
//      `r.WithContext(context.WithValue(ctx, IdentityContextKey, id))`.
//   2. Sky.Live's session-mint code (dispatchRoot's else-branch) reads
//      it back via `IdentityFromContext(r.Context())` and stashes it
//      on the new liveSession.
//   3. Kernels that need it (notably the hub's `Hub_currentIdentity`)
//      reach it via `currentLiveSession().Identity`.
//
// Persistence: the `Identity` field is included in `storableSession`
// so DB-backed session stores round-trip identity across restarts and
// replicas. See encodeSession / decodeSession in live_store.go.
//
// Generic by design — this isn't hub-specific. Any Sky.Live app
// running behind a custom auth gate can use the same machinery.

package rt

import "context"

// identityContextKey is the private context key used to thread
// ConsoleIdentity through the gate → session-mint handshake.
// Private type prevents cross-package collisions.
type identityContextKey struct{}

// IdentityContextKey is the exported key used by gate callbacks to
// write identity to r.Context(). Callers pattern:
//
//	*r = *r.WithContext(context.WithValue(r.Context(), rt.IdentityContextKey, id))
//
// Use the IdentityFromContext helper for reads — it does the type
// assertion and zero-value fallback.
var IdentityContextKey = identityContextKey{}

// IdentityFromContext returns the ConsoleIdentity attached to ctx by
// a gate callback, or the zero ConsoleIdentity and false when no
// identity is present (auth=off, gate didn't run, anonymous path).
//
// Used both inside this package (session-mint) and externally (the
// hub's kernel handlers).
func IdentityFromContext(ctx context.Context) (ConsoleIdentity, bool) {
	if ctx == nil {
		return ConsoleIdentity{}, false
	}
	v := ctx.Value(IdentityContextKey)
	if v == nil {
		return ConsoleIdentity{}, false
	}
	id, ok := v.(ConsoleIdentity)
	return id, ok
}

// SessionIdentity returns the identity stamped on a liveSession at
// mint time. The zero ConsoleIdentity and false when none was set
// (no gate, gate ran but didn't write identity).
//
// Pairs with `currentLiveSession()` for the canonical kernel
// pattern:
//
//	if sess := currentLiveSession(); sess != nil {
//	    if id, ok := SessionIdentity(sess); ok {
//	        tenant := id.Claims["tenant"]
//	        // ... filter query
//	    }
//	}
func SessionIdentity(sess *liveSession) (ConsoleIdentity, bool) {
	if sess == nil {
		return ConsoleIdentity{}, false
	}
	// IdentityValid is false until the gate populates it — distinguishes
	// "no auth ran" from "auth ran and the user has no claims" (both
	// would otherwise look like the zero ConsoleIdentity).
	if !sess.identityValid {
		return ConsoleIdentity{}, false
	}
	return sess.identity, true
}
