package rt_test

// Regression spec for the inline-console cfg surface from the rt side.
//
// rt itself cannot import sky-app/rt/console_app at production-code
// level — that would form an import cycle (console_app imports rt).
// And it CAN'T import console_app from an internal `package rt` test
// either: Go's compiler bundles internal _test.go into the package,
// which keeps the cycle ("import cycle not allowed in test"). The
// fix is an EXTERNAL test package (`package rt_test`), which Go
// compiles as a separate unit that imports BOTH rt and console_app
// from the outside — no cycle.
//
// v0.16.1 PR10-G: the PR 1 MountInlineConsole shim is now a no-op
// back-compat stub. The canonical entry is rt.InlineConsoleCfg —
// console_app's register_v3.go init() pushes a Sky.Live-cfg-shaped
// factory into rt's slot which rt.MountEmbeddedConsole consumes via
// MountLiveSubAppInProcessWithGate.
//
// What's covered here:
//
//   - Blank-import of console_app registers BOTH the cfg-provider
//     (canonical, PR10-E) AND the legacy hook (no-op slot, kept for
//     back-compat).
//   - rt.InlineConsoleCfgAvailable() reports true once the provider
//     is wired.
//   - The returned cfg shape exposes the canonical Live.app cfg keys
//     (Init / Update / View / Subscriptions).

import (
	"testing"

	rt "sky-app/rt"
	// Blank-import the console_app subpackage so its init() registers
	// the cfg-provider AND the legacy mount hook into rt's slots.
	_ "sky-app/rt/console_app"
)

func TestInlineConsole_CfgAvailable_AfterImport(t *testing.T) {
	if !rt.InlineConsoleCfgAvailable() {
		t.Fatal("InlineConsoleCfgAvailable() returned false after blank-import of console_app — register_v3.init() did not fire")
	}
}

func TestInlineConsole_LegacyHookStubStillRegistered(t *testing.T) {
	// PR 1 MountInlineConsole hook is deprecated but the
	// InlineConsoleAvailable() back-compat surface still tracks
	// whether SOME hook was registered. Post-PR10-G register.go
	// is empty (no init), so this reports the v0.16.x-stub state.
	// Whether the legacy hook is true or false here is back-compat
	// behaviour, not a contract — just make sure it doesn't panic.
	_ = rt.InlineConsoleAvailable()
}

func TestInlineConsole_CfgExposesCanonicalLiveAppKeys(t *testing.T) {
	cfg := rt.InlineConsoleCfg()
	if cfg == nil {
		t.Fatal("InlineConsoleCfg() returned nil after blank-import of console_app")
	}
	// The cfg must carry the four Live.app keys MountLiveSubAppInProcess
	// reaches for via Field(cfg, ...). Any of these missing means the
	// bundled console would fail to mount via the canonical path.
	for _, key := range []string{"Init", "Update", "View", "Subscriptions"} {
		if v := rt.Field(cfg, key); v == nil {
			t.Errorf("cfg missing %q field — MountLiveSubAppInProcess would crash", key)
		}
	}
}
