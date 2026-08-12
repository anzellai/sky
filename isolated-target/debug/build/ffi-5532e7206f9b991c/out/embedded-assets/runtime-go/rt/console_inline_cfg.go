// inline-console cfg-provider shim (v0.16.1 PR10-E).
//
// PR 10-E swaps the inline console's bespoke mount path for the
// canonical Sky.Live sub-app machinery (MountLiveSubAppInProcess).
// To do that, rt needs to see the console's init/update/view/sub
// functions in a Sky.Live-cfg-shaped value (a record with `Init`,
// `Update`, `View`, `Subscriptions` fields — the same shape
// `Live.app` consumes).
//
// console_app owns those typed Sky-source functions. rt cannot
// import console_app (cycle). So console_app's package init pushes
// a cfg-provider closure into rt's package-level slot here, and
// rt's MountEmbeddedConsole pulls the cfg out via the
// InlineConsoleCfg accessor before handing it to
// MountLiveSubAppInProcess.
//
// The "cfg" itself is a map[string]any. `Field(cfg, "Init")` in
// rt's reflect-driven accessors returns the typed Sky function;
// the sub-app's session-start path calls it via sky_call, exactly
// as it would for a user-written Live.app.
//
// Why a separate file from console_inline.go: the PR 1 hook there
// is the mount-shim path (DEPRECATED post-PR10-F). Keeping the
// cfg-provider in its own file makes the deletion window for the
// old shim cleaner (PR 10-G can delete console_inline.go's mount
// hook surface once nothing in rt or console_app references it).

package rt

import "sync"

// inlineConsoleCfgProvider is set at init() time by console_app via
// RegisterInlineConsoleCfgProvider. The closure returns a Sky.Live
// cfg record shape (map[string]any with Init / Update / View /
// Subscriptions keys minimum) each time it's called. The factory
// pattern (vs storing the cfg directly) means the cfg is reconstructed
// per mount call — important when test code wants to reset state
// between cases without permanently caching a stale closure.
var (
	inlineConsoleCfgMu       sync.RWMutex
	inlineConsoleCfgProvider func() any
)

// RegisterInlineConsoleCfgProvider is called from console_app's package
// init() to register its Sky.Live-cfg-shaped factory. Last writer
// wins (matches RegisterInlineConsoleHook's contract).
func RegisterInlineConsoleCfgProvider(fn func() any) {
	inlineConsoleCfgMu.Lock()
	inlineConsoleCfgProvider = fn
	inlineConsoleCfgMu.Unlock()
}

// InlineConsoleCfg returns the registered Sky.Live cfg shape for the
// bundled console, or nil when console_app is not linked. Callers in
// rt (MountEmbeddedConsole) pass the result to MountLiveSubAppInProcess.
func InlineConsoleCfg() any {
	inlineConsoleCfgMu.RLock()
	fn := inlineConsoleCfgProvider
	inlineConsoleCfgMu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn()
}

// InlineConsoleCfgAvailable reports whether console_app registered a
// cfg provider. Useful for the MountEmbeddedConsole fallback decision
// — when no cfg is available we can either fall through to the legacy
// HTML shell (PR 1-era behaviour) or skip entirely.
func InlineConsoleCfgAvailable() bool {
	inlineConsoleCfgMu.RLock()
	ok := inlineConsoleCfgProvider != nil
	inlineConsoleCfgMu.RUnlock()
	return ok
}
