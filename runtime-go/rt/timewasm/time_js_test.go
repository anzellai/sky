//go:build js && wasm

// Package timewasm runs the Std.Time kernels in Go's js/wasm build, the
// build of the Sky.Spa client. rt/time_wasm_test.go builds this test and
// runs it under Node.js with no file system (as in a browser), so the zone
// data can come only from the copy that the runtime embeds.
package timewasm

import (
	"os"
	"testing"
	"time"

	"sky-app/rt"
)

func call(name string, args ...any) any {
	return rt.Ffi_callPure(name, args)
}

// plain unwraps the Ok that Ffi.callPure puts around a kernel's plain
// value (Bool, List). The compiled Sky call unwraps it the same way.
func plain(t *testing.T, r any) any {
	t.Helper()
	return ok(t, r)
}

func ok(t *testing.T, r any) any {
	t.Helper()
	sr, good := r.(rt.SkyResult[any, any])
	if !good || sr.Tag != 0 {
		t.Fatalf("want Ok, got %#v", r)
	}
	return sr.OkValue
}

func utcMs(y int, mo time.Month, d, h, mi int) int {
	return int(time.Date(y, mo, d, h, mi, 0, 0, time.UTC).UnixMilli())
}

func TestWasmHasNoZoneFiles(t *testing.T) {
	if _, err := os.ReadFile("/usr/share/zoneinfo/Europe/London"); err == nil {
		t.Fatal("the wasm test can read the host's zone files: it does not prove the embedded data")
	}
}

func TestWasmTimeZones(t *testing.T) {
	// A gap, an overlap and the Disambiguation rules.
	if r := call("Time_fromParts", "Europe/London", 2026, 3, 29, 1, 30, 0).(rt.SkyResult[any, any]); r.Tag != 1 {
		t.Fatal("London 01:30 on 29 March 2026 must be an Err")
	}
	if got := ok(t, call("Time_fromPartsWith", 0, "Europe/London", 2026, 3, 29, 1, 30, 0)); got != utcMs(2026, 3, 29, 1, 30) {
		t.Fatalf("Compatible in the London gap: %v", got)
	}
	if got := ok(t, call("Time_fromPartsWith", 2, "America/New_York", 2026, 11, 1, 1, 30, 0)); got != utcMs(2026, 11, 1, 6, 30) {
		t.Fatalf("Later in the New York overlap: %v", got)
	}
	if r := call("Time_fromParts", "UTC", 2026, 2, 31, 0, 0, 0).(rt.SkyResult[any, any]); r.Tag != 1 {
		t.Fatal("31 February must be an Err")
	}
	// Parts.
	p := ok(t, call("Time_toParts", "Australia/Lord_Howe", utcMs(2026, 7, 1, 0, 0))).(map[string]any)
	if p["offsetMinutes"] != 630 || p["hour"] != 10 || p["minute"] != 30 {
		t.Fatalf("Lord_Howe parts: %v", p)
	}
	// Transitions.
	m := ok(t, call("Time_nextTransition", "Europe/London", utcMs(2026, 1, 1, 0, 0))).(rt.SkyMaybe[any])
	if m.Tag != 0 || m.JustValue != utcMs(2026, 3, 29, 1, 0) {
		t.Fatalf("next London transition: %#v", m)
	}
	// Names.
	if n := len(plain(t, call("Time_zones")).([]any)); n < 300 {
		t.Fatalf("zones has %d names", n)
	}
	for name, want := range map[string]bool{"Asia/Calcutta": true, "Pacific/Apia": true, "europe/london": false, "Local": false} {
		if plain(t, call("Time_isZone", name)) != want {
			t.Fatalf("isZone %q: want %v", name, want)
		}
	}
	// Every listed zone loads from the embedded data.
	for _, z := range plain(t, call("Time_zones")).([]any) {
		if plain(t, call("Time_isZone", z)) != true {
			t.Fatalf("zone %q does not load in wasm", z)
		}
	}
}
