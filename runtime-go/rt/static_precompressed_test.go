//go:build !js

package rt

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"
)

// precompressedFixture writes a raw asset and, when given, its `.br` / `.gz`
// sidecars into a fresh directory. The sidecars carry bytes the on-the-fly
// gzip path can never produce, so a test can tell which path answered.
type precompressedFixture struct {
	dir, name string
	raw       []byte
	br, gz    []byte
	modTime   time.Time
}

func newPrecompressedFixture(t *testing.T, name string, br, gz bool) precompressedFixture {
	t.Helper()
	f := precompressedFixture{
		dir:     t.TempDir(),
		name:    name,
		raw:     bytes.Repeat([]byte("wasm-bytes-highly-compressible "), 400),
		modTime: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	writeAt(t, filepath.Join(f.dir, name), f.raw, f.modTime)
	if br {
		// Not real brotli: the server never decodes a sidecar, it only sends it.
		f.br = []byte("precompressed-brotli-sidecar")
		writeAt(t, filepath.Join(f.dir, name+".br"), f.br, f.modTime.Add(time.Second))
	}
	if gz {
		var b bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
		zw.Comment = "sidecar" // differs from any on-the-fly gzip header
		_, _ = zw.Write(f.raw)
		_ = zw.Close()
		f.gz = b.Bytes()
		writeAt(t, filepath.Join(f.dir, name+".gz"), f.gz, f.modTime.Add(time.Second))
	}
	return f
}

func writeAt(t *testing.T, p string, b []byte, mt time.Time) {
	t.Helper()
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
}

func serveStatic(h http.Handler, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func gunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("body is not gzip: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	return out
}

// A client that accepts brotli gets the `.br` file the build wrote, with the
// original's Content-Type, an exact Content-Length and the original's
// Last-Modified. Before, it got the raw file gzipped on the fly.
func TestPrecompressedBrotliSidecarIsServed(t *testing.T) {
	f := newPrecompressedFixture(t, "main.abc.wasm", true, true)
	h := staticMount("/static/", f.dir, "")
	rec := serveStatic(h, "GET", "/static/main.abc.wasm", map[string]string{"Accept-Encoding": "gzip, deflate, br"})
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "br" {
		t.Fatalf("Content-Encoding = %q, want br", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), f.br) {
		t.Fatalf("body is not the .br sidecar (%d bytes)", rec.Body.Len())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/wasm" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(f.br)) {
		t.Fatalf("Content-Length = %q, want %d", got, len(f.br))
	}
	if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Fatalf("Vary = %q", got)
	}
	if got := rec.Header().Get("Last-Modified"); got != f.modTime.Format(http.TimeFormat) {
		t.Fatalf("Last-Modified = %q, want the original file's", got)
	}
}

// A client that accepts gzip but not brotli gets the `.gz` file the build
// wrote (gzip -9), not an on-the-fly gzip of the raw file.
func TestPrecompressedGzipSidecarIsServed(t *testing.T) {
	f := newPrecompressedFixture(t, "main.abc.wasm", true, true)
	h := staticMount("/static/", f.dir, "")
	rec := serveStatic(h, "GET", "/static/main.abc.wasm", map[string]string{"Accept-Encoding": "gzip"})
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), f.gz) {
		t.Fatalf("body is not the .gz sidecar")
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(f.gz)) {
		t.Fatalf("Content-Length = %q, want %d", got, len(f.gz))
	}
	if !bytes.Equal(gunzip(t, rec.Body.Bytes()), f.raw) {
		t.Fatalf("sidecar does not decode to the file")
	}
}

// `br;q=0` refuses brotli: the gzip sidecar answers instead.
func TestPrecompressedHonoursBrotliQ0(t *testing.T) {
	f := newPrecompressedFixture(t, "app.js", true, true)
	h := staticMount("/static/", f.dir, "")
	rec := serveStatic(h, "GET", "/static/app.js", map[string]string{"Accept-Encoding": "br;q=0, gzip"})
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), f.gz) {
		t.Fatalf("body is not the .gz sidecar")
	}
	if got := rec.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
}

// With no sidecar the response is what it always was: the raw file, gzipped
// on the fly.
func TestPrecompressedAbsentFallsBackToOnTheFlyGzip(t *testing.T) {
	f := newPrecompressedFixture(t, "main.abc.wasm", false, false)
	h := staticMount("/static/", f.dir, "")
	rec := serveStatic(h, "GET", "/static/main.abc.wasm", map[string]string{"Accept-Encoding": "gzip, br"})
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if !bytes.Equal(gunzip(t, rec.Body.Bytes()), f.raw) {
		t.Fatalf("on-the-fly gzip does not decode to the file")
	}
}

// A sidecar older than its source is stale (the source was rebuilt or
// replaced after it): never served.
func TestPrecompressedStaleSidecarIsNotServed(t *testing.T) {
	f := newPrecompressedFixture(t, "main.abc.wasm", true, true)
	newer := []byte("a rebuilt wasm, newer than its sidecars")
	writeAt(t, filepath.Join(f.dir, f.name), newer, f.modTime.Add(time.Hour))
	h := staticMount("/static/", f.dir, "")
	rec := serveStatic(h, "GET", "/static/main.abc.wasm", map[string]string{"Accept-Encoding": "gzip, br"})
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want on-the-fly gzip", got)
	}
	if !bytes.Equal(gunzip(t, rec.Body.Bytes()), newer) {
		t.Fatalf("a stale sidecar was served")
	}
}

// A Range request is served from the uncompressed file, as before.
func TestPrecompressedNotUsedForRange(t *testing.T) {
	f := newPrecompressedFixture(t, "main.abc.wasm", true, true)
	h := staticMount("/static/", f.dir, "")
	rec := serveStatic(h, "GET", "/static/main.abc.wasm",
		map[string]string{"Accept-Encoding": "gzip, br", "Range": "bytes=0-9"})
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status %d, want 206", rec.Code)
	}
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("a Range response must not be encoded")
	}
	if !bytes.Equal(rec.Body.Bytes(), f.raw[:10]) {
		t.Fatalf("range body = %q", rec.Body.Bytes())
	}
}

// HEAD carries the sidecar's headers and no body.
func TestPrecompressedHead(t *testing.T) {
	f := newPrecompressedFixture(t, "main.abc.wasm", true, true)
	h := staticMount("/static/", f.dir, "")
	rec := serveStatic(h, "HEAD", "/static/main.abc.wasm", map[string]string{"Accept-Encoding": "br"})
	if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "br" {
		t.Fatalf("status %d, Content-Encoding %q", rec.Code, rec.Header().Get("Content-Encoding"))
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(f.br)) {
		t.Fatalf("Content-Length = %q, want %d", got, len(f.br))
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD wrote a body")
	}
}

// A conditional request against the original's Last-Modified is a 304 with no
// body, length or encoding.
func TestPrecompressedNotModified(t *testing.T) {
	f := newPrecompressedFixture(t, "main.abc.wasm", true, true)
	h := staticMount("/static/", f.dir, "")
	rec := serveStatic(h, "GET", "/static/main.abc.wasm", map[string]string{
		"Accept-Encoding":   "br",
		"If-Modified-Since": f.modTime.Format(http.TimeFormat),
	})
	if rec.Code != http.StatusNotModified {
		t.Fatalf("status %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "" || rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("304 carried a body or representation headers: %v", rec.Header())
	}
}

// A client that accepts no encoding gets the raw file.
func TestPrecompressedNotUsedWithoutAcceptEncoding(t *testing.T) {
	f := newPrecompressedFixture(t, "main.abc.wasm", true, true)
	h := staticMount("/static/", f.dir, "")
	rec := serveStatic(h, "GET", "/static/main.abc.wasm", nil)
	if rec.Header().Get("Content-Encoding") != "" || !bytes.Equal(rec.Body.Bytes(), f.raw) {
		t.Fatalf("raw request got an encoded body")
	}
}

// A file whose type is not known from its extension is never sent from a
// sidecar: the type would otherwise be sniffed from compressed bytes.
func TestPrecompressedNotUsedForUnknownType(t *testing.T) {
	f := newPrecompressedFixture(t, "blob.skyunknown", true, false)
	h := staticMount("/static/", f.dir, "")
	rec := serveStatic(h, "GET", "/static/blob.skyunknown", map[string]string{"Accept-Encoding": "br"})
	if rec.Header().Get("Content-Encoding") == "br" {
		t.Fatalf("a sidecar was sent for a type known only by sniffing")
	}
}

// A sub-path mount that falls back to the `/` mount (the Sky.Spa dist) also
// finds the fallback's sidecar.
func TestPrecompressedFallbackRoot(t *testing.T) {
	f := newPrecompressedFixture(t, "app.js", true, false)
	root := filepath.Join(f.dir, "dist")
	if err := os.MkdirAll(filepath.Join(root, "static"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"app.js", "app.js.br"} {
		b, _ := os.ReadFile(filepath.Join(f.dir, n))
		st, _ := os.Stat(filepath.Join(f.dir, n))
		writeAt(t, filepath.Join(root, "static", n), b, st.ModTime())
	}
	primary := t.TempDir() // the app's own static dir, without the file
	h := staticMount("/static", primary, root)
	rec := serveStatic(h, "GET", "/static/app.js", map[string]string{"Accept-Encoding": "br"})
	if rec.Header().Get("Content-Encoding") != "br" || !bytes.Equal(rec.Body.Bytes(), f.br) {
		t.Fatalf("fallback sidecar not served: %q", rec.Header().Get("Content-Encoding"))
	}
}

// The sidecar response carries the same headers as the on-the-fly gzip
// response (the strict CSP among them), apart from the length the sidecar
// adds.
func TestPrecompressedKeepsSecurityHeaders(t *testing.T) {
	t.Setenv("SKY_CSP", "strict")
	with := newPrecompressedFixture(t, "main.abc.wasm", true, false)
	without := newPrecompressedFixture(t, "main.abc.wasm", false, false)
	a := serveStatic(staticMount("/s/", with.dir, ""), "GET", "/s/main.abc.wasm", map[string]string{"Accept-Encoding": "br, gzip"})
	b := serveStatic(staticMount("/s/", without.dir, ""), "GET", "/s/main.abc.wasm", map[string]string{"Accept-Encoding": "br, gzip"})
	if a.Header().Get("Content-Encoding") != "br" {
		t.Fatalf("sidecar not served")
	}
	if a.Header().Get("Content-Security-Policy") == "" ||
		a.Header().Get("Content-Security-Policy") != b.Header().Get("Content-Security-Policy") {
		t.Fatalf("CSP differs: %q vs %q", a.Header().Get("Content-Security-Policy"), b.Header().Get("Content-Security-Policy"))
	}
	keys := func(h http.Header) []string {
		var k []string
		for n := range h {
			if n != "Content-Length" && n != "Date" {
				k = append(k, n)
			}
		}
		sort.Strings(k)
		return k
	}
	if ka, kb := keys(a.Header()), keys(b.Header()); !equalStrings(ka, kb) {
		t.Fatalf("header names differ:\n sidecar %v\n on-the-fly %v", ka, kb)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
