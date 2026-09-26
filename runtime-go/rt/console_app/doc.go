//go:build !js

// Package console_app holds the Std.Ui Sky.Live console UI, translated
// to Go ONCE at compiler-release time by scripts/regenerate-console.sh
// and committed alongside the rest of the runtime.
//
// Why a subpackage of `sky-app/rt` rather than a peer?
//   - `runtime-go/` is embedded recursively into the Sky compiler
//     binary via TH (`Sky.Build.EmbeddedRuntime`), then re-materialised
//     into every user app's `sky-out/rt/`. Putting console_app inside
//     `rt/` is the only way to get it materialised alongside the rest
//     of the runtime without changing the embedding mechanism.
//   - The directory layout mirrors what user apps see at build time:
//       sky-out/main.go              package main         imports sky-app/rt
//       sky-out/rt/*.go              package rt
//       sky-out/rt/console_app/*.go  package console_app  imports sky-app/rt
//
// v0.16.1 PR10-G status:
//   - The bespoke MountInlineConsole one-shot HTML render path is
//     DELETED. The canonical mount path is rt.MountEmbeddedConsole,
//     which now uses rt.MountLiveSubAppInProcessWithGate against the
//     Sky-source cfg returned by InlineConsoleCfg() (registered via
//     console_app's init in register_v3.go).
//   - main.go's generated `init_` / `update` / `viewWrapped` /
//     `subscriptions` are still the Sky-source TEA loop — they're just
//     consumed via the canonical Sky.Live machinery now instead of
//     console_app's own handleConsoleRoot.
//   - register.go (PR 1's MountInlineConsole shim) is removed; the
//     hook surface lives in rt.RegisterInlineConsoleHook as a no-op
//     for back-compat.
//   - hydrateInitialModel + computeOverview / computeLogs / etc. are
//
// The console's data path is ONE path: the Sky-source Store (Main.sky
// httpStore / apiGet) reads the host's /_sky/console/api/* with the internal
// token. A second, Go-side bridge (hydrateInitialModel + computeOverview /
// computeLogs / …) used to live here. Nothing called it after PR10-F, yet its
// test kept passing, so it certified a path the running console never took
// while the real path was refused with 401 for months (v0.25.17 – v0.25.19).
// It was removed in v0.25.20. console_live_data_test.go tests the real path.

package console_app
