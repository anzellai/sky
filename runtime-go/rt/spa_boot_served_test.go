//go:build !js

package rt

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// v0.25.19 regression: the SSR page loads the wasm boot loader from
// SpaBootPath (`/spa-boot.<hash>.js`), but the backend never served that path
// itself. It relied on `Server.staticNotFound "/" "../frontend/dist"` finding
// the file in dist. Behind a proxy that serves only `*.wasm` and
// `/wasm_exec.js` from dist (and forwards every other path), dist is not
// reachable from the backend's directory, so the request fell through to the
// SPA NotFound fallback: 200 text/html. The browser refused to run it, and the
// wasm client never booted.
//
// The backend now serves the loader from memory, like the Sky.Live client at
// liveClientPath, and a missing Sky-owned asset is a 404, never HTML.

// spaBackendRoutesNoDist is the route list the auto-split emits for an SSR
// backend, with a dist directory that does not exist.
func spaBackendRoutesNoDist(t *testing.T, dist string) []any {
	t.Helper()
	page := func(_ any) any {
		return Task_succeed[any, any](Server_html(
			`<!doctype html><html><body><div id="app" data-sky-ssr="1">SSR page</div></body></html>`,
		))
	}
	return []any{
		Server_api("GET /{$}", page),
		// A root-level route parameter: the loader route must not conflict
		// with it in the Go mux (a conflict panics at registration).
		Server_api("GET /{slug}", page),
		Server_api("POST /_rpc/Save", page),
		Server_staticNotFound("/", dist, page),
	}
}

func getSpaAsset(t *testing.T, h http.Handler, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSpaBackend_servesBootLoaderWithoutDist(t *testing.T) {
	dist := filepath.Join(t.TempDir(), "frontend", "dist") // never created
	h := serverRouteHandler(spaBackendRoutesNoDist(t, dist))

	rec := getSpaAsset(t, h, http.MethodGet, SpaBootPath, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: want 200, got %d (%s)", SpaBootPath, rec.Code, rec.Header().Get("Content-Type"))
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/javascript; charset=utf-8" {
		t.Fatalf("GET %s: want text/javascript; charset=utf-8, got %q (body %.80q)", SpaBootPath, ct, rec.Body.String())
	}
	if rec.Body.String() != SpaBootJS {
		t.Fatalf("GET %s: body is not SpaBootJS:\n%s", SpaBootPath, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Fatalf("GET %s: want an immutable Cache-Control, got %q", SpaBootPath, cc)
	}
	if v := rec.Header().Get("X-Content-Type-Options"); v != "nosniff" {
		t.Fatalf("GET %s: want X-Content-Type-Options nosniff, got %q", SpaBootPath, v)
	}
	if v := rec.Header().Get("Referrer-Policy"); v == "" {
		t.Fatalf("GET %s: the runtime security headers are missing", SpaBootPath)
	}

	// HEAD answers the same headers with no body.
	head := getSpaAsset(t, h, http.MethodHead, SpaBootPath, nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 ||
		!strings.HasPrefix(head.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("HEAD %s: want 200 text/javascript with no body, got %d %q (%d bytes)",
			SpaBootPath, head.Code, head.Header().Get("Content-Type"), head.Body.Len())
	}

	// gzip when the client accepts it.
	gz := getSpaAsset(t, h, http.MethodGet, SpaBootPath, map[string]string{"Accept-Encoding": "gzip"})
	if gz.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("GET %s with Accept-Encoding gzip: want Content-Encoding gzip, got %q",
			SpaBootPath, gz.Header().Get("Content-Encoding"))
	}
	zr, err := gzip.NewReader(gz.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil || string(plain) != SpaBootJS {
		t.Fatalf("gzipped body does not decode to SpaBootJS (err %v)", err)
	}

	// SKY_CSP=strict reaches the loader like every other runtime asset.
	t.Setenv("SKY_CSP", "strict")
	csp := getSpaAsset(t, h, http.MethodGet, SpaBootPath, nil)
	if !strings.Contains(csp.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("GET %s under SKY_CSP=strict: want the strict policy, got %q",
			SpaBootPath, csp.Header().Get("Content-Security-Policy"))
	}
}

// A missing Sky-owned asset is a 404, never the SPA HTML fallback, so a stale
// page fails loudly. App paths still SSR.
func TestSpaBackend_missingSkyAssetIs404NotHTML(t *testing.T) {
	dist := filepath.Join(t.TempDir(), "frontend", "dist") // never created
	h := serverRouteHandler(spaBackendRoutesNoDist(t, dist))

	for _, p := range []string{
		"/spa-boot.000000000000.js", // a stale loader hash
		"/_sky/live.000000000000.js",
		"/_sky/console-shell.000000000000.js",
		"/_sky/anything.css",
		"/wasm_exec.js",           // dist not reachable
		"/main.0123456789ab.wasm", // dist not reachable
	} {
		rec := getSpaAsset(t, h, http.MethodGet, p, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: want 404, got %d (%s)", p, rec.Code, rec.Header().Get("Content-Type"))
		}
		if ct := rec.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/html") ||
			strings.Contains(rec.Body.String(), "SSR page") {
			t.Errorf("GET %s: a Sky-owned asset must never be answered with HTML, got %q", p, ct)
		}
	}

	// An unmatched APP path still renders the NotFound page (the guard is
	// scoped to Sky-owned asset names).
	rec := getSpaAsset(t, h, http.MethodGet, "/no/such/page", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "SSR page") {
		t.Fatalf("an unmatched app path must still SSR, got %d %q", rec.Code, rec.Body.String())
	}
	// A root route parameter still reaches its handler.
	rec = getSpaAsset(t, h, http.MethodGet, "/about", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "SSR page") {
		t.Fatalf("GET /about must reach the /{slug} route, got %d", rec.Code)
	}
}

// When dist IS reachable, the runtime copy still wins over the file server
// (the build writes the same bytes, so this changes nothing but the source),
// and the dist wasm pair keeps serving from the file server.
func TestSpaBackend_bootLoaderWinsOverDist(t *testing.T) {
	dist := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dist, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(strings.TrimPrefix(SpaBootPath, "/"), "// a different copy\n")
	write("wasm_exec.js", "// go glue\n")
	h := serverRouteHandler(spaBackendRoutesNoDist(t, dist))

	rec := getSpaAsset(t, h, http.MethodGet, SpaBootPath, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != SpaBootJS {
		t.Fatalf("GET %s: want the runtime's SpaBootJS, got %d %q", SpaBootPath, rec.Code, rec.Body.String())
	}
	rec = getSpaAsset(t, h, http.MethodGet, "/wasm_exec.js", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "go glue") {
		t.Fatalf("GET /wasm_exec.js: want the dist file, got %d %q", rec.Code, rec.Body.String())
	}
}

// serverRouteHandler is the request path Server_listen serves, less the
// console, observability, CSRF and dedupe layers.
func serverRouteHandler(routeList []any) http.Handler {
	mux, rootFiles := serverRouteMux(routeList)
	return skyAssetGuard(mux, rootFiles)
}

// The SSR page names the wasm the build baked in, so it does not depend on the
// backend reaching ../frontend/dist at run time. Empty → scan the dist.
func TestSpaSSRWasmName_prefersTheBuiltName(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-dist")
	if got := Spa_ssrWasmNameBuilt("main.0123456789ab.wasm", missing); got != "main.0123456789ab.wasm" {
		t.Fatalf("a baked name must win without a dist, got %q", got)
	}
	if got := Spa_ssrWasmNameBuilt("", missing); got != "main.wasm" {
		t.Fatalf("no baked name and no dist → main.wasm, got %q", got)
	}
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "main.feedfacecafe.wasm"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Spa_ssrWasmNameBuilt("", dist); got != "main.feedfacecafe.wasm" {
		t.Fatalf("no baked name → scan the dist, got %q", got)
	}
}

// The Sky.Spa backend mounts the declared static dir live at /static AND the
// dist at /. A committed file that is only in dist/static (the deploy shipped
// the binary and the dist, not the backend's own copy) must still serve.
func TestStaticSubMountFallsBackToTheRootMount(t *testing.T) {
	dist := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dist, "static"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "static", "app.js"), []byte("// committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	live := t.TempDir() // the backend's own static dir: empty
	if err := os.WriteFile(filepath.Join(live, "upload.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	page := func(_ any) any { return Task_succeed[any, any](Server_html(`<html>page</html>`)) }
	h := serverRouteHandler([]any{
		Server_static("/static", live),
		Server_staticNotFound("/", dist, page),
	})
	rec := getSpaAsset(t, h, http.MethodGet, "/static/app.js", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "committed") ||
		!strings.Contains(rec.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("/static/app.js must fall back to dist/static: %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	rec = getSpaAsset(t, h, http.MethodGet, "/static/upload.png", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "png" {
		t.Fatalf("the live mount still serves its own files: %d", rec.Code)
	}
	rec = getSpaAsset(t, h, http.MethodGet, "/static/missing.js", nil)
	if rec.Code != http.StatusNotFound || strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("a file in neither dir is a plain 404: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}
