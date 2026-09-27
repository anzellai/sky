package rt

import (
	"os"
	"strings"
	"testing"
)

// clearBindEnv unsets every variable resolveBindHost / productionFromEnv read,
// restoring the prior values when the test ends. Env-based, so these tests must
// NOT run t.Parallel().
func clearBindEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"ENV", "SKY_ENV", "SKY_HOST"} {
		if old, ok := os.LookupEnv(k); ok {
			t.Cleanup(func() { os.Setenv(k, old) })
		} else {
			t.Cleanup(func() { os.Unsetenv(k) })
		}
		os.Unsetenv(k)
	}
}

// TestResolveBindHostDevIsLoopback — the whole point of the change. With ENV
// unset (dev) the listener must bind loopback, because /_sky/console and
// /_sky/metrics are unauthenticated in dev and binding them wide exposed them
// to the LAN.
func TestResolveBindHostDevIsLoopback(t *testing.T) {
	clearBindEnv(t)
	if got, _ := resolveBindHost(); got != "127.0.0.1" {
		t.Fatalf("dev (ENV unset) must bind loopback, got %q", got)
	}
	if got := bindAddr(8000); got != "127.0.0.1:8000" {
		t.Fatalf("dev Addr = %q, want 127.0.0.1:8000", got)
	}
}

// TestResolveBindHostProdIsAllInterfaces — ENV=production must bind all
// interfaces, and the empty-host form must produce EXACTLY ":port", the
// byte-for-byte historical behaviour, so no deploy topology regresses.
func TestResolveBindHostProdIsAllInterfaces(t *testing.T) {
	clearBindEnv(t)
	os.Setenv("ENV", "production")
	if got, _ := resolveBindHost(); got != "" {
		t.Fatalf("prod (ENV=production) must bind all interfaces (empty host), got %q", got)
	}
	if got := bindAddr(8000); got != ":8000" {
		t.Fatalf("prod Addr = %q, want :8000 (byte-identical to the old fmt.Sprintf(\":%%d\"))", got)
	}
}

// A non-dev ENV value that is not literally "production" still gates (bias to
// gate), so it must also bind wide.
func TestResolveBindHostStagingIsAllInterfaces(t *testing.T) {
	clearBindEnv(t)
	os.Setenv("ENV", "staging")
	if got, _ := resolveBindHost(); got != "" {
		t.Fatalf("staging must bind all interfaces (empty host), got %q", got)
	}
}

// A dev-marker ENV value stays loopback.
func TestResolveBindHostDevMarkerIsLoopback(t *testing.T) {
	for _, marker := range []string{"dev", "development", "local"} {
		clearBindEnv(t)
		os.Setenv("ENV", marker)
		if got, _ := resolveBindHost(); got != "127.0.0.1" {
			t.Fatalf("ENV=%s must stay loopback, got %q", marker, got)
		}
	}
}

// TestSkyHostOverridesBothWays — an explicit SKY_HOST wins in dev AND in prod.
func TestSkyHostOverridesBothWays(t *testing.T) {
	// dev
	clearBindEnv(t)
	os.Setenv("SKY_HOST", "1.2.3.4")
	if got, _ := resolveBindHost(); got != "1.2.3.4" {
		t.Fatalf("SKY_HOST must win in dev, got %q", got)
	}
	if got := bindAddr(8000); got != "1.2.3.4:8000" {
		t.Fatalf("dev+SKY_HOST Addr = %q, want 1.2.3.4:8000", got)
	}
	// prod
	clearBindEnv(t)
	os.Setenv("ENV", "production")
	os.Setenv("SKY_HOST", "1.2.3.4")
	if got, _ := resolveBindHost(); got != "1.2.3.4" {
		t.Fatalf("SKY_HOST must win in prod, got %q", got)
	}
}

// SKY_HOST=0.0.0.0 is a legitimate way to ask for all interfaces in dev — the
// operator opts back into wide binding explicitly.
func TestSkyHostZeroesInDev(t *testing.T) {
	clearBindEnv(t)
	os.Setenv("SKY_HOST", "0.0.0.0")
	if got, _ := resolveBindHost(); got != "0.0.0.0" {
		t.Fatalf("SKY_HOST=0.0.0.0 must bind wide, got %q", got)
	}
	if got := bindAddr(8000); got != "0.0.0.0:8000" {
		t.Fatalf("Addr = %q, want 0.0.0.0:8000", got)
	}
}

// SKY_HOST is honoured through a custom [env] prefix, because it is read via
// skyGetenv, not raw os.Getenv.
func TestSkyHostHonoursEnvPrefix(t *testing.T) {
	clearBindEnv(t)
	old := envPrefix
	t.Cleanup(func() { envPrefix = old })
	envPrefix = "FENCE"
	os.Setenv("FENCE_HOST", "5.6.7.8")
	t.Cleanup(func() { os.Unsetenv("FENCE_HOST") })
	if got, _ := resolveBindHost(); got != "5.6.7.8" {
		t.Fatalf("FENCE_HOST must be read under prefix FENCE, got %q", got)
	}
}

// TestConsoleURLReflectsBindHost — banner requirement #4: the console URL must
// name the actual bind host. A loopback bind stays localhost (now true); an
// off-box SKY_HOST is shown verbatim.
func TestConsoleURLReflectsBindHost(t *testing.T) {
	if got := consoleDisplayHost("127.0.0.1"); got != "localhost" {
		t.Fatalf("loopback should display as localhost, got %q", got)
	}
	if got := consoleDisplayHost(""); got != "localhost" {
		t.Fatalf("all-interfaces should display as localhost, got %q", got)
	}
	if got := consoleDisplayHost("1.2.3.4"); got != "1.2.3.4" {
		t.Fatalf("off-box host should display verbatim, got %q", got)
	}
}

// TestExposedNoteFiresOnlyWhenOpenConsoleIsOffBox — the exposure note appears
// exactly when an OPEN (dev) console is bound to a non-loopback host, and never
// otherwise.
func TestExposedNoteFiresOnlyWhenOpenConsoleIsOffBox(t *testing.T) {
	gc := gcTuning{reason: "x"}

	// dev console on loopback → no note.
	local := strings.Join(startupReportLines("http://localhost:8000/_sky/console", devBind, false, gc, false), "\n")
	if strings.Contains(local, "exposed") {
		t.Fatalf("loopback dev console must not print the exposed note:\n%s", local)
	}

	// dev console bound to a concrete off-box host → note fires.
	offbox := strings.Join(startupReportLines("http://1.2.3.4:8000/_sky/console", envBind("1.2.3.4"), false, gc, false), "\n")
	if !strings.Contains(offbox, "exposed") {
		t.Fatalf("off-box open dev console must print the exposed note:\n%s", offbox)
	}

	// dev console bound wide via SKY_HOST=0.0.0.0 — the URL renders as
	// localhost for clickability, but the bind is wide, so the note MUST still
	// fire (this is the case the URL-derived check missed).
	wide := strings.Join(startupReportLines("http://localhost:8000/_sky/console", envBind("0.0.0.0"), false, gc, false), "\n")
	if !strings.Contains(wide, "exposed") {
		t.Fatalf("SKY_HOST=0.0.0.0 open dev console must print the exposed note even though the URL says localhost:\n%s", wide)
	}

	// production (gated console) → no dev block at all, so no note even off-box.
	prod := strings.Join(startupReportLines("http://1.2.3.4:8000/_sky/console", prodBind, true, gc, false), "\n")
	if strings.Contains(prod, "exposed") {
		t.Fatalf("production must not print the exposed note (console is gated):\n%s", prod)
	}
}

// TestResolveBindHostReportsItsSource — the start-up bind line names why the
// listener bound where it did, so resolveBindHost returns the reason with the
// host.
func TestResolveBindHostReportsItsSource(t *testing.T) {
	cases := []struct {
		env      map[string]string
		wantHost string
		wantSrc  bindSource
	}{
		{map[string]string{}, "127.0.0.1", bindDevDefault},
		{map[string]string{"ENV": "dev"}, "127.0.0.1", bindDevDefault},
		{map[string]string{"ENV": "production"}, "", bindProductionDefault},
		{map[string]string{"SKY_ENV": "staging"}, "", bindProductionDefault},
		{map[string]string{"SKY_HOST": "10.0.0.5"}, "10.0.0.5", bindFromSkyHost},
		{map[string]string{"SKY_HOST": "0.0.0.0", "ENV": "production"}, "0.0.0.0", bindFromSkyHost},
		{map[string]string{"SKY_HOST": "  "}, "127.0.0.1", bindDevDefault},
	}
	for _, c := range cases {
		clearBindEnv(t)
		for k, v := range c.env {
			os.Setenv(k, v)
		}
		host, src := resolveBindHost()
		if host != c.wantHost || src != c.wantSrc {
			t.Fatalf("env %v: resolveBindHost = (%q, %v), want (%q, %v)", c.env, host, src, c.wantHost, c.wantSrc)
		}
	}
}

// TestBindAddrBracketsAnIPv6SkyHost — "::1:8000" is not an address net.Listen
// can parse, so an IPv6 SKY_HOST (bare or bracketed) must be bracketed.
func TestBindAddrBracketsAnIPv6SkyHost(t *testing.T) {
	for _, h := range []string{"::1", "[::1]"} {
		clearBindEnv(t)
		os.Setenv("SKY_HOST", h)
		if got := bindAddr(8000); got != "[::1]:8000" {
			t.Fatalf("SKY_HOST=%s: Addr = %q, want [::1]:8000", h, got)
		}
	}
}
