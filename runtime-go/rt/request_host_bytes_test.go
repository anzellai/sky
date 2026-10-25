//go:build !js

package rt

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// A user app serves two domains and must tell which name a request used. Go's
// net/http moves the Host header out of r.Header into r.Host, so before the
// fix `req.headers` never held "Host" and Request had no host field.

func hostEchoHandler(seen *SkyRequest) func(any) any {
	return func(req any) any {
		return func() any {
			sr, _ := asSkyRequest(req)
			*seen = sr
			return Ok[any, any](Server_text("ok"))
		}
	}
}

func TestServerRouteRequestCarriesHost(t *testing.T) {
	var seen SkyRequest
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "a.localhost:8000"
	w := httptest.NewRecorder()
	dispatchSkyHandler(w, req, hostEchoHandler(&seen), nil)
	if seen.Host != "a.localhost:8000" {
		t.Fatalf("Request.host = %q, want %q", seen.Host, "a.localhost:8000")
	}
	if got := seen.Headers["Host"]; got != "a.localhost:8000" {
		t.Fatalf("headers[\"Host\"] = %v, want %q", got, "a.localhost:8000")
	}
}

func TestLiveApiRequestCarriesHost(t *testing.T) {
	var seen SkyRequest
	app := &liveApp{}
	req := httptest.NewRequest(http.MethodGet, "/api/host", nil)
	req.Host = "b.localhost:8000"
	w := httptest.NewRecorder()
	app.serveAPI(apiRoute{method: "GET", pattern: "/api/host", handler: hostEchoHandler(&seen)}, nil, w, req)
	if seen.Host != "b.localhost:8000" || seen.Headers["Host"] != "b.localhost:8000" {
		t.Fatalf("Live api request: host=%q headers[Host]=%v, want b.localhost:8000", seen.Host, seen.Headers["Host"])
	}
}

func TestConsoleAuthRequestCarriesHost(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/_sky/console", nil)
	req.Host = "c.localhost:8000"
	got := buildConsoleAuthRequest(req)
	if got.Host != "c.localhost:8000" || got.Headers["Host"] != "c.localhost:8000" {
		t.Fatalf("console auth request: host=%q headers[Host]=%v", got.Host, got.Headers["Host"])
	}
}

// The App.withRequest seed: the headers Dict carries "Host" too.
func TestRequestHeadersToDictCarriesHost(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "a.localhost:8000"
	d := requestHeadersToDict(req)
	got := Dict_get("Host", d)
	if m, ok := got.(SkyMaybe[any]); !ok || m.Tag != 0 || m.JustValue != "a.localhost:8000" {
		t.Fatalf("Dict.get \"Host\" = %#v, want Just a.localhost:8000", got)
	}
}

// asSkyRequest copies Host from a typed Request record.
func TestAsSkyRequestCopiesHost(t *testing.T) {
	type rec struct {
		Method string
		Host   string
	}
	got, ok := asSkyRequest(rec{Method: "GET", Host: "a.localhost:8000"})
	if !ok || got.Host != "a.localhost:8000" {
		t.Fatalf("asSkyRequest lost Host: %#v", got)
	}
}

// 1x1 transparent PNG. It holds bytes that are not valid UTF-8.
const onePixelPngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

func TestServerBytesAnswersExactBytes(t *testing.T) {
	want, _ := base64.StdEncoding.DecodeString(onePixelPngB64)
	m, ok := Bytes_fromBase64(onePixelPngB64).(SkyMaybe[any])
	if !ok || m.Tag != 0 {
		t.Fatalf("Bytes.fromBase64 failed: %#v", m)
	}
	handler := func(req any) any {
		return func() any { return Ok[any, any](Server_bytes("image/png", m.JustValue)) }
	}
	req := httptest.NewRequest(http.MethodGet, "/pixel.png", nil)
	w := httptest.NewRecorder()
	dispatchSkyHandler(w, req, handler, nil)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type %q, want image/png", ct)
	}
	if !bytes.Equal(w.Body.Bytes(), want) {
		t.Fatalf("body bytes differ:\n got %x\nwant %x", w.Body.Bytes(), want)
	}
}

func TestFileWriteBytesRoundTripsNonUtf8(t *testing.T) {
	data := string([]byte{0x89, 0x50, 0x4e, 0x47, 0x00, 0xff, 0xfe, 0x80, 0x0a})
	path := filepath.Join(t.TempDir(), "blob.bin")
	res := anyTaskInvoke(File_writeBytes(path, data))
	if res.Tag != 0 {
		t.Fatalf("writeBytes failed: %#v", res)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != data {
		t.Fatalf("on disk %x (err %v), want %x", got, err, []byte(data))
	}
	back := anyTaskInvoke(File_readFileBytes(path))
	list := back.OkValue.([]any)
	if len(list) != len(data) {
		t.Fatalf("readFileBytes length %d, want %d", len(list), len(data))
	}
	for i := range list {
		if list[i] != int(data[i]) {
			t.Fatalf("byte %d = %v, want %d", i, list[i], data[i])
		}
	}
}

func TestListRepeat(t *testing.T) {
	cases := []struct {
		n    int
		want int
	}{{3, 3}, {1, 1}, {0, 0}, {-2, 0}}
	for _, c := range cases {
		got := List_repeat(c.n, "x").([]any)
		if len(got) != c.want {
			t.Fatalf("repeat %d: len %d, want %d", c.n, len(got), c.want)
		}
		for _, v := range got {
			if v != "x" {
				t.Fatalf("repeat %d: element %v", c.n, v)
			}
		}
	}
}
