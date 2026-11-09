//go:build js

package rt

import (
	"syscall/js"
	"time"
)

// The leaving-page gate starts with the Go program, before main runs, so no
// request the client makes can come before it (spa_leave.go).
func init() { spaInstallLeaveGate() }

// spaInstallLeaveGate takes the leaving time the boot loader recorded and
// listens for the page's later events. Reading the time and adding the
// listeners happen in one turn of the event loop, so no event falls between.
func spaInstallLeaveGate() {
	win := js.Global()
	if win.Get("addEventListener").Type() != js.TypeFunction {
		return // not a browser page
	}
	if v := win.Get("__skyLeavingUntil"); v.Type() == js.TypeNumber {
		spaLeave.adoptBoot(time.Now(), v.Float())
	}
	win.Call("addEventListener", "beforeunload", js.FuncOf(func(this js.Value, args []js.Value) any {
		spaLeave.leave(time.Now())
		return nil
	}))
	// Only a back/forward cache restore ends the wait early. The first
	// pageshow of a load can come after a navigation has started.
	win.Call("addEventListener", "pageshow", js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) > 0 && args[0].Get("persisted").Truthy() {
			spaLeave.stayed()
		}
		return nil
	}))
}
