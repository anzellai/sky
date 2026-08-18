// Package rt — Sky.Live typed-builder config kernels (v0.19 Path A).
//
// `Live.app` used to take a row-open record literal; v0.19 replaces that
// with a typed BUILDER: `Live.config { init, update, view, subscriptions,
// routes, notFound }` produces an opaque `AppConfig`, and optional fields
// are attached with `Live.withHead` / `withGuard` / `withStatic` / … The
// stdlib surface is sky-stdlib/Std/Live.sky; these are the runtime kernels
// the Ffi.kernel aliases route to.
//
// The built object is a `map[string]any` whose keys are EXACTLY the
// PascalCase field names `liveAppRun` reads via `rt.Field(cfg,"…")`
// (live.go). `rt.Field` reads a map by exact key (rt.go), so `Live_app`
// consumes a builder-produced object byte-identically to the pre-v0.19
// lowered Sky-record struct — `Live_app`/`liveAppRun` are UNCHANGED.
//
// FOUR INVARIANTS (each load-bearing for soundness — see
// docs/v0.19/kernel-metadata-unification.md):
//
//  1. Keys are the exact PascalCase names `liveAppRun` reads; values are
//     `any`. An UNSET optional is ABSENT from the map, so `rt.Field`
//     returns untyped nil and every `if X := Field(cfg,"…"); X != nil`
//     gate in liveAppRun stays false — never store a typed-nil.
//  2. `Live_withX` STORE the callback verbatim. They never assert it to a
//     Go func type (that is the db_auth.go "body is not a function" defect
//     class); invocation stays on the existing dispatch in liveAppRun.
//  3. `liveCfgSet` SHALLOW-CLONES the map before setting, so sibling
//     derivations (`withHead h base` vs `withPort 9000 base`) never alias
//     one base map (Go maps are reference types).
//  4. Nested sub-records (Analytics, Status) are stored as their own
//     Field-readable value (the caller's Sky record), read the same way
//     liveAppRun already reads them.
package rt

// Live_config builds the opaque AppConfig from the six required fields.
// `req` is the Sky record `{ init, update, view, subscriptions, routes,
// notFound }` (a Go struct); we read each via rt.Field and materialise a
// fresh map[string]any so the `withX` builders can attach optionals.
func Live_config(req any) any {
	return map[string]any{
		"Init":          Field(req, "Init"),
		"Update":        Field(req, "Update"),
		"View":          Field(req, "View"),
		"Subscriptions": Field(req, "Subscriptions"),
		"Routes":        Field(req, "Routes"),
		"NotFound":      Field(req, "NotFound"),
	}
}

// liveCfgSet returns a shallow clone of the AppConfig map with key=val set
// (invariants 1–3). Accepts a map (the normal case) or, defensively, a raw
// record — in which case it first lifts it through Live_config's key set by
// copying every readable field it can see; but in practice `config` always
// runs first so cfg is already a map.
func liveCfgSet(cfg any, key string, val any) any {
	src, ok := cfg.(map[string]any)
	if !ok {
		// Defensive: someone passed a non-map AppConfig. Start from an
		// empty map keyed off nothing rather than panic; the required
		// fields would already have been copied by Live_config in the
		// normal builder chain.
		src = map[string]any{}
	}
	out := make(map[string]any, len(src)+1)
	for k, v := range src {
		out[k] = v
	}
	out[key] = val
	return out
}

// ── Optional-field builders. Each stores its value verbatim (invariant 2)
//    under the exact key liveAppRun reads. Signatures in Std/Live.sky are
//    `withX : <T> -> AppConfig model msg -> AppConfig model msg`, so the
//    Ffi.kernel dispatch passes (value, cfg). ──────────────────────────

// Live_withHead — `head : model -> List (Html msg)` per-page <head> injection.
func Live_withHead(fn, cfg any) any { return liveCfgSet(cfg, "Head", fn) }

// Live_withConsoleAuth — `consoleAuth : Request -> Task Error (Maybe Identity)`.
func Live_withConsoleAuth(fn, cfg any) any { return liveCfgSet(cfg, "ConsoleAuth", fn) }

// Live_withOnNavigate — `onNavigate : String -> msg` navigation hook.
func Live_withOnNavigate(fn, cfg any) any { return liveCfgSet(cfg, "OnNavigate", fn) }

// Live_withGuard — `guard : msg -> model -> Result Error ()` per-Msg gate.
func Live_withGuard(fn, cfg any) any { return liveCfgSet(cfg, "Guard", fn) }

// Live_withStatic — `static : String` directory served at /static.
func Live_withStatic(dir, cfg any) any { return liveCfgSet(cfg, "Static", dir) }

// Live_withStaticUrl — `staticUrl : String` mount path override.
func Live_withStaticUrl(url, cfg any) any { return liveCfgSet(cfg, "StaticUrl", url) }

// Live_withPort — `port : Int` listen port. An OPERATOR-set
// <PREFIX>_LIVE_PORT still wins; the sky.toml default that generated init()
// seeds into that same variable does not. See resolveLivePort in live.go.
func Live_withPort(port, cfg any) any { return liveCfgSet(cfg, "Port", port) }

// Live_withStore — `store : String` session store kind (env still wins).
func Live_withStore(store, cfg any) any { return liveCfgSet(cfg, "Store", store) }

// Live_withStorePath — `storePath : String` session store path/URL.
func Live_withStorePath(path, cfg any) any { return liveCfgSet(cfg, "StorePath", path) }

// Live_withTtl — `ttl : String` session TTL ("30m", "24h", or bare seconds).
func Live_withTtl(ttl, cfg any) any { return liveCfgSet(cfg, "Ttl", ttl) }

// Live_withIdleEvict — `idleEvict : String` tiered-session-cache idle-evict
// window ("5m", "0"/"off" to disable; env `SKY_LIVE_IDLE_EVICT` wins). After
// this idle window with NO active SSE connection, a durable store (sqlite /
// postgres / redis) drops the session's live pointer from its in-RAM memCache
// while keeping the blob on disk to the full TTL, resurrecting on next access
// — so RAM tracks the ACTIVE working set, not all-within-TTL. Ignored by the
// memory store (no disk backing). See docs/skylive/tiered-session-cache.md.
func Live_withIdleEvict(idleEvict, cfg any) any { return liveCfgSet(cfg, "IdleEvict", idleEvict) }

// Live_withMaxBodyBytes — `maxBodyBytes : Int` upper bound on a single TEA
// event request body (env `SKY_LIVE_MAX_BODY_BYTES` wins). Resolved through the
// same `configLayers` order as the other four; see resolveMaxBodyBytes.
func Live_withMaxBodyBytes(n, cfg any) any { return liveCfgSet(cfg, "MaxBodyBytes", n) }

// Live_withInput — `input : String` input-report mode ("debounce" | "blur";
// env `SKY_LIVE_INPUT_MODE` wins). See resolveInputMode.
func Live_withInput(mode, cfg any) any { return liveCfgSet(cfg, "Input", mode) }

// Live_withAnalytics — `analytics : { pageViews : Bool }` (invariant 4:
// the record is stored verbatim and read via Field(a,"PageViews")).
func Live_withAnalytics(a, cfg any) any { return liveCfgSet(cfg, "Analytics", a) }

// Live_withAnalyticsIdentify — `identify : model -> Maybe String`, consulted on
// each auto page-view to attribute an already-authenticated session (invariant 4:
// the closure is stored verbatim and read via Field(cfg,"AnalyticsIdentify")).
func Live_withAnalyticsIdentify(f, cfg any) any {
	return liveCfgSet(cfg, "AnalyticsIdentify", f)
}

// Live_withStatus — `status : { reconnecting : String, offline : String }`
// connection-banner string overrides (invariant 4).
func Live_withStatus(status, cfg any) any { return liveCfgSet(cfg, "Status", status) }

// Live_withAuthSliding — opt into rolling JWT re-issue (invariant 4: the record
// — including its `revokedCheck : Maybe (String -> Task Error Bool)` closure —
// is stored verbatim under "AuthSliding" and parsed by SetAuthSlidingConfig in
// liveAppRun). Registers the AuthSlidingMiddleware. See auth_sliding.go.
//   `{ cookie : String, secretEnv : String, sameSite : String
//    , revokedCheck : Maybe (String -> Task Error Bool) }`
func Live_withAuthSliding(rec, cfg any) any { return liveCfgSet(cfg, "AuthSliding", rec) }
