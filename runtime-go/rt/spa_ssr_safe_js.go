//go:build js

package rt

// The wasm client never performs an SSR settle (that is a backend-only step), so
// the SSR-settle guard is inert here. These stubs keep the destructive kernels
// in shared files (rt.go / http_wasm.go) compiling for GOOS=js without dragging
// the goroutine-id machinery into the single-threaded wasm runtime.

// InSsrSettle is always false on the wasm client.
func InSsrSettle() bool { return false }

// ssrSuppressedWrite never suppresses on the wasm client (nil = run the effect).
func ssrSuppressedWrite(_ string) any { return nil }
