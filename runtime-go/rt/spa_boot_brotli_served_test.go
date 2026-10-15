//go:build !js

package rt

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The Sky.Spa boot loader (SpaBootPath) is served from memory by the backend.
// It was sent gzipped on the fly, never as brotli, though every current
// browser sends `Accept-Encoding: br`. A brotli request now gets the brotli
// bytes built into the runtime (spa_boot_brotli.go), with an exact
// Content-Length, an ETag of that representation and Vary: Accept-Encoding.

// brotliDecode decodes b with Node.js's zlib (the Go standard library has no
// brotli decoder). Fails the test when node is missing (SKY_LIVE_TESTS=skip
// skips it).
func brotliDecode(t *testing.T, b []byte) string {
	t.Helper()
	node := requireNode(t)
	f := filepath.Join(t.TempDir(), "in.br")
	if err := os.WriteFile(f, b, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, "-e",
		`process.stdout.write(require("zlib").brotliDecompressSync(require("fs").readFileSync(process.argv[1])))`,
		f).Output()
	if err != nil {
		t.Fatalf("node brotli decode: %v", err)
	}
	return string(out)
}

func TestSpaBackend_servesBootLoaderBrotli(t *testing.T) {
	dist := filepath.Join(t.TempDir(), "frontend", "dist") // never created
	h := serverRouteHandler(spaBackendRoutesNoDist(t, dist))
	accept := map[string]string{"Accept-Encoding": "gzip, deflate, br, zstd"}

	rec := getSpaAsset(t, h, http.MethodGet, SpaBootPath, accept)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s (br): want 200, got %d", SpaBootPath, rec.Code)
	}
	if ce := rec.Header().Get("Content-Encoding"); ce != "br" {
		t.Fatalf("GET %s with Accept-Encoding br: want Content-Encoding br, got %q", SpaBootPath, ce)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/javascript; charset=utf-8" {
		t.Fatalf("GET %s (br): want text/javascript; charset=utf-8, got %q", SpaBootPath, ct)
	}
	if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(rec.Body.Len()) {
		t.Fatalf("GET %s (br): Content-Length %q, body %d bytes", SpaBootPath, cl, rec.Body.Len())
	}
	if !strings.Contains(strings.Join(rec.Header().Values("Vary"), ","), "Accept-Encoding") {
		t.Fatalf("GET %s (br): want Vary: Accept-Encoding, got %q", SpaBootPath, rec.Header().Values("Vary"))
	}
	etag := rec.Header().Get("ETag")
	if !strings.HasSuffix(etag, `-br"`) {
		t.Fatalf("GET %s (br): want an ETag of the brotli representation, got %q", SpaBootPath, etag)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Fatalf("GET %s (br): want an immutable Cache-Control, got %q", SpaBootPath, cc)
	}
	if v := rec.Header().Get("X-Content-Type-Options"); v != "nosniff" {
		t.Fatalf("GET %s (br): the runtime security headers are missing", SpaBootPath)
	}
	if rec.Body.Len() >= len(SpaBootJS)/2 {
		t.Fatalf("GET %s (br): %d bytes for a %d-byte script; that is not brotli", SpaBootPath, rec.Body.Len(), len(SpaBootJS))
	}
	if got := brotliDecode(t, rec.Body.Bytes()); got != SpaBootJS {
		t.Fatalf("GET %s (br): the body does not decode to SpaBootJS (%d bytes decoded, want %d)", SpaBootPath, len(got), len(SpaBootJS))
	}

	// HEAD: the same headers, no body.
	head := getSpaAsset(t, h, http.MethodHead, SpaBootPath, accept)
	if head.Code != http.StatusOK || head.Body.Len() != 0 ||
		head.Header().Get("Content-Encoding") != "br" ||
		head.Header().Get("Content-Length") != strconv.Itoa(rec.Body.Len()) {
		t.Fatalf("HEAD %s (br): want 200 br with Content-Length %d and no body, got %d %q %q (%d bytes)",
			SpaBootPath, rec.Body.Len(), head.Code, head.Header().Get("Content-Encoding"),
			head.Header().Get("Content-Length"), head.Body.Len())
	}

	// A revalidation with the brotli ETag is a 304.
	nm := getSpaAsset(t, h, http.MethodGet, SpaBootPath,
		map[string]string{"Accept-Encoding": "br", "If-None-Match": etag})
	if nm.Code != http.StatusNotModified || nm.Body.Len() != 0 {
		t.Fatalf("GET %s If-None-Match %s: want 304 with no body, got %d (%d bytes)", SpaBootPath, etag, nm.Code, nm.Body.Len())
	}

	// A client that refuses brotli gets gzip; a Range request gets the raw bytes.
	gz := getSpaAsset(t, h, http.MethodGet, SpaBootPath, map[string]string{"Accept-Encoding": "gzip, br;q=0"})
	if ce := gz.Header().Get("Content-Encoding"); ce != "gzip" {
		t.Fatalf("GET %s with br;q=0: want gzip, got %q", SpaBootPath, ce)
	}
	rg := getSpaAsset(t, h, http.MethodGet, SpaBootPath, map[string]string{"Accept-Encoding": "br", "Range": "bytes=0-9"})
	if ce := rg.Header().Get("Content-Encoding"); ce == "br" {
		t.Fatalf("GET %s with Range: want no brotli, got Content-Encoding %q", SpaBootPath, ce)
	}

	// SKY_CSP=strict reaches the brotli response too.
	t.Setenv("SKY_CSP", "strict")
	csp := getSpaAsset(t, h, http.MethodGet, SpaBootPath, accept)
	if !strings.Contains(csp.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("GET %s (br) under SKY_CSP=strict: want the strict policy, got %q",
			SpaBootPath, csp.Header().Get("Content-Security-Policy"))
	}
}
