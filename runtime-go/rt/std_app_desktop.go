//go:build !js

package rt

// std_app_desktop.go — the process is a Sky.Live app in a native desktop
// window (`--target desktop`, Std.App's runLiveWindow).
//
// A desktop app runs its Sky.Live server on this machine's loopback for its
// own window, and it is usually launched with no ENV (a packaged .app sets
// none), so without this mode it ran as a dev server: the Sky Console mounted
// open, with no login, on the loopback port. The Host guard stops a browser
// page from reading it (DNS rebinding), but any local process can send
// `Host: localhost` and read the app's logs, traces and session models.
//
// Desktop window mode changes three things, whatever ENV says:
//
//   - the listener binds loopback unless <PREFIX>_HOST names an interface
//     (resolveBindHost), so ENV=production does not open the window's server
//     to the network;
//   - the Host guard stays on (hostGuardApplies), because the window's server
//     serves the user's data to a browser engine on this machine;
//   - the console is off unless SKY_CONSOLE_AUTH names a mode explicitly
//     (resolveConsoleAuthMode): the dev-open default is never used.
//
// The mode is set by Std.App before the server starts, and never cleared.

import "sync/atomic"

var desktopWindowMode atomic.Bool

// desktopWindowActive reports whether this process serves a desktop window.
func desktopWindowActive() bool { return desktopWindowMode.Load() }

// Std_App_desktopWindowMode — `desktopWindowMode_ : () -> Task Error ()`.
// Std.App's runLiveWindow runs it before it starts the Live server.
func Std_App_desktopWindowMode(_ any) any {
	return func() any {
		desktopWindowMode.Store(true)
		// The console auth snapshot may already be cached by an earlier
		// probe; drop it so the mount sees the desktop posture.
		ResetConsoleAuthStateForTesting()
		return Ok[any, any](struct{}{})
	}
}
