//go:build !js

package rt

import (
	cryptorand "crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"reflect"
	"strings"
	"syscall"
	"time"
)

// System_exit: never returns (process terminates) — kept eager and
// polymorphic per the rationale in lookupKernelType.
//
// IMPORTANT: os.Exit BYPASSES all `defer` blocks, including the
// terminal teardown deferred by Sky.Tui's tuiAppRun. If the user
// code calls System.exit from inside a Tui app, the terminal would
// be left in raw mode + alt-screen + dirty modes — readline broken
// for the rest of the shell session. Run tuiTeardown explicitly
// before os.Exit so the user's terminal is restored regardless of
// how the app exits.
//
// tuiTeardown is idempotent (deferred path will no-op when it
// already ran via this fast path). On non-Tui programs the active
// state is nil and the call returns immediately.
func System_exit(code any) any {
	tuiTeardown()
	// ExitProcess, not os.Exit: `Std.System.exit` is the ordinary way a
	// `Sky.Cli` job ends, and os.Exit skips generated main's
	// `defer rt.StopEmbeddedPostgres()`. A one-shot job built with `--embed`
	// would leave its own database running with nothing left to stop it.
	ExitProcess(AsInt(code))
	return struct{}{}
}

func Server_listen(port any, routes any) any {
	p := AsInt(port)
	routeList := AsList(routes)
	mux := http.NewServeMux()

	// v0.16.3 #466 follow-up: count paths so we know when to apply
	// method-aware registration. Method-aware patterns ("GET /api/x")
	// disambiguate two routes on the SAME path with DIFFERENT methods,
	// but they CONFLICT with wildcard-method routes registered on
	// MORE SPECIFIC paths (Go's mux gates this case — neither pattern
	// strictly more specific). The MountEmbeddedConsole subapp
	// registers `/_sky/console/_sky/event` wildcard-method, so a
	// blanket `GET /` from a user handler trips that gate. Fix:
	// use the method prefix ONLY when 2+ routes share the path —
	// otherwise stay path-only (which Go's mux happily treats as
	// "any method on this path"). Preserves the same-path-different-
	// method coexistence that #466 unlocked without breaking the
	// console mount.
	pathRouteCount := make(map[string]int, len(routeList))
	for _, r := range routeList {
		if rt, ok := r.(SkyRoute); ok && rt.StaticDir == "" {
			pathRouteCount[rt.Path]++
		}
	}

	for _, r := range routeList {
		route := r.(SkyRoute)
		handler := route.Handler
		pattern := route.Path

		// Static-file routes (registered via Server_static) bypass
		// the Sky-handler dispatch entirely. http.FileServer +
		// http.StripPrefix delivers path-traversal protection,
		// MIME detection, Last-Modified, and Range support — all
		// the things a hand-rolled static handler in Sky would
		// have to re-implement.
		//
		// Pattern is already prefix-form ("/static/" with trailing
		// slash) so ServeMux longest-prefix-matches every nested
		// path. StripPrefix removes the prefix (keeping the
		// trailing slash) before FileServer maps the rest onto the
		// directory.
		if route.StaticDir != "" {
			stripPattern := pattern
			if len(stripPattern) > 1 && stripPattern[len(stripPattern)-1] == '/' {
				stripPattern = stripPattern[:len(stripPattern)-1]
			}
			mux.Handle(pattern, http.StripPrefix(stripPattern, http.FileServer(http.Dir(route.StaticDir))))
			continue
		}

		// v0.16.3 fix(#466) + refinement: register with Go 1.22+
		// method-aware mux pattern ONLY when 2+ routes share this
		// path (i.e. same-path-different-method case that #466 was
		// originally about). For unique paths, stay path-only — that
		// keeps Go's "wildcard-method on more-specific path" conflict
		// rule from tripping on the console mount (#466 follow-up,
		// caught 2026-06-04 by VerifyScenarioSpec via 15-http-server).
		// Translate `:name` → `{name}` so Go's mux captures the segment
		// (raw `:name` matched only the literal path). paramNames drives
		// req.PathValue read-back inside the handler below.
		translated, paramNames := colonToMuxPattern(pattern)
		muxPattern := translated
		if pathRouteCount[pattern] > 1 && route.Method != "" && route.Method != "*" {
			muxPattern = route.Method + " " + translated
		}
		mux.HandleFunc(muxPattern, func(w http.ResponseWriter, req *http.Request) {
			// Panic recovery — one bad handler mustn't kill the process.
			// Audit P1-5: prod-mode logs omit the Go stack trace from
			// stderr (to avoid leaking internal paths + memory
			// addresses) and write the full frame to .skylog/panic.log
			// for post-mortem inspection. Dev mode keeps the full
			// stack on stderr for fast-feedback debugging.
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				// http.ErrAbortHandler is Go's sentinel value
				// handlers use to abort cleanly (httputil.
				// ReverseProxy panics with it when a client
				// disconnects mid-SSE-stream). Re-panic so
				// net/http's own handler-recover treats it as
				// the no-op abort it's meant to be.
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				logPanicFrame(req.Method, req.URL.Path, rec)
				w.WriteHeader(500)
				fmt.Fprint(w, "Internal Server Error")
			}()
			// Bound body read to prevent memory exhaustion.
			req.Body = http.MaxBytesReader(w, req.Body, serverMaxBodyBytes)

			skyReq := SkyRequest{
				Method:     req.Method,
				Path:       req.URL.Path,
				Headers:    make(map[string]any),
				Params:     make(map[string]any),
				Query:      make(map[string]any),
				Cookies:    make(map[string]string),
				RemoteAddr: req.RemoteAddr,
			}
			for _, ck := range req.Cookies() {
				skyReq.Cookies[ck.Name] = ck.Value
			}
			for k, v := range req.Header {
				if len(v) > 0 {
					skyReq.Headers[k] = v[0]
				}
			}
			if req.Body != nil {
				bodyBytes, err := io.ReadAll(req.Body)
				if err != nil {
					w.WriteHeader(413) // Payload Too Large
					fmt.Fprint(w, "request body too large")
					return
				}
				skyReq.Body = string(bodyBytes)
			}
			// Parse form data (application/x-www-form-urlencoded)
			// from the body so Server.formValue works.
			if req.Method == "POST" || req.Method == "PUT" || req.Method == "PATCH" {
				skyReq.Form = make(map[string]string)
				ct := req.Header.Get("Content-Type")
				if strings.HasPrefix(ct, "application/x-www-form-urlencoded") || ct == "" {
					vals, err := url.ParseQuery(skyReq.Body)
					if err == nil {
						for k, v := range vals {
							if len(v) > 0 {
								skyReq.Form[k] = v[0]
							}
						}
					}
				}
			}
			for k, v := range req.URL.Query() {
				if len(v) > 0 {
					skyReq.Query[k] = v[0]
				}
			}
			// URL path captures — Server.param reads these. paramNames was
			// derived from the `:name` segments this route registered.
			for _, pn := range paramNames {
				skyReq.Params[pn] = req.PathValue(pn)
			}

			// Call the Sky handler and invoke the returned Task
			// thunk. SkyCall uses reflect so it accepts any
			// callable shape (any/typed codegen both work).
			// anyTaskInvoke normalises the thunk regardless of
			// whether it's `func() any`, `SkyTask[any, any]`, or
			// an already-resolved SkyResult.
			task := SkyCall(handler, skyReq)
			result := any(anyTaskInvoke(task))

			// Accept both the bare SkyResult[any,any] AND the
			// wider typed SkyResult shapes that typed codegen may
			// now emit. Fall back via reflect.
			resp, ok := result.(SkyResult[any, any])
			if !ok {
				rv := reflect.ValueOf(result)
				if rv.IsValid() && rv.Kind() == reflect.Struct {
					tagF := rv.FieldByName("Tag")
					okF := rv.FieldByName("OkValue")
					if tagF.IsValid() && okF.IsValid() {
						resp = SkyResult[any, any]{
							Tag:     int(tagF.Int()),
							OkValue: okF.Interface(),
						}
						ok = true
					}
				}
			}
			if ok && resp.Tag == 0 {
				// v0.15.44: bridge typed Sky_Http_Server_Response_R
				// (record alias declared in Layer-3 Server.sky) into
				// the runtime SkyResponse shape. Older handlers that
				// return bare `rt.SkyResponse` (FFI direct path,
				// Server_text/json/html before Layer-3 wrap) keep
				// the fast-path assertion via asSkyResponse.
				skyResp, okR := asSkyResponse(resp.OkValue)
				if !okR {
					w.WriteHeader(500)
					fmt.Fprint(w, "Internal Server Error")
					return
				}
				// v0.15.46: WebSocket upgrade.  The user's handler returned
				// Server.WebSocket.upgrade; asSkyResponse has already resolved
				// the cfg out of the pending registry into WSUpgrade (draining
				// the token). Hijack the connection and run the upgrade-and-loop
				// dance.
				if skyResp.WSUpgrade != nil {
					serveWebSocketUpgrade(w, req, *skyResp.WSUpgrade)
					return
				}
				// Streaming response (Sky.Http.Server.Stream): dispatch
				// the user's handler over a chunk-writer instead of
				// buffering the body. The branch sets headers + flushes,
				// then drives the handler Task to completion. After
				// return the connection closes naturally.
				if skyResp.StreamHandler != nil {
					serveStreamingResponse(w, req, skyResp)
					return
				}
				// Apply the response's default ContentType FIRST so the
				// Headers map (populated by Server.withHeader) can
				// override it. Otherwise an explicit
				// `withHeader "Content-Type" "application/javascript"`
				// applied to a Server.text/json/html response would be
				// silently clobbered by the default ("text/plain" etc).
				if skyResp.ContentType != "" {
					w.Header().Set("Content-Type", skyResp.ContentType)
				}
				applySkyResponseHeaders(w.Header(), req, skyResp)
				// Safe-by-default security headers (callers can override);
				// honours SKY_LIVE_FRAME_ANCESTORS for embeddable deploys.
				setSecurityHeaders(w.Header())
				// CSRF auto-injection: for HTML responses, walk every
				// `<form method="POST">` (case-insensitive on both tag
				// and attribute) and inject a hidden `__sky_csrf` input
				// just inside the opening tag. The submitted token will
				// match the cookie via the `r.FormValue("__sky_csrf")`
				// fallback in the CSRF middleware. Skip injection when
				// the form already declares the field (idempotent on
				// double-render). User code stays clean — no per-form
				// boilerplate.
				body := skyResp.Body
				if strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
					tok := CurrentCsrfToken(req)
					if tok != "" {
						body = injectCsrfIntoForms(body, tok)
					}
					// Dev-only "🔍 Console" floating link. Injected
					// just before </body> so it lives outside any
					// user route container. Returns "" in production
					// (productionFromEnv() == true), making this a
					// no-op for staging / prod deployments.
					if banner := devBannerHTML(); banner != "" {
						body = injectDevBanner(body, banner)
					}
				}
				if skyResp.Status > 0 {
					w.WriteHeader(skyResp.Status)
				}
				fmt.Fprint(w, body)
			} else {
				w.WriteHeader(500)
				fmt.Fprint(w, "Internal Server Error")
			}
		})
	}

	// Observability endpoints (Phase 1.1a Step 4). Mount BEFORE
	// any of the user's routes so the catch-all "/" pattern in
	// user code doesn't shadow them. Opt-out via
	// OBSERVABILITY_DISABLED=1.
	// v0.16.0: in-process inline Sky Console mount. Replaces the
	// v0.15.x subprocess + reverse-proxy mount. Must run BEFORE
	// MountObservabilityEndpoints so the legacy console HTML root
	// inside it can defer to the inline one. Same gates as
	// Sky.Live (production+no-secret → skipped; SKY_LIVE_BASE_PATH
	// set → skipped; SKY_CONSOLE_EMBED=off → skipped). No port
	// argument needed — handler runs on this mux directly.
	_ = p
	// v0.16.1 PR7 — seed SKY_PARENT_URL so the inline console_app's
	// init_ reads OUR OWN listener's loopback when it builds the
	// initial Model. Mirror of the liveAppRun setup; see that path's
	// comment for the full rationale. Same safety: StartPushExporter
	// needs BOTH SKY_PARENT_URL + SKY_LIVE_NAMESPACE — we only seed
	// the URL, so push-export stays inactive for standalone apps.
	if os.Getenv("SKY_PARENT_URL") == "" {
		os.Setenv("SKY_PARENT_URL", fmt.Sprintf("http://127.0.0.1:%d", p))
	}
	MountEmbeddedConsole(mux)
	MountObservabilityEndpoints(mux)
	// v0.16.1 PR 2 — boot-time mount-precedence invariant. When the
	// user EXPLICITLY asked for a console (SKY_CONSOLE_AUTH=token|app,
	// not a sub-app, SKY_CONSOLE_EMBED not off) but neither the
	// inline nor the legacy mount actually claimed /_sky/console,
	// this prints a FATAL stderr line + os.Exit(1).
	AssertConsoleInvariantOrExit()
	// If THIS process is a sub-app (SKY_PARENT_URL + SKY_LIVE_NAMESPACE
	// set), start the push-exporter so Log.* / metric / span writes
	// flow back to the parent. No-op for standalone runs.
	StartPushExporter()
	// Production-mode gate — single source of truth in
	// `productionFromEnv`: any ENV / SKY_ENV value EXCEPT
	// {"dev", "development", "local"} gates. Unset → open.
	SetProductionMode(productionFromEnv())

	// Step 7 — OTel tracer init. Same shape as Sky.Live; non-fatal
	// on failure (logs + continues with noop tracer).
	if err := InitTracingFromEnv(); err != nil {
		fmt.Fprintf(os.Stderr, "[sky.http] OTel init failed (continuing without trace export): %v\n", err)
	}

	// Wrap with CSRF (Phase 1.2) + observability (Phase 1.1a Step 3).
	// Order: observability is OUTER so CSRF rejections still get
	// metered as 403 — surfaces attacks / misconfigs in dashboards.
	csrfed := CSRFMiddleware(mux)
	observed := ObservabilityMiddleware(csrfed)

	srv := &http.Server{
		// bindAddr → 127.0.0.1:port in dev, :port (all interfaces) in
		// prod, SKY_HOST:port when set. Shared with Sky.Live so the two
		// listeners cannot drift. See resolveBindHost (live.go).
		Addr:              bindAddr(p),
		Handler:           observed,
		ReadHeaderTimeout: httpEnvTimeout("SKY_HTTP_READ_HEADER_TIMEOUT", serverReadHeaderTimeout),
		ReadTimeout:       httpEnvTimeout("SKY_HTTP_READ_TIMEOUT", serverReadTimeout),
		WriteTimeout:      httpEnvTimeout("SKY_HTTP_WRITE_TIMEOUT", serverWriteTimeout),
		IdleTimeout:       httpEnvTimeout("SKY_HTTP_IDLE_TIMEOUT", serverIdleTimeout),
		MaxHeaderBytes:    serverMaxHeaderBytes,
	}
	// Under `--embed` the supervisor in pg_embed.go owns the shutdown
	// SEQUENCE, because the embedded database must be stopped strictly after
	// the app has stopped accepting and drained. Handing it the listener is
	// what makes its first phase real; it is a no-op registration when no
	// cluster is being supervised.
	RegisterAcceptStopper("http.Server", func() { _ = srv.Close() })
	// v0.16.0: inline console runs in-process — no children to
	// signal. Still install a SIGINT/SIGTERM/SIGHUP handler so the
	// server closes gracefully (drains in-flight requests) rather
	// than dropping connections.
	srvSigCh := make(chan os.Signal, 2)
	signal.Notify(srvSigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-srvSigCh
		// v0.16.1: drain HubExporter (and any other shutdown
		// hook) BEFORE srv.Close so pending telemetry pushes
		// reach the hub within Cloud Run / k8s grace windows.
		// 8 s budget matches Sky.Live's signal handler. The
		// release phase that follows the drain closes whatever
		// registered a resource closer (a mounted sub-app's
		// session store, on this shape).
		drainAndRelease(8*time.Second, func() { _ = srv.Close() })
	}()
	fmt.Printf("Sky server listening on http://localhost:%d\n", p)
	printStartupReport(p) // see startup_report.go — added under, never in place of
	err := srv.ListenAndServe()
	signal.Stop(srvSigCh)
	// If the listener closed because the embedded-PostgreSQL supervisor is
	// mid-shutdown, returning here would let main exit and take the database
	// down with a kill instead of a clean stop. It exits the process itself
	// once PostgreSQL is down.
	BlockIfEmbeddedShuttingDown()
	if err != nil && err != http.ErrServerClosed {
		if isAddrInUse(err) {
			reportPortInUse(p, "pass a different port to Server.listen")
			ExitProcess(1)
		}
		return Err[any, any](ErrFfi(err.Error()))
	}
	return Ok[any, any](struct{}{})
}

// Server_api registers an API route — a REST / machine-to-machine
// endpoint (server-to-server calls, webhooks) that authenticates via
// Bearer token or HMAC, NOT the browser session cookie. API routes
// bypass CSRF protection: the double-submit CSRF guard is a
// browser-form-forgery defence for cookie-authed requests, and an
// API client neither has nor needs a CSRF token. Mirrors Sky.Live's
// Live.api so the "API endpoint" category is consistent across both
// server kinds.
//
// `spec` is "METHOD /path" (e.g. "POST /v1/generate"); an omitted
// method matches any verb.
func Server_api(spec any, handler any) any {
	s := fmt.Sprintf("%v", spec)
	method, pattern := "", s
	if idx := strings.Index(s, " "); idx > 0 {
		method = strings.ToUpper(strings.TrimSpace(s[:idx]))
		pattern = strings.TrimSpace(s[idx+1:])
	}
	WithoutCsrf(pattern)
	return SkyRoute{Method: method, Path: pattern, Handler: handler}
}

// Middleware.withCsrf : Handler -> Handler
//
// task #663 — CSRF protection via the double-submit cookie pattern.
// See sky-stdlib/Sky/Http/Middleware.sky's `withCsrf` docstring for
// the full contract.  Defaults (cookie name `__Host-sky_csrf`,
// header `X-Csrf-Token`, form field `_csrf`) are baked in; a future
// config-record overload can ship if real apps demand it.
//
// Token gen: 32 bytes from crypto/rand → base64-URL (no padding).
// Token compare: subtle.ConstantTimeCompare (no timing leak).
// Cookie attrs: Path=/; Secure; SameSite=Lax — unconditional, because
// the `__Host-` name prefix mandates Secure (RFC 6265bis §4.1.3.2) and a
// client rejects the cookie without it, in dev as well as production.
// (`securifyCookieAttrs` only ever APPENDS Secure, never strips it, so
// the literal in the attrs below stands on its own either way.)
func Middleware_withCsrf(handler any) any {
	const (
		csrfCookie    = "__Host-sky_csrf"
		csrfHeader    = "X-Csrf-Token"
		csrfFormField = "_csrf"
	)
	return func(req any) any {
		return func() any {
			r, ok := asSkyRequest(req)
			if !ok {
				// Non-request shape: defer to handler.
				task := SkyCall(handler, req)
				return any(anyTaskInvoke(task))
			}
			method := strings.ToUpper(r.Method)
			isSafe := method == "GET" || method == "HEAD" || method == "OPTIONS"

			if isSafe {
				// Safe method — pass through; ensure cookie is set
				// if not already present so the next unsafe request
				// can supply the matching token.
				task := SkyCall(handler, req)
				var resp any = anyTaskInvoke(task)
				if _, hasCookie := r.Cookies[csrfCookie]; !hasCookie {
					b := make([]byte, 32)
					if _, err := cryptorand.Read(b); err == nil {
						token := base64.RawURLEncoding.EncodeToString(b)
						// anyTaskInvoke returns the handler's payload —
						// commonly Ok[any, any](SkyResponse{...}). Unwrap
						// the SkyResponse so the Set-Cookie header lands
						// on the response struct, then re-wrap.
						// `__Host-` mandates Secure (RFC 6265bis
						// §4.1.3.2) — not env-conditional.
						cookieHeader := fmt.Sprintf("%s=%s; %s",
							csrfCookie, token, "Path=/; Secure; SameSite=Lax")
						// Unwrap via asSkyResponse, NOT a raw
						// `.(SkyResponse)` assertion: a handler whose
						// return type is the typed Sky record
						// (`Sky_Http_Server_Response_R`) failed that
						// assertion and fell through to
						// `setCookieHeader(resp, …)` — whose argument
						// was the Ok WRAPPER, which asSkyResponse
						// rejects, so the CSRF cookie was dropped
						// entirely and every later POST 403'd.
						if okResult, isResult := resp.(SkyResult[any, any]); isResult && okResult.Tag == 0 {
							if sr, isResp := asSkyResponse(okResult.OkValue); isResp {
								return Ok[any, any](any(addSetCookie(sr, cookieHeader)))
							}
						}
						// Fallback for handlers that return a bare
						// response rather than Ok-wrapped.
						if sr, isResp := asSkyResponse(resp); isResp {
							resp = addSetCookie(sr, cookieHeader)
						}
					}
				}
				return resp
			}

			// Unsafe method — validate.
			cookieToken, hasCookie := r.Cookies[csrfCookie]
			if !hasCookie || cookieToken == "" {
				return Ok[any, any](SkyResponse{
					Status:  403,
					Body:    "CSRF protection: missing token cookie. Submit the form after first loading a safe (GET) page.",
					Headers: map[string]string{"Content-Type": "text/plain"},
				})
			}
			// Header takes precedence over form field.
			var providedToken string
			if v, ok := r.Headers[csrfHeader].(string); ok && v != "" {
				providedToken = v
			} else if r.Form != nil {
				if v, ok := r.Form[csrfFormField]; ok {
					providedToken = v
				}
			}
			if providedToken == "" {
				return Ok[any, any](SkyResponse{
					Status:  403,
					Body:    "CSRF protection: missing token in request. Include the token via X-Csrf-Token header or _csrf form field.",
					Headers: map[string]string{"Content-Type": "text/plain"},
				})
			}
			if subtle.ConstantTimeCompare([]byte(cookieToken), []byte(providedToken)) != 1 {
				return Ok[any, any](SkyResponse{
					Status:  403,
					Body:    "CSRF protection: token mismatch. Re-load the form and re-submit.",
					Headers: map[string]string{"Content-Type": "text/plain"},
				})
			}
			// Validated — defer to handler.
			task := SkyCall(handler, req)
			return any(anyTaskInvoke(task))
		}
	}
}

// Server.withCookie — flexible arity so Sky can pipe either a pre-built
// cookie object or a name/value/attrs triple straight into a response.
// Forms:
//
//	withCookie(Cookie, Response) -> Response
//	withCookie(name, value, Response) -> Response      (no extra attrs)
//	withCookie(name, value, attrs, Response) -> Response
func Server_withCookie(args ...any) any {
	switch len(args) {
	case 2:
		cookie, resp := args[0], args[1]
		// v0.15.44: bridge typed Sky_Http_Server_Response_R.
		r, ok := asSkyResponse(resp)
		if !ok {
			return resp
		}
		c, cok := cookie.(SkyCookie)
		if !cok {
			return resp
		}
		return addSetCookie(r, fmt.Sprintf("%s=%s; %s", c.Name, c.Value,
			securifyCookieAttrs("Path=/; HttpOnly; SameSite=Lax")))
	case 3:
		name, value, resp := args[0], args[1], args[2]
		return setCookieHeader(resp, fmt.Sprintf("%v", name), fmt.Sprintf("%v", value), "Path=/; HttpOnly; SameSite=Lax")
	case 4:
		name, value, attrs, resp := args[0], args[1], args[2], args[3]
		return setCookieHeader(resp, fmt.Sprintf("%v", name), fmt.Sprintf("%v", value), fmt.Sprintf("%v", attrs))
	default:
		return nil
	}
}

func setCookieHeader(resp any, name, value, attrs string) any {
	r, ok := asSkyResponse(resp)
	if !ok {
		return resp
	}
	return addSetCookie(r, fmt.Sprintf("%s=%s; %s", name, value, securifyCookieAttrs(attrs)))
}

// Server.csrfIssue : SkyResponse -> ( String, SkyResponse )
// Generates a fresh token and attaches it as a Set-Cookie header on
// the response. Returns the token + updated response as a Sky tuple
// so the caller can embed the token in their HTML form.
func Server_csrfIssue(resp any) any {
	r, ok := asSkyResponse(resp)
	if !ok {
		// Honour the contract even when the wrong shape comes in;
		// the caller's pattern-match will catch the (empty, resp)
		// pair if the response wasn't a SkyResponse.
		return SkyTuple2{V0: "", V1: resp}
	}
	token := generateCsrfToken()
	r = addSetCookie(r, fmt.Sprintf(
		"%s=%s; %s", csrfCookieName, token,
		securifyCookieAttrs("Path=/; HttpOnly; SameSite=Strict")))
	return SkyTuple2{V0: token, V1: r}
}
