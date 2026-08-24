package rt

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// a handler that mimics http.FileServer's contract: it sets Content-Type and a
// Content-Length, then writes the body — so the gzip wrapper decides on the
// same signals a real static handler provides.
func fakeStatic(contentType string, body []byte, status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
		}
		_, _ = w.Write(body)
	})
}

func doGzipReq(h http.Handler, method, acceptEnc, rangeHdr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/main.wasm", nil)
	if acceptEnc != "" {
		req.Header.Set("Accept-Encoding", acceptEnc)
	}
	if rangeHdr != "" {
		req.Header.Set("Range", rangeHdr)
	}
	rec := httptest.NewRecorder()
	gzipStatic(h).ServeHTTP(rec, req)
	return rec
}

func TestGzipStaticCompressesWasm(t *testing.T) {
	body := bytes.Repeat([]byte("wasm-bytes-highly-compressible "), 400) // ~12 KB
	rec := doGzipReq(fakeStatic("application/wasm", body, 200), "GET", "gzip, deflate, br", "")

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length must be dropped under gzip, got %q", got)
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("Vary must include Accept-Encoding, got %q", rec.Header().Get("Vary"))
	}
	// The wire bytes are gzip and decode back to the original.
	if rec.Body.Len() >= len(body) {
		t.Fatalf("gzipped size %d not smaller than raw %d", rec.Body.Len(), len(body))
	}
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("body is not valid gzip: %v", err)
	}
	got, _ := io.ReadAll(zr)
	if !bytes.Equal(got, body) {
		t.Fatalf("gunzipped body differs from original")
	}
}

func TestGzipStaticSkipsWithoutAcceptEncoding(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 4096)
	rec := doGzipReq(fakeStatic("application/wasm", body, 200), "GET", "", "")
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("must not compress when client did not accept gzip")
	}
	if !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatalf("passthrough body altered")
	}
}

func TestGzipStaticHonoursQ0(t *testing.T) {
	rec := doGzipReq(fakeStatic("text/html", []byte("<html>"), 200), "GET", "gzip;q=0", "")
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("gzip;q=0 must disable compression")
	}
}

func TestGzipStaticSkipsIncompressibleType(t *testing.T) {
	body := bytes.Repeat([]byte{0x89, 0x50, 0x4e, 0x47}, 1024) // pseudo-PNG
	rec := doGzipReq(fakeStatic("image/png", body, 200), "GET", "gzip", "")
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("already-compressed media must not be gzipped")
	}
	if !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatalf("passthrough body altered")
	}
}

func TestGzipStaticSkipsRangeRequests(t *testing.T) {
	// A Range request must pass through untouched: gzipping would break the
	// byte-range semantics the client asked for.
	rec := doGzipReq(fakeStatic("application/wasm", bytes.Repeat([]byte("a"), 4096), 200),
		"GET", "gzip", "bytes=0-1023")
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("Range request must not be gzipped")
	}
}

func TestGzipStaticSkipsNon200(t *testing.T) {
	rec := doGzipReq(fakeStatic("text/html", []byte("not found"), http.StatusNotFound), "GET", "gzip", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("non-200 must not be gzipped")
	}
}

func TestGzipStaticSkipsHeadAndPost(t *testing.T) {
	for _, m := range []string{http.MethodHead, http.MethodPost} {
		rec := doGzipReq(fakeStatic("application/wasm", []byte("body"), 200), m, "gzip", "")
		if rec.Header().Get("Content-Encoding") != "" {
			t.Fatalf("%s must not be gzipped", m)
		}
	}
}
