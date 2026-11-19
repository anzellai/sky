//go:build js

package rt

// routedPageStop reports a routed page mismatch in the browser console. A
// browser tab cannot exit (see System_exit in main_preamble_js.go); the page
// is still never written, because writeRoutedPage returns the model unchanged.
func routedPageStop(msg string) {
	logEmit(logLevelError, "error", msg, nil)
}
