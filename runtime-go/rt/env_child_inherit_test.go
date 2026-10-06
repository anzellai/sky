//go:build !js

package rt

// v0.27.7: a Sky program leaked its own defaults into the process environment,
// and so into every child process it started. Generated init() calls
// rt.SetPortDefault / rt.SetSkyDefault, which used os.Setenv; a child Sky
// program then saw SKY_LIVE_PORT=8000 in its environment and ranked it as an
// OPERATOR override (configLayers: operator env > builder > seeded), so its own
// sky.toml / builder port lost. The same held for `withX` values written by
// ApplyConfig and for the embedded console's per-boot internal token.
//
// The program's own values now live in procenv. These tests pin both halves:
// the program still sees them, and a child does not.

import (
	"os"
	"strings"
	"testing"

	"sky-app/rt/procenv"
)

// cleanName removes name from the process env and from procenv for the test,
// restoring the operator's value afterwards.
func cleanName(t *testing.T, name string) {
	t.Helper()
	orig, had := os.LookupEnv(name)
	procenv.Delete(name)
	_ = os.Unsetenv(name)
	t.Cleanup(func() {
		procenv.Delete(name)
		if had {
			_ = os.Setenv(name, orig)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}

func childSees(t *testing.T, name string) string {
	t.Helper()
	res := Process_run("/bin/sh", []any{"-c", `printf '%s' "${` + name + `-unset}"`}).(func() any)()
	r, ok := res.(SkyResult[any, any])
	if !ok || r.Tag != 0 {
		t.Fatalf("Process.run /bin/sh failed: %#v", res)
	}
	return r.OkValue.(string)
}

func environHas(env []string, name string) (string, bool) {
	for _, e := range env {
		if strings.HasPrefix(e, name+"=") {
			return strings.TrimPrefix(e, name+"="), true
		}
	}
	return "", false
}

func TestSeededDefaultIsNotInheritedByChildren(t *testing.T) {
	name := skyEnvName("LIVE_PORT")
	cleanName(t, name)

	SetPortDefault("8000") // what every generated init() does

	// The program itself still sees its seeded default ...
	if got := skyGetenv("LIVE_PORT"); got != "8000" {
		t.Fatalf("skyGetenv(LIVE_PORT) = %q, want the seeded 8000", got)
	}
	if got := resolveLivePort(nil); got != 8000 {
		t.Fatalf("resolveLivePort = %d, want the seeded 8000", got)
	}
	// ... but the process environment does not carry it, so no child does.
	if v, ok := os.LookupEnv(name); ok {
		t.Errorf("os env has %s=%s after SetPortDefault: the seed leaks to every child", name, v)
	}
	if v, ok := environHas(procEnviron(procSpec{}), name); ok {
		t.Errorf("Process.spawn child env has %s=%s", name, v)
	}
	if got := childSees(t, name); got != "unset" {
		t.Errorf("Process.run child sees %s=%s, want unset", name, got)
	}
}

func TestOperatorEnvIsStillInheritedByChildren(t *testing.T) {
	name := skyEnvName("LIVE_PORT")
	cleanName(t, name)
	_ = os.Setenv(name, "7777") // the operator's choice

	SetPortDefault("8000") // set-if-unset: the operator wins
	if got := resolveLivePort(nil); got != 7777 {
		t.Fatalf("resolveLivePort = %d, want the operator's 7777", got)
	}
	if v, ok := environHas(procEnviron(procSpec{}), name); !ok || v != "7777" {
		t.Errorf("Process.spawn child env %s = (%q, %v), want the operator's 7777", name, v, ok)
	}
	if got := childSees(t, name); got != "7777" {
		t.Errorf("Process.run child sees %s=%s, want the operator's 7777", name, got)
	}
}

func TestAppliedConfigIsNotInheritedByChildren(t *testing.T) {
	name := skyEnvName("LIVE_STORE")
	cleanName(t, name)

	ApplyConfig(map[string]any{"LiveStore": "sqlite"})
	if got := resolveStoreKind(""); got != "sqlite" {
		t.Fatalf("resolveStoreKind = %q, want the applied sqlite", got)
	}
	if !isConfigApplied(name) {
		t.Errorf("%s is not ranked as the builder layer", name)
	}
	if v, ok := os.LookupEnv(name); ok {
		t.Errorf("os env has %s=%s after ApplyConfig: the withX value leaks to every child", name, v)
	}
	if got := childSees(t, name); got != "unset" {
		t.Errorf("Process.run child sees %s=%s, want unset", name, got)
	}

	lit := "SKY_TELEMETRY_DB_CAPACITY"
	cleanName(t, lit)
	ApplyConfig(map[string]any{"TelemetryDbCapacity": "1GB"})
	if got := procenv.Getenv(lit); got != "1GB" {
		t.Fatalf("procenv.Getenv(%s) = %q, want the applied 1GB", lit, got)
	}
	if v, ok := os.LookupEnv(lit); ok {
		t.Errorf("os env has %s=%s after ApplyConfig", lit, v)
	}
}

func TestSystemGetenvSeesTheProgramsOwnValues(t *testing.T) {
	name := skyEnvName("LIVE_TTL")
	cleanName(t, name)
	SetSkyDefault("LIVE_TTL", "1800")

	if got := AsString(System_getenvOr(name, "none")); got != "1800" {
		t.Errorf("System.getenvOr %s = %q, want the seeded 1800", name, got)
	}
	// An explicit System.unsetenv removes the program's own value too.
	_ = System_unsetenv(name).(func() any)()
	if got := AsString(System_getenvOr(name, "none")); got != "none" {
		t.Errorf("after System.unsetenv, System.getenvOr %s = %q, want none", name, got)
	}
}

func TestConsoleInternalTokenIsNotInheritedByChildren(t *testing.T) {
	const name = "SKY_CONSOLE_INTERNAL_TOKEN"
	cleanName(t, name)
	prev := consoleInternalTokenVal.Load()
	consoleInternalTokenVal.Store("")
	t.Cleanup(func() {
		if prev != nil {
			consoleInternalTokenVal.Store(prev)
		}
	})

	tok := ConsoleInternalTokenInit()
	if tok == "" {
		t.Fatal("no token minted")
	}
	if got := procenv.Getenv(name); got != tok {
		t.Errorf("the in-process console reads %q, want the minted token", got)
	}
	if _, ok := os.LookupEnv(name); ok {
		t.Errorf("os env has %s: the per-boot console token leaks to every child", name)
	}
	if got := childSees(t, name); got != "unset" {
		t.Errorf("Process.run child sees %s, want unset", name)
	}
}
