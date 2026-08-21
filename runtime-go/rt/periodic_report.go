//go:build !js

package rt

import (
	"fmt"

	"sky-app/rt/periodic"
)

// periodicReport routes every panic and every error from `rt`'s background
// loops into the structured log — the same ring the console's Logs tab reads,
// so "was it reported" means "could an operator have seen it".
//
// It exists because periodic.Reporter deliberately does no logging of its own:
// `rt/telemetry`, `rt/hub` and `rt/jobs` cannot import `rt`, so the shared
// mechanism cannot know what any one caller's operators read. Each package
// supplies its own adapter; this is `rt`'s.
//
// The event names are `<loop>.cycle_panicked` / `<loop>.cycle_failed`, so a
// dashboard filter for `.cycle_` catches the whole class at once.
// The stack goes through LogRecoveredPanic, which is the ONE production-gated
// capture path (rt/panic_log.go): dev prints the full stack, production writes
// the frame to `.skylog/panic.log` and prints only the class, so internal
// frames never reach a production log. `periodic` captures nothing itself
// precisely so this stays the only copy of that policy — see periodic.Guard.
func periodicReport(r periodic.Report) {
	switch {
	case r.Recovered != nil:
		LogRecoveredPanic("sky.periodic", r.Loop, r.Recovered)
		logStructured("warn", r.Loop+".cycle_panicked",
			"detail", "this cycle is lost; the loop continues and the next tick will retry",
			"panic", fmt.Sprintf("%v", r.Recovered))
	case r.Err != nil:
		logStructured("warn", r.Loop+".cycle_failed",
			"detail", "the cycle did not complete its work",
			"error", r.Err.Error())
	}
}
