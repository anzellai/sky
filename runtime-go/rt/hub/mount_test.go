// Tests for v0.16.4 Option B B7 — bundled console mounted on the hub.
//
// Verifies:
//   - GET / returns 303 → /console/
//   - GET /console/ returns 200 (Sky.Live first-paint HTML)
//   - OTLP receiver routes are NOT shadowed by the console catch-all
//   - Health probes still work
//   - Unknown root-level paths return 404 (not redirect-loop)
//
// The blank import of `sky-app/rt/console_app` in hub.go drags the
// console_app's package init() into the hub binary, which registers
// `rt.InlineConsoleCfgProvider`. So buildMux() in tests sees a
// non-nil cfg without test setup.
//
// `rt.MountLiveSubAppInProcess` keeps process-global state to
// prevent double-mount at the same prefix, so all tests SHARE one
// mux (set up via TestMain). This also matches production reality:
// a hub binary builds its mux ONCE at boot.

package hub

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

var (
	sharedMux     *http.ServeMux
	sharedSrv     *httptest.Server
	sharedCleanup func()
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sky-hub-mount-test-")
	if err != nil {
		panic(err)
	}
	cfg := HubConfig{
		Port:            0,
		DataDir:         dir,
		AuthMode:        "off",
		MaxPayloadBytes: DefaultMaxPayloadBytes,
		RetentionHours:  1,
		PruneInterval:   time.Hour,
	}
	if err := cfg.Validate(); err != nil {
		panic(err)
	}
	store, err := newStore(dir, storeOptions{
		retentionHours: cfg.RetentionHours,
		pruneInterval:  cfg.PruneInterval,
	})
	if err != nil {
		panic(err)
	}
	sharedMux = buildMux(cfg, store)
	sharedSrv = httptest.NewServer(sharedMux)
	sharedCleanup = func() {
		sharedSrv.Close()
		_ = store.Close()
		_ = os.RemoveAll(dir)
	}
	code := m.Run()
	sharedCleanup()
	os.Exit(code)
}

// TestBuildMux_RootRedirectsToConsole — the operator-facing
// invariant: hitting the hub's bare URL lands on the UI.
func TestBuildMux_RootRedirectsToConsole(t *testing.T) {
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(sharedSrv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("GET / status = %d, want %d (303 See Other)",
			resp.StatusCode, http.StatusSeeOther)
	}
	if got := resp.Header.Get("Location"); got != "/console/" {
		t.Errorf("GET / Location = %q, want %q", got, "/console/")
	}
}

// TestBuildMux_ConsoleMounted — direct GET on the console root
// returns Sky.Live's first-paint HTML. We only check the response
// is 200 + carries an HTML content type; the rendered HTML itself
// is the console_app's responsibility and shouldn't be pinned
// byte-for-byte here.
func TestBuildMux_ConsoleMounted(t *testing.T) {
	resp, err := http.Get(sharedSrv.URL + "/console/")
	if err != nil {
		t.Fatalf("GET /console/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /console/ status = %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET /console/ Content-Type = %q, want text/html…", ct)
	}
}

// TestBuildMux_OTLPRoutesNotShadowed — registering the catch-all
// `/` for the redirect MUST NOT shadow the receiver's explicit
// OTLP routes (Go's ServeMux longest-prefix-match keeps /v1/*
// exact matches winning). A 404 would mean the catch-all ate it.
func TestBuildMux_OTLPRoutesNotShadowed(t *testing.T) {
	for _, path := range []string{"/v1/traces", "/v1/metrics", "/v1/logs"} {
		resp, err := http.Get(sharedSrv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			t.Errorf("GET %s returned 404 — the catch-all `/` handler is shadowing the OTLP route", path)
		}
	}
}

// TestBuildMux_HealthProbes — operators rely on /_hub/healthz +
// /_hub/readyz. The catch-all MUST NOT shadow these either.
func TestBuildMux_HealthProbes(t *testing.T) {
	for _, path := range []string{"/_hub/healthz", "/_hub/readyz"} {
		resp, err := http.Get(sharedSrv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", path, resp.StatusCode)
		}
	}
}

// TestBuildMux_NonRootUnknownPath404 — paths under `/` other than
// the bare `/` should 404, not redirect. A blanket redirect on
// the catch-all would create infinite-loop traps for typo'd
// paths like /favicon.icoo → /console/ → …
func TestBuildMux_NonRootUnknownPath404(t *testing.T) {
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(sharedSrv.URL + "/some-typoed-path")
	if err != nil {
		t.Fatalf("GET unknown: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET unknown status = %d, want 404", resp.StatusCode)
	}
}
