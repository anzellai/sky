//go:build !js

package rt

import "testing"

// The desktop window must target the port Sky.Live actually binds: an
// operator's SKY_LIVE_PORT overrides the WebOpts port for the server, so it
// must override it for the window too (SA-9).
func TestStdAppLivePort_FollowsEnvOverride(t *testing.T) {
	t.Setenv("SKY_LIVE_PORT", "9123")
	if got := AsInt(Std_App_livePort(8080)); got != 9123 {
		t.Fatalf("window port = %d, want the SKY_LIVE_PORT the server binds (9123)", got)
	}
	t.Setenv("SKY_LIVE_PORT", "")
	if got := AsInt(Std_App_livePort(8421)); got != 8421 {
		t.Fatalf("window port = %d, want the builder port 8421", got)
	}
}

// A Std.App web config that does not set a port carries the "unset" sentinel
// (WebOpts.port = -1, webDefaults). It must resolve to the sky.toml port the
// generated init() seeded, not to a hardcoded default: a `[live] port` in
// sky.toml used to have no effect on a Std.App app, because Std.App forwarded
// its 8080 default as an explicit `Live.withPort` that beat the seeded value.
func TestStdAppLivePort_UnsetSentinelTakesTheSkyTomlPort(t *testing.T) {
	withCleanPortEnv(t)
	SetPortDefault("8792") // what init() emits for `[live] port = 8792`
	if got := AsInt(Std_App_livePort(-1)); got != 8792 {
		t.Fatalf("unset WebOpts.port: got :%d want the sky.toml port :8792", got)
	}
	// An explicit WebOpts.port still beats sky.toml.
	if got := AsInt(Std_App_livePort(8421)); got != 8421 {
		t.Fatalf("explicit WebOpts.port 8421 with sky.toml 8792: got :%d want :8421", got)
	}
	// An operator's env var beats both.
	setEnvRaw(skyEnvName("LIVE_PORT"), "8555")
	clearSeededDefault(skyEnvName("LIVE_PORT"))
	if got := AsInt(Std_App_livePort(-1)); got != 8555 {
		t.Fatalf("operator LIVE_PORT with unset WebOpts.port: got :%d want :8555", got)
	}
	if got := AsInt(Std_App_livePort(8421)); got != 8555 {
		t.Fatalf("operator LIVE_PORT with WebOpts.port 8421: got :%d want :8555", got)
	}
}
