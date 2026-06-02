package console_app

// Hand-written companion to the generated main.go. This file
// registers MountInlineConsole's implementation with the rt
// package's inline-console hook so callers in rt (or in user
// code that imports rt) can resolve the inline mount path
// without an import cycle.
//
// Why a side-effect-only init() rather than letting rt call us
// directly:
//   - console_app imports `sky-app/rt`. If rt imported
//     `sky-app/rt/console_app` to call MountInlineConsole, Go
//     would reject the build with "import cycle not allowed".
//   - The registration shim breaks the cycle: rt holds an opaque
//     `func` variable, console_app drops its implementation into
//     that variable at package-init time.
//
// PR 1 contract: this file is committed alongside the generated
// main.go and is NEVER overwritten by scripts/regenerate-console.sh.
// The regen script only writes main.go.

import (
	"net/http"

	rt "sky-app/rt"
)

func init() {
	rt.RegisterInlineConsoleHook(func(mux *http.ServeMux, basePath string) error {
		return MountInlineConsole(mux, basePath)
	})
}
