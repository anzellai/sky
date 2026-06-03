// inline-console mount shim — back-compat stub (v0.16.1 PR10-G).
//
// PR 1 (v0.16.0) introduced this hook so console_app could register
// its bespoke MountInlineConsole implementation against rt's package-
// level slot. PR 10-F replaced that mount path with the canonical
// MountLiveSubAppInProcess — driven by the new cfg-provider hook in
// console_inline_cfg.go.
//
// This file retains the PR 1 surface (RegisterInlineConsoleHook /
// MountInlineConsole / InlineConsoleAvailable / ErrInlineConsoleUnavailable)
// as a back-compat stub so downstream binaries still link. The hook
// is a no-op: callers that register a function get to keep that
// function, but rt's MountEmbeddedConsole no longer consults it.
//
// v1.0.0 will drop this stub; v0.16.x keeps it to preserve linkability
// across the architectural transition.

package rt

import (
	"errors"
	"net/http"
	"sync"
)

// ErrInlineConsoleUnavailable is returned by MountInlineConsole when
// the host binary did not link console_app. Kept as a public sentinel
// for callers that switch on it.
var ErrInlineConsoleUnavailable = errors.New("sky-app/rt: inline console mount hook is no longer the canonical path; rt.MountEmbeddedConsole now uses MountLiveSubAppInProcess directly")

// inlineConsoleHook is set at init() time by console_app via
// RegisterInlineConsoleHook. Post-PR10-G no rt code consults it,
// but the slot stays so registration calls don't error out.
var (
	inlineConsoleHookMu sync.RWMutex
	inlineConsoleHook   func(mux *http.ServeMux, basePath string) error
)

// RegisterInlineConsoleHook is the PR 1 registration shim. Kept as a
// no-op-friendly slot so console_app's existing init() in register.go
// still links. v1.0.0 may delete; v0.16.x preserves linkability.
func RegisterInlineConsoleHook(fn func(mux *http.ServeMux, basePath string) error) {
	inlineConsoleHookMu.Lock()
	inlineConsoleHook = fn
	inlineConsoleHookMu.Unlock()
}

// MountInlineConsole forwards to the registered hook for back-compat.
// rt's MountEmbeddedConsole no longer calls this — it routes through
// the canonical Sky.Live sub-app primitive instead. Callers that
// reach for this directly get the legacy one-shot HTML render path
// from console_app/mount.go (also deprecated).
//
// Deprecated: use rt.InlineConsoleCfg + rt.MountLiveSubAppInProcess
// when you need a console mount outside of the auto-mount path.
func MountInlineConsole(mux *http.ServeMux, basePath string) error {
	inlineConsoleHookMu.RLock()
	fn := inlineConsoleHook
	inlineConsoleHookMu.RUnlock()
	if fn == nil {
		return ErrInlineConsoleUnavailable
	}
	return fn(mux, basePath)
}

// InlineConsoleAvailable reports whether console_app has been linked
// into this binary AND registered its hook. Post-PR10-G the canonical
// signal is rt.InlineConsoleCfgAvailable(); this stays for back-compat.
func InlineConsoleAvailable() bool {
	inlineConsoleHookMu.RLock()
	ok := inlineConsoleHook != nil
	inlineConsoleHookMu.RUnlock()
	return ok
}
