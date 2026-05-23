package rt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ============================================================================
// Sky sub-app mount — reverse-proxy a separate Sky process under a URL prefix
// ============================================================================
//
// Lets a parent Sky.Live or Sky.Http.Server app host other Sky apps
// (or arbitrary HTTP servers) as sub-apps under a path prefix. Each
// sub-app runs as its OWN child process listening on a free
// localhost port; the parent's mux proxies `<prefix>/*` requests to
// the child's port. Single user-visible port (the parent's), single
// process tree (children die when parent exits), zero shared state.
//
// Used by the Sky Console auto-mount in dev mode (see liveAppRun /
// Server_listen). Generalises to any sub-app: an admin panel, a
// billing widget, a docs site — wherever a self-contained Sky app
// needs to live alongside the main app at a sub-URL.
//
// Why reverse-proxy instead of importing the sub-app as a Go
// sub-package: zero compiler-coupling. The child can be ANY HTTP
// server (Sky or otherwise), built with any toolchain, deployed
// independently. Sub-app process gets its own session store,
// observability namespace, signal handling — no shared globals
// to coordinate.
//
// Cost: ~5MB RAM + ~5ms extra latency per request hop through
// httputil.ReverseProxy. Both negligible for the dev console class
// of sub-app. For high-RPS production sub-apps, the parent can mount
// the sub-app at a path that bypasses the dev-only auto-mount.

// SpawnFn produces a running HTTP child process bound to a localhost
// port. The returned port + *exec.Cmd let the supervisor proxy +
// shutdown the child correctly.
//
// `basePath` is the URL prefix the parent intends to mount the child
// at (e.g. "/_sky/console"). The implementation should propagate this
// to the child via SKY_LIVE_BASE_PATH so the child's HTML output
// uses correct absolute URLs in fetch / EventSource calls (see
// `__skyBase` JS variable).
type SpawnFn func(ctx context.Context, basePath string) (port int, cmd *exec.Cmd, err error)

// SpawnBinary returns a SpawnFn that runs an external Sky binary
// as a sub-app on a randomly-chosen free localhost port. Stdout +
// stderr go to /dev/null by default to keep the parent's terminal
// clean; set SKY_SUBAPP_VERBOSE=1 to surface child output for
// debugging.
//
// `parentPort` is the parent app's listen port — used to seed
// SKY_PARENT_URL on the child so its push-exporter can ship logs /
// metrics / spans back. Pass 0 to skip (the child still runs but
// without observability federation; useful for sub-apps that
// shouldn't be allowed to push, or for arbitrary non-Sky binaries
// that don't speak the ingest protocol).
func SpawnBinary(parentPort int, binPath string, extraArgs ...string) SpawnFn {
	return func(ctx context.Context, basePath string) (int, *exec.Cmd, error) {
		port, err := pickFreeLocalhostPort()
		if err != nil {
			return 0, nil, fmt.Errorf("sub-app port pick failed: %w", err)
		}
		args := append([]string(nil), extraArgs...)
		cmd := exec.CommandContext(ctx, binPath, args...)
		// On ctx cancel, send SIGTERM (not the default SIGKILL) so
		// the child gets a chance to forward to its own grandchildren.
		// Without this, recursive process trees orphan grandchildren
		// to PID 1 (see app/Main.hs runConsole signal handlers — the
		// chain only works if every level receives SIGTERM, not
		// SIGKILL). WaitDelay then escalates to SIGKILL after 2 s.
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = 2 * time.Second
		envExtra := []string{
			"SKY_LIVE_PORT=" + strconv.Itoa(port),
			"SKY_LIVE_BASE_PATH=" + basePath,
			// Suppress the child's connection-status banner: the user
			// is interacting with the PARENT app, the child's "I'm
			// reconnecting" chrome would just be noise.
			"SKY_LIVE_BANNER=off",
			"SKY_LIVE_NAMESPACE=" + subAppNamespaceFromPath(basePath),
		}
		if parentPort > 0 {
			envExtra = append(envExtra,
				fmt.Sprintf("SKY_PARENT_URL=http://127.0.0.1:%d", parentPort),
				"SKY_INGEST_TOKEN="+CurrentIngestToken(),
			)
		}
		cmd.Env = append(os.Environ(), envExtra...)
		if os.Getenv("SKY_SUBAPP_VERBOSE") == "1" {
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
		} else {
			cmd.Stdout = io.Discard
			cmd.Stderr = io.Discard
		}
		// Put the child in its own process group so a Ctrl-C on the
		// parent's terminal doesn't ALSO kill the child directly —
		// the parent's signal handler will tear it down cleanly.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			return 0, nil, fmt.Errorf("sub-app spawn failed: %w", err)
		}
		// Wait for the child to start accepting connections. 60 ×
		// 100ms = 6s, enough for sky-console's first-run build but
		// still bounded.
		ready := waitForPort("127.0.0.1", port, 60, 100*time.Millisecond)
		if !ready {
			_ = cmd.Process.Kill()
			return 0, nil, fmt.Errorf("sub-app on :%d did not become ready within 6s", port)
		}
		return port, cmd, nil
	}
}

// SpawnSkyConsole spawns the bundled Sky Console mini-app via
// `sky console --port <free>`. `parentPort` is the port the
// parent app is listening on — passed to the child via
// SKY_PARENT_URL so the child can push observability data back
// to the parent's /_sky/observability/ingest endpoint. Pass 0 for
// standalone scenarios where there's no parent (the child then
// runs without the push exporter).
//
// Resolves the `sky` binary by honouring (in order) the SKY_BIN
// env var, the running parent's own argv[0] (if it IS sky), then
// exec.LookPath("sky"). Returns an error explaining the lookup
// failure if none of those find a usable binary — the caller logs
// the warning and continues without auto-mounting.
//
// Note: SpawnBinary's generic SKY_LIVE_PORT env is IGNORED by the
// `sky console` CLI (which always overrides SKY_LIVE_PORT from its
// own --port flag, default 8025). So we pass --port explicitly with
// the picked free port and let SpawnBinary's env-setting fall on
// the floor.
func SpawnSkyConsole(parentPort int) SpawnFn {
	return func(ctx context.Context, basePath string) (int, *exec.Cmd, error) {
		skyBin, err := resolveSkyBinary()
		if err != nil {
			return 0, nil, err
		}
		port, err := pickFreeLocalhostPort()
		if err != nil {
			return 0, nil, fmt.Errorf("sub-app port pick failed: %w", err)
		}
		cmd := exec.CommandContext(ctx, skyBin, "console",
			"--port", strconv.Itoa(port))
		// On ctx cancel, send SIGTERM so `sky console`'s signal
		// handler (app/Main.hs runConsole) can propagate to its own
		// `app-live` child instead of being SIGKILL'd outright.
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = 2 * time.Second
		envExtra := []string{
			// Tells the bundled console's Sky.Live runtime to emit
			// URLs prefixed with our mount point.
			"SKY_LIVE_BASE_PATH=" + basePath,
			// Suppress the child's reconnect-status banner — the
			// user interacts with the PARENT's chrome; the child's
			// "I'm reconnecting" chrome would just be noise.
			"SKY_LIVE_BANNER=off",
			// Namespace for the push-exporter — every log /
			// metric / span the child emits is labelled
			// `subapp=console` on the parent's store.
			"SKY_LIVE_NAMESPACE=" + subAppNamespaceFromPath(basePath),
			// Stop the bundled console from recursively auto-
			// mounting ANOTHER console under itself. Without this
			// every spawn fan-out adds an orphaned grandchild every
			// time the parent restarts.
			"SKY_CONSOLE_EMBED=off",
		}
		// Tell the child where to push observability data when the
		// parent is reachable. Empty parentPort skips this —
		// standalone `sky console` invocations (no parent) just
		// keep observability local.
		if parentPort > 0 {
			envExtra = append(envExtra,
				fmt.Sprintf("SKY_PARENT_URL=http://127.0.0.1:%d", parentPort),
				"SKY_INGEST_TOKEN="+CurrentIngestToken(),
			)
		}
		cmd.Env = append(os.Environ(), envExtra...)
		if os.Getenv("SKY_SUBAPP_VERBOSE") == "1" {
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
		} else {
			cmd.Stdout = io.Discard
			cmd.Stderr = io.Discard
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			return 0, nil, fmt.Errorf("sky console spawn failed: %w", err)
		}
		// Wait up to 30 s. Local dev usually has the bundled
		// console mini-app ready in ~1 s, but Cloud Run cold
		// starts on small instances (e2-micro / 256 MiB / 1 vCPU)
		// take significantly longer — `sky console` has to load
		// the runtime, init its SQLite + memory store, bind a
		// port, and respond. SKY_CONSOLE_READY_TIMEOUT_MS overrides
		// for unusual environments. Was 6 s pre-2026-05-23 and
		// regularly timed out on Cloud Run e2-micro deploys.
		readyMs := 30000
		if v := os.Getenv("SKY_CONSOLE_READY_TIMEOUT_MS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				readyMs = n
			}
		}
		ticks := readyMs / 100
		if !waitForPort("127.0.0.1", port, ticks, 100*time.Millisecond) {
			_ = cmd.Process.Kill()
			return 0, nil, fmt.Errorf("sky console on :%d did not become ready within %dms", port, readyMs)
		}
		return port, cmd, nil
	}
}

// MountSubApp spawns a child via `spawn` and mounts a reverse-proxy
// at `prefix + "/"` on `mux`. Also adds a redirect from `prefix`
// (no trailing slash) → `prefix + "/"` so users typing the bare
// prefix in the address bar reach the sub-app.
//
// On spawn failure the parent app continues — a warning is logged
// to stderr, the prefix routes are NOT mounted, and the function
// returns the spawn error so callers can choose to escalate.
//
// The child is supervised: it's killed when the parent process
// receives SIGINT/SIGTERM/SIGHUP (the same shutdown handler
// Sky.Live + Sky.Http.Server already register). If the parent
// crashes hard (SIGKILL, OOM), the child orphans — best-effort
// cleanup, not a hard guarantee.
func MountSubApp(mux *http.ServeMux, prefix string, spawn SpawnFn) error {
	if mux == nil {
		return fmt.Errorf("MountSubApp: mux is nil")
	}
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" || prefix == "/" {
		return fmt.Errorf("MountSubApp: prefix must be a non-root path like /admin")
	}

	ctx, cancel := context.WithCancel(context.Background())
	port, cmd, err := spawn(ctx, prefix)
	if err != nil {
		cancel()
		fmt.Fprintf(os.Stderr, "[sky.subapp] mount at %s skipped: %v\n", prefix, err)
		return err
	}
	registerSubAppChild(cmd, cancel)

	target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	proxy := httputil.NewSingleHostReverseProxy(target)
	// FlushInterval -1 means immediate-flush — critical for SSE
	// (/_sky/sse): without it, httputil buffers chunks and the
	// browser never sees events arrive.
	proxy.FlushInterval = -1
	// Silence the noisy "http: proxy error: context canceled" log
	// that the default ErrorHandler emits whenever a client
	// disconnects mid-stream (browser refresh of /_sky/console
	// drops the in-flight SSE; same on navigation away). The
	// errors are routine and not actionable. Real errors (child
	// unreachable, malformed upstream response) still write 502 +
	// log via the explicit branch.
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
			return // client gave up; nothing to do, no log noise
		}
		fmt.Fprintf(os.Stderr, "[sky.subapp] proxy %s %s -> %s: %v\n",
			r.Method, r.URL.Path, target.Host, err)
		w.WriteHeader(http.StatusBadGateway)
	}
	// The ErrorHandler above only fires for errors BEFORE the
	// response is committed. An error mid-body-copy (the upstream
	// child closing during shutdown, or an SSE stream cut on
	// Ctrl-C) instead goes to proxy.ErrorLog as
	// "ReverseProxy read error during body copy: unexpected EOF".
	// Those are routine disconnects, not faults — route ErrorLog
	// through a filter that drops them and keeps everything else.
	proxy.ErrorLog = log.New(filteredProxyLog{}, "", 0)
	// Strip the prefix so the child sees `/_sky/event` instead of
	// `/_sky/console/_sky/event`. Both Sky.Live and Sky.Http.Server
	// register their routes at root-relative paths, so prefix-strip
	// makes the routing work without any child-side awareness.
	handler := http.StripPrefix(prefix, proxy)
	mux.Handle(prefix+"/", handler)
	// Without this redirect, a request to bare `/admin` (no slash)
	// would 404: the StripPrefix above only matches `/admin/`. Same
	// pattern that http.FileServer uses for directory paths.
	mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, prefix+"/", http.StatusTemporaryRedirect)
	})
	fmt.Fprintf(os.Stderr, "[sky.subapp] mounted %s -> 127.0.0.1:%d\n", prefix, port)
	return nil
}

// ============================================================================
// Internals
// ============================================================================

// subAppNamespaceFromPath derives a label-safe namespace string
// from a mount prefix. "/_sky/console" → "console";
// "/admin/v2" → "admin_v2". Empty or all-symbol input falls back
// to "subapp" so the label always has a non-empty value.
//
// Rules:
//   * Trim leading "/"
//   * Drop any "_sky/" prefix (internal mounts) so the console
//     namespace is "console", not "_sky_console"
//   * Replace each "/" with "_"
//   * Strip anything outside [a-zA-Z0-9_]
func subAppNamespaceFromPath(p string) string {
	p = strings.Trim(p, "/")
	p = strings.TrimPrefix(p, "_sky/")
	p = strings.Trim(p, "/")
	var b strings.Builder
	for _, c := range p {
		switch {
		case c == '/':
			b.WriteByte('_')
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_':
			b.WriteRune(c)
		}
	}
	out := b.String()
	if out == "" {
		return "subapp"
	}
	return out
}

// pickFreeLocalhostPort asks the kernel for a free localhost port
// via net.Listen(":0"), immediately closes the listener, and
// returns the port number. There's a brief race window between
// close and the child's Listen — in practice not an issue because
// the kernel doesn't reuse ports for ~2s by default.
func pickFreeLocalhostPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port, nil
}

// waitForPort polls until tcp connect succeeds OR attempts exhausted.
func waitForPort(host string, port int, attempts int, every time.Duration) bool {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	for i := 0; i < attempts; i++ {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return true
		}
		time.Sleep(every)
	}
	return false
}

// resolveSkyBinary picks the best `sky` to invoke for sub-app
// spawning. Priority:
//  1. SKY_BIN env override (explicit user intent).
//  2. The running parent's argv[0] IF it's named `sky` (or ends
//     in /sky) — typical when the user is running `sky run` and the
//     compiled app inherits the parent's PATH.
//  3. exec.LookPath("sky") — first match on PATH.
func resolveSkyBinary() (string, error) {
	if v := strings.TrimSpace(os.Getenv("SKY_BIN")); v != "" {
		if _, err := os.Stat(v); err == nil {
			return v, nil
		}
	}
	if p, err := exec.LookPath("sky"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("sky binary not found (set SKY_BIN or add sky to PATH)")
}

// ── child supervision ──────────────────────────────────────────────
//
// Children spawned via MountSubApp are tracked in a singleton
// registry. The parent's signal handler (which Sky.Live + Sky.Http
// already install) calls ShutdownSubApps to tear them down before
// the parent exits.

var (
	subAppMu       sync.Mutex
	subAppChildren []subAppChild
)

type subAppChild struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
}

func registerSubAppChild(cmd *exec.Cmd, cancel context.CancelFunc) {
	subAppMu.Lock()
	defer subAppMu.Unlock()
	subAppChildren = append(subAppChildren, subAppChild{cmd: cmd, cancel: cancel})
}

// consoleAutoMounted records whether the Std.Ui sub-app console has
// been wired onto a mux. Set by maybeAutoMountConsole on success;
// MountConsoleEndpoints (the legacy hand-written /_sky/console)
// reads this and skips its own mount when the new console is
// active, avoiding pattern conflicts AND avoiding mixed-version
// dispatch (e.g. legacy /_sky/console/api/overview being shadowed
// by the sub-app's catch-all).
//
// Atomic for ordering safety across the Sky.Live + Sky.Http.Server
// callers; both run setup serially today, but a future shared-mux
// scenario would race.
var consoleAutoMounted atomic.Bool

// ConsoleAutoMounted exposes the flag for read-only inspection,
// used by MountConsoleEndpoints in observability.go to decide
// whether the legacy console routes should register.
func ConsoleAutoMounted() bool { return consoleAutoMounted.Load() }

// maybeAutoMountConsole auto-mounts the bundled Sky Console as a
// sub-app at /_sky/console on `mux`, gated on a stack of conditions
// that mirror the dev-banner's visibility rule:
//
//   1. NOT productionFromEnv() — never spawn the console for
//      staging / production deployments (same rule the dev banner
//      uses, so the banner + console either both appear or both
//      stay hidden).
//   2. SKY_CONSOLE_EMBED != "off" / "0" / "false" — explicit
//      opt-out escape hatch.
//   3. parentBasePath == "" — never auto-mount inside an already-
//      mounted sub-app (the console itself, when running as the
//      child, would otherwise try to spawn ANOTHER console under
//      itself, leading to infinite fork-bomb territory).
//   4. SKY_BIN or PATH must reach a usable `sky` binary.
//
// Any failure is logged + skipped — the parent app's startup is
// never blocked by console-mount issues; the legacy
// MountConsoleEndpoints path then provides a working fallback.
func maybeAutoMountConsole(mux *http.ServeMux, parentBasePath string, parentPort int) {
	if parentBasePath != "" {
		// We're running AS a sub-app — don't recursively mount
		// another console inside ourselves.
		return
	}
	if v := os.Getenv("SKY_CONSOLE_EMBED"); v == "off" || v == "0" || v == "false" {
		return
	}

	// PRO+ CONSOLE — a single per-app admin secret unlocks the
	// console AND the /_sky/metrics scrape behind the same trust
	// domain. Canonical env var SKY_ADMIN_TOKEN; SKY_METRICS_TOKEN
	// and SKY_CONSOLE_TOKEN_SECRET are honoured as v0.14.21 / v0.14.20
	// legacy aliases (see adminTokenSecret). Setting any of them:
	//   – wraps /_sky/console in MountConsoleAuth (JWT URL token
	//     + session cookie; see console_auth.go),
	//   – leaves the production gate on for the dev banner.
	// The control-plane (skydeploy.app) mints the URL JWT signed
	// with the same secret it provisions per tenant.
	if secret := adminTokenSecret(); secret != "" {
		IngestTokenInit()
		if err := MountConsoleAuth(mux, parentPort, secret); err == nil {
			consoleAutoMounted.Store(true)
		}
		return
	}

	// DEV-MODE CONSOLE — production gate blocks; everything below
	// is the existing pre-2026-05-23 behaviour, unchanged.
	if productionFromEnv() {
		return
	}
	// IMPORTANT: materialise the ingest token NOW so SpawnSkyConsole
	// → CurrentIngestToken() reads a real value when seeding the
	// child's SKY_INGEST_TOKEN env. Without this, the parent's
	// MountObservabilityEndpoints call (which would otherwise init
	// the token first) runs AFTER spawn, the child gets
	// SKY_INGEST_TOKEN="" and every push hits 401.
	IngestTokenInit()
	// Spawn failures already log via MountSubApp's internal
	// fmt.Fprintf — caller doesn't need to.
	if err := MountSubApp(mux, "/_sky/console", SpawnSkyConsole(parentPort)); err == nil {
		consoleAutoMounted.Store(true)
	}
}


// adminTokenSecret returns the per-app admin secret that gates
// every privileged Sky.Live surface — /_sky/console (HS256 JWT
// signing), /_sky/metrics (Bearer auth), and any future admin-
// only endpoint. One secret per app, multiple surfaces, one
// trust domain.
//
// The canonical env var is SKY_ADMIN_TOKEN. Two legacy aliases
// are honoured for tenants seeded on earlier Sky versions:
//
//   - SKY_METRICS_TOKEN — v0.14.21's first-pass unification name
//     (kept "metrics" in the name; promoted to admin-wide).
//   - SKY_CONSOLE_TOKEN_SECRET — v0.14.20's console-specific
//     secret before any unification.
//
// Returns "" when nothing is set — Pro+ console auth stays off
// and the deploy falls back to dev-mode rules (nothing mounts
// in production).
func adminTokenSecret() string {
	if s := os.Getenv("SKY_ADMIN_TOKEN"); s != "" {
		return s
	}
	if s := os.Getenv("SKY_METRICS_TOKEN"); s != "" {
		return s
	}
	return os.Getenv("SKY_CONSOLE_TOKEN_SECRET")
}


// consoleAdminSecret is a thin alias kept so callers that imported
// this name from v0.14.21 keep compiling. Use adminTokenSecret for
// new code.
func consoleAdminSecret() string {
	return adminTokenSecret()
}


// MountConsoleAuth mounts the Sky Console as a token-gated sub-app
// at /_sky/console. Used by Pro+ deploy mode to expose the console
// in production with the owner's skydeploy.app session validating
// access. The token is a JWT issued by the control-plane and
// signed with the shared SKY_CONSOLE_TOKEN_SECRET; this runtime
// verifies + issues a session cookie + strips token from URL.
// See console_auth.go for the middleware + cookie flow.
//
// Mirrors MountSubApp's proxy setup but inserts consoleTokenAuth
// between the mux and the reverse-proxy.
func MountConsoleAuth(mux *http.ServeMux, parentPort int, secret string) error {
	if mux == nil {
		return fmt.Errorf("MountConsoleAuth: mux is nil")
	}
	const prefix = "/_sky/console"

	ctx, cancel := context.WithCancel(context.Background())
	port, cmd, err := SpawnSkyConsole(parentPort)(ctx, prefix)
	if err != nil {
		cancel()
		fmt.Fprintf(os.Stderr, "[sky.console-auth] mount skipped: %v\n", err)
		return err
	}
	registerSubAppChild(cmd, cancel)

	target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
			return
		}
		fmt.Fprintf(os.Stderr, "[sky.console-auth] proxy %s %s -> %s: %v\n",
			r.Method, r.URL.Path, target.Host, err)
		w.WriteHeader(http.StatusBadGateway)
	}
	proxy.ErrorLog = log.New(filteredProxyLog{}, "", 0)

	// Auth wraps the prefix-stripped proxy. StripPrefix is OUTSIDE
	// the auth gate so the gate sees the original `/_sky/console`
	// path (its 401 page and redirect-to-strip-token logic both
	// use r.URL.Path / RequestURI as-is).
	gated := consoleTokenAuth(secret, http.StripPrefix(prefix, proxy))

	mux.Handle(prefix+"/", gated)
	// Bare path (no trailing slash) — the iframe src from skydeploy
	// hits exactly this URL with `?token=…`. Don't pre-redirect
	// before auth: the auth wrapper handles the token + sets the
	// cookie, then redirects to the slash-form WITHOUT the token.
	mux.HandleFunc(prefix, func(w http.ResponseWriter, r *http.Request) {
		target := prefix + "/"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
	})
	fmt.Fprintf(os.Stderr, "[sky.console-auth] mounted %s (token-gated) -> 127.0.0.1:%d\n", prefix, port)
	return nil
}

// filteredProxyLog is the io.Writer behind a reverse proxy's
// ErrorLog. It drops the routine mid-stream-disconnect lines
// (upstream EOF / context cancelled — a browser navigating away
// from an SSE stream, or a child dying during shutdown) and
// forwards anything genuinely actionable to stderr.
type filteredProxyLog struct{}

func (filteredProxyLog) Write(p []byte) (int, error) {
	s := string(p)
	if strings.Contains(s, "body copy") ||
		strings.Contains(s, "unexpected EOF") ||
		strings.Contains(s, "context canceled") {
		return len(p), nil // routine disconnect — swallow
	}
	return os.Stderr.Write(p)
}

// ShutdownSubApps signals every tracked child to stop. Idempotent.
// Sends SIGTERM via context cancel first; processes that don't
// exit within 2s receive SIGKILL.
func ShutdownSubApps() {
	subAppMu.Lock()
	children := subAppChildren
	subAppChildren = nil
	subAppMu.Unlock()
	for _, c := range children {
		c.cancel()
	}
	// Give children a brief chance to exit gracefully (SIGTERM via
	// ctx cancel), then escalate to SIGKILL on stragglers.
	deadline := time.Now().Add(2 * time.Second)
	for _, c := range children {
		if c.cmd == nil || c.cmd.Process == nil {
			continue
		}
		done := make(chan struct{})
		go func() { _, _ = c.cmd.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Until(deadline)):
			_ = c.cmd.Process.Kill()
		}
	}
}
