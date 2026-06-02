// inline-console registration shim (v0.16.0 PR 1).
//
// PR 1 introduces a new mount path: in-process, no subprocess, the
// console UI handlers register directly on the host app's
// *http.ServeMux. The console source lives at
// runtime-go/rt/console_app/, which is a subpackage of `sky-app/rt`.
//
// Direction of imports matters: console_app imports rt (the
// generated Go calls hundreds of rt.* helpers). The reverse —
// rt importing console_app — would create a circular import.
//
// To bridge that, console_app registers its MountInlineConsole
// implementation into rt's package-level hook here via
// RegisterInlineConsoleHook (called from console_app's init()).
// Code in rt that wants to mount the inline console calls
// MountInlineConsole, which forwards to the registered hook
// if one is present, or returns ErrInlineConsoleUnavailable
// otherwise.
//
// For PR 1, NOTHING in this rt package side-effect-imports
// console_app — that wiring lands in PR 2 along with the
// switch from subprocess-default to inline-default. PR 1's
// goal is purely to prove the path: a Go test inside
// console_app calls its own MountInlineConsole directly.

package rt

import (
	"errors"
	"net/http"
	"sync"
)

// ErrInlineConsoleUnavailable is returned by MountInlineConsole when
// the host binary did not link console_app (no blank import; no PR-2
// codegen wiring yet). Callers can fall back to the legacy
// subprocess / HTML-shell path on this error.
var ErrInlineConsoleUnavailable = errors.New("sky-app/rt: inline console not linked into this binary (import _ \"sky-app/rt/console_app\" or set SKY_CONSOLE_MODE=subprocess)")

// inlineConsoleHook is set at init() time by console_app via
// RegisterInlineConsoleHook. nil when console_app is not linked.
var (
	inlineConsoleHookMu sync.RWMutex
	inlineConsoleHook   func(mux *http.ServeMux, basePath string) error
)

// RegisterInlineConsoleHook is called from console_app's package
// init() to register its MountInlineConsole implementation against
// this shim. It's exported because the registration crosses a
// package boundary, but it isn't part of the rt public API — user
// code should never call it.
//
// Idempotent: a second call replaces the first. Concurrent calls
// are serialised by the mutex.
func RegisterInlineConsoleHook(fn func(mux *http.ServeMux, basePath string) error) {
	inlineConsoleHookMu.Lock()
	inlineConsoleHook = fn
	inlineConsoleHookMu.Unlock()
}

// MountInlineConsole forwards to the console_app-registered hook
// when present, otherwise returns ErrInlineConsoleUnavailable.
//
// Public API. Stable from v0.16.0 onward.
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
// into this binary. Useful for the SKY_CONSOLE_MODE selector when it
// needs to choose between inline and subprocess fallback without
// actually attempting the mount.
func InlineConsoleAvailable() bool {
	inlineConsoleHookMu.RLock()
	ok := inlineConsoleHook != nil
	inlineConsoleHookMu.RUnlock()
	return ok
}
