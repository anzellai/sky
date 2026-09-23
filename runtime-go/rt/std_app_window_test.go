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
