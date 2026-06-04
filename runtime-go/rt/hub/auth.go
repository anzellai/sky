package hub

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// authMiddleware wraps an OTLP receiver handler with bearer-token
// auth gated by HubConfig.AuthMode.
//
//	mode=off    → no check
//	mode=token  → constant-time compare against cfg.Token
//	mode=app    → not implemented in v0.16.4 Chunk 2 (returns 501);
//	              Chunk 7 lands the Sky.Live consoleAuth integration
//
// The constant-time compare prevents timing-side-channel token leak
// (Std.Auth's verifyToken uses the same primitive).
//
// 401 response body is intentionally empty — never echo back what
// the caller sent so a probe can't differentiate "missing header" /
// "wrong token" / "wrong scheme".
func authMiddleware(cfg HubConfig, next http.Handler) http.Handler {
	switch cfg.AuthMode {
	case "off":
		return next
	case "app":
		// Reject early — token-mode operators see a clear 501 if
		// they typo the mode in their startup script.
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "auth=app not implemented in v0.16.4", http.StatusNotImplemented)
		})
	}
	// token mode
	expected := cfg.Token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := bearerToken(r.Header.Get("Authorization"))
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="sky-hub"`)
			http.Error(w, "", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerToken extracts the credential portion of "Bearer <token>".
// Returns "" when the header is absent, malformed, or uses a
// different scheme.
func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		// Case-insensitive fallback for "bearer " — RFC 7235 §2.1
		// says the scheme is case-insensitive.
		if len(header) >= len(prefix) &&
			strings.EqualFold(header[:len(prefix)], prefix) {
			return strings.TrimSpace(header[len(prefix):])
		}
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}
