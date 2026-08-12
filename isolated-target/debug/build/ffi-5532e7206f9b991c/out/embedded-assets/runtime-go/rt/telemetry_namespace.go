// telemetry_namespace.go — `service.namespace` context propagation (v0.16.1 PR10-D).
//
// When multiple Sky.Live apps run in the same process (one host via
// Live_app + N sub-apps via MountLiveSubAppInProcess), every Log /
// Metric / Span needs a label identifying which app produced it.
// Otherwise the console aggregates everything into one undifferentiated
// soup.
//
// The mechanism:
//
//  1. Every incoming HTTP request lands at the parent mux.
//  2. The ObservabilityMiddleware (live.go) wraps each request in
//     WithSubAppNamespace, which inspects r.URL.Path against the
//     in-process sub-app prefix table (snapshotInProcessSubAppRoutes()).
//  3. If the path matches a sub-app prefix, the request's context is
//     stamped with that prefix as the namespace label.
//  4. Telemetry sites (Log_info / Log_warn / spans) read the namespace
//     from the active request context and attach `service.namespace=<v>`
//     to every emission.
//
// The host app's signals end up with `service.namespace=""` (empty
// string, which the console UI renders as "host"). Sub-apps get their
// prefix (e.g. `/billing`, `/jobs`, `/_sky/console`).
//
// Backward compat: code that doesn't go through the middleware
// (background goroutines, deferred Cmd.perform tasks, scheduled jobs)
// gets the empty namespace by default. Sky-side code can explicitly
// propagate via `Std.Trace.span` (which carries the context).
//
// v0.16.1 PR10-D. Companion to subapp_inprocess.go.

package rt

import (
	"context"
	"net/http"
	"strings"
)

// namespaceContextKey is the unexported key used to stash the
// resolved namespace in a request's context. Reads happen via
// NamespaceFromContext.
type namespaceContextKey struct{}

// WithNamespace returns a new context carrying `namespace`. Used by
// the request middleware + Sky-side `Std.Trace.span` (when v0.17
// surfaces a context-passing API).
func WithNamespace(ctx context.Context, namespace string) context.Context {
	return context.WithValue(ctx, namespaceContextKey{}, namespace)
}

// NamespaceFromContext extracts the `service.namespace` label from
// the request context. Returns "" when no namespace is set.
func NamespaceFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(namespaceContextKey{}).(string)
	return v
}

// WithSubAppNamespace wraps `next` so every incoming request's
// context carries the resolved namespace. The lookup is O(N) over
// the snapshot of mounted sub-apps (typically N is 1–3 so this is
// trivially fast even on the hot path).
//
// Path matching uses the longest-prefix-wins ordering established
// by rebuildInProcessSubAppRoutes() — so a request to
// `/_sky/console/_sky/event` correctly resolves to the
// `/_sky/console` namespace, not to the host's empty one.
//
// Apps that don't use MountLiveSubAppInProcess pay zero cost: when
// snapshotInProcessSubAppRoutes returns an empty slice (no sub-apps
// mounted), the loop is skipped and the request's context passes
// through unchanged.
//
// Mounted by ObservabilityMiddleware in live.go.
func WithSubAppNamespace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routes := snapshotInProcessSubAppRoutes()
		if len(routes) > 0 {
			path := r.URL.Path
			for _, rt := range routes {
				if path == rt.prefix || strings.HasPrefix(path, rt.prefix+"/") {
					r = r.WithContext(WithNamespace(r.Context(), rt.namespace))
					break
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// activeRequestNamespace — request-scoped namespace stamp. Reads
// happen from telemetry call sites that DON'T have access to the
// http.Request directly (e.g. Std.Log.* called from inside an update
// reducer). We thread the value through the per-goroutine processing
// chain via the request's context — but the canonical reader is
// `NamespaceFromContext(r.Context())` where r is in scope.
//
// For now this file exposes the building blocks; subsequent PRs in
// the v0.16.x cycle wire individual telemetry sites to use them.
// Even before those sites are wired, the middleware tagging the
// context is a no-op for telemetry but DOES surface the namespace
// on the request itself — useful for downstream debugging via
// X-Sky-Namespace response header (see live.go's
// ObservabilityMiddleware for the header wiring).
