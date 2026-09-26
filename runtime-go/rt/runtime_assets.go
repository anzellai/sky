//go:build !js

package rt

// Runtime-owned assets: the scripts a Sky page loads from a Sky-owned URL, and
// the guard that keeps a missing one from being answered with a page.
//
// Every script a Sky page names by a content-hashed, Sky-owned URL is served
// by the runtime itself, from memory, whatever sits in front of the process:
//
//   - the Sky.Live client     /_sky/live.<hash>.js        (liveClientPath)
//   - the console shell       /_sky/console-shell.<hash>.js (consoleShellPath)
//   - the Sky.Spa boot loader /spa-boot.<hash>.js         (SpaBootPath)
//
// v0.25.19 moved the Sky.Spa loader into a file and relied on the backend's
// `Server.staticNotFound "/" "../frontend/dist"` to find it. Behind a proxy
// that serves only the wasm pair from dist and forwards every other path, the
// dist is not reachable from the backend's directory, so the request fell
// through to the SPA NotFound fallback: 200 text/html. The browser refused to
// run it ("MIME type ('text/html') is not executable") and the client never
// booted. The backend now registers the loader the way the Sky.Live runtime
// registers its client.
//
// The wasm pair (main.<hash>.wasm and wasm_exec.js) stays a dist file: the
// wasm is build output that an app may host on a CDN, and wasm_exec.js must
// match the Go toolchain that built that wasm. The backend serves the pair
// from its `/` static mount when the dist is reachable; otherwise the static
// host in front of it must (docs/skyspa/overview.md, "Deploying behind a
// proxy").
//
// skyAssetGuard enforces the other half: a request for a Sky-owned asset name
// that the process cannot serve is a 404, never the HTML of an app route or the
// SPA NotFound page, so a stale page fails loudly instead of running HTML as
// script.

import (
	"net/http"
	"path"
	"strconv"
	"strings"
)

// serveStaticJS serves a constant script under a content-hashed name. It goes
// through gzipStatic like every static file (gzip for a client that accepts
// it, and the strict policy under SKY_CSP=strict), and it carries the same
// security headers as a Sky handler response.
func serveStaticJS(js string) http.HandlerFunc {
	body := []byte(js)
	inner := gzipStatic(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "text/javascript; charset=utf-8")
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
		setSecurityHeaders(h)
		if r.Method == http.MethodHead {
			h.Set("Content-Length", strconv.Itoa(len(body)))
			return
		}
		_, _ = w.Write(body)
	}))
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		inner.ServeHTTP(w, r)
	}
}

// registerSpaBootLoader serves the Sky.Spa boot loader at SpaBootPath from
// memory. The pattern carries the GET method so it is more specific than both
// the `/` static catch-all and a root route parameter (`GET /{slug}`); a
// path-only pattern would conflict with the latter and panic the mux. A route
// the app registered at the same path keeps it.
func registerSpaBootLoader(mux *http.ServeMux, routeList []any) {
	for _, r := range routeList {
		if route, ok := r.(SkyRoute); ok && route.Path == SpaBootPath {
			return
		}
	}
	mux.Handle(http.MethodGet+" "+SpaBootPath, serveStaticJS(SpaBootJS))
}

// skyAssetKind classifies a request path by whether Sky owns the name.
type skyAssetKind int

const (
	notSkyAsset skyAssetKind = iota
	// skyNamespaceAsset — a script, style sheet or wasm under a `/_sky/`
	// segment (any base path or sub-app prefix).
	skyNamespaceAsset
	// skyDistAsset — a root-level name the Sky.Spa build writes to the
	// frontend dist: wasm_exec.js, main.wasm, main.<hash>.wasm,
	// spa-boot.<hash>.js.
	skyDistAsset
)

func classifySkyAsset(p string) skyAssetKind {
	name := p[strings.LastIndexByte(p, '/')+1:]
	switch path.Ext(name) {
	case ".js", ".mjs", ".css", ".map", ".wasm":
	default:
		return notSkyAsset
	}
	if strings.Contains(p, "/_sky/") {
		return skyNamespaceAsset
	}
	if strings.Count(p, "/") != 1 {
		return notSkyAsset
	}
	if name == "wasm_exec.js" || name == "main.wasm" ||
		isHashedName(name, "main.", ".wasm") || isHashedName(name, "spa-boot.", ".js") {
		return skyDistAsset
	}
	return notSkyAsset
}

// isHashedName reports whether name is prefix + <lower-case hex> + suffix.
func isHashedName(name, prefix, suffix string) bool {
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) ||
		len(name) <= len(prefix)+len(suffix) {
		return false
	}
	for _, c := range name[len(prefix) : len(name)-len(suffix)] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// muxRouteIsExact reports whether the mux pattern that would serve r names
// r's path exactly (a runtime asset or an app route registered for that
// file), rather than a catch-all or a route parameter.
func muxRouteIsExact(mux *http.ServeMux, r *http.Request) bool {
	_, pattern := mux.Handler(r)
	if i := strings.IndexByte(pattern, ' '); i >= 0 {
		pattern = pattern[i+1:]
	}
	return pattern == r.URL.Path
}

// skyAssetGuard wraps a server's route mux so a Sky-owned asset is never
// answered with HTML:
//
//   - A dist name (wasm_exec.js, main.<hash>.wasm, spa-boot.<hash>.js) that no
//     route names exactly goes straight to the `/` static mount (rootFiles),
//     bypassing route parameters and the SPA NotFound fallback. Present in the
//     dist → the file; absent → the file server's 404.
//   - Any other Sky-owned name goes through the mux, and an HTML answer is
//     replaced by a 404.
//
// rootFiles is nil when the server has no `/` static mount (Sky.Live, or a
// Sky.Http.Server app without one).
func skyAssetGuard(mux *http.ServeMux, rootFiles http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind := classifySkyAsset(r.URL.Path)
		if kind == notSkyAsset {
			mux.ServeHTTP(w, r)
			return
		}
		if kind == skyDistAsset && rootFiles != nil && !muxRouteIsExact(mux, r) {
			rootFiles.ServeHTTP(w, r)
			return
		}
		nw := &noHTMLAssetWriter{ResponseWriter: w}
		mux.ServeHTTP(nw, r)
		nw.finish()
	})
}

// noHTMLAssetWriter passes a response through unless it is HTML, which it
// replaces with a plain 404. The decision waits for the Content-Type: a
// handler that sets none has it sniffed from the first body bytes, the way
// net/http would.
type noHTMLAssetWriter struct {
	http.ResponseWriter
	status  int
	decided bool
	blocked bool
}

func (w *noHTMLAssetWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *noHTMLAssetWriter) WriteHeader(code int) {
	if w.decided {
		if !w.blocked {
			w.ResponseWriter.WriteHeader(code)
		}
		return
	}
	w.status = code
	if w.Header().Get("Content-Type") != "" {
		w.decide(nil)
	}
}

func (w *noHTMLAssetWriter) Write(b []byte) (int, error) {
	if !w.decided {
		w.decide(b)
	}
	if w.blocked {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}

// finish forwards a status the handler set but never followed with a body.
func (w *noHTMLAssetWriter) finish() {
	if !w.decided && w.status != 0 {
		w.decide(nil)
	}
}

func (w *noHTMLAssetWriter) decide(first []byte) {
	w.decided = true
	h := w.Header()
	ct := h.Get("Content-Type")
	if ct == "" && first != nil {
		ct = http.DetectContentType(first)
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct)), "text/html") {
		if w.status != 0 {
			w.ResponseWriter.WriteHeader(w.status)
		}
		return
	}
	w.blocked = true
	for _, k := range []string{"Content-Type", "Content-Length", "Content-Encoding", "Set-Cookie", "ETag", "Last-Modified"} {
		h.Del(k)
	}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.ResponseWriter.WriteHeader(http.StatusNotFound)
	_, _ = w.ResponseWriter.Write([]byte("404 page not found\n"))
}

// rootStaticDir is the directory of the route list's `/` static mount, or "".
func rootStaticDir(routeList []any) string {
	for _, r := range routeList {
		if route, ok := r.(SkyRoute); ok && route.StaticDir != "" && route.Path == "/" {
			return route.StaticDir
		}
	}
	return ""
}

// staticFallThrough serves from primary and, on a genuine 404 (no such file),
// from fallback. Every other status passes straight through.
func staticFallThrough(primary, fallback http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		iw := &spaNotFoundInterceptWriter{ResponseWriter: w}
		primary.ServeHTTP(iw, r)
		if !iw.swallowed404 {
			return
		}
		w.Header().Del("Content-Type")
		w.Header().Del("Content-Length")
		w.Header().Del("X-Content-Type-Options")
		fallback.ServeHTTP(w, r)
	})
}
