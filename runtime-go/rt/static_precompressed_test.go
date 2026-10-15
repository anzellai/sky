//go:build !js

package rt

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
	if br || gz {
		writeManifest(t, f.dir, name)
	}
	return f
}

// writeManifest records names (and whichever of their `.br` / `.gz` sidecars
// exist) in dir's precompress manifest, as `sky build` does after it writes
// them: each file's size and SHA-256.
func writeManifest(t *testing.T, dir string, names ...string) {
	t.Helper()
	sum := func(p string) (map[string]any, bool) {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, false
		}
		h := sha256.Sum256(b)
		return map[string]any{"size": len(b), "sha256": hex.EncodeToString(h[:])}, true
	}
	files := map[string]any{}
	for _, n := range names {
		e, ok := sum(filepath.Join(dir, n))
		if !ok {
			t.Fatalf("no %s", n)
		}
		enc := map[string]any{}
		if s, ok := sum(filepath.Join(dir, n+".br")); ok {
			enc["br"] = s
		}
		if s, ok := sum(filepath.Join(dir, n+".gz")); ok {
			enc["gzip"] = s
		}
		e["encodings"] = enc
		files[n] = e
	}
	b, err := json.Marshal(map[string]any{"version": 1, "level": "max", "files": files})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sky-precompress.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
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

// A Range request from a client that accepts brotli is served from the `.br`
// representation it was given in full (nginx gzip_static does the same, and
// RFC 9110 ranges apply to the selected representation): a 206 of the
// sidecar's bytes with Content-Encoding and Vary. Before, the 206 carried the
// raw file's offsets with no Content-Encoding, so a client resuming a brotli
// 200 with If-Range (on the shared Last-Modified) spliced raw bytes into
// brotli bytes.
func TestPrecompressedRangeAfterSidecar200(t *testing.T) {
	f := newPrecompressedFixture(t, "main.abc.wasm", true, true)
	h := staticMount("/static/", f.dir, "")
	full := serveStatic(h, "GET", "/static/main.abc.wasm", map[string]string{"Accept-Encoding": "br, gzip"})
	if full.Code != 200 || full.Header().Get("Content-Encoding") != "br" {
		t.Fatalf("200: status %d, Content-Encoding %q", full.Code, full.Header().Get("Content-Encoding"))
	}
	if full.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("the 200 does not offer ranges")
	}
	etag := full.Header().Get("ETag")
	if etag == "" || !strings.Contains(etag, "br") {
		t.Fatalf("the sidecar 200 needs a validator of its own representation, ETag = %q", etag)
	}
	for _, ifRange := range []string{full.Header().Get("Last-Modified"), etag} {
		rec := serveStatic(h, "GET", "/static/main.abc.wasm", map[string]string{
			"Accept-Encoding": "br, gzip", "Range": "bytes=5-14", "If-Range": ifRange,
		})
		if rec.Code != http.StatusPartialContent {
			t.Fatalf("If-Range %q: status %d, want 206", ifRange, rec.Code)
		}
		if got := rec.Header().Get("Content-Encoding"); got != "br" {
			t.Fatalf("If-Range %q: Content-Encoding = %q, want br", ifRange, got)
		}
		if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
			t.Fatalf("If-Range %q: Vary = %q", ifRange, got)
		}
		if !bytes.Equal(rec.Body.Bytes(), f.br[5:15]) {
			t.Fatalf("If-Range %q: body %q is not bytes 5-14 of the sidecar", ifRange, rec.Body.Bytes())
		}
		if got, want := rec.Header().Get("Content-Range"), "bytes 5-14/"+strconv.Itoa(len(f.br)); got != want {
			t.Fatalf("Content-Range = %q, want %q", got, want)
		}
		if got := rec.Header().Get("Content-Length"); got != "10" {
			t.Fatalf("Content-Length = %q, want 10", got)
		}
	}
	// A gzip-only client ranges over the `.gz` representation.
	rec := serveStatic(h, "GET", "/static/main.abc.wasm", map[string]string{"Accept-Encoding": "gzip", "Range": "bytes=0-3"})
	if rec.Code != http.StatusPartialContent || rec.Header().Get("Content-Encoding") != "gzip" || !bytes.Equal(rec.Body.Bytes(), f.gz[:4]) {
		t.Fatalf("gzip range: status %d, Content-Encoding %q", rec.Code, rec.Header().Get("Content-Encoding"))
	}
	// A stale validator gets the whole representation.
	rec = serveStatic(h, "GET", "/static/main.abc.wasm", map[string]string{
		"Accept-Encoding": "br", "Range": "bytes=0-3", "If-Range": `"stale"`,
	})
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), f.br) {
		t.Fatalf("stale If-Range: status %d", rec.Code)
	}
}

// A Range request from a client that accepts no encoding is served from the
// raw file, and still says the resource varies by Accept-Encoding (a shared
// cache must not hand this 206 to a brotli client, or the reverse).
func TestPrecompressedRangeWithoutEncodingIsRaw(t *testing.T) {
	f := newPrecompressedFixture(t, "main.abc.wasm", true, true)
	h := staticMount("/static/", f.dir, "")
	for _, ae := range []string{"", "identity", "br;q=0, gzip;q=0"} {
		hdr := map[string]string{"Range": "bytes=0-9"}
		if ae != "" {
			hdr["Accept-Encoding"] = ae
		}
		rec := serveStatic(h, "GET", "/static/main.abc.wasm", hdr)
		if rec.Code != http.StatusPartialContent {
			t.Fatalf("%q: status %d, want 206", ae, rec.Code)
		}
		if rec.Header().Get("Content-Encoding") != "" {
			t.Fatalf("%q: a raw Range response must not be encoded", ae)
		}
		if !bytes.Equal(rec.Body.Bytes(), f.raw[:10]) {
			t.Fatalf("%q: range body = %q", ae, rec.Body.Bytes())
		}
		if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
			t.Fatalf("%q: Vary = %q", ae, got)
		}
	}
}

// The on-the-fly gzip path never gzips a Range request, and its 206 says
// Vary: Accept-Encoding like its 200 does.
func TestOnTheFlyRangeCarriesVary(t *testing.T) {
	f := newPrecompressedFixture(t, "main.abc.wasm", false, false)
	h := staticMount("/static/", f.dir, "")
	for _, method := range []string{"GET", "HEAD"} {
		rec := serveStatic(h, method, "/static/main.abc.wasm", map[string]string{"Accept-Encoding": "gzip, br", "Range": "bytes=0-9"})
		if rec.Code != http.StatusPartialContent || rec.Header().Get("Content-Encoding") != "" {
			t.Fatalf("%s: status %d, Content-Encoding %q", method, rec.Code, rec.Header().Get("Content-Encoding"))
		}
		if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
			t.Fatalf("%s: Vary = %q", method, got)
		}
	}
}

// Sidecars a user committed in their own static directory (no manifest from
// `sky build`) are never served, even when the sidecar looks newer than its
// source (a fresh clone sets every mtime to the checkout time). The request
// takes the on-the-fly gzip path, as it did before sidecars were served.
func TestPrecompressedUnlistedSidecarIsNotServed(t *testing.T) {
	dir := t.TempDir()
	raw := bytes.Repeat([]byte("console.log('the current app.js');\n"), 100)
	mt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	writeAt(t, filepath.Join(dir, "app.js"), raw, mt)
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	_, _ = zw.Write([]byte("console.log('an old app.js, committed long ago');"))
	_ = zw.Close()
	writeAt(t, filepath.Join(dir, "app.js.gz"), b.Bytes(), mt.Add(time.Hour))
	writeAt(t, filepath.Join(dir, "app.js.br"), []byte("old brotli"), mt.Add(time.Hour))
	h := staticMount("/static/", dir, "")
	rec := serveStatic(h, "GET", "/static/app.js", map[string]string{"Accept-Encoding": "br, gzip"})
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want the on-the-fly gzip", rec.Header().Get("Content-Encoding"))
	}
	if !bytes.Equal(gunzip(t, rec.Body.Bytes()), raw) {
		t.Fatalf("a committed sidecar was served instead of the current file")
	}
	// A file the manifest does not list is not served from a sidecar either.
	writeAt(t, filepath.Join(dir, "other.js"), raw, mt)
	writeManifest(t, dir, "other.js")
	rec = serveStatic(h, "GET", "/static/app.js", map[string]string{"Accept-Encoding": "br"})
	if rec.Header().Get("Content-Encoding") == "br" {
		t.Fatalf("an unlisted sidecar was served")
	}
}

// A manifest entry whose source has changed since the build (same size, so
// only the content tells; an older mtime than the sidecar, so freshness by
// mtime would pass) is not served from its sidecar: the current file is.
func TestPrecompressedManifestSourceChangedIsNotServed(t *testing.T) {
	f := newPrecompressedFixture(t, "wasm_exec.js", true, true)
	changed := bytes.Repeat([]byte("x"), len(f.raw))
	writeAt(t, filepath.Join(f.dir, f.name), changed, f.modTime.Add(-time.Hour))
	h := staticMount("/static/", f.dir, "")
	rec := serveStatic(h, "GET", "/static/wasm_exec.js", map[string]string{"Accept-Encoding": "br, gzip"})
	if rec.Header().Get("Content-Encoding") != "gzip" || !bytes.Equal(gunzip(t, rec.Body.Bytes()), changed) {
		t.Fatalf("the sidecar of the old source was served (Content-Encoding %q)", rec.Header().Get("Content-Encoding"))
	}
	rec = serveStatic(h, "GET", "/static/wasm_exec.js", map[string]string{"Accept-Encoding": "br", "Range": "bytes=0-3"})
	if rec.Header().Get("Content-Encoding") != "" || !bytes.Equal(rec.Body.Bytes(), changed[:4]) {
		t.Fatalf("a range of the old sidecar was served")
	}
}

// A sidecar replaced after the build (its bytes no longer the ones the
// manifest records) is not served; the other, intact sidecar still is.
func TestPrecompressedManifestSidecarChangedIsNotServed(t *testing.T) {
	f := newPrecompressedFixture(t, "main.abc.wasm", true, true)
	forged := bytes.Repeat([]byte("Z"), len(f.br))
	writeAt(t, filepath.Join(f.dir, f.name+".br"), forged, f.modTime.Add(time.Second))
	h := staticMount("/static/", f.dir, "")
	rec := serveStatic(h, "GET", "/static/main.abc.wasm", map[string]string{"Accept-Encoding": "br, gzip"})
	if rec.Header().Get("Content-Encoding") != "gzip" || !bytes.Equal(rec.Body.Bytes(), f.gz) {
		t.Fatalf("Content-Encoding %q: want the intact .gz sidecar", rec.Header().Get("Content-Encoding"))
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
	writeManifest(t, root, "static/app.js")
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
			// The sidecar adds its length and a validator of its own
			// representation; the rest is the same.
			if n != "Content-Length" && n != "Date" && n != "Etag" {
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

// The build's precompress manifest is metadata for the server, not an asset:
// a mount that uses it answers its exact path as it answers a file that does
// not exist (404 here, the app's NotFound page under Server.staticNotFound),
// and leaves it out of a directory listing. Before, every Sky.Spa backend
// served `/sky-precompress.json` publicly. A file of another name, and a
// `sky-precompress.json` that is not a manifest the build wrote (a user's own
// file in their static directory), are served as before.
func TestPrecompressManifestIsNotServed(t *testing.T) {
	f := newPrecompressedFixture(t, "app.js", true, true)
	h := staticMount("/", f.dir, "")
	for _, target := range []string{"/sky-precompress.json", "/SKY-PRECOMPRESS.JSON", "/./sky-precompress.json"} {
		for _, hdr := range []map[string]string{nil, {"Accept-Encoding": "br, gzip"}} {
			rec := serveStatic(h, "GET", target, hdr)
			if rec.Code != http.StatusNotFound && rec.Code != http.StatusMovedPermanently {
				t.Fatalf("GET %s (%v) = %d, want 404: %q", target, hdr, rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "sha256") {
				t.Fatalf("GET %s (%v) leaked the manifest: %q", target, hdr, rec.Body.String())
			}
		}
	}
	if rec := serveStatic(h, "HEAD", "/sky-precompress.json", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("HEAD manifest = %d, want 404", rec.Code)
	}
	// The manifest is still used: the sidecar is sent.
	rec := serveStatic(h, "GET", "/app.js", map[string]string{"Accept-Encoding": "br"})
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "br" || !bytes.Equal(rec.Body.Bytes(), f.br) {
		t.Fatalf("the manifest must still drive the sidecar: %d %q", rec.Code, rec.Header().Get("Content-Encoding"))
	}
	// A directory listing leaves it out.
	sub := filepath.Join(f.dir, "assets")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAt(t, filepath.Join(sub, "x.css"), []byte("a{}"), f.modTime)
	writeManifest(t, sub, "x.css")
	if rec := serveStatic(h, "GET", "/assets/", nil); rec.Code != http.StatusOK ||
		strings.Contains(rec.Body.String(), "sky-precompress") || !strings.Contains(rec.Body.String(), "x.css") {
		t.Fatalf("listing: %d %q", rec.Code, rec.Body.String())
	}
	if rec := serveStatic(h, "GET", "/assets/sky-precompress.json", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("a nested manifest = %d, want 404", rec.Code)
	}
	// Other names are untouched.
	writeAt(t, filepath.Join(f.dir, "sky-precompress.json.txt"), []byte("mine"), f.modTime)
	if rec := serveStatic(h, "GET", "/sky-precompress.json.txt", nil); rec.Code != http.StatusOK || rec.Body.String() != "mine" {
		t.Fatalf("a user file of another name: %d %q", rec.Code, rec.Body.String())
	}
	// A file of that name the build did not write is the user's: served.
	own := t.TempDir()
	writeAt(t, filepath.Join(own, "sky-precompress.json"), []byte(`{"mine":true}`), f.modTime)
	if rec := serveStatic(staticMount("/", own, ""), "GET", "/sky-precompress.json", nil); rec.Code != http.StatusOK || rec.Body.String() != `{"mine":true}` {
		t.Fatalf("a user's own sky-precompress.json: %d %q", rec.Code, rec.Body.String())
	}
	// A sub-path mount whose file falls back to the `/` dist does not reach
	// the dist's manifest either.
	if rec := serveStatic(staticMount("/static", t.TempDir(), f.dir), "GET", "/sky-precompress.json", nil); rec.Code == http.StatusOK {
		t.Fatalf("fallback root served the manifest: %q", rec.Body.String())
	}
	// Under Server.staticNotFound the path takes the NotFound handler, as a
	// missing file does.
	nf := spaStaticFallbackHandler(h, func(_ any) any {
		return Task_succeed[any, any](Server_html("not-found-page"))
	})
	if rec := serveStatic(nf, "GET", "/sky-precompress.json", nil); !strings.HasPrefix(rec.Body.String(), "not-found-page") {
		t.Fatalf("staticNotFound: %d %q, want the NotFound page", rec.Code, rec.Body.String())
	}
}
