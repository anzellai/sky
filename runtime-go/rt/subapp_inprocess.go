// subapp_inprocess.go — in-process Sky.Live sub-app mounting (v0.16.1 PR10-B/C).
//
// `MountLiveSubAppInProcess(parentMux, prefix, cfg)` lets a host
// Sky.Live or Sky.Http.Server install a SECOND Sky.Live app on the
// same mux under a path prefix. The sub-app gets its own:
//
//   - session map (cookieName scoped to its own prefix path so the
//     parent app's `sky_sid` and the sub-app's `sky_<sub>_sid` never
//     collide on the same browser origin),
//   - sky-id namespace (so logs / diffs / handler lookups stay
//     unambiguous when both apps render into the same browser tab),
//   - broker / topics (pub/sub stays scoped to the sub-app — parents
//     do NOT share topics with children),
//   - update / view / subscriptions cycle (same Sky.Live machinery,
//     just bound to handlers registered under `prefix`).
//
// What DOES get shared, by design:
//
//   - The process-global `telemetry.Default()` Store. Logs, metrics,
//     and spans from ANY liveApp in the process land in one place
//     — the inline console reads from telemetry.Default() so it sees
//     host + every sub-app's signals in one pane. The optional
//     `service.namespace` label (set via WithSubAppNamespace, see
//     telemetry_namespace.go) lets operators filter per-app.
//
//   - The parent's catch-all `/` route. The sub-app's pages render
//     at `<prefix>/...`, not at `/`. The parent owns the root path
//     because there can only be one catch-all per mux.
//
// What is INTENTIONALLY SKIPPED for sub-apps:
//
//   - MountObservabilityEndpoints (/_sky/healthz, /_sky/readyz,
//     /_sky/metrics, /_sky/buildinfo). The parent owns those. A
//     sub-app's "health" is bundled into the parent's.
//   - MountEmbeddedConsole. Console is parent-only — only ONE
//     console per process. The console READS from
//     telemetry.Default() so sub-apps' signals appear automatically.
//   - SIGINT/SIGTERM handlers + srv.ListenAndServe. No listener;
//     the parent's listener routes through the parent mux to the
//     sub-app's handlers.
//   - AssertConsoleInvariantOrExit. Parent-only invariant.
//
// Telemetry topology, four shapes (see docs/v0.16.x-console/TELEMETRY_FLOW.md):
//
//   1. Single-process, host only — host writes to telemetry.Default();
//      console reads. Simplest case; no sub-apps.
//   2. Single-process, nested in-process sub-apps — host + each
//      sub-app writes to telemetry.Default() with a `service.namespace`
//      label tagged by the WithSubAppNamespace middleware (see
//      telemetry_namespace.go). Console reads aggregated; UI offers
//      a namespace filter pill.
//   3. Same host, multiple processes (MountSubApp fork+exec) — child
//      processes use PushExporter to POST to the parent's
//      /_sky/observability/ingest. Already shipped (v0.14+).
//   4. Distributed: multiple Sky processes on different hosts —
//      HubExporter (v0.16.1 PR4) pushes to a hub. Hub-mode console
//      ships in v0.16.2 (#429).
//
// Cookie + sky-id namespace decisions:
//
//   | Concern              | Host app   | Sub-app (e.g. /billing)
//   |----------------------|------------|--------------------------
//   | Session cookie name  | sky_sid    | sky_billing_sid
//   | sky-id prefix        | r          | sky-billing
//   | Cookie Path          | /          | /billing/
//
// The sub-app's session cookie has `Path=<prefix>/`, so the host
// app's Sky.Live can't read it (browsers scope cookies by Path).
// The sub-app's `sky-<sanitised>` id prefix means any diff /
// handler-lookup log line names a single app unambiguously.
//
// PR10-B + PR10-C of v0.16.1. See
// docs/v0.16.x-console/RFC-v0.16.1-pr10-architecture.md.

package rt

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// liveMountOpts carries the sub-app-specific configuration the
// mount path needs. Centralising the field set here keeps the
// host-vs-sub-app divergence visible at one site.
type liveMountOpts struct {
	// basePath: URL prefix this sub-app is mounted under. Empty for
	// root-mount (the parent itself).
	basePath string

	// isSubApp: when true, the mount path skips listener creation,
	// observability endpoints, console auto-mount, and signal
	// handlers — the parent owns all of those.
	isSubApp bool

	// cookieName: session cookie name. Defaults to "sky_sid" for the
	// host app; sub-apps use `sky_<sanitised(basePath)>_sid`.
	cookieName string

	// skyIDPrefix: sky-id namespace prefix. Defaults to "r" for the
	// host; sub-apps use `sky-<sanitised(basePath)>`.
	skyIDPrefix string

	// authGate: optional pre-handler gate. When set, every route
	// registered for the sub-app first calls authGate(w, r); if it
	// returns false, the response has already been written and the
	// handler is skipped. Used by MountEmbeddedConsole (v0.16.1
	// PR10-F) to keep the bundled console behind ConsoleGate without
	// requiring the gate to live inside every Sky.Live handler.
	authGate func(http.ResponseWriter, *http.Request) bool
}

// sanitiseBasePathForCookie turns a URL prefix into a cookie /
// sky-id-safe identifier. Strips the leading "/" and replaces
// every non-alphanumeric char with an underscore. Edge cases:
//
//   - empty input → "app" (defensive fallback; sub-apps should
//     always pass a non-empty basePath)
//   - leading/trailing slashes stripped
//   - "/" stripped recursively
//   - chars outside [A-Za-z0-9] collapsed to "_"
//
// Examples:
//
//	"/_sky/console" → "_sky_console" → trimmed to "sky_console"
//	"/billing"      → "billing"
//	"/api/v2/jobs"  → "api_v2_jobs"
func sanitiseBasePathForCookie(basePath string) string {
	s := strings.Trim(basePath, "/")
	if s == "" {
		return "app"
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	// Collapse runs of underscores + strip leading/trailing.
	out := b.String()
	for strings.Contains(out, "__") {
		out = strings.ReplaceAll(out, "__", "_")
	}
	out = strings.Trim(out, "_")
	if out == "" {
		return "app"
	}
	return out
}

// inProcessSubApps — registry of mounted in-process sub-apps. Keyed
// on the basePath so a double-mount with the same prefix can be
// rejected cleanly (Go's ServeMux would panic, but we want a
// friendly error). Read-locked on lookup; write-locked on register.
//
// Lifetime: process-bound. Sub-apps don't deregister — the program
// exits when its listener returns.
var (
	inProcessSubAppsMu sync.RWMutex
	inProcessSubApps   = map[string]*liveApp{}
)

// LookupInProcessSubApp returns the *liveApp registered at `prefix`,
// or nil when no sub-app has been mounted there. Exported for tests
// and for the cross-process MountSubApp shim (which uses it to
// detect "same-process child" topologies).
func LookupInProcessSubApp(prefix string) *liveApp {
	p := normaliseBasePath(prefix)
	inProcessSubAppsMu.RLock()
	defer inProcessSubAppsMu.RUnlock()
	return inProcessSubApps[p]
}

// inProcessSubAppMatch — atomic snapshot of all currently-mounted
// in-process sub-app prefixes, used by the request middleware
// (WithSubAppNamespace) to tag every incoming request with the
// correct `service.namespace` label. Updated under
// inProcessSubAppsMu, but read lock-free via atomic.Pointer for
// the hot dispatch path.
type inProcessSubAppRoute struct {
	prefix    string
	namespace string
}

var inProcessSubAppRoutes atomic.Pointer[[]inProcessSubAppRoute]

func snapshotInProcessSubAppRoutes() []inProcessSubAppRoute {
	p := inProcessSubAppRoutes.Load()
	if p == nil {
		return nil
	}
	return *p
}

// rebuildInProcessSubAppRoutes refreshes the atomic snapshot. Caller
// MUST hold inProcessSubAppsMu (read or write — we copy the map).
func rebuildInProcessSubAppRoutes() {
	out := make([]inProcessSubAppRoute, 0, len(inProcessSubApps))
	for prefix := range inProcessSubApps {
		out = append(out, inProcessSubAppRoute{
			prefix:    prefix,
			namespace: prefix, // namespace == URL prefix by default
		})
	}
	// Longest-prefix first so "/billing/v2" wins over "/billing".
	longestFirst(out)
	inProcessSubAppRoutes.Store(&out)
}

// longestFirst sorts by prefix length descending. Tiny manual sort
// to avoid pulling sort just for this.
func longestFirst(rs []inProcessSubAppRoute) {
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && len(rs[j].prefix) > len(rs[j-1].prefix); j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
}

// MountLiveSubAppInProcess mounts a Sky.Live app as a same-process
// sub-app on the parent's mux under `prefix`. Returns the new
// *liveApp so callers can attach lifecycle hooks (rare — most
// callers ignore the return).
//
// Each sub-app gets its own:
//
//   - session map (the sub-app's cookieName scopes the browser
//     cookie to its prefix Path)
//   - sky-id namespace (per-app prefix derived from basePath)
//   - broker / topics (pub/sub is per-app)
//   - init / update / view cycle
//
// Process-global state is shared:
//
//   - telemetry.Default() so the console sees aggregated signals
//
// Panics on double-mount at the same prefix (Go's ServeMux would
// panic anyway; we catch it earlier with a clearer message).
//
// `prefix` MUST start with "/" and NOT end with "/". The function
// normalises via normaliseBasePath (handles "//x" or "/x/" defensively).
//
// `cfg` is a Sky-side `Live.app` cfg value (the same record shape
// that `Live_app` consumes). Field lookup goes through `Field(cfg, ...)`.
//
// Example (Go-side wiring):
//
//	mux := http.NewServeMux()
//	rt.MountLiveSubAppInProcess(mux, "/billing", billingCfg)
//	rt.MountLiveSubAppInProcess(mux, "/jobs", jobsCfg)
//	rt.Live_app(hostCfg)  // host owns the catch-all + listener
//
// PR10-C (v0.16.1).
func MountLiveSubAppInProcess(parentMux *http.ServeMux, prefix string, cfg any) *liveApp {
	return mountLiveSubAppInProcessWithGate(parentMux, prefix, cfg, nil)
}

// MountLiveSubAppInProcessWithGate is the auth-gated variant of
// MountLiveSubAppInProcess. The `gate` callback runs BEFORE every
// route handler the sub-app installs (handleEvent / handleSSE /
// handleConfig / handleInitial / static files). Returning false from
// the gate skips the handler — the gate is expected to have already
// written the response (401 / 403 / 503 / etc).
//
// Use case: rt.MountEmbeddedConsole wraps the bundled console behind
// rt.ConsoleGate (token cookie / app callback / production gate). The
// gate is per-app rather than per-route because a sub-app's HTTP
// surface is uniform — if you can hit GET /_sky/console/, you should
// also be able to POST /_sky/console/_sky/event.
//
// PR10-F (v0.16.1).
func MountLiveSubAppInProcessWithGate(
	parentMux *http.ServeMux,
	prefix string,
	cfg any,
	gate func(http.ResponseWriter, *http.Request) bool,
) *liveApp {
	return mountLiveSubAppInProcessWithGate(parentMux, prefix, cfg, gate)
}

func mountLiveSubAppInProcessWithGate(
	parentMux *http.ServeMux,
	prefix string,
	cfg any,
	gate func(http.ResponseWriter, *http.Request) bool,
) *liveApp {
	if parentMux == nil {
		panic("rt.MountLiveSubAppInProcess: parentMux is nil")
	}
	prefix = normaliseBasePath(prefix)
	if prefix == "" {
		panic("rt.MountLiveSubAppInProcess: prefix must be non-empty (use Live_app for root-mounted host apps)")
	}

	inProcessSubAppsMu.Lock()
	if existing, ok := inProcessSubApps[prefix]; ok {
		inProcessSubAppsMu.Unlock()
		panic(fmt.Sprintf("rt.MountLiveSubAppInProcess: sub-app already mounted at %q (existing: %p)", prefix, existing))
	}
	// Reserve the slot eagerly so a re-entrant call from
	// init/update during construction (unlikely but possible) doesn't
	// double-mount.
	inProcessSubApps[prefix] = nil
	inProcessSubAppsMu.Unlock()

	sanitised := sanitiseBasePathForCookie(prefix)
	opts := liveMountOpts{
		basePath:    prefix,
		isSubApp:    true,
		cookieName:  "sky_" + sanitised + "_sid",
		skyIDPrefix: "sky-" + sanitised,
		authGate:    gate,
	}

	app := newLiveAppFromCfg(cfg, opts)

	// Register routes under the prefix. Go's ServeMux longest-prefix
	// matching ensures the parent's catch-all "/" sees them last.
	registerSubAppRoutes(parentMux, app, prefix, gate)

	// Commit to the registry. Once visible, namespace propagation
	// (telemetry_namespace.go's middleware) starts tagging incoming
	// requests under this prefix.
	inProcessSubAppsMu.Lock()
	inProcessSubApps[prefix] = app
	rebuildInProcessSubAppRoutes()
	inProcessSubAppsMu.Unlock()

	return app
}

// newLiveAppFromCfg builds an in-process *liveApp from a Sky cfg
// value. Mirrors the construction half of liveAppRun, minus the
// listener / signal-handler / console-mount / observability-mount
// chunks (those are host-app only).
//
// Returns the *liveApp ready to have routes registered against its
// handler methods.
func newLiveAppFromCfg(cfg any, opts liveMountOpts) *liveApp {
	app := &liveApp{
		init:          Field(cfg, "Init"),
		update:        Field(cfg, "Update"),
		view:          Field(cfg, "View"),
		subscriptions: Field(cfg, "Subscriptions"),
		notFound:      Field(cfg, "NotFound"),
		guard:         Field(cfg, "Guard"),
		head:          Field(cfg, "Head"),
		consoleAuth:   Field(cfg, "ConsoleAuth"),
		locker:        newSessionLocker(),
		msgTags:       make(map[string]int),
		bannerCfg:     resolveBannerStrings(loadLiveBannerConfig(), cfg),
		basePath:      opts.basePath,
		cookieName:    opts.cookieName,
		skyIDPrefix:   opts.skyIDPrefix,
	}
	for _, r := range asList(Field(cfg, "Routes")) {
		if lr, ok := r.(liveRoute); ok {
			app.routes = append(app.routes, lr)
		}
	}
	for _, r := range asList(Field(cfg, "Api")) {
		if ar, ok := r.(apiRoute); ok {
			app.api = append(app.api, ar)
		}
	}
	// Static dir — same precedence as liveAppRun.
	if sd := Field(cfg, "Static"); sd != nil {
		app.staticDir = fmt.Sprintf("%v", sd)
	}
	app.staticURL = "/static"
	if su := Field(cfg, "StaticUrl"); su != nil {
		if s := fmt.Sprintf("%v", su); s != "" {
			app.staticURL = s
		}
	}
	storeKind := stringField(cfg, "Store")
	storePath := stringField(cfg, "StorePath")
	ttl := parseTTL(skyGetenv("LIVE_TTL"), stringField(cfg, "Ttl"), defaultSubAppSessionTTL())
	app.store = chooseStore(storeKind, storePath, ttl)
	app.sessionTTL = ttl
	app.topics = app.store.Broker()

	// Pre-register model types with gob — same defensive pre-walk as
	// the host-app path so DB-backed session stores can decode existing
	// sub-app sessions on restart.
	func() {
		defer func() { recover() }()
		req := map[string]any{"path": opts.basePath, "query": ""}
		res := sky_call(app.init, req)
		model := tupleFirst(res)
		GobRegisterTypeGraph(reflect.TypeOf(model))
		gobRegisterAll(model)
	}()
	return app
}

// defaultSubAppSessionTTL — sub-apps default to the same 30m as the
// host. Pulled out so a future env knob (SKY_LIVE_SUBAPP_TTL) can
// hook here without touching newLiveAppFromCfg.
func defaultSubAppSessionTTL() time.Duration {
	return 30 * time.Minute
}

// registerSubAppRoutes wires `app`'s handler methods onto the parent
// mux under `prefix`. Each Sky.Live framework path becomes
// `<prefix>/_sky/event`, `<prefix>/_sky/sse`, `<prefix>/_sky/config`.
// The sub-app's catch-all "page handler" goes at `<prefix>/`.
//
// Important: the parent's `/_sky/event` etc. + the sub-app's
// `<prefix>/_sky/event` are DIFFERENT pattern strings on the same
// ServeMux. Go's longest-prefix matching ensures no cross-talk.
//
// When `gate` is non-nil, every registered handler is wrapped in a
// shim that calls gate(w, r) FIRST. Returning false from gate
// short-circuits (gate already wrote the response). Used by
// MountEmbeddedConsole (v0.16.1 PR10-F) to enforce ConsoleGate
// across every console route uniformly.
func registerSubAppRoutes(
	parentMux *http.ServeMux,
	app *liveApp,
	prefix string,
	gate func(http.ResponseWriter, *http.Request) bool,
) {
	wrap := func(h http.HandlerFunc) http.HandlerFunc {
		if gate == nil {
			return h
		}
		return func(w http.ResponseWriter, r *http.Request) {
			if !gate(w, r) {
				return
			}
			h(w, r)
		}
	}
	parentMux.HandleFunc(prefix+"/_sky/event", wrap(app.handleEvent))
	parentMux.HandleFunc(prefix+"/_sky/sse", wrap(app.handleSSE))
	parentMux.HandleFunc(prefix+"/_sky/config", wrap(app.handleConfig))
	// Static files for the sub-app (if configured).
	if app.staticDir != "" {
		sp := prefix + app.staticURL
		if !strings.HasSuffix(sp, "/") {
			sp += "/"
		}
		fileHandler := http.StripPrefix(sp, http.FileServer(http.Dir(app.staticDir)))
		if gate == nil {
			parentMux.Handle(sp, fileHandler)
		} else {
			parentMux.HandleFunc(sp, func(w http.ResponseWriter, r *http.Request) {
				if !gate(w, r) {
					return
				}
				fileHandler.ServeHTTP(w, r)
			})
		}
	}
	// The sub-app's "render this page" catch-all. Sub-app's
	// dispatchRoot inspects r.URL.Path against its routes; the host
	// catches everything else via its own catch-all "/".
	parentMux.HandleFunc(prefix+"/", wrap(app.dispatchRoot))
	// Bare prefix without trailing "/" should redirect to the
	// trailing-slash form so relative URLs in the sub-app's HTML
	// resolve correctly.
	parentMux.HandleFunc(prefix, wrap(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == prefix {
			http.Redirect(w, r, prefix+"/", http.StatusFound)
			return
		}
		app.dispatchRoot(w, r)
	}))
}
