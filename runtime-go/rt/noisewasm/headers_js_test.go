//go:build js && wasm

package noisewasm

import (
	"os"
	"testing"

	"sky-app/rt"
)

// The Sky.Spa client's HTTP response carries its headers, under canonical
// names (v0.29.1). The browser's fetch gives every name in lower case; before
// v0.29.1 the client's `resp.headers` was always empty, so an app that read a
// header worked on the server and not in the client.
func TestWasmHttpResponseHeadersAreCanonical(t *testing.T) {
	base := os.Getenv("SKY_NOISE_SERVER")
	if base == "" {
		t.Fatal("SKY_NOISE_SERVER is not set: run this through rt/noise_wasm_interop_test.go")
	}
	resp := run(t, rt.Http_get(base+"/headers")).(rt.HttpResponse)
	if resp.Status != 200 {
		t.Fatalf("GET /headers: status %d", resp.Status)
	}
	for name, want := range map[string]string{"X-Sky-Api-Key": "k3", "X-Upstream-Id": "u3"} {
		m, isMaybe := rt.Dict_get(name, resp.Headers).(rt.SkyMaybe[any])
		if !isMaybe || m.Tag != 0 || m.JustValue != want {
			t.Fatalf("Dict.get %q resp.headers = %#v, want Just %q", name, rt.Dict_get(name, resp.Headers), want)
		}
	}
	if m, _ := rt.Dict_get("x-sky-api-key", resp.Headers).(rt.SkyMaybe[any]); m.Tag == 0 {
		t.Fatal("the client kept the lower-case name x-sky-api-key; the Dict must hold canonical names")
	}
}
