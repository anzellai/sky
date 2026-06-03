package console_app

// register.go — stub remainder of the PR 1 hook shim (v0.16.1 PR10-G).
//
// PR 1 (v0.16.0) registered a MountInlineConsole implementation in
// rt's package-level slot so console_app's bespoke one-shot HTML
// render path could be called via rt.MountInlineConsole. PR 10-F
// replaced that mount with rt.MountLiveSubAppInProcessWithGate
// against the cfg returned by InlineConsoleCfg() (registered via
// register_v3.go's package init).
//
// This file used to contain an init() that pushed the bespoke
// MountInlineConsole into rt's slot. With the bespoke surface
// deleted, the registration becomes a no-op. The file is retained
// (rather than deleted) because the regenerate-console.sh script
// explicitly contracts NOT to overwrite the *_v* sibling files
// alongside main.go — and removing this file from the script's
// contract requires a coordinated update to the regen tooling.
//
// In practice the file is empty modulo this header; v1.0.0 may
// remove it once the regen contract is updated.
