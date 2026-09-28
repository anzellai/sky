//go:build !js && !linux && !darwin

package rt

// newWatchBackend: file watching is implemented for Linux (inotify) and
// macOS (kqueue). Elsewhere Watch.watch returns Err Unavailable.
func newWatchBackend(w *watcher) (watchBackend, error) {
	return nil, errWatchUnsupported
}
