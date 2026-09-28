//go:build !js

package rt

// live_serve.go — starting a Sky.Live app, and stopping it again.
//
// `Live.app` (and `App.run` on the web target) runs an app for the life of
// the process: it blocks until the listener closes. `Live.serve` (and
// `App.serve`) starts the SAME app in embedded mode (no signal handler, no
// process exit) and returns once the listener is bound, with a handle:
//
//	Live.address : Running -> String       -- the bound host:port (port 0 works)
//	Live.stop    : Running -> Task Error ()  -- graceful, bounded, idempotent
//
// Both paths build the app with buildLiveServer, so a served app is exactly
// the app `Live.app` would run.
//
// PER-APP VERSUS PROCESS-WIDE STATE. Two apps served in one process on
// different ports must not share or clobber each other's state, and stopping
// one must not touch the other. Audit (v0.27):
//
//	per app (owned by the liveServer, released by stop):
//	  - the listener, http.Server, ServeMux and handler chain
//	  - the session store, its cleanup / idle-evict goroutines, and every
//	    live session (relay, Sub.every tickers, topic subscriptions)
//	  - the pub/sub broker (store-bound, or the SKY_LIVE_BROKER_URL override)
//	  - the route tables, api routes, message-tag cache, locker
//	  - the revocation gate (Live.withRevocation; was process-wide) and its
//	    verdict cache key (now the gate's Db plus the user id)
//	  - the sliding-auth middleware config (Live.withAuthSliding; was
//	    process-wide)
//	  - the session transport (Live.withSessionTransport)
//	  - its /_sky/readyz store probe, its shutdown hook, accept-stopper and
//	    release-phase store closer (all removed again by stop)
//	  - its registration as a Std.PubSub.publish target (removed by stop)
//
//	one per process, by design (documented in docs/skylive/embedded.md):
//	  - the inline Sky Console: the first listener to mount it owns it; it
//	    renders process-wide telemetry, and its auth callback is the owner
//	    app's. stop releases it, so a later app can mount it.
//	  - Auth.setSlidingCookie's config: it has no app in scope, so two apps
//	    with DIFFERENT sliding configs refuse to share a process.
//	  - telemetry (the log / metric / trace rings, persistence, OTel), the
//	    production gate, readiness (SetReady), the Std.Jobs worker, the
//	    shutdown sequence, the sessionless WebSocket reaper, the gob type
//	    registry, the Cmd.perform concurrency limit, the process epoch.
//	  - settings read from the environment and sky.toml (ENV, SKY_CSRF, the
//	    bind host, SKY_LIVE_* not given by a builder).
//	  - CSRF exemptions registered by `Live.api` / `Server.api` /
//	    `Server.rpc` / `WithoutCsrf`: keyed by method and path, process-wide.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"
)

// liveInitTracing installs the process tracer at boot. A variable so the
// in-process Live.serve tests can keep the process-wide tracer untouched for
// the tests that run after them.
var liveInitTracing = InitTracingFromEnv

// liveStopTimeout bounds Live.stop's graceful drain of in-flight requests. A
// variable so tests can shorten it.
var liveStopTimeout = 5 * time.Second

// liveServer is one started Sky.Live app: the value behind `Live.Running`.
type liveServer struct {
	app      *liveApp
	srv      *http.Server
	ln       net.Listener
	port     int
	embedded bool
	// cleanups run LIFO on stop (and on a failed start): each undoes one
	// process-wide registration this app made.
	cleanups []func()
	// console is the inline console sub-app this app mounted (nil when it
	// mounted none).
	console *liveApp
	served  chan struct{} // closed when Serve returns
	stopped chan struct{} // closed when stop has finished
	once    sync.Once
	stopErr error
}

// errLivePortInUse is buildLiveServer's port-already-bound failure.
type errLivePortInUse struct{ port int }

func (e errLivePortInUse) Error() string {
	return fmt.Sprintf("port %d is already in use", e.port)
}

// errLiveConsoleInvariant is buildLiveServer's console-invariant failure.
type errLiveConsoleInvariant struct{ err error }

func (e errLiveConsoleInvariant) Error() string { return "console invariant: " + e.err.Error() }

// errLiveStore is buildLiveServer's session-store refusal (embedded only; a
// process-owning app exits in chooseStore instead).
type errLiveStore struct{ err error }

func (e errLiveStore) Error() string { return e.err.Error() }

func (ls *liveServer) addCleanup(fn func()) {
	if fn != nil {
		ls.cleanups = append(ls.cleanups, fn)
	}
}

func (ls *liveServer) runCleanups() {
	for i := len(ls.cleanups) - 1; i >= 0; i-- {
		func(fn func()) {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "[sky.live] stop: cleanup panicked: %v\n", r)
				}
			}()
			fn()
		}(ls.cleanups[i])
	}
	ls.cleanups = nil
}

// buildLiveServer builds the app from its config, binds its listener, and
// returns it ready to Serve. On error every registration it made is undone
// and the listener (if bound) is closed.
//
// embedded: the app is a guest in a larger Task program (Live.withEmbedded,
// Live.serve). A store refusal and the console invariant are errors, never an
// exit; the host owns shutdown.
func buildLiveServer(cfg any, embedded bool) (ls *liveServer, err error) {
	app := &liveApp{
		init:               Field(cfg, "Init"),
		update:             Field(cfg, "Update"),
		view:               Field(cfg, "View"),
		subscriptions:      Field(cfg, "Subscriptions"),
		notFound:           Field(cfg, "NotFound"),
		guard:              Field(cfg, "Guard"),
		head:               Field(cfg, "Head"),
		consoleAuth:        Field(cfg, "ConsoleAuth"),
		onNavigate:         Field(cfg, "OnNavigate"),
		analyticsPageViews: analyticsPageViewsFromCfg(cfg),
		analyticsIdentify:  analyticsIdentifyFromCfg(cfg),
		durable:            durableCtxOf(Field(cfg, "Durable")),
		locker:             newSessionLocker(),
		msgTags:            make(map[string]int),
		bannerCfg:          resolveBannerStrings(loadLiveBannerConfig(), cfg),
		basePath:           normaliseBasePath(skyGetenv("LIVE_BASE_PATH")),
		cookieName:         "sky_sid",
		skyIDPrefix:        "r",
		stopCh:             make(chan struct{}),
	}
	ls = &liveServer{
		app:      app,
		embedded: embedded,
		served:   make(chan struct{}),
		stopped:  make(chan struct{}),
	}
	defer func() {
		if err != nil {
			ls.runCleanups()
			if app.store != nil {
				_ = app.store.Close()
			}
		}
	}()
	app.routes, app.api = collectLiveRoutes(cfg)
	// Static file serving. Sky-side: `static = "public"` → serve
	// <cwd>/public/* at /static/*. Mount URL can be overridden with
	// `staticUrl = "/assets"`.
	if sd := Field(cfg, "Static"); sd != nil {
		app.staticDir = fmt.Sprintf("%v", sd)
	} else if v := skyGetenv("LIVE_STATIC_DIR"); v != "" {
		// <PREFIX>_LIVE_STATIC_DIR is the documented name (matches
		// the <PREFIX>_LIVE_* env var convention). <PREFIX>_STATIC_DIR
		// is kept as a backward-compat alias so existing deployments
		// don't break — read it only when the canonical name is
		// unset. Both honour the configured env-prefix.
		app.staticDir = v
	} else if v := skyGetenv("STATIC_DIR"); v != "" {
		app.staticDir = v
	}
	app.staticURL = "/static"
	if su := Field(cfg, "StaticUrl"); su != nil {
		if s := fmt.Sprintf("%v", su); s != "" {
			app.staticURL = s
		}
	}
	// Session transport: the session id rides in a cookie (default), or in
	// the X-Sky-Session header (Live.withSessionTransport "header", for hosts
	// that cannot keep cookies). live_session_header.go.
	app.headerSessions = resolveSessionTransport(stringField(cfg, "SessionTransport")) == sessionTransportHeader
	// Session store, TTL and idle-evict window — all four resolved by the one
	// rule in `configLayers` (live_config_precedence.go):
	//
	//	operator env > withX builder > seeded sky.toml default > fallback
	//
	// Each accepts a Go-duration string ("30m", "24h", "1h30m", "45s") or a
	// bare integer read as SECONDS, at every layer; an empty or unparseable
	// value falls through to the next layer rather than to the fallback.
	storeKind := resolveStoreKind(stringField(cfg, "Store"))
	storePath := resolveStorePath(stringField(cfg, "StorePath"))
	ttl := resolveTTL(stringField(cfg, "Ttl"), defaultSessionTTL)
	// "0"/"off"/"none"/"disable(d)" disables idle-evict outright, which is the
	// one way it differs from ttl. Bounds a durable store's RAM to the ACTIVE
	// working set. See docs/skylive/tiered-session-cache.md.
	idleEvict := resolveIdleEvict(stringField(cfg, "IdleEvict"), defaultIdleEvict)
	// Event-body cap and input-report mode — resolved by the same one rule, so a
	// Live.withMaxBodyBytes / Live.withInput builder beats a seeded sky.toml
	// default while still losing to an operator env override. Resolved ONCE here
	// (not per request / per /_sky/config hit) and stored on the app.
	app.maxBodyBytes = resolveMaxBodyBytes(stringField(cfg, "MaxBodyBytes"), 5<<20)
	app.inputMode = resolveInputMode(stringField(cfg, "Input"))
	if embedded {
		store, release, serr := chooseStoreOrErrScoped(storeKind, storePath, ttl, idleEvict)
		if serr != nil {
			return ls, errLiveStore{serr}
		}
		app.store, app.storeRelease = store, release
	} else {
		app.store, app.storeRelease = chooseStoreScoped(storeKind, storePath, ttl, idleEvict)
	}
	ls.addCleanup(app.storeRelease)
	app.sessionTTL = ttl
	// Wire the session store into /_sky/readyz so the endpoint reports 503 when
	// the backing DB is unreachable — instead of returning 200 while the store
	// is down (the "readyz lies by default" class). Main app only — sub-apps
	// (the inline console) don't own the readiness surface; the parent does.
	// Scoped: a stopped app removes its probe, so readyz does not report a
	// store that was closed on purpose.
	if app.basePath == "" {
		ls.addCleanup(registerReadinessProbeScoped("session-store", app.store.Ping))
	}
	// Cycle 3 P46: cache the store-bound broker on the app for
	// hot-path Subscribe/Publish call sites.
	app.topics = app.store.Broker()
	// Phase 2: the broker is app-scoped, not store-scoped, so a deploy
	// can run a Redis broker even with a non-Redis session store (e.g.
	// Postgres sessions + Redis pub/sub) via SKY_LIVE_BROKER_URL. No-op
	// when unset or when the store already provides a cross-instance
	// broker (store=redis).
	app.topics = maybeOverrideBroker(app.topics, "")
	if app.topics != nil && app.topics != app.store.Broker() {
		// The override broker belongs to this app alone: close it on stop.
		b := app.topics
		ls.addCleanup(func() { _ = b.Close() })
	}
	// L6: a non-Redis broker is IN-PROCESS, so cross-replica broadcasts
	// (Cmd.publish, and multi-tab fan-out across instances) silently don't reach
	// users on OTHER replicas. Main app + production only, once at startup.
	if app.basePath == "" && productionFromEnv() {
		if _, isRedis := app.topics.(*redisBroker); !isRedis {
			logEmit(logLevelWarn, "warn",
				"Sky.Live pub/sub broker is in-process — cross-replica broadcasts "+
					"(Cmd.publish / multi-tab fan-out across instances) will NOT reach other replicas. "+
					"If you run more than one replica, set SKY_LIVE_BROKER_URL to a Redis (or use "+
					"store=redis). Single-instance deploys can ignore this.",
				nil)
		}
	}
	// Cycle 4 PT: register as a process publish target so Std.PubSub.publish
	// (Task-shaped, callable from raw api handlers / post-init goroutines /
	// scheduled jobs) reaches this app without an update-tuple context.
	registerProcessBroker(app)
	ls.addCleanup(func() { unregisterProcessBrokerApp(app) })

	// Sliding auth token (opt-in via Live.withAuthSliding). The middleware
	// reads the app's own config; the builder-owned login setter
	// (Auth.setSlidingCookie) reads the process-wide one, which this claims.
	// Absent field ⇒ nil ⇒ inert.
	app.sliding = parseAuthSlidingConfig(Field(cfg, "AuthSliding"))
	releaseSliding, serr := claimAuthSlidingConfig(app.sliding)
	if serr != nil {
		return ls, serr
	}
	ls.addCleanup(releaseSliding)
	// PULL-model revocation gate (opt-in via Live.withRevocation). The app-
	// supplied Db is where the shared sky_revocations / users.disabled_at state
	// lives (NOT the session store). Absent field ⇒ nil ⇒ the gate stays inert.
	if dbAny := Field(cfg, "Revocation"); dbAny != nil {
		if d, ok := dbAny.(*SkyDb); ok && d != nil {
			app.revocation = &revocationGateConfig{db: d, ttl: revocationCacheTTLFromEnv()}
			liveRevocationApps.Add(1)
			ls.addCleanup(func() { liveRevocationApps.Add(-1) })
		}
	}

	// Bind the listener BEFORE anything is mounted: the console's loopback
	// URL needs the real port (port 0 asks the kernel for a free one), and a
	// port already in use is reported before the app announces itself.
	port := resolveLivePort(cfg)
	bindHost, _ := resolveBindHost()
	ln, lerr := net.Listen("tcp", joinBindAddr(bindHost, port))
	if lerr != nil {
		if isAddrInUse(lerr) {
			return ls, errLivePortInUse{port}
		}
		return ls, lerr
	}
	ls.ln = ln
	ls.addCleanup(func() { _ = ln.Close() })
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok {
		port = tcp.Port
	}
	ls.port = port

	mux := http.NewServeMux()
	mux.HandleFunc("/_sky/event", app.handleEvent)
	mux.HandleFunc("/_sky/sse", app.handleSSE)
	// Session-id rotation: the signing-in tab exchanges its ticket for the new
	// session credential (live_session_rotation.go). CSRF-checked like
	// /_sky/event.
	mux.HandleFunc("/_sky/rotate", app.handleRotate)
	// Header session transport: the EventSource fallback's one-time ticket
	// (live_session_header.go). Answers 404 in cookie mode.
	mux.HandleFunc("/_sky/sse-ticket", app.handleSSETicket)
	mux.HandleFunc("/_sky/config", app.handleConfig)
	// The client script (live_client_asset.go): a same-origin, content-hashed
	// file so a strict Content-Security-Policy (script-src 'self') runs it.
	mux.HandleFunc(liveClientPath, serveStaticJS(liveClientJS))
	// v0.16.1 PR7 — seed SKY_PARENT_URL so the inline console_app's init_
	// reads OUR OWN listener's loopback when it builds the initial Model.
	// Only seed when UNSET — never overwrite a user-supplied value.
	if os.Getenv("SKY_PARENT_URL") == "" {
		os.Setenv("SKY_PARENT_URL", fmt.Sprintf("http://127.0.0.1:%d", port))
	}
	// v0.16.0: in-process inline Sky Console mount. The function internally
	// gates on production-mode + sub-app context. Must run BEFORE
	// MountObservabilityEndpoints so the legacy HTML shell inside the latter
	// doesn't collide on /_sky/console.
	//
	// One console per process (console.go): the app that mounts it installs
	// the console auth callbacks (the app's optional consoleAuth /
	// Std.App.withConsoleAuth check) when it takes the claim.
	ls.console = mountEmbeddedConsoleFor(mux, ls, func() {
		SetConsoleAuthCallback(app.consoleAuth)
		SetConsoleAuthModel(nil)
		// Std.App.withConsoleAuth: the check also receives the console
		// request's signed-in model. It replaces a one-argument consoleAuth
		// if both are set.
		if check := Field(cfg, "ConsoleAuthModel"); check != nil {
			SetConsoleAuthCallback(check)
			SetConsoleAuthModel(app.consoleModelFor)
		}
	})
	ls.addCleanup(func() { releaseConsole(ls) })
	// If THIS process is a sub-app (env vars from MountSubApp set),
	// kick the push exporter — Log.* / counter / span writes flow
	// to the parent. No-op for standalone (parent) runs.
	StartPushExporter()
	// Observability endpoints — healthz / readyz / metrics / buildinfo.
	// Skipped when this app is running AS a sub-app (basePath set).
	if app.basePath == "" {
		MountObservabilityEndpoints(mux)
	}
	// v0.16.1 PR 2 — boot-time mount-precedence invariant. When the user
	// EXPLICITLY asked for a console (SKY_CONSOLE_AUTH=token|app) but no
	// mount claimed /_sky/console, a process-owning app exits; an embedded
	// one returns the error.
	if embedded {
		if cerr := consoleInvariantError(); cerr != nil {
			return ls, errLiveConsoleInvariant{cerr}
		}
	} else {
		AssertConsoleInvariantOrExit()
	}
	// Static assets (if configured) mounted first so api/page routing
	// doesn't shadow them.
	if app.staticDir != "" {
		prefix := app.staticURL
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		mux.Handle(prefix,
			gzipStatic(http.StripPrefix(prefix, http.FileServer(http.Dir(app.staticDir)))))
	}
	// API handler dispatcher — matches method + pattern before page handler.
	mux.HandleFunc("/", app.dispatchRoot)

	// Pre-register model types with gob so DB-backed session stores
	// can decode existing sessions on restart.
	// Two passes:
	//   1. Type-graph walk: registers SkyMaybe[User_R] etc. even when
	//      init returns Nothing/[]/empty — walks the struct DEFINITION,
	//      not the runtime value.
	//   2. Value walk: catches anything the type walker misses.
	func() {
		defer func() { recover() }()
		// v0.16.9 — keys are LOWERCASE for backward-compat with apps
		// that read fields via Sky's `Dict.get "path" req`.
		req := map[string]any{
			"path":    "/",
			"query":   "",
			"params":  Dict_empty(),
			"method":  "GET",
			"headers": Dict_empty(),
			"cookies": Dict_empty(),
		}
		res := sky_call(app.init, req)
		model := tupleFirst(res)
		GobRegisterTypeGraph(reflect.TypeOf(model))
		gobRegisterAll(model)
	}()

	// Production-mode gate for /_sky/console + /_sky/metrics auth. PURELY
	// env-based (productionFromEnv); the bind HOST is a separate decision
	// made by resolveBindHost.
	SetProductionMode(productionFromEnv())

	// Step 7 — OTel tracer init. Honours OTEL_EXPORTER_OTLP_ENDPOINT.
	// Non-fatal: any failure logs + falls back to noop tracer.
	if terr := liveInitTracing(); terr != nil {
		fmt.Fprintf(os.Stderr, "[sky.live] OTel init failed (continuing without trace export): %v\n", terr)
	}

	// The whole chain (Host guard, panic recovery, observability, CSRF or the
	// header-session guard, sliding auth, the mux) is built by
	// liveListenerHandlerFor so a test can drive the same handler the
	// listener serves.
	ls.srv = &http.Server{
		Handler:           liveListenerHandlerFor(app, mux, bindHost),
		ReadHeaderTimeout: 10 * time.Second,
		// IMPORTANT: do not set ReadTimeout or WriteTimeout here — the SSE
		// endpoint needs to stream indefinitely.
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}
	// Under `--embed` the supervisor in pg_embed.go owns the shutdown
	// SEQUENCE (stop accepting → drain → stop PostgreSQL). Handing it the
	// listener is what makes its first phase real.
	srv := ls.srv
	ls.addCleanup(registerAcceptStopperScoped("live.Server", func() { _ = srv.Close() }))
	if embedded {
		// The host owns the process: no signal handler, no process-wide
		// teardown. The app still stops in the right place when the host
		// runs a termination sequence: closing the listener is a drain-phase
		// hook, so it happens before the release phase closes this app's
		// session store.
		ls.addCleanup(registerShutdownHookScoped("live.embedded", func(context.Context) { _ = srv.Close() }))
	}
	return ls, nil
}

// announce prints the start-up lines. The first one is load-bearing:
// `apps/fieldbook/verify.sh` greps it literally, and both `xtask
// build_run_gate` and `sky run`'s supervisor lift the port from the last
// `:PORT` of any line whose lowercase form contains "listening". See
// startup_report.go.
func (ls *liveServer) announce() {
	fmt.Printf("Sky.Live listening on :%d\n", ls.port)
	// The console line names this listener only when the console is served
	// here: the app mounted the inline console, or no inline console exists
	// and the legacy shell was mounted on this mux.
	consoleHere := ls.console != nil || (!InlineConsoleHealthy() && LegacyConsoleHealthy())
	printStartupReportConsole(ls.port, consoleHere)
}

// liveAppRun runs a Sky.Live app for the life of its listener: `Live.app`,
// and `App.run` on the web target.
func liveAppRun(cfg any) any {
	// Embedded mode (Live.withEmbedded / App.withEmbedded): the app is a guest
	// in a larger Task program. It installs no signal handler and never exits
	// the process; every refusal to start is the Task's Err, and the host owns
	// shutdown.
	embedded := AsBoolOrFalse(Field(cfg, "Embedded"))
	ls, err := buildLiveServer(cfg, embedded)
	if err != nil {
		var inUse errLivePortInUse
		if errors.As(err, &inUse) {
			if embedded {
				// A guest does not end its host: the host decides.
				return Err[any, any](ErrUnavailable(fmt.Sprintf(
					"Sky.Live (embedded) did not start: port %d is already in use "+
						"(set SKY_LIVE_PORT, or [live] port in sky.toml)", inUse.port)))
			}
			// A port-already-bound failure is the common startup error —
			// make it LOUD + actionable on stderr instead of a silent Task-Err
			// exit.
			reportPortInUse(inUse.port, "set SKY_LIVE_PORT, or [live] port in sky.toml")
			ExitProcess(1)
		}
		return Err[any, any](liveStartError(err, embedded))
	}
	srv := ls.srv
	// Shutdown on SIGINT / SIGTERM / SIGHUP (a process-owning app only). SSE
	// connections are long-lived, so the graceful `srv.Shutdown` would block
	// on them; `srv.Close` forcibly closes the listener and every active
	// connection. A second signal forces the exit (liveSignalShutdown).
	var sigCh chan os.Signal
	if !embedded {
		sigCh = make(chan os.Signal, 2)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		go liveSignalShutdown(sigCh, srv)
	}
	ls.announce()
	serr := srv.Serve(ls.ln)
	if sigCh != nil {
		signal.Stop(sigCh)
	}
	// See the note in Server_listen: exiting here mid-shutdown would kill the
	// embedded database instead of stopping it.
	BlockIfEmbeddedShuttingDown()
	if serr != nil && serr != http.ErrServerClosed {
		return Err[any, any](ErrFfi(serr.Error()))
	}
	return Ok[any, any](struct{}{})
}

// liveStartError maps a buildLiveServer failure to the Task's Error.
func liveStartError(err error, embedded bool) any {
	prefix := "Sky.Live did not start: "
	if embedded {
		prefix = "Sky.Live (embedded) did not start: "
	}
	var inv errLiveConsoleInvariant
	if errors.As(err, &inv) {
		return ErrInvalidInput(prefix + err.Error())
	}
	var st errLiveStore
	if errors.As(err, &st) {
		return ErrUnavailable(prefix + err.Error())
	}
	var inUse errLivePortInUse
	if errors.As(err, &inUse) {
		return ErrUnavailable(fmt.Sprintf("%sport %d is already in use "+
			"(set SKY_LIVE_PORT, or [live] port in sky.toml, or use port 0)", prefix, inUse.port))
	}
	return ErrInvalidInput(prefix + err.Error())
}

// ─── Live.serve / Live.address / Live.stop ──────────────────────────

// Live_serve — `Live.serve : AppConfig model msg -> Task Error Running`.
//
// Starts the app in embedded mode (no signal handler, no process exit) and
// returns once its listener is bound: a bind failure, a store refusal or the
// console invariant is the Task's Err. The app serves on its own goroutine
// until Live.stop.
func Live_serve(cfg any) any {
	return func() any {
		ls, err := buildLiveServer(cfg, true)
		if err != nil {
			return Err[any, any](liveStartError(err, true))
		}
		ls.announce()
		go ls.run()
		return Ok[any, any](ls)
	}
}

// run serves until the listener closes.
func (ls *liveServer) run() {
	defer close(ls.served)
	if err := ls.srv.Serve(ls.ln); err != nil && err != http.ErrServerClosed {
		logEmit(logLevelError, "error",
			fmt.Sprintf("Sky.Live (served on %s) stopped serving: %v", ls.address(), err),
			map[string]any{"class": "LiveServeFailed"})
	}
}

// address is the bound listener address, host:port.
func (ls *liveServer) address() string {
	if ls == nil || ls.ln == nil {
		return ""
	}
	return ls.ln.Addr().String()
}

// Live_address — `Live.address : Running -> String`: the bound address as
// host:port ("127.0.0.1:54321" in development, "[::]:8080" when bound to all
// interfaces). With port 0 it names the port the kernel picked.
func Live_address(h any) any {
	ls, ok := h.(*liveServer)
	if !ok {
		return ""
	}
	return ls.address()
}

// Live_stop — `Live.stop : Running -> Task Error ()`. Graceful and bounded:
// the listener stops accepting, every SSE stream of the app closes, in-flight
// requests get liveStopTimeout to finish (then their connections are
// closed), every session ends (its Sub.every tickers, relay and topic
// subscriptions stop), the store closes, and every process-wide registration
// the app made is removed. Idempotent: a second stop waits for the first and
// returns its result.
func Live_stop(h any) any {
	return func() any {
		ls, ok := h.(*liveServer)
		if !ok || ls == nil {
			return Err[any, any](ErrInvalidInput("Live.stop: not a running Sky.Live app"))
		}
		if err := ls.stop(liveStopTimeout); err != nil {
			return Err[any, any](ErrUnavailable("Live.stop: " + err.Error()))
		}
		return Ok[any, any](struct{}{})
	}
}

// stop runs the stop sequence once; every caller gets its result.
func (ls *liveServer) stop(timeout time.Duration) error {
	ls.once.Do(func() {
		ls.stopErr = ls.doStop(timeout)
		close(ls.stopped)
	})
	<-ls.stopped
	return ls.stopErr
}

func (ls *liveServer) doStop(timeout time.Duration) error {
	app := ls.app
	// 1. Every open SSE stream returns (handleSSE selects on stopCh). The
	//    client sees a dropped stream, the same as a deploy.
	close(app.stopCh)
	// 2. Stop accepting and drain in-flight requests, bounded.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var drainErr error
	if err := ls.srv.Shutdown(ctx); err != nil {
		// The drain ran out of time: close what is left.
		_ = ls.srv.Close()
		if !errors.Is(err, context.DeadlineExceeded) {
			drainErr = err
		}
	}
	select {
	case <-ls.served:
	case <-time.After(timeout):
		drainErr = errors.New("the listener did not close")
	}
	// 3. The console this app mounted, its sessions, its store, and every
	//    process-wide registration (LIFO).
	app.releaseSessionsAndStore()
	ls.runCleanups()
	return drainErr
}

// releaseSessionsAndStore ends every live session of the app (so their
// goroutines stop) and closes its session store. Idempotent.
func (app *liveApp) releaseSessionsAndStore() {
	if app == nil || app.store == nil {
		return
	}
	if l, ok := app.store.(liveSessionLister); ok {
		for _, sess := range l.liveSessions() {
			sess.markDone()
		}
	}
	if app.storeRelease != nil {
		app.storeRelease()
	}
	if err := app.store.Close(); err != nil {
		logEmit(logLevelWarn, "warn",
			fmt.Sprintf("Sky.Live stop: session store close: %v", err), nil)
	}
}
