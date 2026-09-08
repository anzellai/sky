//go:build !js

package rt

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fix 6: the Sky.Spa static mount's SPA NotFound fallback. A request that maps to
// a REAL file must serve the file (200), never shadowed by the fallback; a
// genuinely-unmatched path must SSR the app's NotFound page (200 + data-sky-ssr),
// not a bare file-server 404. This is what makes a cold unmatched deep-link boot
// the shell and render NotFound instead of returning a text/plain 404.
func TestSpaStaticFallback_realFileServedUnmatchedSSRsNotFound(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wasm_exec.js"), []byte("// go glue\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fileHandler := gzipStatic(http.StripPrefix("", http.FileServer(http.Dir(dir))))
	// A stand-in for the generated ssrHandler: returns an HTML NotFound page.
	notFound := func(_ any) any {
		return Task_succeed[any, any](Server_html(
			`<!doctype html><html><body><div id="app" data-sky-ssr="1">No such page here</div></body></html>`,
		))
	}
	h := spaStaticFallbackHandler(fileHandler, notFound)

	// A real asset → served as the file (200), fallback NOT invoked (not shadowed).
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wasm_exec.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("a real asset must serve 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "go glue") {
		t.Fatalf("a real asset must serve the FILE, got %q", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "No such page here") {
		t.Fatalf("a real asset must NOT be shadowed by the NotFound page")
	}

	// A genuinely-unmatched path → SSR the NotFound page (200), not a bare 404.
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/some/unknown/deep/path", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("an unmatched path must SSR NotFound (200), got %d", rec2.Code)
	}
	body := rec2.Body.String()
	if !strings.Contains(body, "No such page here") || !strings.Contains(body, "data-sky-ssr") {
		t.Fatalf("an unmatched path must render the SSR NotFound page, got %q", body)
	}
	if strings.Contains(body, "404 page not found") {
		t.Fatalf("the bare file-server 404 body must be suppressed, got %q", body)
	}
	if ct := rec2.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("the NotFound SSR must be text/html, got %q", ct)
	}
}
