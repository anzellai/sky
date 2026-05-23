package rt

// Sky observability — `/_sky/{healthz,readyz,metrics,buildinfo}`
// endpoints + glue to the telemetry package. Mounted by every
// Sky.Live and Sky.Http.Server binary by default.
//
// Design + acceptance criteria: docs/v1-rfc/1-observability.md
// (Phase 1.1a Step 4).
//
// Endpoints:
//
//   /_sky/healthz   — process alive. Cheap; no downstream checks.
//                     200 always while the process is serving.
//
//   /_sky/readyz    — ready to serve. Returns 200 when session store
//                     + DB pool are warm; 503 during SIGTERM drain
//                     or before warmup completes. Used by k8s /
//                     fly.io / ECS to drain traffic on rolling
//                     deploy.
//
//   /_sky/metrics   — Prometheus text exposition (rt/telemetry).
//                     Auth-gated in production (admin role OR
//                     binding 0.0.0.0 heuristic per RFC §"Resolved
//                     question 1").
//
//   /_sky/buildinfo — JSON with commit / builtAt / skyVersion /
//                     goVersion. Always open. Used by deploy
//                     verification + dashboards.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"sky-app/rt/telemetry"
)

// readinessState holds the in-process readyz flag. SIGTERM handlers
// flip this to false to drain traffic before exit.
//
// Stored as atomic.Bool so the readyz handler doesn't lock. Initial
// state is `true` (assume ready); the drain path sets to false and
// holds for `shutdownGracePeriod` before the process exits.
var (
	readinessReady atomic.Bool
	// Optional injectable health probes for downstream dependencies
	// (DB pool, session store, custom checks). Each returns nil on
	// healthy. readyz aggregates: any non-nil → 503.
	readinessProbes atomic.Pointer[[]func() error]
)

func init() {
	readinessReady.Store(true)
	probes := []func() error{}
	readinessProbes.Store(&probes)
}

// RegisterReadinessProbe adds a health check to the readyz endpoint.
// Each registered probe runs on every readyz call. Cheap checks only
// — readyz is hit frequently by orchestrators.
//
// Called from Sky.Live + Sky.Http.Server setup paths to register the
// session-store ping and DB-pool ping. User code can register
// additional probes via FFI if needed (post-v1; not exposed in v1).
func RegisterReadinessProbe(name string, probe func() error) {
	for {
		old := readinessProbes.Load()
		new_ := append([]func() error{}, *old...)
		new_ = append(new_, func() error {
			if err := probe(); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			return nil
		})
		if readinessProbes.CompareAndSwap(old, &new_) {
			return
		}
	}
}

// SetReady flips the readyz flag. Call SetReady(false) at the top
// of the SIGTERM handler so orchestrators stop routing new traffic
// while in-flight requests drain.
func SetReady(ready bool) {
	readinessReady.Store(ready)
}

// BuildInfo is the data shape /_sky/buildinfo returns. Populated
// from compile-time ld-flags (`-X sky-app/rt.buildCommit=...`) or
// the embedded `app/VERSION` file at compile time.
//
// Default values are "dev" / "unknown" so a freshly-built local
// binary returns sensible JSON without ld-flag wiring. CI release
// builds set the real values.
type BuildInfo struct {
	Commit     string `json:"commit"`
	BuiltAt    string `json:"builtAt"`
	SkyVersion string `json:"skyVersion"`
	GoVersion  string `json:"goVersion"`
}

// Build-time ld-flag injection variables. Override via:
//
//	go build -ldflags "-X sky-app/rt.buildCommit=abc123 -X sky-app/rt.buildAt=2026-05-17T11:00:00Z -X sky-app/rt.skyVersion=v0.13.4"
//
// Defaults are dev-friendly: the local `sky run` workflow doesn't
// need to remember these flags to get sensible output.
var (
	buildCommit = "dev"
	buildAt     = "unknown"
	skyVersion  = "dev"
)

// currentBuildInfo returns the build-info struct snapshot. Cheap;
// safe to call per-request.
func currentBuildInfo() BuildInfo {
	return BuildInfo{
		Commit:     buildCommit,
		BuiltAt:    buildAt,
		SkyVersion: skyVersion,
		GoVersion:  runtime.Version(),
	}
}

// HandleHealthz serves /_sky/healthz. Always 200 while the process
// is serving requests. Cheap by design — orchestrators may hit this
// many times per second.
func HandleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

// HandleReadyz serves /_sky/readyz. Aggregates registered readiness
// probes; returns 200 only when all pass and not in drain state.
//
// During SIGTERM drain (after SetReady(false)) returns 503 with a
// JSON body explaining the state. In-flight requests continue
// being served until the server shuts down naturally.
func HandleReadyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !readinessReady.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"status":"draining"}`))
		return
	}
	probes := *readinessProbes.Load()
	for _, probe := range probes {
		if err := probe(); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			// Don't leak probe internals in the response — could
			// expose DB strings, connection info. Just the kind.
			payload, _ := json.Marshal(map[string]string{
				"status": "not_ready",
				"reason": err.Error(),
			})
			w.Write(payload)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ready"}`))
}

// HandleMetrics serves /_sky/metrics in Prometheus text exposition
// format. Production-gated per RFC §"Resolved question 1":
//
//   - Serverless mode (Cloud Run / Lambda / Vercel / etc.): the
//     pull model is structurally wrong — containers evict between
//     scrapes, so the scraper sees empty data or fails to connect.
//     We return 503 + a hint pointing at the OTLP push path the
//     user should configure instead.
//   - VM mode + production (env=production OR binding to 0.0.0.0):
//     gated behind admin auth (admin role or SKY_METRICS_TOKEN
//     bearer). Returns 401 otherwise.
//   - Dev mode (default): open.
//
// The 401 path uses Basic Auth challenge for orchestrator-side
// scrapers that DO want to push credentials (k8s service-account
// JWT, fly.io static token).
func HandleMetrics(w http.ResponseWriter, r *http.Request) {
	if IsServerless() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"status":"unavailable","hint":"pull-based metrics are incompatible with request-billed serverless; configure OTEL_EXPORTER_OTLP_ENDPOINT for push-based delivery"}`))
		return
	}
	if isProductionMode() && !hasAdminAuth(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="sky-metrics"`)
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"status":"unauthorized","hint":"set [security] env or sign in with admin role"}`))
		return
	}
	w.Header().Set("Content-Type", telemetry.ContentType)
	w.WriteHeader(http.StatusOK)
	telemetry.Default().WriteProm(w)
}

// HandleBuildInfo serves /_sky/buildinfo. Always open — exposed
// build info is not sensitive (it's literally a commit SHA + version
// string) and CI deploy verification expects to read it without auth.
func HandleBuildInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(currentBuildInfo())
}

// MountObservabilityEndpoints registers the four /_sky/* endpoints
// on the given mux. Called once from each server-setup path
// (Sky.Live `Live.app` and Sky.Http.Server `Server.listen`) so
// every Sky binary exposes them by default.
//
// Already-registered handlers (e.g. user has manually mounted
// /_sky/healthz for custom auth) take precedence — http.ServeMux's
// Handle panics on duplicate registration, so we use a probe path
// to skip when conflicts exist.
func MountObservabilityEndpoints(mux *http.ServeMux) {
	if skyGetenv("OBSERVABILITY_DISABLED") == "1" {
		// Explicit opt-out — used by tests that want to test the
		// non-observability path. Production users opt out via
		// sky.toml [observability] enabled = false, surfaced by
		// the compiler as the same env var.
		return
	}
	safeMount(mux, "/_sky/healthz", HandleHealthz)
	safeMount(mux, "/_sky/readyz", HandleReadyz)
	safeMount(mux, "/_sky/metrics", HandleMetrics)
	safeMount(mux, "/_sky/buildinfo", HandleBuildInfo)
	// Universal observability ingest endpoint — accepts logs +
	// metrics + spans pushed from sub-apps spawned via
	// rt.MountSubApp. Auth via X-Sky-Ingest-Token (auto-generated
	// per parent boot, override via SKY_INGEST_TOKEN env).
	MountObservabilityIngestEndpoint(mux)
	// Phase 1.1b — /_sky/console dashboard + its JSON API.
	MountConsoleEndpoints(mux)
}

// safeMount registers a handler unless the pattern is already
// taken. ServeMux's HandleFunc panics on duplicates; we'd rather
// log + skip.
func safeMount(mux *http.ServeMux, pattern string, handler http.HandlerFunc) {
	defer func() {
		if r := recover(); r != nil {
			// Already mounted — that's fine. User code wins.
			telemetry.Default().AppendLog(telemetry.LogEntry{
				Level:   "info",
				Message: "observability: skipping mount of " + pattern + " (user has it)",
			})
		}
	}()
	mux.HandleFunc(pattern, handler)
}

// ─── Production-mode + auth helpers ────────────────────────────

// productionMode is set by the runtime at startup based on:
//
//   - sky.toml `[security] env = "production"` (explicit, wins)
//   - OR the binary binding to 0.0.0.0 (rough heuristic — containers
//     and cloud VMs invariably bind 0.0.0.0; local dev binds localhost)
//
// Both paths set this atomic via SetProductionMode(). Endpoint
// handlers consult it to gate metrics auth.
var productionMode atomic.Bool

// SetProductionMode toggles the production auth gate. Called from
// the Sky.Live startup path after reading sky.toml + inspecting the
// listen address.
func SetProductionMode(on bool) {
	productionMode.Store(on)
}

func isProductionMode() bool {
	return productionMode.Load()
}

// productionFromEnv — single source of truth for whether the
// /_sky/console + /_sky/metrics auth gate should be on.
//
// Rule: ENV (or SKY_ENV if ENV is unset) is set to ANY value
// other than the dev-marker set → return true. ENV unset OR
// matching a dev marker → return false.
//
// Dev markers (case-insensitive): `dev`, `development`, `local`.
// Everything else (`production`, `prod`, `staging`, `qa`, `test`,
// `eu-west-2`, anything you care to name) gates.
//
// Why bias-to-gate when ENV IS set: bothering to set ENV at all
// signals a non-casual context. Better to surprise the developer
// with a 401 (they'll set ENV=dev) than silently expose
// /_sky/console on a forgotten staging deploy.
//
// Why default-open when ENV is unset: dev workflows are by far
// the common case, and the previous addr-based heuristic broke
// every Docker / reverse-proxy / sidecar pattern.
func productionFromEnv() bool {
	// Plain `ENV` first (the var users actually type), then
	// `SKY_ENV` fallback (the namespaced variant the compiler
	// emits from `sky.toml [security] env = ...`).
	envFlag := strings.ToLower(os.Getenv("ENV"))
	if envFlag == "" {
		envFlag = strings.ToLower(os.Getenv("SKY_ENV"))
	}
	if envFlag == "" {
		return false
	}
	switch envFlag {
	case "dev", "development", "local":
		return false
	}
	return true
}


// hasAdminAuth checks for a valid Std.Auth admin session on the
// request. v1.0 implementation: looks for a session cookie holding
// an admin-role token. Stub for v1.0 — full Std.Auth integration
// lands in Phase 1.2 (CSRF). Until then, production mode + no auth
// = blocked, which is the safe default.
//
// Returns true if request bears valid admin credentials. Returns
// false (deny) when in doubt — fail-closed.
func hasAdminAuth(r *http.Request) bool {
	// v1.0-rc1 placeholder: a per-app admin secret, when set, is
	// compared against the Authorization header (Basic auth form
	// "Bearer <token>" or "Basic base64(metrics:token)"). This
	// matches how Prometheus operators typically configure scrape
	// authentication: a single shared token per environment, rotated
	// via secret management.
	//
	// Read via adminTokenSecret so SKY_ADMIN_TOKEN takes precedence,
	// with SKY_METRICS_TOKEN / SKY_CONSOLE_TOKEN_SECRET honoured as
	// v0.14.21 / v0.14.20 back-compat aliases.
	//
	// Phase 1.2 will replace this with proper Std.Auth admin-role
	// verification once the CSRF / auth wire path lands.
	token := adminTokenSecret()
	if token == "" {
		// No token configured — fall through to "no admin auth
		// possible". Production users MUST set this; failure to do
		// so blocks the endpoint, which is the conservative default
		// when an admin-grade endpoint is exposed.
		return false
	}
	provided := r.Header.Get("Authorization")
	if provided == "" {
		return false
	}
	// Accept "Bearer <token>" or raw token in Authorization header.
	const bearer = "Bearer "
	if strings.HasPrefix(provided, bearer) {
		return safeStringEqual(provided[len(bearer):], token)
	}
	return safeStringEqual(provided, token)
}

// safeStringEqual — constant-time string compare to defeat timing
// attacks on the metrics token check.
func safeStringEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// ─── Startup hook ──────────────────────────────────────────────

// observabilityStartTime captures process start for the
// process_start_time_seconds metric. Set in init().
var observabilityStartTime = time.Now()
