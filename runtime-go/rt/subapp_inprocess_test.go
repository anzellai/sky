// subapp_inprocess_test.go — regression suite for in-process sub-app
// mounting (v0.16.1 PR10-C).
//
// Coverage:
//
//   - sanitiseBasePathForCookie maps every documented input to its
//     documented output (cookie + sky-id namespace decisions).
//   - LookupInProcessSubApp returns nil when nothing is mounted and
//     the registered *liveApp when one is.
//   - rebuildInProcessSubAppRoutes orders prefixes longest-first so
//     /billing/v2 wins over /billing.
//   - Double-mount at the same prefix panics with a clear message.
//   - normaliseBasePath edge cases (slashes, empty) handled.
//
// We do NOT spin up a full http.Server here — MountLiveSubAppInProcess's
// route-registration half is exercised end-to-end by the
// examples/34-multi-tier-console Playwright suite. The unit tests
// stay focused on registry / sanitisation contracts.

package rt

import (
	"net/http"
	"strings"
	"testing"
)

func TestSanitiseBasePathForCookie(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/_sky/console", "sky_console"},
		{"/billing", "billing"},
		{"/api/v2/jobs", "api_v2_jobs"},
		{"/admin", "admin"},
		{"/admin/", "admin"},
		{"//admin//", "admin"},
		{"", "app"},
		{"/", "app"},
		{"/A-B-C", "A_B_C"},
		{"/_x", "x"},     // trim leading underscore
		{"/x_", "x"},     // trim trailing underscore
		{"/x__y", "x_y"}, // collapse double underscores
	}
	for _, c := range cases {
		got := sanitiseBasePathForCookie(c.in)
		if got != c.want {
			t.Errorf("sanitiseBasePathForCookie(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLookupInProcessSubApp_EmptyRegistry(t *testing.T) {
	resetInProcessSubAppRegistry()
	defer resetInProcessSubAppRegistry()

	if got := LookupInProcessSubApp("/billing"); got != nil {
		t.Errorf("expected nil for unmounted prefix, got %p", got)
	}
}

func TestLookupInProcessSubApp_NormalisesInput(t *testing.T) {
	resetInProcessSubAppRegistry()
	defer resetInProcessSubAppRegistry()

	// Manually register via the registry to avoid pulling in the full
	// cfg machinery — the lookup contract is independent of the mount
	// machinery.
	inProcessSubAppsMu.Lock()
	fake := &liveApp{basePath: "/billing"}
	inProcessSubApps["/billing"] = fake
	rebuildInProcessSubAppRoutes()
	inProcessSubAppsMu.Unlock()

	cases := []string{"/billing", "/billing/", "/billing///"}
	for _, in := range cases {
		got := LookupInProcessSubApp(in)
		if got != fake {
			t.Errorf("LookupInProcessSubApp(%q) = %p, want %p", in, got, fake)
		}
	}
}

func TestInProcessSubAppRoutes_LongestFirst(t *testing.T) {
	resetInProcessSubAppRegistry()
	defer resetInProcessSubAppRegistry()

	inProcessSubAppsMu.Lock()
	inProcessSubApps["/billing"] = &liveApp{basePath: "/billing"}
	inProcessSubApps["/billing/v2"] = &liveApp{basePath: "/billing/v2"}
	inProcessSubApps["/api"] = &liveApp{basePath: "/api"}
	rebuildInProcessSubAppRoutes()
	inProcessSubAppsMu.Unlock()

	routes := snapshotInProcessSubAppRoutes()
	if len(routes) != 3 {
		t.Fatalf("expected 3 routes, got %d", len(routes))
	}
	if routes[0].prefix != "/billing/v2" {
		t.Errorf("expected longest-first: routes[0] = %q, want /billing/v2", routes[0].prefix)
	}
	// "/billing" and "/api" both have length 8 and 4 — order between
	// them is stable insertion-aware, but the v2 one must come first.
}

func TestWithSubAppNamespace_TagsMatchingRequest(t *testing.T) {
	resetInProcessSubAppRegistry()
	defer resetInProcessSubAppRegistry()

	inProcessSubAppsMu.Lock()
	inProcessSubApps["/billing"] = &liveApp{basePath: "/billing"}
	rebuildInProcessSubAppRoutes()
	inProcessSubAppsMu.Unlock()

	cases := []struct {
		path, wantNS string
	}{
		{"/billing", "/billing"},
		{"/billing/", "/billing"},
		{"/billing/invoice/123", "/billing"},
		{"/billings", ""}, // prefix-with-trailing-slash check prevents this from matching
		{"/", ""},
		{"/other", ""},
	}
	for _, c := range cases {
		req, _ := http.NewRequest("GET", c.path, nil)
		gotNS := ""
		handler := WithSubAppNamespace(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotNS = NamespaceFromContext(r.Context())
		}))
		handler.ServeHTTP(nil, req)
		if gotNS != c.wantNS {
			t.Errorf("path %q: got namespace %q, want %q", c.path, gotNS, c.wantNS)
		}
	}
}

func TestWithSubAppNamespace_LongestPrefixWins(t *testing.T) {
	resetInProcessSubAppRegistry()
	defer resetInProcessSubAppRegistry()

	inProcessSubAppsMu.Lock()
	inProcessSubApps["/billing"] = &liveApp{basePath: "/billing"}
	inProcessSubApps["/billing/admin"] = &liveApp{basePath: "/billing/admin"}
	rebuildInProcessSubAppRoutes()
	inProcessSubAppsMu.Unlock()

	req, _ := http.NewRequest("GET", "/billing/admin/users", nil)
	var got string
	WithSubAppNamespace(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = NamespaceFromContext(r.Context())
	})).ServeHTTP(nil, req)
	if got != "/billing/admin" {
		t.Errorf("got %q, want /billing/admin (longest-prefix-wins)", got)
	}
}

func TestWithSubAppNamespace_ZeroCostWhenNoSubApps(t *testing.T) {
	resetInProcessSubAppRegistry()
	defer resetInProcessSubAppRegistry()

	req, _ := http.NewRequest("GET", "/anything", nil)
	var got string
	WithSubAppNamespace(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = NamespaceFromContext(r.Context())
	})).ServeHTTP(nil, req)
	if got != "" {
		t.Errorf("expected empty namespace when no sub-apps mounted, got %q", got)
	}
}

func TestMountLiveSubAppInProcess_DoubleMount_Panics(t *testing.T) {
	resetInProcessSubAppRegistry()
	defer resetInProcessSubAppRegistry()

	// First mount via the registry directly to avoid pulling in cfg.
	inProcessSubAppsMu.Lock()
	inProcessSubApps["/billing"] = &liveApp{basePath: "/billing"}
	inProcessSubAppsMu.Unlock()

	mux := http.NewServeMux()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on double-mount, got nil")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "already mounted") {
			t.Errorf("panic message %q should mention 'already mounted'", msg)
		}
	}()
	// Passing nil cfg is OK because the panic fires BEFORE cfg reads.
	MountLiveSubAppInProcess(mux, "/billing", nil)
}

func TestMountLiveSubAppInProcess_NilMuxPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil mux")
		}
	}()
	MountLiveSubAppInProcess(nil, "/billing", nil)
}

func TestMountLiveSubAppInProcess_EmptyPrefixPanics(t *testing.T) {
	mux := http.NewServeMux()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty prefix")
		}
	}()
	MountLiveSubAppInProcess(mux, "", nil)
}

func TestDefaultSubAppSessionTTL(t *testing.T) {
	got := defaultSubAppSessionTTL()
	if got.Minutes() != 30 {
		t.Errorf("expected 30m default TTL, got %v", got)
	}
}

// resetInProcessSubAppRegistry — test helper.
func resetInProcessSubAppRegistry() {
	inProcessSubAppsMu.Lock()
	inProcessSubApps = map[string]*liveApp{}
	rebuildInProcessSubAppRoutes()
	inProcessSubAppsMu.Unlock()
}
