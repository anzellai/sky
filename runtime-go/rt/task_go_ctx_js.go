//go:build js

package rt

// goCtx under wasm: a Sky.Spa client has no Live session stamp, no trace
// context and no SSR settle, so a goSky goroutine inherits nothing.
type goCtx struct{}

func captureGoCtx() goCtx { return goCtx{} }

func (goCtx) run(fn func()) { fn() }

// skyPanicStack: the wasm driver owns panic reporting; no stack is carried.
func skyPanicStack() []byte { return nil }
